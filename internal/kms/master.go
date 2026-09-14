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
	"strconv"
	"strings"
	"time"

	"github.com/edward-b-1/opens3/internal/sse"
)

// Master is the ring of master keys that wrap everything else: named KMS
// keys, SSE-S3 data keys and stored IAM secrets. The newest key wraps;
// every key in the ring can unwrap, so a key can be rotated in and older
// data re-wrapped later. A master key is never derived from a password.
type Master struct {
	keys   []masterKey // newest first
	source string      // "file:<path>", "env" or "test"
	path   string      // key file, when source is a file
}

type masterKey struct {
	id      string
	key     []byte
	created time.Time
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

// KeyInfo describes one key of the ring without revealing it.
type KeyInfo struct {
	ID          string
	Created     time.Time
	Fingerprint string
	Source      string
}

// ErrMasterMismatch means the configured master key cannot unwrap this
// data directory's key-check value.
var ErrMasterMismatch = errors.New("kms: master key does not match this data directory")

// ErrNotFile is returned by ring-editing operations on a ring that does
// not come from a key file.
var ErrNotFile = errors.New("kms: master key ring is not backed by a file")

// MinMasterMaterial is the minimum length of OPENS3_MASTER_KEY: it must be
// random material (e.g. `openssl rand -base64 32`), not a password.
const MinMasterMaterial = 32

// LoadOrCreateMasterFile reads the master key ring at path, generating a
// fresh random key (and the file, mode 0600) when it does not exist.
func LoadOrCreateMasterFile(path string) (m *Master, created bool, err error) {
	m, err = LoadMasterFile(path)
	if err == nil {
		return m, false, nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return nil, false, err
	}
	key := make([]byte, sse.KeySize)
	if _, err := rand.Read(key); err != nil {
		return nil, false, err
	}
	m = &Master{keys: []masterKey{{id: "k1", key: key, created: time.Now().UTC()}}, source: "file:" + path, path: path}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, false, err
	}
	if err := m.save(); err != nil {
		return nil, false, err
	}
	return m, true, nil
}

// LoadMasterFile reads an existing master key ring. A missing file is
// reported with an error wrapping os.ErrNotExist.
func LoadMasterFile(path string) (*Master, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var f masterFile
	if err := json.Unmarshal(b, &f); err != nil || len(f.Keys) == 0 {
		return nil, fmt.Errorf("kms: master key file %s is malformed", path)
	}
	m := &Master{source: "file:" + path, path: path}
	for i := len(f.Keys) - 1; i >= 0; i-- {
		k, err := base64.StdEncoding.DecodeString(f.Keys[i].Key)
		if err != nil || len(k) != sse.KeySize {
			return nil, fmt.Errorf("kms: master key %s in %s is invalid", f.Keys[i].ID, path)
		}
		m.keys = append(m.keys, masterKey{id: f.Keys[i].ID, key: k, created: f.Keys[i].Created})
	}
	return m, nil
}

// save writes the ring to its file atomically (temporary file, fsync,
// rename) with owner-only permissions.
func (m *Master) save() error {
	if m.path == "" {
		return ErrNotFile
	}
	f := masterFile{Version: 1}
	for i := len(m.keys) - 1; i >= 0; i-- {
		k := m.keys[i]
		f.Keys = append(f.Keys, masterEntry{ID: k.id, Key: base64.StdEncoding.EncodeToString(k.key), Created: k.created})
	}
	out, _ := json.MarshalIndent(f, "", "  ")
	tmp := m.path + ".tmp"
	fh, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	if _, err := fh.Write(append(out, '\n')); err != nil {
		fh.Close()
		os.Remove(tmp)
		return err
	}
	if err := fh.Sync(); err != nil {
		fh.Close()
		os.Remove(tmp)
		return err
	}
	if err := fh.Close(); err != nil {
		os.Remove(tmp)
		return err
	}
	if err := os.Rename(tmp, m.path); err != nil {
		os.Remove(tmp)
		return err
	}
	if d, err := os.Open(filepath.Dir(m.path)); err == nil {
		d.Sync()
		d.Close()
	}
	return nil
}

// AddKey generates a new random key, makes it the current (wrapping) key
// and writes the ring back to its file. Everything wrapped so far stays
// readable through the older keys until it is re-wrapped.
func (m *Master) AddKey() (KeyInfo, error) {
	if m.path == "" {
		return KeyInfo{}, ErrNotFile
	}
	key := make([]byte, sse.KeySize)
	if _, err := rand.Read(key); err != nil {
		return KeyInfo{}, err
	}
	next := 0
	for _, k := range m.keys {
		if n, err := strconv.Atoi(strings.TrimPrefix(k.id, "k")); err == nil && n > next {
			next = n
		}
	}
	nk := masterKey{id: "k" + strconv.Itoa(next+1), key: key, created: time.Now().UTC()}
	m.keys = append([]masterKey{nk}, m.keys...)
	if err := m.save(); err != nil {
		m.keys = m.keys[1:]
		return KeyInfo{}, err
	}
	return m.info(0), nil
}

// Prune removes every key but the current one from the ring and its file.
// The caller must first make sure nothing is still wrapped under the
// removed keys (see package masterkey); afterwards they are gone.
func (m *Master) Prune() ([]KeyInfo, error) {
	if m.path == "" {
		return nil, ErrNotFile
	}
	if len(m.keys) < 2 {
		return nil, nil
	}
	var removed []KeyInfo
	for i := 1; i < len(m.keys); i++ {
		removed = append(removed, m.info(i))
	}
	old := m.keys
	m.keys = m.keys[:1:1]
	if err := m.save(); err != nil {
		m.keys = old
		return nil, err
	}
	return removed, nil
}

// MasterFromMaterial builds a single-key ring from operator-supplied
// material (OPENS3_MASTER_KEY). The material must be at least
// MinMasterMaterial bytes; it is stretched to a key with SHA-256.
func MasterFromMaterial(material []byte) (*Master, error) {
	return masterFromMaterial(material, "env")
}

func masterFromMaterial(material []byte, id string) (*Master, error) {
	if len(material) < MinMasterMaterial {
		return nil, fmt.Errorf("kms: master key material must be at least %d characters of random data (try `openssl rand -base64 32`), not a password", MinMasterMaterial)
	}
	return &Master{keys: []masterKey{{id: id, key: sse.DeriveKey(material, "kms-master")}}, source: "env"}, nil
}

// MasterFromEnv builds a ring from the material in the named environment
// variable; the key is labelled with the variable's name. It returns nil
// without error when the variable is unset or empty.
func MasterFromEnv(name string) (*Master, error) {
	v := os.Getenv(name)
	if v == "" {
		return nil, nil
	}
	m, err := masterFromMaterial([]byte(v), name)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", name, err)
	}
	return m, nil
}

// WithFallback returns a ring that wraps with m's current key and can
// additionally unwrap with every key of the given rings, in order. It is
// how data is moved between an environment key and a key file: the old
// source becomes a fallback while everything is re-wrapped. Nil rings are
// skipped; the result keeps m's file, so ring edits still apply to m's
// keys only.
func (m *Master) WithFallback(rings ...*Master) *Master {
	out := &Master{keys: append([]masterKey(nil), m.keys...), source: m.source, path: m.path}
	for _, r := range rings {
		if r != nil {
			out.keys = append(out.keys, r.keys...)
		}
	}
	return out
}

// Wrap encrypts data under the current master key.
func (m *Master) Wrap(data, aad []byte) ([]byte, error) { return sse.Wrap(m.keys[0].key, data, aad) }

// Unwrap tries every key in the ring, newest first. AES-GCM authenticates,
// so a wrong key fails cleanly.
func (m *Master) Unwrap(wrapped, aad []byte) ([]byte, error) {
	_, out, err := m.UnwrapIndex(wrapped, aad)
	return out, err
}

// UnwrapIndex is Unwrap that also reports which key succeeded: 0 is the
// current key, higher indexes are older keys (or fallbacks) whose data
// should be re-wrapped.
func (m *Master) UnwrapIndex(wrapped, aad []byte) (int, []byte, error) {
	for i, k := range m.keys {
		if out, err := sse.Unwrap(k.key, wrapped, aad); err == nil {
			return i, out, nil
		}
	}
	return -1, nil, sse.ErrCorrupt
}

// Fingerprint identifies the current key without revealing it.
func (m *Master) Fingerprint() string { return fingerprint(m.keys[0].key) }

func fingerprint(key []byte) string {
	sum := sha256.Sum256(key)
	return hex.EncodeToString(sum[:8])
}

// Source says where the ring came from (for logs).
func (m *Master) Source() string { return m.source }

// Path is the key file backing the ring, or "" for other sources.
func (m *Master) Path() string { return m.path }

// Keys returns the number of keys in the ring.
func (m *Master) Keys() int { return len(m.keys) }

// Info describes the keys in the ring, current key first.
func (m *Master) Info() []KeyInfo {
	out := make([]KeyInfo, len(m.keys))
	for i := range m.keys {
		out[i] = m.info(i)
	}
	return out
}

func (m *Master) info(i int) KeyInfo {
	k := m.keys[i]
	src := m.source
	if k.id != "" && !strings.HasPrefix(k.id, "k") {
		src = "env" // labelled with the variable name
	}
	return KeyInfo{ID: k.id, Created: k.created, Fingerprint: fingerprint(k.key), Source: src}
}

// TestMaster returns a deterministic ring for tests.
func TestMaster() *Master {
	return &Master{keys: []masterKey{{id: "test", key: sse.DeriveKey([]byte("opens3-test-master-key-material-0000"), "kms-master")}}, source: "test"}
}
