package sse

import (
	"bytes"
	"crypto/rand"
	"io"
	"testing"
)

func TestWrapUnwrap(t *testing.T) {
	kek := DeriveKey([]byte("master"), "test")
	dek := NewDEK()
	w, err := Wrap(kek, dek, []byte("aad"))
	if err != nil {
		t.Fatal(err)
	}
	got, err := Unwrap(kek, w, []byte("aad"))
	if err != nil || !bytes.Equal(got, dek) {
		t.Fatalf("unwrap: %v", err)
	}
	if _, err := Unwrap(kek, w, []byte("other")); err != ErrCorrupt {
		t.Fatal("aad mismatch should fail")
	}
	if _, err := Unwrap(DeriveKey([]byte("wrong"), "test"), w, []byte("aad")); err != ErrCorrupt {
		t.Fatal("wrong kek should fail")
	}
}

func TestEncryptDecryptRanges(t *testing.T) {
	sizes := []int{0, 1, ChunkSize - 1, ChunkSize, ChunkSize + 1, 3*ChunkSize + 12345}
	for _, sz := range sizes {
		plain := make([]byte, sz)
		rand.Read(plain)
		dek := NewDEK()
		nonce := NewNonce()
		var ct bytes.Buffer
		enc, _ := NewEncrypter(&ct, dek, nonce)
		// Write in odd-sized pieces.
		for i := 0; i < len(plain); i += 1000 {
			end := i + 1000
			if end > len(plain) {
				end = len(plain)
			}
			enc.Write(plain[i:end])
		}
		enc.Close()
		if int64(ct.Len()) != EncryptedSize(int64(sz)) {
			t.Fatalf("size %d: ct len %d want %d", sz, ct.Len(), EncryptedSize(int64(sz)))
		}
		open := func(off, ln int64) (io.ReadCloser, error) {
			b := ct.Bytes()
			if off > int64(len(b)) {
				off = int64(len(b))
			}
			end := off + ln
			if ln < 0 || end > int64(len(b)) {
				end = int64(len(b))
			}
			return io.NopCloser(bytes.NewReader(b[off:end])), nil
		}
		ranges := [][2]int64{{0, -1}, {0, 1}, {int64(sz) / 2, 10}, {int64(sz) - 1, 1}, {ChunkSize - 1, 2}, {ChunkSize, ChunkSize}, {int64(sz), 5}}
		for _, r := range ranges {
			off, ln := r[0], r[1]
			if off > int64(sz) || off < 0 {
				continue
			}
			dr, err := NewDecryptReader(open, dek, nonce, int64(sz), off, ln)
			if err != nil {
				t.Fatal(err)
			}
			got, err := io.ReadAll(dr)
			if err != nil {
				t.Fatalf("size %d range %v: %v", sz, r, err)
			}
			end := int64(sz)
			if ln >= 0 && off+ln < end {
				end = off + ln
			}
			want := plain[off:end]
			if !bytes.Equal(got, want) {
				t.Fatalf("size %d range %v: got %d bytes want %d", sz, r, len(got), len(want))
			}
		}
		if sz > 0 {
			// Tamper.
			b := ct.Bytes()
			b[len(b)/2] ^= 1
			dr, _ := NewDecryptReader(open, dek, nonce, int64(sz), 0, -1)
			if _, err := io.ReadAll(dr); err != ErrCorrupt {
				t.Fatalf("size %d: tamper not detected: %v", sz, err)
			}
		}
	}
}
