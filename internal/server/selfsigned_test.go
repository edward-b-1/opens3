package server

import (
	"crypto/x509"
	"net"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"testing"
	"time"
)

func TestSelfSignedNames(t *testing.T) {
	dns, ips := selfSignedNames("10.1.2.3:9000", []string{"s3.example.com", " ", "S3.Internal"})
	host, _ := os.Hostname()
	for _, want := range []string{"localhost", host, "s3.example.com", "*.s3.example.com", "s3.internal", "*.s3.internal"} {
		if !slices.Contains(dns, want) {
			t.Fatalf("dns names lack %q: %v", want, dns)
		}
	}
	has := func(ip string) bool {
		return slices.ContainsFunc(ips, func(x net.IP) bool { return x.String() == ip })
	}
	if !has("127.0.0.1") || !has("::1") || !has("10.1.2.3") {
		t.Fatalf("ip names: %v", ips)
	}
	// An unspecified listen address adds nothing; a host name in it is a DNS name.
	dns, ips2 := selfSignedNames(":9000", nil)
	if len(ips2) != len(ips)-1 {
		t.Fatalf("unspecified address added an IP: %v", ips2)
	}
	dns, _ = selfSignedNames("s3.lan:9000", nil)
	if !slices.Contains(dns, "s3.lan") {
		t.Fatalf("listen host name missing: %v", dns)
	}
}

func TestSelfSignedLifecycle(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "tls")
	now := time.Date(2026, 9, 14, 12, 0, 0, 0, time.UTC)

	ss, err := loadOrCreateSelfSigned(dir, "127.0.0.1:9000", []string{"s3.example.com"}, now)
	if err != nil {
		t.Fatal(err)
	}
	if !ss.created || ss.renewed || len(ss.missing) != 0 {
		t.Fatalf("first call: %+v", ss)
	}
	if st, _ := os.Stat(filepath.Join(dir, "tls.key")); st.Mode().Perm() != 0o600 {
		t.Fatalf("key mode %v", st.Mode().Perm())
	}
	if st, _ := os.Stat(filepath.Join(dir, "tls.crt")); st.Mode().Perm() != 0o644 {
		t.Fatalf("cert mode %v", st.Mode().Perm())
	}
	if !regexp.MustCompile(`^([0-9A-F]{2}:){31}[0-9A-F]{2}$`).MatchString(ss.fingerprint) {
		t.Fatalf("fingerprint %q", ss.fingerprint)
	}
	if !ss.leaf.IsCA || ss.leaf.NotAfter.Sub(now) != selfSignedValidity || ss.leaf.VerifyHostname("bucket.s3.example.com") != nil || ss.leaf.VerifyHostname("127.0.0.1") != nil {
		t.Fatalf("certificate: ca=%v not_after=%v names=%v", ss.leaf.IsCA, ss.leaf.NotAfter, ss.names())
	}
	// The certificate verifies against itself as the only root, which is
	// how clients pass the file as a CA bundle.
	pool := x509.NewCertPool()
	pool.AddCert(ss.leaf)
	if _, err := ss.leaf.Verify(x509.VerifyOptions{Roots: pool, DNSName: "localhost", CurrentTime: now}); err != nil {
		t.Fatalf("self verification: %v", err)
	}

	// Reused on the next start, with the same fingerprint; a new name the
	// server now answers to is reported as missing.
	again, err := loadOrCreateSelfSigned(dir, "127.0.0.1:9000", []string{"s3.example.com", "other.example.org"}, now.Add(24*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if again.created || again.renewed || again.fingerprint != ss.fingerprint {
		t.Fatalf("second call: created=%v renewed=%v fp equal=%v", again.created, again.renewed, again.fingerprint == ss.fingerprint)
	}
	if !slices.Equal(again.missing, []string{"*.other.example.org", "other.example.org"}) {
		t.Fatalf("missing: %v", again.missing)
	}

	// After expiry it is regenerated in place.
	renewed, err := loadOrCreateSelfSigned(dir, "127.0.0.1:9000", nil, now.Add(selfSignedValidity+time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if !renewed.renewed || renewed.created || renewed.fingerprint == ss.fingerprint {
		t.Fatalf("renewal: %+v", renewed)
	}
	if _, err := os.Stat(filepath.Join(dir, "tls.crt.tmp")); !os.IsNotExist(err) {
		t.Fatal("temporary file left behind")
	}

	// A damaged file is an error, not silently replaced.
	os.WriteFile(filepath.Join(dir, "tls.crt"), []byte("garbage"), 0o644)
	if _, err := loadOrCreateSelfSigned(dir, "127.0.0.1:9000", nil, now); err == nil {
		t.Fatal("malformed certificate accepted")
	}
}

func TestTLSConfigValidation(t *testing.T) {
	base := Config{Root: t.TempDir(), RootUser: "root", RootPassword: "rootsecret", NoFsync: true}
	for _, tc := range []struct {
		name string
		mod  func(*Config)
	}{
		{"unknown mode", func(c *Config) { c.TLS = "acme" }},
		{"self-signed with files", func(c *Config) { c.TLS = "self-signed"; c.TLSCert = "a"; c.TLSKey = "b" }},
		{"cert without key", func(c *Config) { c.TLSCert = "a" }},
	} {
		c := base
		tc.mod(&c)
		if _, err := New(c); err == nil {
			t.Fatalf("%s: accepted", tc.name)
		}
	}
	c := base
	c.TLS = "off"
	s, err := New(c)
	if err != nil {
		t.Fatal(err)
	}
	s.Close()
	if s.Addr() != nil {
		t.Fatal("address before listening")
	}
}
