package proxy

import (
	"crypto/tls"
	"net/http/httptest"
	"testing"
)

func TestParseAndContains(t *testing.T) {
	if _, err := Parse([]string{"not-an-ip"}); err == nil {
		t.Fatal("bad entry accepted")
	}
	if _, err := Parse([]string{"10.0.0.0/33"}); err == nil {
		t.Fatal("bad cidr accepted")
	}
	tr, err := Parse([]string{"10.0.0.5", " 192.168.0.0/16 ", "", "::1"})
	if err != nil {
		t.Fatal(err)
	}
	for addr, want := range map[string]bool{
		"10.0.0.5:1234": true, "10.0.0.6:1234": false, "192.168.7.8:9": true, "[::1]:80": true, "[::2]:80": false, "garbage": false,
	} {
		if tr.Contains(addr) != want {
			t.Errorf("Contains(%q) = %v", addr, !want)
		}
	}
	var none Trusted
	if none.Contains("10.0.0.5:1") {
		t.Fatal("empty set trusts an address")
	}
}

func TestSecureAndClientIP(t *testing.T) {
	tr, _ := Parse([]string{"10.0.0.5"})
	req := func(remote string, hdr map[string]string) *httptest.ResponseRecorder {
		return nil
	}
	_ = req
	mk := func(remote string, proto, xff string) *httptest.ResponseRecorder {
		return nil
	}
	_ = mk
	r := httptest.NewRequest("GET", "/", nil)
	r.RemoteAddr = "10.0.0.5:5000"
	r.Header.Set("X-Forwarded-Proto", "https")
	r.Header.Set("X-Forwarded-For", "203.0.113.9, 198.51.100.2")
	if !tr.Secure(r) || tr.ClientIP(r) != "198.51.100.2" {
		t.Fatalf("trusted proxy: secure=%v ip=%s", tr.Secure(r), tr.ClientIP(r))
	}
	// The same headers from an untrusted address mean nothing.
	r.RemoteAddr = "203.0.113.9:5000"
	if tr.Secure(r) || tr.ClientIP(r) != "203.0.113.9" {
		t.Fatalf("untrusted peer: secure=%v ip=%s", tr.Secure(r), tr.ClientIP(r))
	}
	var none Trusted
	if none.Secure(r) {
		t.Fatal("no trusted proxies but header believed")
	}
	// A real TLS connection is secure regardless.
	r.TLS = &tls.ConnectionState{}
	if !none.Secure(r) {
		t.Fatal("TLS connection not secure")
	}
	// A bad forwarded-for value falls back to the peer.
	r.TLS = nil
	r.RemoteAddr = "10.0.0.5:5000"
	r.Header.Set("X-Forwarded-For", "not an ip")
	if tr.ClientIP(r) != "10.0.0.5" {
		t.Fatalf("bad xff: %s", tr.ClientIP(r))
	}
}
