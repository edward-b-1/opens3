package server

import (
	"fmt"
	"os"
	"time"

	"github.com/edward-b-1/opens3/internal/lifecycle"
)

// The lifecycle worker runs expiration, delete-marker cleanup, multipart
// abort and storage-class transitions in the background. The pass
// interval comes from OPENS3_LIFECYCLE_INTERVAL (a Go duration such as
// "1h" or "5m"; default 1h); "0" or "off" disables the worker.
func init() {
	RegisterExtension(func(s *Server) error {
		opts := lifecycle.Options{}
		if v := os.Getenv("OPENS3_LIFECYCLE_INTERVAL"); v != "" {
			if v == "0" || v == "off" || v == "false" {
				s.log.Info("lifecycle worker disabled by OPENS3_LIFECYCLE_INTERVAL")
				return nil
			}
			d, err := time.ParseDuration(v)
			if err != nil || d <= 0 {
				return fmt.Errorf("OPENS3_LIFECYCLE_INTERVAL: invalid duration %q", v)
			}
			opts.Interval = d
		}
		w := lifecycle.New(s.Obj, s.Log(), opts)
		s.Ext["lifecycle"] = w
		w.Start()
		return nil
	})
	RegisterStopper(func(s *Server) {
		if w, ok := s.Ext["lifecycle"].(*lifecycle.Worker); ok {
			w.Stop()
		}
	})
}
