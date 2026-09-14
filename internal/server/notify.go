package server

import (
	"github.com/edward-b-1/opens3/internal/notify"
)

// Notification subsystem: targets come from OPENS3_NOTIFY_* environment
// variables (see docs/NOTIFICATIONS.md); bucket configurations are set by
// clients through PutBucketNotificationConfiguration.
func init() {
	RegisterExtension(func(s *Server) error {
		cfg, err := notify.ConfigFromEnv()
		if err != nil {
			return err
		}
		cfg.Region = s.cfg.Region
		d, err := notify.New(cfg, s.Log().With("subsystem", "notify"), s.Registry)
		if err != nil {
			return err
		}
		s.Obj.Notify = d.Handle
		s.API.ValidateTarget = d.ValidateTarget
		s.Ext["notify"] = d
		return nil
	})
	RegisterStopper(func(s *Server) {
		if d, ok := s.Ext["notify"].(*notify.Dispatcher); ok {
			if err := d.Close(); err != nil {
				s.Log().Warn("notify close", "err", err)
			}
		}
	})
}
