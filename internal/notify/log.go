package notify

import (
	"context"
	"log/slog"
)

// Log is a target that writes each record to slog (tests, debugging).
type Log struct {
	log   *slog.Logger
	level slog.Level
}

// NewLog returns a Log target; a nil logger uses slog.Default.
func NewLog(l *slog.Logger) *Log {
	if l == nil {
		l = slog.Default()
	}
	return &Log{log: l, level: slog.LevelInfo}
}

// Kind implements Kinder.
func (l *Log) Kind() string { return "log" }

// Send logs every record.
func (l *Log) Send(ctx context.Context, m *Message) error {
	for _, r := range m.Records {
		l.log.Log(ctx, l.level, "s3 event", "event", r.EventName, "bucket", r.S3.Bucket.Name, "key", r.S3.Object.Key,
			"versionId", r.S3.Object.VersionID, "size", r.S3.Object.Size, "configurationId", r.S3.ConfigurationID)
	}
	return nil
}

// Close is a no-op.
func (l *Log) Close() error { return nil }
