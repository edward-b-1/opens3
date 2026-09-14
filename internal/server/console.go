package server

import (
	"net/http"

	"github.com/edward-b-1/opens3/internal/console"
)

// Version is reported by the console; cmd/opens3 may set it at start-up.
var Version = "dev"

func init() {
	RegisterExtension(func(s *Server) error {
		s.Ext["console"] = console.New(console.Deps{IAM: s.IAM, Obj: s.Obj, KMS: s.KMS, Log: s.Log(), Region: s.Config().Region, Version: Version})
		return nil
	})
	RegisterMount(func(s *Server, mux *http.ServeMux) {
		h, _ := s.Ext["console"].(*console.Handler)
		if h == nil {
			return
		}
		mux.Handle("/console/", h)
		mux.Handle("/console", h)
	})
}
