package lifecycle

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"sync"
	"time"

	"github.com/edward-b-1/opens3/internal/kv"
	"github.com/edward-b-1/opens3/internal/meta"
	"github.com/edward-b-1/opens3/internal/object"
	"github.com/edward-b-1/opens3/internal/s3err"
)

// Actor is the principal lifecycle actions are performed as.
var Actor = object.Actor{CanonicalID: "lifecycle", DisplayName: "OpenS3 Lifecycle"}

// Event names emitted through object.Service.Notify.
const (
	EventExpirationDelete             = "s3:LifecycleExpiration:Delete"
	EventExpirationDeleteMarkerCreate = "s3:LifecycleExpiration:DeleteMarkerCreated"
	EventTransition                   = "s3:LifecycleTransition"
)

// Options configure the worker.
type Options struct {
	// Interval between passes over all buckets (default 1h).
	Interval time.Duration
	// BatchSize bounds the number of actions collected per read
	// transaction before they are applied (default 1000).
	BatchSize int
	// MaxScan bounds the number of version records read per read
	// transaction (default 20000).
	MaxScan int
	// Now overrides the clock (tests).
	Now func() time.Time
}

// Stats counts what one pass did.
type Stats struct {
	Buckets              int // buckets with a lifecycle configuration
	Versions             int // version records examined
	Expired              int // versions permanently deleted
	DeleteMarkersCreated int // current versions expired in versioned buckets
	DeleteMarkersRemoved int // expired object delete markers removed
	Transitioned         int // storage-class changes recorded
	Aborted              int // multipart uploads aborted
	Skipped              int // actions refused (object lock, concurrent change)
	Errors               int // actions that failed
}

// Worker applies lifecycle configurations in the background.
type Worker struct {
	obj  *object.Service
	log  *slog.Logger
	opts Options

	mu     sync.Mutex
	cancel context.CancelFunc
	done   chan struct{}
}

// New creates a worker over the object service.
func New(obj *object.Service, log *slog.Logger, opts Options) *Worker {
	if log == nil {
		log = slog.Default()
	}
	if opts.Interval <= 0 {
		opts.Interval = time.Hour
	}
	if opts.BatchSize <= 0 {
		opts.BatchSize = 1000
	}
	if opts.MaxScan <= 0 {
		opts.MaxScan = 20000
	}
	if opts.Now == nil {
		opts.Now = time.Now
	}
	return &Worker{obj: obj, log: log.With("subsystem", "lifecycle"), opts: opts}
}

// Run executes a pass immediately and then every Interval until ctx is
// cancelled.
func (w *Worker) Run(ctx context.Context) {
	t := time.NewTicker(w.opts.Interval)
	defer t.Stop()
	for {
		if _, err := w.RunOnce(ctx); err != nil && ctx.Err() == nil {
			w.log.Error("lifecycle pass failed", "err", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// Start runs the worker in a goroutine until Stop is called.
func (w *Worker) Start() {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.cancel != nil {
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	w.cancel, w.done = cancel, make(chan struct{})
	go func(done chan struct{}) {
		defer close(done)
		w.Run(ctx)
	}(w.done)
}

// Stop cancels a started worker and waits for it to finish.
func (w *Worker) Stop() {
	w.mu.Lock()
	cancel, done := w.cancel, w.done
	w.cancel, w.done = nil, nil
	w.mu.Unlock()
	if cancel != nil {
		cancel()
		<-done
	}
}

// RunOnce performs a single pass over every bucket.
func (w *Worker) RunOnce(ctx context.Context) (Stats, error) {
	var st Stats
	buckets, err := w.obj.ListBuckets(ctx)
	if err != nil {
		return st, err
	}
	now := w.opts.Now().UTC()
	for _, b := range buckets {
		if err := ctx.Err(); err != nil {
			return st, err
		}
		if len(b.LifecycleXML) == 0 {
			continue
		}
		cfg, err := Parse(b.LifecycleXML)
		if err == nil {
			err = Validate(cfg)
		}
		if err != nil {
			w.log.Warn("invalid lifecycle configuration, skipping bucket", "bucket", b.Name, "err", err)
			continue
		}
		st.Buckets++
		before := st
		if err := w.processObjects(ctx, b, cfg, now, &st); err != nil {
			if ctx.Err() != nil {
				return st, err
			}
			w.log.Error("lifecycle object scan failed", "bucket", b.Name, "err", err)
			st.Errors++
		}
		if err := w.processUploads(ctx, b, cfg, now, &st); err != nil {
			if ctx.Err() != nil {
				return st, err
			}
			w.log.Error("lifecycle upload scan failed", "bucket", b.Name, "err", err)
			st.Errors++
		}
		if st.Expired+st.DeleteMarkersCreated+st.DeleteMarkersRemoved+st.Transitioned+st.Aborted+st.Skipped+st.Errors !=
			before.Expired+before.DeleteMarkersCreated+before.DeleteMarkersRemoved+before.Transitioned+before.Aborted+before.Skipped+before.Errors {
			w.log.Info("lifecycle pass", "bucket", b.Name,
				"expired", st.Expired-before.Expired, "delete_markers_created", st.DeleteMarkersCreated-before.DeleteMarkersCreated,
				"delete_markers_removed", st.DeleteMarkersRemoved-before.DeleteMarkersRemoved, "transitioned", st.Transitioned-before.Transitioned,
				"aborted", st.Aborted-before.Aborted, "skipped", st.Skipped-before.Skipped, "errors", st.Errors-before.Errors)
		}
	}
	return st, nil
}

// candidate is one version with the action decided for it.
type candidate struct {
	obj    *meta.Object
	action Action
}

// processObjects scans the bucket's version records in pages. Each page is
// read in its own read transaction (grouping the versions of a key so the
// rules can see current/noncurrent relationships) and the collected
// actions are applied after the transaction has ended, so the KV read
// transaction never overlaps the mutations.
func (w *Worker) processObjects(ctx context.Context, b *meta.Bucket, cfg *Config, now time.Time, st *Stats) error {
	resume := meta.ObjectBucketPrefix(b.Name)
	for resume != nil {
		if err := ctx.Err(); err != nil {
			return err
		}
		var batch []candidate
		var next []byte
		err := w.obj.KV().View(func(tx kv.Txn) error {
			var err error
			batch, next, err = w.collect(tx, b.Name, cfg, resume, now, st)
			return err
		})
		if err != nil {
			return err
		}
		for _, c := range batch {
			if err := ctx.Err(); err != nil {
				return err
			}
			w.apply(ctx, b, c, now, st)
		}
		resume = next
	}
	return nil
}

// collect reads version records starting at start and returns the actions
// for complete key groups, plus the key to resume from (nil when the
// bucket has been fully scanned).
func (w *Worker) collect(tx kv.Txn, bucket string, cfg *Config, start []byte, now time.Time, st *Stats) ([]candidate, []byte, error) {
	prefix := meta.ObjectBucketPrefix(bucket)
	var batch []candidate
	var group []*meta.Object
	var groupKey string
	scanned := 0
	flush := func() {
		if len(group) > 0 {
			batch = append(batch, w.evalGroup(cfg, group, now)...)
			group = nil
		}
	}
	it := tx.Seek(start)
	defer it.Close()
	for it.Valid() && bytes.HasPrefix(it.Key(), prefix) {
		_, key, _, ok := meta.SplitObjectKey(it.Key())
		if !ok {
			it.Next()
			continue
		}
		if key != groupKey && len(group) > 0 {
			flush()
			if len(batch) >= w.opts.BatchSize || scanned >= w.opts.MaxScan {
				// Resume after every version of the last complete key.
				return batch, kv.PrefixSuccessor(meta.ObjectPrefix(bucket, groupKey)), nil
			}
		}
		groupKey = key
		var o meta.Object
		if err := json.Unmarshal(it.Value(), &o); err != nil {
			return nil, nil, err
		}
		group = append(group, &o)
		scanned++
		st.Versions++
		it.Next()
	}
	flush()
	return batch, nil, nil
}

// evalGroup evaluates all versions of one key, newest first.
func (w *Worker) evalGroup(cfg *Config, versions []*meta.Object, now time.Time) []candidate {
	var out []candidate
	for i, v := range versions {
		in := EvalInput{Key: v.Key, Size: v.Size, Tags: v.Tags, ModTime: v.ModTime, StorageClass: v.StorageClass,
			IsLatest: i == 0, DeleteMarker: v.DeleteMarker, IsNull: v.IsNull(), HasOlderVersions: len(versions) > 1}
		if i > 0 {
			in.NoncurrentSince = versions[i-1].ModTime
			in.NewerNoncurrentCount = i - 1
		}
		if a := cfg.Eval(in, now); a.Kind != KindNone {
			out = append(out, candidate{obj: v, action: a})
		}
	}
	return out
}

// apply performs one action, counting the outcome in st.
func (w *Worker) apply(ctx context.Context, b *meta.Bucket, c candidate, now time.Time, st *Stats) {
	o := c.obj
	log := w.log.With("bucket", b.Name, "key", o.Key, "version", o.VersionID, "action", c.action.Kind.String(), "rule", c.action.RuleID)
	switch c.action.Kind {
	case KindExpireCurrent:
		// No VersionID: an unversioned bucket deletes permanently, a
		// versioned one inserts a delete marker. IfSeq guards against the
		// key having been overwritten since the scan (an ETag would not:
		// a same-content replacement shares it).
		res, err := w.obj.DeleteObject(ctx, Actor, object.DeleteInput{Bucket: b.Name, Key: o.Key, IfSeq: o.Seq})
		if err != nil {
			w.failed(log, err, st)
			return
		}
		name := EventExpirationDelete
		if res.DeleteMarker {
			st.DeleteMarkersCreated++
			name = EventExpirationDeleteMarkerCreate
		} else {
			st.Expired++
		}
		w.emit(object.Event{Name: name, Bucket: b, Object: o, Key: o.Key, VersionID: res.VersionID, Time: now})
		log.Debug("lifecycle expired current version", "delete_marker", res.DeleteMarker)
	case KindExpireNoncurrentVersion, KindRemoveDeleteMarker:
		if _, err := w.obj.DeleteObject(ctx, Actor, object.DeleteInput{Bucket: b.Name, Key: o.Key, VersionID: o.VersionID, IfSeq: o.Seq}); err != nil {
			w.failed(log, err, st)
			return
		}
		if c.action.Kind == KindRemoveDeleteMarker {
			st.DeleteMarkersRemoved++
		} else {
			st.Expired++
		}
		w.emit(object.Event{Name: EventExpirationDelete, Bucket: b, Object: o, Key: o.Key, VersionID: o.VersionID, Time: now})
		log.Debug("lifecycle deleted version")
	case KindTransition, KindNoncurrentTransition:
		sc := c.action.StorageClass
		upd, err := w.obj.UpdateObjectMeta(object.WithExpectedObject(ctx, o), b.Name, o.Key, o.VersionID, func(v *meta.Object) error {
			if classRank[sc] <= classRank[v.StorageClass] {
				return errAlreadyTransitioned
			}
			v.StorageClass = sc
			return nil
		})
		if err != nil {
			w.failed(log, err, st)
			return
		}
		st.Transitioned++
		w.emit(object.Event{Name: EventTransition, Bucket: b, Object: upd, Key: o.Key, VersionID: o.VersionID, Time: now})
		log.Debug("lifecycle transitioned version", "storage_class", sc)
	}
}

var errAlreadyTransitioned = errors.New("lifecycle: version already in a colder storage class")

// failed classifies an action error: refusals (object lock, the version
// changed or vanished since the scan) are expected and only logged at
// debug level; anything else is an error.
func (w *Worker) failed(log *slog.Logger, err error, st *Stats) {
	var e *s3err.Error
	if errors.As(err, &e) {
		switch e.Code {
		case s3err.AccessDenied, s3err.PreconditionFailed, s3err.NoSuchKey, s3err.NoSuchVersion, s3err.MethodNotAllowed, s3err.NoSuchBucket:
			st.Skipped++
			log.Debug("lifecycle action skipped", "err", err)
			return
		}
	}
	if errors.Is(err, errAlreadyTransitioned) {
		st.Skipped++
		log.Debug("lifecycle action skipped", "err", err)
		return
	}
	st.Errors++
	log.Error("lifecycle action failed", "err", err)
}

func (w *Worker) emit(e object.Event) {
	if w.obj.Notify == nil {
		return
	}
	e.Actor = Actor
	w.obj.Notify(e)
}

// processUploads aborts multipart uploads that have outlived their rule.
// Candidates are collected under a read transaction in bounded batches
// and aborted after it ends; the scan repeats while batches come back
// full, because aborted uploads no longer appear.
func (w *Worker) processUploads(ctx context.Context, b *meta.Bucket, cfg *Config, now time.Time, st *Stats) error {
	hasAbort := false
	for _, r := range cfg.Rules {
		if r.Status == "Enabled" && r.AbortIncompleteMultipartUpload != nil {
			hasAbort = true
		}
	}
	if !hasAbort {
		return nil
	}
	type up struct{ key, id string }
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		var batch []up
		err := w.obj.KV().View(func(tx kv.Txn) error {
			return meta.ScanBucketUploads(tx, b.Name, func(u *meta.Upload) bool {
				if cfg.AbortAfter(u.Initiated, u.Key, now) {
					batch = append(batch, up{u.Key, u.UploadID})
				}
				return len(batch) < w.opts.BatchSize
			})
		})
		if err != nil {
			return err
		}
		failed := false
		for _, u := range batch {
			if err := ctx.Err(); err != nil {
				return err
			}
			err := w.obj.AbortUpload(ctx, b.Name, u.key, u.id)
			var e *s3err.Error
			switch {
			case err == nil:
				st.Aborted++
				w.log.Debug("lifecycle aborted multipart upload", "bucket", b.Name, "key", u.key, "upload", u.id)
			case errors.As(err, &e) && e.Code == s3err.NoSuchUpload:
				st.Skipped++
			default:
				failed = true
				st.Errors++
				w.log.Error("lifecycle abort upload failed", "bucket", b.Name, "key", u.key, "upload", u.id, "err", err)
			}
		}
		if len(batch) < w.opts.BatchSize || failed {
			return nil
		}
	}
}
