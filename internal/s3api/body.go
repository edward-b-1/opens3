package s3api

import (
	"io"
	"net/http"

	"gitlab.com/Birdsall/opens3/internal/auth/sigv4"
)

// bodyReader wraps the request body, decoding aws-chunked streams and
// counting bytes.
type bodyReader struct {
	r       io.Reader
	chunked *sigv4.ChunkedReader
	n       int64
	// size is the declared decoded length (-1 if unknown)
	size           int64
	expectedSHA256 string
}

func (b *bodyReader) Read(p []byte) (int, error) {
	n, err := b.r.Read(p)
	b.n += int64(n)
	return n, err
}

// Trailer returns trailing headers once the body is consumed.
func (b *bodyReader) Trailer() http.Header {
	if b.chunked != nil {
		return b.chunked.Trailer()
	}
	return nil
}

// expectedSHA256 is set when x-amz-content-sha256 carries a real digest
// that the object write path must verify.
func (b *bodyReader) setExpectedSHA256(v string) { b.expectedSHA256 = v }
