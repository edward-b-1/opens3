// Package kms is the local key management service: a master key from
// configuration plus named keys (for SSE-KMS) stored wrapped in the KV
// store. It is deliberately small; external KMS backends implement the
// same interface.
package kms

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"gitlab.com/Birdsall/opens3/internal/kv"
	"gitlab.com/Birdsall/opens3/internal/sse"
)

// Errors.
var (
	ErrKeyNotFound = errors.New("kms: key not found")
	ErrKeyExists   = errors.New("kms: key exists")
	ErrInvalidKey  = errors.New("kms: invalid key id")
)

// DefaultKeyID is the SSE-KMS key used when a request specifies aws:kms
// without a key ID.
const DefaultKeyID = "opens3-default-key"

const nsKey = "i/kms/"

// KMS wraps and unwraps data keys.
type KMS interface {
	// Wrap encrypts dek under keyID ("" = master key) bound to context.
	Wrap(keyID string, dek []byte, context map[string]string) ([]byte, error)
	Unwrap(keyID string, wrapped []byte, context map[string]string) ([]byte, error)
	// KeyExists reports whether a named key exists.
	KeyExists(keyID string) bool
}

// Key is a named key record (material stored wrapped by the master key).
type Key struct {
	ID       string    `json:"id"`
	Wrapped  []byte    `json:"w"`
	Created  time.Time `json:"c"`
	Disabled bool      `json:"d,omitempty"`
}

// Local is the built-in KMS.
type Local struct {
	master []byte
	kv     kv.Store
}

// NewLocal creates the local KMS and ensures the default key exists.
func NewLocal(db kv.Store, masterMaterial []byte) (*Local, error) {
	if len(masterMaterial) < 8 {
		return nil, errors.New("kms: master key material too short")
	}
	l := &Local{master: sse.DeriveKey(masterMaterial, "kms-master"), kv: db}
	if err := l.CreateKey(DefaultKeyID); err != nil && !errors.Is(err, ErrKeyExists) {
		return nil, err
	}
	return l, nil
}

// MasterKey returns the derived master key (for IAM secret wrapping).
func (l *Local) MasterKey() []byte { return l.master }

func contextAAD(ctx map[string]string) []byte {
	if len(ctx) == 0 {
		return nil
	}
	keys := make([]string, 0, len(ctx))
	for k := range ctx {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var b strings.Builder
	for _, k := range keys {
		b.WriteString(k + "=" + ctx[k] + "&")
	}
	return []byte(b.String())
}

// CreateKey creates a named key.
func (l *Local) CreateKey(id string) error {
	if id == "" || len(id) > 128 || strings.ContainsAny(id, "/\x00") {
		return ErrInvalidKey
	}
	material := sse.NewDEK()
	w, err := sse.Wrap(l.master, material, []byte("kms-key:"+id))
	if err != nil {
		return err
	}
	return l.kv.Update(func(tx kv.Txn) error {
		if _, err := tx.Get([]byte(nsKey + id)); err == nil {
			return ErrKeyExists
		}
		b, _ := json.Marshal(&Key{ID: id, Wrapped: w, Created: time.Now().UTC()})
		return tx.Put([]byte(nsKey+id), b)
	})
}

// ListKeys lists named keys.
func (l *Local) ListKeys() ([]Key, error) {
	var out []Key
	err := l.kv.View(func(tx kv.Txn) error {
		it := tx.Seek([]byte(nsKey))
		defer it.Close()
		for ; it.Valid() && bytes.HasPrefix(it.Key(), []byte(nsKey)); it.Next() {
			var k Key
			if err := json.Unmarshal(it.Value(), &k); err != nil {
				return err
			}
			k.Wrapped = nil
			out = append(out, k)
		}
		return nil
	})
	return out, err
}

// DeleteKey removes a named key. Objects encrypted with it become
// unreadable; the admin API confirms before calling this.
func (l *Local) DeleteKey(id string) error {
	if id == DefaultKeyID {
		return ErrInvalidKey
	}
	return l.kv.Update(func(tx kv.Txn) error {
		if _, err := tx.Get([]byte(nsKey + id)); err != nil {
			return ErrKeyNotFound
		}
		return tx.Delete([]byte(nsKey + id))
	})
}

func (l *Local) keyMaterial(id string) ([]byte, error) {
	var k Key
	err := l.kv.View(func(tx kv.Txn) error {
		b, err := tx.Get([]byte(nsKey + id))
		if err != nil {
			return ErrKeyNotFound
		}
		return json.Unmarshal(b, &k)
	})
	if err != nil {
		return nil, err
	}
	if k.Disabled {
		return nil, fmt.Errorf("kms: key %s is disabled", id)
	}
	return sse.Unwrap(l.master, k.Wrapped, []byte("kms-key:"+id))
}

// KeyExists implements KMS.
func (l *Local) KeyExists(id string) bool {
	_, err := l.keyMaterial(id)
	return err == nil
}

// Wrap implements KMS.
func (l *Local) Wrap(keyID string, dek []byte, context map[string]string) ([]byte, error) {
	kek := l.master
	if keyID != "" {
		var err error
		if kek, err = l.keyMaterial(keyID); err != nil {
			return nil, err
		}
	}
	return sse.Wrap(kek, dek, contextAAD(context))
}

// Unwrap implements KMS.
func (l *Local) Unwrap(keyID string, wrapped []byte, context map[string]string) ([]byte, error) {
	kek := l.master
	if keyID != "" {
		var err error
		if kek, err = l.keyMaterial(keyID); err != nil {
			return nil, err
		}
	}
	return sse.Unwrap(kek, wrapped, contextAAD(context))
}

var _ KMS = (*Local)(nil)
