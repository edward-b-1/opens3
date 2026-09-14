package notify

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/edward-b-1/opens3/internal/object"
)

// Dispatcher routes object events to targets.
type Dispatcher struct {
	cfg     Config
	log     *slog.Logger
	targets map[string]*worker // lower-cased name

	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup

	mu     sync.RWMutex // guards closed and the config cache
	closed bool
	cache  map[string]*cachedConfig

	events *prometheus.CounterVec
	depth  *prometheus.GaugeVec
}

type cachedConfig struct {
	raw []byte
	cfg *Configuration
}

// worker owns one target's queue and delivery goroutines.
type worker struct {
	name   string
	kind   string
	target Target
	ch     chan *Message // in-memory mode
	queue  *diskQueue    // persistent mode (ch is nil)
	kick   chan struct{} // persistent mode wake-up
	done   chan struct{} // persistent mode stop
}

// New builds the dispatcher and starts its workers. reg may be nil.
func New(cfg Config, log *slog.Logger, reg *prometheus.Registry) (*Dispatcher, error) {
	if log == nil {
		log = slog.Default()
	}
	if cfg.QueueSize <= 0 {
		cfg.QueueSize = 10000
	}
	if cfg.Workers <= 0 {
		cfg.Workers = 1
	}
	if cfg.SendTimeout <= 0 {
		cfg.SendTimeout = 2 * time.Minute
	}
	if cfg.DrainTimeout <= 0 {
		cfg.DrainTimeout = 10 * time.Second
	}
	d := &Dispatcher{cfg: cfg, log: log, targets: map[string]*worker{}, cache: map[string]*cachedConfig{}}
	d.ctx, d.cancel = context.WithCancel(context.Background())
	d.events = prometheus.NewCounterVec(prometheus.CounterOpts{Name: "opens3_notify_events_total", Help: "Notification events by target and result (sent, failed, dropped)."}, []string{"target", "result"})
	d.depth = prometheus.NewGaugeVec(prometheus.GaugeOpts{Name: "opens3_notify_queue_length", Help: "Events waiting for delivery per target."}, []string{"target"})
	if reg != nil {
		if err := reg.Register(d.events); err != nil {
			return nil, err
		}
		if err := reg.Register(d.depth); err != nil {
			return nil, err
		}
	}
	for _, tc := range cfg.Targets {
		if tc.Name == "" {
			return nil, errors.New("notify: target name is required")
		}
		key := strings.ToLower(tc.Name)
		if _, dup := d.targets[key]; dup {
			return nil, fmt.Errorf("notify: duplicate target %q", tc.Name)
		}
		t := tc.Target
		kind := tc.Type
		if t == nil {
			switch tc.Type {
			case "webhook":
				var err error
				if t, err = NewWebhook(WebhookConfig{Endpoint: tc.Endpoint, AuthToken: tc.AuthToken}); err != nil {
					return nil, err
				}
			case "log":
				t = NewLog(log)
			default:
				return nil, fmt.Errorf("notify: target %q: unsupported type %q", tc.Name, tc.Type)
			}
		}
		if k, ok := t.(Kinder); ok && kind == "" {
			kind = k.Kind()
		}
		w := &worker{name: tc.Name, kind: kind, target: t}
		if tc.QueueDir != "" {
			q, err := openDiskQueue(tc.QueueDir, tc.QueueLimit)
			if err != nil {
				return nil, fmt.Errorf("notify: target %q: queue dir: %w", tc.Name, err)
			}
			w.queue, w.kick, w.done = q, make(chan struct{}, 1), make(chan struct{})
			d.depth.WithLabelValues(w.name).Set(float64(q.length()))
			d.wg.Add(1)
			go d.runPersistent(w)
			w.kick <- struct{}{} // replay whatever survived a restart
		} else {
			w.ch = make(chan *Message, cfg.QueueSize)
			d.depth.WithLabelValues(w.name).Set(0)
			for i := 0; i < cfg.Workers; i++ {
				d.wg.Add(1)
				go d.runMemory(w)
			}
		}
		d.targets[key] = w
		for _, r := range []string{"sent", "failed", "dropped"} {
			d.events.WithLabelValues(w.name, r)
		}
		log.Info("notification target configured", "target", tc.Name, "type", kind, "arn", TargetARN(tc.Name, kind), "persistent", tc.QueueDir != "")
	}
	return d, nil
}

// Targets returns the native ARNs of the configured targets.
func (d *Dispatcher) Targets() []string {
	var out []string
	for _, w := range d.targets {
		out = append(out, TargetARN(w.name, w.kind))
	}
	return out
}

// Resolve maps an ARN to the configured target name.
func (d *Dispatcher) Resolve(arn string) (string, error) {
	a, err := ParseARN(arn)
	if err != nil {
		return "", err
	}
	w := d.targets[strings.ToLower(a.Name)]
	if w == nil {
		return "", fmt.Errorf("%w: %s", ErrUnknownTarget, arn)
	}
	if a.Type != "" && w.kind != "" && !strings.EqualFold(a.Type, w.kind) {
		return "", fmt.Errorf("%w: %s is a %s target, not %s", ErrUnknownTarget, w.name, w.kind, a.Type)
	}
	return w.name, nil
}

// ValidateTarget is the s3api hook: it errors for ARNs that do not
// reference a configured target.
func (d *Dispatcher) ValidateTarget(arn string) error {
	_, err := d.Resolve(arn)
	return err
}

// Handle routes one event. It never blocks: in-memory queues drop (with a
// metric and a warning) when full; persistent queues write to disk.
func (d *Dispatcher) Handle(e object.Event) {
	if e.Bucket == nil || len(e.Bucket.NotificationXML) == 0 || len(d.targets) == 0 {
		return
	}
	cfg := d.configFor(e.Bucket.Name, e.Bucket.NotificationXML)
	if cfg == nil {
		return
	}
	key := e.Key
	if key == "" && e.Object != nil {
		key = e.Object.Key
	}
	rules := cfg.Match(e.Name, key)
	if len(rules) == 0 {
		return
	}
	d.mu.RLock()
	defer d.mu.RUnlock()
	if d.closed {
		return
	}
	for _, r := range rules {
		name, err := d.Resolve(r.ARN)
		if err != nil {
			d.log.Warn("notification target not configured", "bucket", e.Bucket.Name, "arn", r.ARN, "err", err)
			continue
		}
		w := d.targets[strings.ToLower(name)]
		m := &Message{Records: []Record{BuildRecord(e, RecordOptions{ConfigurationID: r.ID, Region: d.cfg.Region})}}
		d.enqueue(w, m)
	}
}

func (d *Dispatcher) enqueue(w *worker, m *Message) {
	if w.queue != nil {
		if _, err := w.queue.push(m); err != nil {
			d.events.WithLabelValues(w.name, "dropped").Inc()
			d.log.Warn("notification dropped", "target", w.name, "err", err)
			return
		}
		d.depth.WithLabelValues(w.name).Set(float64(w.queue.length()))
		select {
		case w.kick <- struct{}{}:
		default:
		}
		return
	}
	select {
	case w.ch <- m:
		d.depth.WithLabelValues(w.name).Set(float64(len(w.ch)))
	default:
		d.events.WithLabelValues(w.name, "dropped").Inc()
		d.log.Warn("notification dropped: queue full", "target", w.name, "queue", cap(w.ch))
	}
}

func (d *Dispatcher) configFor(bucket string, raw []byte) *Configuration {
	d.mu.RLock()
	c := d.cache[bucket]
	d.mu.RUnlock()
	if c != nil && bytes.Equal(c.raw, raw) {
		return c.cfg
	}
	cfg, err := ParseConfiguration(raw)
	if err != nil {
		d.log.Warn("invalid notification configuration", "bucket", bucket, "err", err)
		cfg = &Configuration{}
	}
	d.mu.Lock()
	d.cache[bucket] = &cachedConfig{raw: append([]byte(nil), raw...), cfg: cfg}
	d.mu.Unlock()
	return cfg
}

func (d *Dispatcher) send(w *worker, m *Message) error {
	ctx, cancel := context.WithTimeout(d.ctx, d.cfg.SendTimeout)
	defer cancel()
	err := w.target.Send(ctx, m)
	if err == nil {
		d.events.WithLabelValues(w.name, "sent").Inc()
		return nil
	}
	d.events.WithLabelValues(w.name, "failed").Inc()
	rec := ""
	if len(m.Records) > 0 {
		rec = m.Records[0].EventName + " " + m.Records[0].S3.Bucket.Name + "/" + m.Records[0].S3.Object.Key
	}
	d.log.Warn("notification delivery failed", "target", w.name, "event", rec, "err", err)
	return err
}

func (d *Dispatcher) runMemory(w *worker) {
	defer d.wg.Done()
	for m := range w.ch {
		d.depth.WithLabelValues(w.name).Set(float64(len(w.ch)))
		_ = d.send(w, m)
	}
}

// runPersistent drains the on-disk queue whenever kicked or every
// retryInterval; a failed entry is kept for the next pass. On close it
// makes one final pass (bounded by the dispatcher context).
func (d *Dispatcher) runPersistent(w *worker) {
	defer d.wg.Done()
	tick := time.NewTicker(retryInterval)
	defer tick.Stop()
	for {
		select {
		case <-w.done:
			d.drain(w)
			return
		case <-w.kick:
		case <-tick.C:
		}
		d.drain(w)
	}
}

func (d *Dispatcher) drain(w *worker) {
	for _, path := range w.queue.list() {
		if d.ctx.Err() != nil {
			return
		}
		m, err := w.queue.load(path)
		if err != nil {
			d.log.Warn("notification queue entry unreadable, discarding", "target", w.name, "file", path, "err", err)
			w.queue.remove(path)
			continue
		}
		if err := d.send(w, m); err != nil {
			if errors.Is(err, errPermanent) {
				w.queue.remove(path)
				continue
			}
			return // retry on the next pass
		}
		w.queue.remove(path)
		d.depth.WithLabelValues(w.name).Set(float64(w.queue.length()))
	}
}

var retryInterval = 15 * time.Second

// Close stops accepting events, waits up to DrainTimeout for the queues
// to drain, then closes the targets. Persistent queues keep undelivered
// entries on disk for the next start.
func (d *Dispatcher) Close() error {
	d.mu.Lock()
	if d.closed {
		d.mu.Unlock()
		return nil
	}
	d.closed = true
	for _, w := range d.targets {
		if w.ch != nil {
			close(w.ch) // in-memory workers drain the channel then exit
		}
		if w.done != nil {
			close(w.done) // persistent workers make a final pass then exit
		}
	}
	d.mu.Unlock()
	finished := make(chan struct{})
	go func() { d.wg.Wait(); close(finished) }()
	select {
	case <-finished:
	case <-time.After(d.cfg.DrainTimeout):
		d.log.Warn("notification queues not drained on close; aborting in-flight deliveries")
		d.cancel()
		select {
		case <-finished:
		case <-time.After(5 * time.Second):
			d.log.Warn("notification workers did not stop")
		}
	}
	d.cancel()
	var err error
	for _, w := range d.targets {
		if cerr := w.target.Close(); cerr != nil && err == nil {
			err = cerr
		}
	}
	return err
}
