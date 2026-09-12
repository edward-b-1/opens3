// Package notify delivers S3 bucket event notifications to operator
// configured targets (webhooks today; Kafka/NATS/AMQP/MQTT/Redis/Postgres
// later by implementing Target).
//
// The object service emits an object.Event after every successful
// mutation; Dispatcher.Handle matches it against the bucket's
// NotificationConfiguration, builds the AWS event record and hands it to
// the per-target queue. The request path never blocks on delivery.
package notify

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Target delivers event messages somewhere. Implementations must be safe
// for concurrent use; Send should honour ctx and is expected to do its own
// retries so that delivery is at-least-once (the dispatcher retries only
// via the persistent queue).
type Target interface {
	Send(ctx context.Context, m *Message) error
	Close() error
}

// Kinder is optionally implemented by targets to report their type
// ("webhook", "kafka", …). The type token in an opens3/minio ARN must
// match it.
type Kinder interface {
	Kind() string
}

// TargetConfig describes one notification target. Either Target is set
// (programmatic registration) or Type/Endpoint describe a target that
// New instantiates.
type TargetConfig struct {
	Name string
	Type string // webhook | log | …
	// Webhook settings.
	Endpoint  string
	AuthToken string
	// QueueDir enables the persistent on-disk queue for this target;
	// QueueLimit caps the number of queued events (0 = unlimited).
	QueueDir   string
	QueueLimit int
	// Target is a ready-made implementation (from AddTarget).
	Target Target
}

// Config is the dispatcher configuration.
type Config struct {
	Targets []TargetConfig
	// Region is the awsRegion used when the bucket record has none.
	Region string
	// QueueSize is the in-memory queue length per target (default 10000).
	QueueSize int
	// Workers is the number of delivery goroutines per in-memory target
	// (default 1, which preserves event order).
	Workers int
	// SendTimeout bounds one Send call including retries (default 2m).
	SendTimeout time.Duration
	// DrainTimeout bounds Close (default 10s).
	DrainTimeout time.Duration
}

// AddTarget registers a programmatic target under name.
func (c *Config) AddTarget(name string, t Target) {
	kind := "custom"
	if k, ok := t.(Kinder); ok {
		kind = k.Kind()
	}
	c.Targets = append(c.Targets, TargetConfig{Name: name, Type: kind, Target: t})
}

// AddQueuedTarget is AddTarget with a persistent on-disk queue.
func (c *Config) AddQueuedTarget(name string, t Target, queueDir string, limit int) {
	c.AddTarget(name, t)
	tc := &c.Targets[len(c.Targets)-1]
	tc.QueueDir, tc.QueueLimit = queueDir, limit
}

const envPrefix = "OPENS3_NOTIFY_"

// ConfigFromEnv builds a Config from the process environment.
//
//	OPENS3_NOTIFY_WEBHOOK_<NAME>_ENDPOINT=https://…   (required)
//	OPENS3_NOTIFY_WEBHOOK_<NAME>_AUTH_TOKEN=…         (optional)
//	OPENS3_NOTIFY_WEBHOOK_<NAME>_QUEUE_DIR=/path      (optional)
//	OPENS3_NOTIFY_WEBHOOK_<NAME>_QUEUE_LIMIT=100000   (optional)
func ConfigFromEnv() (Config, error) { return ParseEnv(os.Environ()) }

// suffixes are ordered longest first so that a name containing an
// underscore is split correctly.
var envSuffixes = []string{"_QUEUE_LIMIT", "_AUTH_TOKEN", "_QUEUE_DIR", "_ENDPOINT"}

// ParseEnv parses KEY=VALUE pairs (see ConfigFromEnv).
func ParseEnv(environ []string) (Config, error) {
	var cfg Config
	byName := map[string]*TargetConfig{}
	var names []string
	for _, kv := range environ {
		k, v, ok := strings.Cut(kv, "=")
		if !ok || !strings.HasPrefix(k, envPrefix) {
			continue
		}
		rest := strings.TrimPrefix(k, envPrefix)
		typ, rest, ok := strings.Cut(rest, "_")
		if !ok {
			continue
		}
		var suffix string
		for _, s := range envSuffixes {
			if strings.HasSuffix(rest, s) {
				suffix, rest = s, strings.TrimSuffix(rest, s)
				break
			}
		}
		if suffix == "" || rest == "" {
			return cfg, fmt.Errorf("notify: unrecognised environment variable %s", k)
		}
		if typ != "WEBHOOK" {
			return cfg, fmt.Errorf("notify: unsupported target type %q in %s", strings.ToLower(typ), k)
		}
		name := rest
		tc := byName[name]
		if tc == nil {
			tc = &TargetConfig{Name: name, Type: "webhook"}
			byName[name] = tc
			names = append(names, name)
		}
		switch suffix {
		case "_ENDPOINT":
			tc.Endpoint = v
		case "_AUTH_TOKEN":
			tc.AuthToken = v
		case "_QUEUE_DIR":
			tc.QueueDir = v
		case "_QUEUE_LIMIT":
			n, err := strconv.Atoi(v)
			if err != nil || n < 0 {
				return cfg, fmt.Errorf("notify: %s must be a non-negative integer", k)
			}
			tc.QueueLimit = n
		}
	}
	sort.Strings(names)
	for _, n := range names {
		tc := byName[n]
		if tc.Endpoint == "" {
			return cfg, fmt.Errorf("notify: %sWEBHOOK_%s_ENDPOINT is required", envPrefix, n)
		}
		cfg.Targets = append(cfg.Targets, *tc)
	}
	return cfg, nil
}

// ErrUnknownTarget is returned when an ARN does not reference a configured
// target.
var ErrUnknownTarget = errors.New("notify: unknown target")
