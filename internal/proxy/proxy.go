// Package proxy decides when forwarded headers may be believed. A reverse
// proxy that terminates TLS talks plain HTTP to the server and describes
// the client's connection in X-Forwarded-Proto and X-Forwarded-For; those
// headers are only meaningful when the connection really comes from that
// proxy, so they are honoured only from addresses the operator lists
// (OPENS3_TRUSTED_PROXIES). With no trusted proxies, every connection is
// judged by itself: TLS or not, and the peer address as seen.
package proxy

import (
	"fmt"
	"net"
	"net/http"
	"strings"
)

// Trusted is the set of proxy addresses whose forwarded headers are
// believed.
type Trusted []*net.IPNet

// Parse accepts IP addresses and CIDR ranges ("10.0.0.5", "10.0.0.0/8",
// "::1"); blanks are ignored.
func Parse(entries []string) (Trusted, error) {
	var t Trusted
	for _, e := range entries {
		e = strings.TrimSpace(e)
		if e == "" {
			continue
		}
		if !strings.Contains(e, "/") {
			ip := net.ParseIP(e)
			if ip == nil {
				return nil, fmt.Errorf("trusted proxy %q is not an IP address or CIDR range", e)
			}
			bits := 32
			if ip.To4() == nil {
				bits = 128
			}
			t = append(t, &net.IPNet{IP: ip, Mask: net.CIDRMask(bits, bits)})
			continue
		}
		_, n, err := net.ParseCIDR(e)
		if err != nil {
			return nil, fmt.Errorf("trusted proxy %q: %v", e, err)
		}
		t = append(t, n)
	}
	return t, nil
}

// Contains reports whether remoteAddr ("host:port" or "host") is a
// trusted proxy.
func (t Trusted) Contains(remoteAddr string) bool {
	if len(t) == 0 {
		return false
	}
	host, _, err := net.SplitHostPort(remoteAddr)
	if err != nil {
		host = remoteAddr
	}
	ip := net.ParseIP(host)
	if ip == nil {
		return false
	}
	for _, n := range t {
		if n.Contains(ip) {
			return true
		}
	}
	return false
}

// Secure reports whether the client's connection is encrypted: this
// connection uses TLS, or a trusted proxy says the client's does.
func (t Trusted) Secure(r *http.Request) bool {
	if r.TLS != nil {
		return true
	}
	return t.Contains(r.RemoteAddr) && strings.EqualFold(r.Header.Get("X-Forwarded-Proto"), "https")
}

// ClientIP is the client's address: from a trusted proxy, the last
// X-Forwarded-For entry (the address that proxy accepted the connection
// from); otherwise the peer address.
func (t Trusted) ClientIP(r *http.Request) string {
	if t.Contains(r.RemoteAddr) {
		if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
			parts := strings.Split(xff, ",")
			if ip := strings.TrimSpace(parts[len(parts)-1]); net.ParseIP(ip) != nil {
				return ip
			}
		}
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}
