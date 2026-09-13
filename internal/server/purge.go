package server

import (
	"context"
	"os"
	"time"

	"github.com/prometheus/client_golang/prometheus"
)

// Expired temporary credentials (STS sessions, console sessions, expiring
// service-account keys) are rejected on use but stay in the database until
// swept. This sweeps on startup and then periodically.

const purgeExt = "purge"

type purger struct {
	cancel context.CancelFunc
	done   chan struct{}
	purged prometheus.Counter
}

func init() {
	RegisterExtension(func(s *Server) error {
		interval := time.Hour
		if v := os.Getenv("OPENS3_PURGE_INTERVAL"); v != "" {
			d, err := time.ParseDuration(v)
			if err != nil || d < time.Minute {
				s.log.Warn("invalid OPENS3_PURGE_INTERVAL, using 1h", "value", v)
			} else {
				interval = d
			}
		}
		p := &purger{done: make(chan struct{}), purged: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "opens3_iam_expired_credentials_purged_total", Help: "Expired temporary credentials removed by the periodic sweep."})}
		s.reg.MustRegister(p.purged)
		s.Ext[purgeExt] = p
		ctx, cancel := context.WithCancel(context.Background())
		p.cancel = cancel
		go p.run(ctx, s, interval)
		return nil
	})
	RegisterStopper(func(s *Server) {
		if p, ok := s.Ext[purgeExt].(*purger); ok {
			p.cancel()
			<-p.done
		}
	})
}

func (p *purger) run(ctx context.Context, s *Server, interval time.Duration) {
	defer close(p.done)
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		p.once(s)
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// once runs a single sweep and returns the number of records removed.
func (p *purger) once(s *Server) int {
	n, err := s.IAM.PurgeExpired()
	if err != nil {
		s.log.Warn("purge expired credentials", "err", err)
		return 0
	}
	if n > 0 {
		p.purged.Add(float64(n))
		s.log.Info("purged expired credentials", "count", n)
	}
	return n
}

// PurgeExpiredCredentials runs one sweep now (used by tests and the admin API).
func (s *Server) PurgeExpiredCredentials() int {
	if p, ok := s.Ext[purgeExt].(*purger); ok {
		return p.once(s)
	}
	n, _ := s.IAM.PurgeExpired()
	return n
}
