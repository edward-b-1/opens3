package s3api

import (
	"encoding/xml"
	"net/http"
	"strings"

	"github.com/edward-b-1/opens3/internal/policy"
	"github.com/edward-b-1/opens3/internal/s3err"
)

type corsConfig struct {
	rules []xmlCORSRule
}

func parseCORS(raw []byte) (*corsConfig, error) {
	if len(raw) == 0 {
		return nil, nil
	}
	var cfg xmlCORSConfiguration
	if err := xml.Unmarshal(raw, &cfg); err != nil {
		return nil, err
	}
	return &corsConfig{rules: cfg.Rules}, nil
}

func validateCORS(cfg *xmlCORSConfiguration) error {
	if len(cfg.Rules) == 0 || len(cfg.Rules) > 100 {
		return errMalformedXML()
	}
	for _, r := range cfg.Rules {
		if len(r.AllowedOrigins) == 0 || len(r.AllowedMethods) == 0 {
			return errMalformedXML()
		}
		for _, m := range r.AllowedMethods {
			switch m {
			case "GET", "PUT", "HEAD", "POST", "DELETE":
			default:
				return s3err.New(s3err.InvalidRequest).WithMessage("Found unsupported HTTP method in CORS config. Unsupported method is %s", m)
			}
		}
	}
	return nil
}

func (c *corsConfig) match(origin, method string, reqHeaders []string) *xmlCORSRule {
	for i := range c.rules {
		r := &c.rules[i]
		originOK := false
		for _, o := range r.AllowedOrigins {
			if o == "*" || policy.Match(o, origin) {
				originOK = true
				break
			}
		}
		if !originOK {
			continue
		}
		methodOK := false
		for _, m := range r.AllowedMethods {
			if m == method {
				methodOK = true
				break
			}
		}
		if !methodOK {
			continue
		}
		headersOK := true
		for _, h := range reqHeaders {
			h = strings.TrimSpace(h)
			if h == "" {
				continue
			}
			ok := false
			for _, a := range r.AllowedHeaders {
				if a == "*" || strings.EqualFold(a, h) || policy.Match(strings.ToLower(a), strings.ToLower(h)) {
					ok = true
					break
				}
			}
			if !ok {
				headersOK = false
				break
			}
		}
		if !headersOK {
			continue
		}
		return r
	}
	return nil
}

func (r *xmlCORSRule) apply(h http.Header, origin string) {
	allow := origin
	for _, o := range r.AllowedOrigins {
		if o == "*" {
			allow = "*"
		}
	}
	h.Set("Access-Control-Allow-Origin", allow)
	if len(r.AllowedMethods) > 0 {
		h.Set("Access-Control-Allow-Methods", strings.Join(r.AllowedMethods, ", "))
	}
	if allow != "*" {
		h.Set("Access-Control-Allow-Credentials", "true")
		h.Add("Vary", "Origin")
	}
	if len(r.ExposeHeaders) > 0 {
		h.Set("Access-Control-Expose-Headers", strings.Join(r.ExposeHeaders, ", "))
	}
}
