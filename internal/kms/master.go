package kms

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/edward-b-1/OpenS3/internal/sse"
)

// Master is the ring of master keys that wrap everything else: named KMS
// keys, SSE-S3 data keys and stored IAM secrets. The newest key wraps;
// every key in the ring can unwrap, so a key can be rotated in and older
// data re-wrapped later. A master key is never derived from a password.
type Master struct {
	keys   [][]byte // newest first
	source string   // "file:<path>" or "env"
}

// masterFile is the on-disk form (owner-only permissions).
type masterFile struct {
	Version int           `json:"version"`
	Keys    []masterEntry `json:"keys"` // oldest first
}

type masterEntry struct {
	ID      string    `json:"id"`
	Key     string    `json:"key"` // base64, 32 bytes
	Created time.Time `json:"created"`
}

// ErrMasterMismatch means the configured master key cannot unwrap this
// data directory's key-check value.
var ErrMasterMismatch = errors.New("kms: master key does not match this data directory")

// MinMasterMaterial is the minimum length of OPENS3_MASTER_KEY: it must be
// random material (e.g. `openssl rand -base64 32`), not a password.
const MinMasterMaterial = 32

// LoadOrCreateMasterFile reads the master key ring at path, generating a
// fresh random key (and the file, mode 0600) when it does not exist.
func LoadOrCreateMasterFile(path string) (m *Master, created bool, err error) {
	b, err := os.ReadFile(path)
	if err == nil {
		var f masterFile
		if err := json.Unmarshal(b, &f); err != nil || len(f.Keys) == 0 {
			return nil, false, fmt.Errorf("kms: master key file %s is malformed", path)
		}
		m = &Master{source: "file:" + path}
		for i := len(f.Keys) - 1; i >= 0; i-- {
			k, err := base64.StdEncoding.DecodeString(f.Keys[i].Key)
			if err != nil || len(k) != sse.KeySize {
				return nil, false, fmt.Errorf("kms: master key %s in %s is invalid", f.Keys[i].ID, path)
			}
			m.keys = append(m.keys, k)
		}
		return m, false, nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return nil, false, err
	}
	key := make([]byte, sse.KeySize)
	if _, err := rand.Read(key); err != nil {
		return nil, false, err
	}
	f := masterFile{Version: 1, Keys: []masterEntry{{ID: "k1", Key: base64.StdEncoding.EncodeToString(key), Created: time.Now().UTC()}}}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, false, err
	}
	out, _ := json.MarshalIndent(f, "", "  ")
	if err := os.WriteFile(path, append(out, '\n'), 0o600); err != nil {
		return nil, false, err
	}
	return &Master{keys: [][]byte{key}, source: "file:" + path}, true, nil
}

// MasterFromMaterial builds a single-key ring from operator-supplied
// material (OPENS3_MASTER_KEY). The material must be at least
// MinMasterMaterial bytes; it is stretched to a key with SHA-256.
func MasterFromMaterial(material []byte) (*Master, error) {
	if len(material) < MinMasterMaterial {
		return nil, fmt.Errorf("kms: master key material must be at least %d characters of random data (try `openssl rand -base64 32`), not a password", MinMasterMaterial)
	}
	return &Master{keys: [][]byte{sse.DeriveKey(material, "kms-master")}, source: "env"}, nil
}

// Wrap encrypts data under the current master key.
func (m *Master) Wrap(data, aad []byte) ([]byte, error) { return sse.Wrap(m.keys[0], data, aad) }

// Unwrap tries every key in the ring, newest first. AES-GCM authenticates,
// so a wrong key fails cleanly.
func (m *Master) Unwrap(wrapped, aad []byte) ([]byte, error) {
	for _, k := range m.keys {
		if out, err := sse.Unwrap(k, wrapped, aad); err == nil {
			return out, nil
		}
	}
	return nil, sse.ErrCorrupt
}

// Fingerprint identifies the current key without revealing it.
func (m *Master) Fingerprint() string {
	sum := sha256.Sum256(m.keys[0])
	return hex.EncodeToString(sum[:8])
}

// Source says where the ring came from (for logs).
func (m *Master) Source() string { return m.source }

// Keys returns the number of keys in the ring.
func (m *Master) Keys() int { return len(m.keys) }

// TestMaster returns a deterministic ring for tests.
func TestMaster() *Master {
	return &Master{keys: [][]byte{sse.DeriveKey([]byte("opens3-test-master-key-material-0000"), "kms-master")}, source: "test"}
}
