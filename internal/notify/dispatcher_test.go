package notify

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/edward-b-1/opens3/internal/blob"
	"github.com/edward-b-1/opens3/internal/kms"
	"github.com/edward-b-1/opens3/internal/kv"
	"github.com/edward-b-1/opens3/internal/meta"
	"github.com/edward-b-1/opens3/internal/object"
)

var actor = object.Actor{CanonicalID: "owner", DisplayName: "owner"}

// counter reads opens3_notify_events_total{target,result} from reg.
func counter(t *testing.T, reg *prometheus.Registry, target, result string) float64 {
	t.Helper()
	fams, err := reg.Gather()
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range fams {
		if f.GetName() != "opens3_notify_events_total" {
			continue
		}
		for _, m := range f.GetMetric() {
			ok := 0
			for _, l := range m.GetLabel() {
				if (l.GetName() == "target" && l.GetValue() == target) || (l.GetName() == "result" && l.GetValue() == result) {
					ok++
				}
			}
			if ok == 2 {
				return m.GetCounter().GetValue()
			}
		}
	}
	return 0
}

func newService(t *testing.T) *object.Service {
	t.Helper()
	dir := t.TempDir()
	db, err := kv.OpenBolt(filepath.Join(dir, "meta.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	bs, err := blob.OpenFS(dir, false)
	if err != nil {
		t.Fatal(err)
	}
	k, err := kms.NewLocal(db, kms.TestMaster())
	if err != nil {
		t.Fatal(err)
	}
	return object.New(db, bs, k, "us-east-1", nil)
}

// hook collects webhook deliveries.
type hook struct {
	srv  *httptest.Server
	mu   sync.Mutex
	msgs []Message
	got  chan Message
	fail func() bool
}

func newHook(t *testing.T) *hook {
	h := &hook{got: make(chan Message, 100)}
	h.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if h.fail != nil && h.fail() {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		var m Message
		b, _ := io.ReadAll(r.Body)
		if err := json.Unmarshal(b, &m); err != nil {
			t.Errorf("bad body %s: %v", b, err)
		}
		h.mu.Lock()
		h.msgs = append(h.msgs, m)
		h.mu.Unlock()
		h.got <- m
	}))
	t.Cleanup(h.srv.Close)
	return h
}

func (h *hook) wait(t *testing.T) Message {
	t.Helper()
	select {
	case m := <-h.got:
		return m
	case <-time.After(10 * time.Second):
		t.Fatal("no webhook delivery")
		return Message{}
	}
}

const bucketXML = `<NotificationConfiguration>
  <QueueConfiguration><Id>created</Id><Queue>arn:opens3:sqs::hook:webhook</Queue><Event>s3:ObjectCreated:*</Event>
    <Filter><S3Key><FilterRule><Name>prefix</Name><Value>in/</Value></FilterRule></S3Key></Filter></QueueConfiguration>
  <QueueConfiguration><Id>deleted</Id><Queue>arn:minio:sqs::HOOK:webhook</Queue><Event>s3:ObjectRemoved:*</Event></QueueConfiguration>
</NotificationConfiguration>`

func TestDispatcherEndToEnd(t *testing.T) {
	h := newHook(t)
	var cfg Config
	cfg.Targets = append(cfg.Targets, TargetConfig{Name: "hook", Type: "webhook", Endpoint: h.srv.URL, AuthToken: "t"})
	cfg.AddTarget("logger", NewLog(nil))
	reg := prometheus.NewRegistry()
	d, err := New(cfg, nil, reg)
	if err != nil {
		t.Fatal(err)
	}
	// ARN validation hook.
	for _, arn := range []string{"arn:opens3:sqs::hook:webhook", "arn:minio:sqs:us-east-1:HOOK:webhook", "arn:aws:sqs:us-east-1:1:hook", "arn:opens3:sqs::logger:log"} {
		if err := d.ValidateTarget(arn); err != nil {
			t.Errorf("%s: %v", arn, err)
		}
	}
	for _, arn := range []string{"arn:opens3:sqs::nope:webhook", "arn:opens3:sqs::hook:kafka", "junk"} {
		if err := d.ValidateTarget(arn); err == nil {
			t.Errorf("%s: expected error", arn)
		}
	}

	svc := newService(t)
	svc.Notify = d.Handle
	ctx := context.Background()
	if _, err := svc.CreateBucket(ctx, actor, object.CreateBucketInput{Name: "bkt"}); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.UpdateBucket(ctx, "bkt", func(b *meta.Bucket) error { b.NotificationXML = []byte(bucketXML); return nil }); err != nil {
		t.Fatal(err)
	}
	put := func(key string) {
		t.Helper()
		data := []byte("hello")
		_, err := svc.PutObject(ctx, actor, object.PutInput{Bucket: "bkt", Key: key, Body: bytes.NewReader(data), Size: int64(len(data)),
			Attrs: object.ObjectAttrs{ContentType: "text/plain", UserMeta: map[string]string{"k": "v"}}})
		if err != nil {
			t.Fatal(err)
		}
	}
	put("in/a b.txt")
	m := h.wait(t)
	if len(m.Records) != 1 {
		t.Fatalf("records: %+v", m)
	}
	r := m.Records[0]
	if r.EventName != "ObjectCreated:Put" || r.S3.ConfigurationID != "created" || r.S3.Bucket.Name != "bkt" || r.S3.Object.Key != "in/a+b.txt" ||
		r.S3.Object.Size != 5 || r.S3.Object.ContentType != "text/plain" || r.S3.Object.VersionID != "null" || r.UserIdentity.PrincipalID != "owner" ||
		r.S3.Object.UserMetadata["X-Amz-Meta-K"] != "v" || r.S3.Object.Sequencer == "" {
		t.Fatalf("record: %+v", r)
	}
	// Outside the prefix: nothing.
	put("out/x")
	select {
	case m := <-h.got:
		t.Fatalf("unexpected delivery %+v", m)
	case <-time.After(200 * time.Millisecond):
	}
	// Delete matches the MinIO-style ARN rule.
	if _, err := svc.DeleteObject(ctx, actor, object.DeleteInput{Bucket: "bkt", Key: "out/x"}); err != nil {
		t.Fatal(err)
	}
	m = h.wait(t)
	if m.Records[0].EventName != "ObjectRemoved:Delete" || m.Records[0].S3.ConfigurationID != "deleted" || m.Records[0].S3.Object.Key != "out/x" {
		t.Fatalf("delete record: %+v", m.Records[0])
	}
	if err := d.Close(); err != nil {
		t.Fatal(err)
	}
	if n := counter(t, reg, "hook", "sent"); n != 2 {
		t.Fatalf("sent metric %v", n)
	}
	// Handle after Close is a no-op.
	svc.PutObject(ctx, actor, object.PutInput{Bucket: "bkt", Key: "in/late", Body: bytes.NewReader(nil), Size: 0})
}

func TestDispatcherDropsWhenFull(t *testing.T) {
	block := make(chan struct{})
	h := newHook(t)
	h.fail = func() bool { <-block; return false }
	var cfg Config
	cfg.Targets = []TargetConfig{{Name: "hook", Type: "webhook", Endpoint: h.srv.URL}}
	cfg.QueueSize, cfg.DrainTimeout = 1, 100*time.Millisecond
	reg := prometheus.NewRegistry()
	d, err := New(cfg, nil, reg)
	if err != nil {
		t.Fatal(err)
	}
	ev := object.Event{Name: "s3:ObjectCreated:Put", Bucket: &meta.Bucket{Name: "b", NotificationXML: []byte(bucketXML)}, Key: "in/x", Object: &meta.Object{Key: "in/x"}}
	for i := 0; i < 5; i++ {
		d.Handle(ev) // first is taken by the worker, second fills the queue, rest drop
	}
	if n := counter(t, reg, "hook", "dropped"); n < 2 {
		t.Fatalf("dropped %v", n)
	}
	close(block)
	d.Close()
}

func TestDispatcherPersistentQueue(t *testing.T) {
	h := newHook(t)
	failing := true
	h.fail = func() bool { return failing }
	dir := filepath.Join(t.TempDir(), "q")
	cfg := Config{Targets: []TargetConfig{{Name: "hook", Type: "webhook", Endpoint: h.srv.URL, QueueDir: dir, QueueLimit: 2}}, SendTimeout: 300 * time.Millisecond, DrainTimeout: time.Second}
	// Webhook retries would take too long: use a fast one.
	wh, _ := NewWebhook(WebhookConfig{Endpoint: h.srv.URL, Backoff: time.Millisecond, MaxRetries: 1})
	cfg.Targets[0].Target = wh
	reg := prometheus.NewRegistry()
	d, err := New(cfg, nil, reg)
	if err != nil {
		t.Fatal(err)
	}
	ev := func(key string) object.Event {
		return object.Event{Name: "s3:ObjectCreated:Put", Bucket: &meta.Bucket{Name: "b", NotificationXML: []byte(bucketXML)}, Key: key, Object: &meta.Object{Key: key}}
	}
	d.Handle(ev("in/1"))
	d.Handle(ev("in/2"))
	d.Handle(ev("in/3")) // over the limit: dropped
	time.Sleep(200 * time.Millisecond)
	if n := counter(t, reg, "hook", "dropped"); n != 1 {
		t.Fatalf("dropped %v", n)
	}
	d.Close()
	files, _ := os.ReadDir(dir)
	if len(files) != 2 {
		t.Fatalf("queued files after close: %d", len(files))
	}
	// Restart with a healthy endpoint: both entries replay in order.
	failing = false
	d, err = New(cfg, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	a, b := h.wait(t), h.wait(t)
	if a.Records[0].S3.Object.Key != "in/1" || b.Records[0].S3.Object.Key != "in/2" {
		t.Fatalf("replayed %s %s", a.Records[0].S3.Object.Key, b.Records[0].S3.Object.Key)
	}
	d.Close()
	if files, _ = os.ReadDir(dir); len(files) != 0 {
		t.Fatalf("files left: %d", len(files))
	}
}

func TestDispatcherErrors(t *testing.T) {
	if _, err := New(Config{Targets: []TargetConfig{{Name: "a", Type: "webhook", Endpoint: "bad"}}}, nil, nil); err == nil {
		t.Fatal("expected endpoint error")
	}
	if _, err := New(Config{Targets: []TargetConfig{{Name: "a", Type: "log"}, {Name: "A", Type: "log"}}}, nil, nil); err == nil {
		t.Fatal("expected duplicate error")
	}
	d, err := New(Config{}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := d.ValidateTarget("arn:opens3:sqs::x:webhook"); !errors.Is(err, ErrUnknownTarget) {
		t.Fatalf("got %v", err)
	}
	d.Handle(object.Event{Name: "s3:ObjectCreated:Put"})
	d.Close()
}
