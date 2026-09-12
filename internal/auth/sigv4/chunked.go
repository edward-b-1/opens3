package sigv4

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// Chunked-payload errors.
var (
	ErrChunkMalformed = errors.New("sigv4: malformed aws-chunked payload")
	ErrChunkSignature = errors.New("sigv4: chunk signature mismatch")
	ErrTrailerMissing = errors.New("sigv4: expected trailer not present")
)

const maxChunkHeaderLine = 4096

// ChunkedReader decodes the aws-chunked transfer encoding used by
// streaming SigV4 uploads. It verifies per-chunk signatures for the signed
// variants and collects trailing headers (checksums) for the trailer
// variants. Trailers are available via Trailer() after io.EOF.
type ChunkedReader struct {
	br              *bufio.Reader
	signed          bool
	hasTrailer      bool
	key             []byte
	prevSig         string
	dateScope       string // "<date>\n<scope>\n"
	remaining       int64  // bytes left in current chunk
	chunkHash       [32]byte
	hasher          hashAcc
	inChunk         bool
	eof             bool
	trailer         http.Header
	expectedTrailer []string // lower-case header names announced in x-amz-trailer
	pendingSig      string
}

type hashAcc struct {
	h interface {
		io.Writer
		Sum([]byte) []byte
		Reset()
	}
}

// NewChunkedReader wraps body. mode is the x-amz-content-sha256 value.
// seedSignature and signingKey are needed for the signed variants.
func NewChunkedReader(body io.Reader, mode string, seedSignature string, signingKey []byte, date time.Time, scope string, trailerHeader string) (*ChunkedReader, error) {
	c := &ChunkedReader{br: bufio.NewReaderSize(body, 64*1024), key: signingKey, prevSig: seedSignature,
		dateScope: date.Format(timeFormat) + "\n" + scope + "\n", trailer: http.Header{}}
	switch mode {
	case StreamingSigned:
		c.signed = true
	case StreamingSignedTrailer:
		c.signed, c.hasTrailer = true, true
	case StreamingUnsignedTrailer:
		c.hasTrailer = true
	default:
		return nil, fmt.Errorf("sigv4: not a streaming mode: %s", mode)
	}
	if trailerHeader != "" {
		for _, h := range strings.Split(trailerHeader, ",") {
			c.expectedTrailer = append(c.expectedTrailer, strings.ToLower(strings.TrimSpace(h)))
		}
	}
	c.hasher.h = sha256.New()
	return c, nil
}

// Trailer returns the trailing headers (valid after Read returned io.EOF).
func (c *ChunkedReader) Trailer() http.Header { return c.trailer }

func (c *ChunkedReader) Read(p []byte) (int, error) {
	if c.eof {
		return 0, io.EOF
	}
	for {
		if c.inChunk && c.remaining > 0 {
			if int64(len(p)) > c.remaining {
				p = p[:c.remaining]
			}
			n, err := c.br.Read(p)
			c.remaining -= int64(n)
			if c.signed {
				c.hasher.h.Write(p[:n])
			}
			if err != nil && err != io.EOF {
				return n, err
			}
			if n == 0 && err == io.EOF {
				return 0, ErrChunkMalformed
			}
			return n, nil
		}
		if c.inChunk {
			// End of chunk data: expect CRLF, verify signature.
			if err := c.expectCRLF(); err != nil {
				return 0, err
			}
			if c.signed {
				if err := c.verifyChunk(); err != nil {
					return 0, err
				}
			}
			c.inChunk = false
		}
		size, sig, err := c.readChunkHeader()
		if err != nil {
			return 0, err
		}
		if c.signed {
			c.pendingSig = sig
		}
		c.hasher.h.Reset()
		if size == 0 {
			// Final chunk. For signed mode verify the empty-chunk signature.
			if c.signed {
				if err := c.verifyChunk(); err != nil {
					return 0, err
				}
			}
			if c.hasTrailer {
				if err := c.readTrailer(); err != nil {
					return 0, err
				}
			} else if err := c.expectCRLF(); err != nil && err != io.EOF {
				// Some clients omit the final CRLF; tolerate EOF here.
				return 0, err
			}
			c.eof = true
			return 0, io.EOF
		}
		c.remaining = size
		c.inChunk = true
	}
}

// pendingSig is the signature announced in the current chunk header.
func (c *ChunkedReader) verifyChunk() error {
	sum := c.hasher.h.Sum(nil)
	sts := Algorithm + "-PAYLOAD\n" + c.dateScope + c.prevSig + "\n" + EmptySHA256 + "\n" + hex.EncodeToString(sum)
	want := hex.EncodeToString(hmacSHA256(c.key, []byte(sts)))
	if subtle.ConstantTimeCompare([]byte(want), []byte(c.pendingSig)) != 1 {
		return ErrChunkSignature
	}
	c.prevSig = c.pendingSig
	return nil
}

func (c *ChunkedReader) readChunkHeader() (int64, string, error) {
	line, err := c.readLine()
	if err != nil {
		return 0, "", err
	}
	sizeStr, rest, _ := strings.Cut(line, ";")
	size, err := strconv.ParseInt(strings.TrimSpace(sizeStr), 16, 64)
	if err != nil || size < 0 {
		return 0, "", ErrChunkMalformed
	}
	sig := ""
	if c.signed {
		k, v, ok := strings.Cut(strings.TrimSpace(rest), "=")
		if !ok || k != "chunk-signature" || len(v) != 64 {
			return 0, "", ErrChunkMalformed
		}
		sig = v
	}
	return size, sig, nil
}

func (c *ChunkedReader) readLine() (string, error) {
	line, err := c.br.ReadSlice('\n')
	if err != nil {
		if err == bufio.ErrBufferFull {
			return "", ErrChunkMalformed
		}
		if err == io.EOF && len(line) == 0 {
			return "", ErrChunkMalformed
		}
		if err != io.EOF {
			return "", err
		}
	}
	if len(line) > maxChunkHeaderLine {
		return "", ErrChunkMalformed
	}
	return strings.TrimRight(string(line), "\r\n"), nil
}

func (c *ChunkedReader) expectCRLF() error {
	b, err := c.br.Peek(2)
	if err != nil {
		if err == io.EOF {
			return io.EOF
		}
		return err
	}
	if !bytes.Equal(b, []byte("\r\n")) {
		return ErrChunkMalformed
	}
	c.br.Discard(2)
	return nil
}

// readTrailer parses "name:value\r\n" lines, then (for signed mode) the
// x-amz-trailer-signature line, then the terminating blank line.
func (c *ChunkedReader) readTrailer() error {
	var canon strings.Builder
	var trailerSig string
	for {
		line, err := c.readLine()
		if err != nil {
			if err == ErrChunkMalformed && len(c.trailer) > 0 && !c.signed {
				break
			}
			return err
		}
		if line == "" {
			break
		}
		k, v, ok := strings.Cut(line, ":")
		if !ok {
			return ErrChunkMalformed
		}
		k = strings.ToLower(strings.TrimSpace(k))
		v = strings.TrimSpace(v)
		if k == "x-amz-trailer-signature" {
			trailerSig = v
			continue
		}
		c.trailer.Set(k, v)
		canon.WriteString(k + ":" + v + "\n")
	}
	for _, h := range c.expectedTrailer {
		if c.trailer.Get(h) == "" {
			return ErrTrailerMissing
		}
	}
	if c.signed {
		if trailerSig == "" {
			return ErrChunkMalformed
		}
		sts := Algorithm + "-TRAILER\n" + c.dateScope + c.prevSig + "\n" + hexSHA256([]byte(canon.String()))
		want := hex.EncodeToString(hmacSHA256(c.key, []byte(sts)))
		if subtle.ConstantTimeCompare([]byte(want), []byte(trailerSig)) != 1 {
			return ErrChunkSignature
		}
	}
	// Consume a trailing blank line if the client sent one after the
	// trailer block (some send "\r\n\r\n").
	if b, err := c.br.Peek(2); err == nil && bytes.Equal(b, []byte("\r\n")) {
		c.br.Discard(2)
	}
	return nil
}
