package notify

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/rand/v2"
	"net/http"
	"net/url"
	"time"
)

// WebhookConfig configures a Webhook target.
type WebhookConfig struct {
	Endpoint  string
	AuthToken string
	// Timeout bounds one HTTP attempt (default 10s).
	Timeout time.Duration
	// MaxRetries is the number of re-attempts after the first (default 5).
	MaxRetries int
	// Backoff is the first retry delay; it doubles per attempt up to
	// MaxBackoff (defaults 500ms and 30s).
	Backoff    time.Duration
	MaxBackoff time.Duration
	// Client overrides the HTTP client (tests).
	Client *http.Client
}

// Webhook POSTs each message as JSON to an HTTP(S) endpoint.
type Webhook struct {
	cfg    WebhookConfig
	client *http.Client
}

// NewWebhook validates the endpoint and returns a target.
func NewWebhook(cfg WebhookConfig) (*Webhook, error) {
	u, err := url.Parse(cfg.Endpoint)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return nil, fmt.Errorf("notify: webhook endpoint %q must be an http(s) URL", cfg.Endpoint)
	}
	if cfg.Timeout <= 0 {
		cfg.Timeout = 10 * time.Second
	}
	if cfg.MaxRetries < 0 {
		cfg.MaxRetries = 0
	} else if cfg.MaxRetries == 0 {
		cfg.MaxRetries = 5
	}
	if cfg.Backoff <= 0 {
		cfg.Backoff = 500 * time.Millisecond
	}
	if cfg.MaxBackoff <= 0 {
		cfg.MaxBackoff = 30 * time.Second
	}
	c := cfg.Client
	if c == nil {
		c = &http.Client{Timeout: cfg.Timeout}
	}
	return &Webhook{cfg: cfg, client: c}, nil
}

// Kind implements Kinder.
func (w *Webhook) Kind() string { return "webhook" }

// Send delivers m, retrying transient failures (network errors, 408/429
// and 5xx) with exponential backoff until ctx expires or the retry
// budget is spent.
func (w *Webhook) Send(ctx context.Context, m *Message) error {
	body, err := json.Marshal(m)
	if err != nil {
		return err
	}
	delay := w.cfg.Backoff
	var last error
	for attempt := 0; attempt <= w.cfg.MaxRetries; attempt++ {
		if attempt > 0 {
			d := delay + time.Duration(rand.Int64N(int64(delay)/2+1))
			select {
			case <-ctx.Done():
				return fmt.Errorf("%w (last error: %v)", ctx.Err(), last)
			case <-time.After(d):
			}
			if delay *= 2; delay > w.cfg.MaxBackoff {
				delay = w.cfg.MaxBackoff
			}
		}
		retry, err := w.post(ctx, body)
		if err == nil {
			return nil
		}
		last = err
		if !retry {
			return err
		}
	}
	return fmt.Errorf("notify: webhook %s: giving up after %d attempts: %w", w.cfg.Endpoint, w.cfg.MaxRetries+1, last)
}

func (w *Webhook) post(ctx context.Context, body []byte) (retry bool, err error) {
	actx, cancel := context.WithTimeout(ctx, w.cfg.Timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(actx, http.MethodPost, w.cfg.Endpoint, bytes.NewReader(body))
	if err != nil {
		return false, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "OpenS3 notify")
	if w.cfg.AuthToken != "" {
		req.Header.Set("Authorization", "Bearer "+w.cfg.AuthToken)
	}
	resp, err := w.client.Do(req)
	if err != nil {
		// The caller's context ending is final; per-attempt timeouts retry.
		if ctx.Err() != nil {
			return false, err
		}
		return true, err
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10))
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		return false, nil
	}
	err = fmt.Errorf("notify: webhook %s returned %s", w.cfg.Endpoint, resp.Status)
	switch {
	case resp.StatusCode >= 500, resp.StatusCode == http.StatusRequestTimeout, resp.StatusCode == http.StatusTooManyRequests:
		return true, err
	}
	return false, errors.Join(errPermanent, err)
}

var errPermanent = errors.New("permanent delivery failure")

// Close releases idle connections.
func (w *Webhook) Close() error {
	w.client.CloseIdleConnections()
	return nil
}
