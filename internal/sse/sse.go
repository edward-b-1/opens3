// Package sse implements server-side encryption of blobs: a 32-byte data
// encryption key (DEK) per object, wrapped by a master key (SSE-S3), a
// named KMS key (SSE-KMS) or a customer key (SSE-C). Data is encrypted in
// 64 KiB chunks with AES-256-GCM so byte ranges can be served without
// decrypting the whole object. See docs/FORMAT.md.
package sse

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"io"
)

const (
	// ChunkSize is the plaintext bytes per GCM chunk.
	ChunkSize = 64 * 1024
	tagSize   = 16
	nonceSize = 12
	// ChunkOverhead is the ciphertext expansion per chunk.
	ChunkOverhead = tagSize
	KeySize       = 32
)

var (
	ErrBadKey     = errors.New("sse: bad key")
	ErrCorrupt    = errors.New("sse: ciphertext corrupt or key mismatch")
	ErrBadWrapped = errors.New("sse: malformed wrapped key")
)

// NewDEK generates a random data encryption key.
func NewDEK() []byte {
	k := make([]byte, KeySize)
	if _, err := rand.Read(k); err != nil {
		panic(err)
	}
	return k
}

// Wrap encrypts dek with kek using AES-GCM with a random nonce. The
// output is nonce || ciphertext.
func Wrap(kek, dek []byte, aad []byte) ([]byte, error) {
	if len(kek) != KeySize {
		return nil, ErrBadKey
	}
	block, err := aes.NewCipher(kek)
	if err != nil {
		return nil, err
	}
	g, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	nonce := make([]byte, nonceSize)
	if _, err := rand.Read(nonce); err != nil {
		return nil, err
	}
	return append(nonce, g.Seal(nil, nonce, dek, aad)...), nil
}

// Unwrap reverses Wrap.
func Unwrap(kek, wrapped []byte, aad []byte) ([]byte, error) {
	if len(kek) != KeySize {
		return nil, ErrBadKey
	}
	if len(wrapped) < nonceSize+tagSize {
		return nil, ErrBadWrapped
	}
	block, err := aes.NewCipher(kek)
	if err != nil {
		return nil, err
	}
	g, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	dek, err := g.Open(nil, wrapped[:nonceSize], wrapped[nonceSize:], aad)
	if err != nil {
		return nil, ErrCorrupt
	}
	return dek, nil
}

// DeriveKey derives a 32-byte key from arbitrary secret material (used for
// the master key from configuration and for SSE-C keys, which are already
// 32 bytes).
func DeriveKey(material []byte, context string) []byte {
	h := sha256.New()
	h.Write([]byte("opens3-sse-v1:" + context + ":"))
	h.Write(material)
	return h.Sum(nil)
}

// EncryptedSize returns the ciphertext length for a plaintext length.
func EncryptedSize(plain int64) int64 {
	if plain == 0 {
		return 0
	}
	chunks := (plain + ChunkSize - 1) / ChunkSize
	return plain + chunks*ChunkOverhead
}

// NonceSize is the length of the per-part random nonce base; the 4-byte
// chunk counter fills the rest of the 96-bit GCM nonce.
const NonceSize = 8

// NewNonce returns a random nonce base for one part.
func NewNonce() []byte {
	n := make([]byte, NonceSize)
	if _, err := rand.Read(n); err != nil {
		panic(err)
	}
	return n
}

func chunkNonce(base []byte, idx uint64) []byte {
	n := make([]byte, nonceSize)
	copy(n, base[:NonceSize])
	binary.BigEndian.PutUint32(n[NonceSize:], uint32(idx))
	return n
}

// Encrypter wraps a writer, encrypting chunks. nonceBase is NonceSize
// random bytes stored with the part (the chunk counter fills the rest).
type Encrypter struct {
	w      io.Writer
	gcm    cipher.AEAD
	base   []byte
	buf    []byte
	idx    uint64
	closed bool
}

// NewEncrypter creates an encrypting writer.
func NewEncrypter(w io.Writer, dek, nonceBase []byte) (*Encrypter, error) {
	if len(dek) != KeySize || len(nonceBase) < NonceSize {
		return nil, ErrBadKey
	}
	block, err := aes.NewCipher(dek)
	if err != nil {
		return nil, err
	}
	g, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	return &Encrypter{w: w, gcm: g, base: nonceBase, buf: make([]byte, 0, ChunkSize+ChunkOverhead)}, nil
}

func (e *Encrypter) Write(p []byte) (int, error) {
	n := len(p)
	for len(p) > 0 {
		room := ChunkSize - len(e.buf)
		if room > len(p) {
			room = len(p)
		}
		e.buf = append(e.buf, p[:room]...)
		p = p[room:]
		if len(e.buf) == ChunkSize {
			if err := e.flush(); err != nil {
				return n - len(p), err
			}
		}
	}
	return n, nil
}

func (e *Encrypter) flush() error {
	if len(e.buf) == 0 {
		return nil
	}
	ct := e.gcm.Seal(e.buf[:0], chunkNonce(e.base, e.idx), e.buf, nil)
	e.idx++
	_, err := e.w.Write(ct)
	e.buf = e.buf[:0]
	return err
}

// Close flushes the final partial chunk.
func (e *Encrypter) Close() error {
	if e.closed {
		return nil
	}
	e.closed = true
	return e.flush()
}

// NewDecryptReader returns a reader over plaintext bytes [offset,
// offset+length) given a function that opens the ciphertext at a byte
// offset. length < 0 means to the end. plainSize is the plaintext length.
func NewDecryptReader(open func(ctOffset, ctLength int64) (io.ReadCloser, error), dek, nonceBase []byte, plainSize, offset, length int64) (io.ReadCloser, error) {
	if len(dek) != KeySize {
		return nil, ErrBadKey
	}
	block, err := aes.NewCipher(dek)
	if err != nil {
		return nil, err
	}
	g, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	if offset < 0 {
		offset = 0
	}
	if length < 0 || offset+length > plainSize {
		length = plainSize - offset
	}
	if offset >= plainSize || length <= 0 {
		return io.NopCloser(&emptyReader{}), nil
	}
	firstChunk := offset / ChunkSize
	lastChunk := (offset + length - 1) / ChunkSize
	ctOff := firstChunk * (ChunkSize + ChunkOverhead)
	ctLen := (lastChunk - firstChunk + 1) * (ChunkSize + ChunkOverhead)
	// Last chunk may be short.
	totalCT := EncryptedSize(plainSize)
	if ctOff+ctLen > totalCT {
		ctLen = totalCT - ctOff
	}
	rc, err := open(ctOff, ctLen)
	if err != nil {
		return nil, err
	}
	return &decryptReader{rc: rc, gcm: g, base: nonceBase, idx: uint64(firstChunk), skip: offset - firstChunk*ChunkSize, remain: length,
		buf: make([]byte, ChunkSize+ChunkOverhead)}, nil
}

type emptyReader struct{}

func (*emptyReader) Read([]byte) (int, error) { return 0, io.EOF }

type decryptReader struct {
	rc     io.ReadCloser
	gcm    cipher.AEAD
	base   []byte
	idx    uint64
	skip   int64
	remain int64
	buf    []byte
	plain  []byte
	err    error
}

func (d *decryptReader) Read(p []byte) (int, error) {
	if d.err != nil {
		return 0, d.err
	}
	for len(d.plain) == 0 {
		if d.remain <= 0 {
			d.err = io.EOF
			return 0, io.EOF
		}
		n, err := io.ReadFull(d.rc, d.buf)
		if err == io.ErrUnexpectedEOF {
			err = nil
		}
		if n == 0 {
			if err == nil || err == io.EOF {
				err = ErrCorrupt
			}
			d.err = err
			return 0, err
		} else if err != nil {
			d.err = err
			return 0, err
		}
		pt, err := d.gcm.Open(d.buf[:0], chunkNonce(d.base, d.idx), d.buf[:n], nil)
		if err != nil {
			d.err = ErrCorrupt
			return 0, ErrCorrupt
		}
		d.idx++
		if d.skip > 0 {
			if d.skip >= int64(len(pt)) {
				d.skip -= int64(len(pt))
				continue
			}
			pt = pt[d.skip:]
			d.skip = 0
		}
		if int64(len(pt)) > d.remain {
			pt = pt[:d.remain]
		}
		d.plain = pt
	}
	n := copy(p, d.plain)
	d.plain = d.plain[n:]
	d.remain -= int64(n)
	return n, nil
}

func (d *decryptReader) Close() error { return d.rc.Close() }
