package notify

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

func TestWebhookRetry(t *testing.T) {
	var calls atomic.Int32
	var gotAuth, gotCT string
	var body []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := calls.Add(1)
		if n < 3 {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		gotAuth, gotCT = r.Header.Get("Authorization"), r.Header.Get("Content-Type")
		body, _ = io.ReadAll(r.Body)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()
	wh, err := NewWebhook(WebhookConfig{Endpoint: srv.URL, AuthToken: "tok", Backoff: time.Millisecond, MaxRetries: 5})
	if err != nil {
		t.Fatal(err)
	}
	defer wh.Close()
	m := &Message{Records: []Record{{EventName: "ObjectCreated:Put"}}}
	if err := wh.Send(context.Background(), m); err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 3 || gotAuth != "Bearer tok" || gotCT != "application/json" {
		t.Fatalf("calls=%d auth=%q ct=%q", calls.Load(), gotAuth, gotCT)
	}
	var got Message
	if err := json.Unmarshal(body, &got); err != nil || len(got.Records) != 1 || got.Records[0].EventName != "ObjectCreated:Put" {
		t.Fatalf("body %s: %v", body, err)
	}
}

func TestWebhookGivesUp(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusBadGateway)
	}))
	defer srv.Close()
	wh, _ := NewWebhook(WebhookConfig{Endpoint: srv.URL, Backoff: time.Millisecond, MaxRetries: 2})
	if err := wh.Send(context.Background(), &Message{}); err == nil || calls.Load() != 3 {
		t.Fatalf("err=%v calls=%d", err, calls.Load())
	}
	// 4xx is permanent: one attempt.
	calls.Store(0)
	srv2 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls.Add(1); w.WriteHeader(http.StatusForbidden) }))
	defer srv2.Close()
	wh, _ = NewWebhook(WebhookConfig{Endpoint: srv2.URL, Backoff: time.Millisecond})
	if err := wh.Send(context.Background(), &Message{}); !errors.Is(err, errPermanent) || calls.Load() != 1 {
		t.Fatalf("err=%v calls=%d", err, calls.Load())
	}
	// Cancelled context stops retrying.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	wh, _ = NewWebhook(WebhookConfig{Endpoint: srv.URL, Backoff: time.Second})
	if err := wh.Send(ctx, &Message{}); err == nil {
		t.Fatal("expected error")
	}
	if _, err := NewWebhook(WebhookConfig{Endpoint: "ftp://x"}); err == nil {
		t.Fatal("expected scheme error")
	}
}
