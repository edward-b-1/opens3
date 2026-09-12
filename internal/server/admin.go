package server

import (
	"net/http"

	"gitlab.com/Birdsall/opens3/internal/admin"
)

// The admin REST API is mounted at /opens3/admin/v1/. See docs/ADMIN.md.
func init() {
	RegisterMount(func(s *Server, mux *http.ServeMux) {
		h := admin.New(admin.Options{IAM: s.IAM, KMS: s.KMS, Obj: s.Obj, Region: s.cfg.Region, EnforceRegion: s.cfg.EnforceRegion, Log: s.log.With("component", "admin")})
		mux.Handle(admin.Prefix+"/", h)
	})
}
