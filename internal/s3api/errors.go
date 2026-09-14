package s3api

import (
	"encoding/xml"
	"errors"
	"net/http"

	"github.com/edward-b-1/OpenS3/internal/s3err"
)

func errInternal() *s3err.Error { return s3err.New(s3err.InternalError) }
func errNotImplemented(msg string) *s3err.Error {
	return s3err.New(s3err.NotImplemented).WithMessage("%s", msg)
}
func errMethodNotAllowed() *s3err.Error { return s3err.New(s3err.MethodNotAllowed) }
func errAccessDenied() *s3err.Error     { return s3err.New(s3err.AccessDenied) }
func errInvalidArg(msg string) *s3err.Error {
	return s3err.New(s3err.InvalidArgument).WithMessage("%s", msg)
}
func errMalformedXML() *s3err.Error { return s3err.New(s3err.MalformedXML) }
func errInvalidRequest(msg string) *s3err.Error {
	return s3err.New(s3err.InvalidRequest).WithMessage("%s", msg)
}

// errorResponse is the S3 error XML body.
type errorResponse struct {
	XMLName    xml.Name `xml:"Error"`
	Code       string   `xml:"Code"`
	Message    string   `xml:"Message"`
	Resource   string   `xml:"Resource,omitempty"`
	BucketName string   `xml:"BucketName,omitempty"`
	Key        string   `xml:"Key,omitempty"`
	Extra      []xmlKV  `xml:",any"`
	RequestID  string   `xml:"RequestId"`
	HostID     string   `xml:"HostId"`
}

type xmlKV struct {
	XMLName xml.Name
	Value   string `xml:",chardata"`
}

// writeError renders err as an S3 error response.
func (s *Server) writeError(c *reqCtx, err error) {
	var e *s3err.Error
	if !errors.As(err, &e) {
		s.log.Error("internal error", "op", opName(c), "req", c.id, "err", err)
		e = errInternal()
	}
	if c.op != nil && c.op.name == "AWSQuery" {
		// Query-protocol clients (IAM/STS SDKs) expect the ErrorResponse shape.
		_ = s.stsError(c, string(e.Code), e.Message)
		return
	}
	h := c.w.Header()
	for k, v := range e.Headers {
		h.Set(k, v)
	}
	if e.Code == s3err.NotModified {
		c.w.WriteHeader(http.StatusNotModified)
		return
	}
	if c.r.Method == http.MethodHead {
		c.w.WriteHeader(e.Status)
		return
	}
	body := errorResponse{Code: string(e.Code), Message: e.Message, Resource: e.Resource, BucketName: e.BucketName, Key: e.Key, RequestID: c.id, HostID: s.cfg.HostID}
	if body.Resource == "" {
		body.Resource = c.r.URL.Path
	}
	for k, v := range e.Extra {
		body.Extra = append(body.Extra, xmlKV{XMLName: xml.Name{Local: k}, Value: v})
	}
	h.Set("Content-Type", "application/xml")
	h.Del("Content-Length")
	c.w.WriteHeader(e.Status)
	c.w.Write([]byte(xml.Header))
	xml.NewEncoder(c.w).Encode(body)
}

func opName(c *reqCtx) string {
	if c.op == nil {
		return ""
	}
	return c.op.name
}
