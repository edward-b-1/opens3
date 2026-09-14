package server

import "testing"

func TestConfigFromEnvPrecedence(t *testing.T) {
	t.Setenv("OPENS3_ROOT", "/from/env")
	t.Setenv("OPENS3_ADDRESS", ":1234")
	t.Setenv("OPENS3_TLS", "self-signed")
	t.Setenv("OPENS3_ROOT_USER", "envroot")
	cfg := Config{Root: "./data", Address: ":9000", TLS: ""} // flag defaults
	// Nothing typed: the environment overrides the defaults.
	got := ConfigFromEnv(cfg, nil)
	if got.Root != "/from/env" || got.Address != ":1234" || got.TLS != "self-signed" || got.RootUser != "envroot" {
		t.Fatalf("env over defaults: %+v", got)
	}
	// Flags typed for root and tls win; address still comes from the environment.
	cfg.Root, cfg.TLS = "/typed", "off"
	got = ConfigFromEnv(cfg, map[string]bool{"root": true, "tls": true})
	if got.Root != "/typed" || got.TLS != "off" || got.Address != ":1234" {
		t.Fatalf("flags over env: %+v", got)
	}
}
