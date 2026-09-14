// Package masterkey re-wraps everything the master key ring protects
// after a rotation: the key-check value, named encryption keys, stored
// access-key secrets and the data keys of SSE-S3 objects and multipart
// uploads. It works on a closed data directory (the server holds the
// database lock while it runs) and is driven by `opens3 master`.
//
// Re-wrapping is idempotent and safe to interrupt: a record is rewritten
// only when it is wrapped by a key other than the ring's current one, and
// the ring keeps every older key until it is pruned, so a half-finished run
// leaves nothing unreadable.
package masterkey

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/edward-b-1/opens3/internal/iam"
	"github.com/edward-b-1/opens3/internal/kms"
	"github.com/edward-b-1/opens3/internal/kv"
	"github.com/edward-b-1/opens3/internal/meta"
)

// Kind is the outcome for one class of protected records.
type Kind struct {
	Name string
	// Total is the number of records wrapped by the master ring (records
	// not encrypted, or encrypted under a named key or a customer key, are
	// not counted).
	Total int
	// ByKey counts records by the index of the ring key that unwraps them:
	// ByKey[0] is the current key, higher indexes are older keys.
	ByKey []int
	// Unreadable records are unwrapped by no key in the ring.
	Unreadable int
	// Rewrapped records were rewritten under the current key in this run.
	Rewrapped int
}

// Stale is the number of records still under an older key (after the run,
// when not a dry run, this is what could not be re-wrapped).
func (k Kind) Stale() int {
	n := 0
	for i := 1; i < len(k.ByKey); i++ {
		n += k.ByKey[i]
	}
	return n
}

// Current is the number of records under the current key.
func (k Kind) Current() int {
	if len(k.ByKey) == 0 {
		return 0
	}
	return k.ByKey[0]
}

// Report is the outcome of a run over every kind of record.
type Report struct {
	Kinds []Kind
}

// Stale sums Kind.Stale over the report.
func (r Report) Stale() int {
	n := 0
	for _, k := range r.Kinds {
		n += k.Stale()
	}
	return n
}

// Unreadable sums Kind.Unreadable over the report.
func (r Report) Unreadable() int {
	n := 0
	for _, k := range r.Kinds {
		n += k.Unreadable
	}
	return n
}

// Rewrapped sums Kind.Rewrapped over the report.
func (r Report) Rewrapped() int {
	n := 0
	for _, k := range r.Kinds {
		n += k.Rewrapped
	}
	return n
}

// ByKey sums the per-key counts over the report.
func (r Report) ByKey(i int) int {
	n := 0
	for _, k := range r.Kinds {
		if i < len(k.ByKey) {
			n += k.ByKey[i]
		}
	}
	return n
}

// Options control a run.
type Options struct {
	// DryRun only counts; nothing is written.
	DryRun bool
	// Progress, if set, is called after every scanned batch.
	Progress func(kind string, scanned int)
	// Batch is the number of records per transaction (default 1000).
	Batch int
}

// codec extracts the master-wrapped value from a record and rebuilds the
// record with a new one. ok is false for records the ring does not wrap.
type codec func(key, value []byte) (wrapped, aad []byte, rebuild func(newWrapped []byte) ([]byte, error), ok bool, err error)

// Run scans every protected record and, unless DryRun is set, re-wraps
// those under an older key of the ring with its current key.
func Run(db kv.Store, ring *kms.Master, opts Options) (Report, error) {
	if opts.Batch <= 0 {
		opts.Batch = 1000
	}
	r := &runner{db: db, ring: ring, opts: opts}
	var rep Report
	for _, ns := range []struct {
		name   string
		prefix string
		codec  codec
	}{
		{"key-check value", kms.CheckKey, checkCodec},
		{"encryption keys", kms.KeyPrefix, kmsKeyCodec},
		{"access-key secrets", iam.KeyPrefix, iamKeyCodec},
		{"objects (SSE-S3)", meta.ObjectNamespace, objectCodec},
		{"multipart uploads (SSE-S3)", meta.UploadNamespace, uploadCodec},
	} {
		k := Kind{Name: ns.name, ByKey: make([]int, ring.Keys())}
		if err := r.walk([]byte(ns.prefix), ns.codec, &k); err != nil {
			return rep, fmt.Errorf("%s: %w", ns.name, err)
		}
		rep.Kinds = append(rep.Kinds, k)
	}
	return rep, nil
}

type runner struct {
	db   kv.Store
	ring *kms.Master
	opts Options
}

// walk scans prefix in batches: a read transaction classifies records and
// collects the stale ones, then a write transaction re-reads and re-wraps
// each of them. Iterating and writing in the same transaction is avoided
// because bbolt cursors are invalidated by writes.
func (r *runner) walk(prefix []byte, c codec, k *Kind) error {
	start := prefix
	scanned := 0
	for {
		var stale [][]byte
		var last []byte
		n := 0
		err := r.db.View(func(tx kv.Txn) error {
			it := tx.Seek(start)
			defer it.Close()
			for ; it.Valid() && bytes.HasPrefix(it.Key(), prefix) && n < r.opts.Batch; it.Next() {
				n++
				last = append(last[:0], it.Key()...)
				w, aad, _, ok, err := c(it.Key(), it.Value())
				if err != nil {
					return fmt.Errorf("record %q: %w", it.Key(), err)
				}
				if !ok {
					continue
				}
				k.Total++
				idx, _, err := r.ring.UnwrapIndex(w, aad)
				switch {
				case err != nil:
					k.Unreadable++
				case idx == 0:
					k.ByKey[0]++
				default:
					k.ByKey[idx]++
					stale = append(stale, append([]byte(nil), it.Key()...))
				}
			}
			return nil
		})
		if err != nil {
			return err
		}
		if !r.opts.DryRun && len(stale) > 0 {
			err := r.db.Update(func(tx kv.Txn) error {
				for _, key := range stale {
					v, err := tx.Get(key)
					if err != nil {
						continue
					}
					w, aad, rebuild, ok, err := c(key, v)
					if err != nil || !ok {
						continue
					}
					idx, dek, err := r.ring.UnwrapIndex(w, aad)
					if err != nil || idx == 0 {
						continue
					}
					nw, err := r.ring.Wrap(dek, aad)
					if err != nil {
						return err
					}
					nv, err := rebuild(nw)
					if err != nil {
						return err
					}
					if err := tx.Put(key, nv); err != nil {
						return err
					}
					k.Rewrapped++
					k.ByKey[idx]--
					k.ByKey[0]++
				}
				return nil
			})
			if err != nil {
				return err
			}
		}
		scanned += n
		if r.opts.Progress != nil {
			r.opts.Progress(k.Name, scanned)
		}
		if n < r.opts.Batch {
			return nil
		}
		start = append(last, 0) // the key after the last one scanned
	}
}

func checkCodec(key, value []byte) ([]byte, []byte, func([]byte) ([]byte, error), bool, error) {
	if string(key) != kms.CheckKey {
		return nil, nil, nil, false, nil
	}
	return value, []byte(kms.CheckAAD), func(nw []byte) ([]byte, error) { return nw, nil }, true, nil
}

func kmsKeyCodec(key, value []byte) ([]byte, []byte, func([]byte) ([]byte, error), bool, error) {
	if string(key) == kms.CheckKey {
		return nil, nil, nil, false, nil
	}
	var k kms.Key
	if err := json.Unmarshal(value, &k); err != nil {
		return nil, nil, nil, false, err
	}
	id := strings.TrimPrefix(string(key), kms.KeyPrefix)
	return k.Wrapped, kms.KeyAAD(id), func(nw []byte) ([]byte, error) { k.Wrapped = nw; return json.Marshal(&k) }, true, nil
}

func iamKeyCodec(_, value []byte) ([]byte, []byte, func([]byte) ([]byte, error), bool, error) {
	var k iam.Key
	if err := json.Unmarshal(value, &k); err != nil {
		return nil, nil, nil, false, err
	}
	return k.SecretWrapped, []byte(iam.SecretAAD), func(nw []byte) ([]byte, error) { k.SecretWrapped = nw; return json.Marshal(&k) }, true, nil
}

// sseS3AAD is the associated data of an SSE-S3 data key (see
// object.Service.resolveSSE).
func sseS3AAD(bucket string) []byte { return kms.ContextAAD(map[string]string{"bucket": bucket}) }

func objectCodec(key, value []byte) ([]byte, []byte, func([]byte) ([]byte, error), bool, error) {
	var o meta.Object
	if err := json.Unmarshal(value, &o); err != nil {
		return nil, nil, nil, false, err
	}
	if o.SSE == nil || o.SSE.Type != "AES256" {
		return nil, nil, nil, false, nil
	}
	bucket, _, _, ok := meta.SplitObjectKey(key)
	if !ok {
		return nil, nil, nil, false, fmt.Errorf("malformed object key")
	}
	return o.SSE.WrappedKey, sseS3AAD(bucket), func(nw []byte) ([]byte, error) { o.SSE.WrappedKey = nw; return json.Marshal(&o) }, true, nil
}

func uploadCodec(key, value []byte) ([]byte, []byte, func([]byte) ([]byte, error), bool, error) {
	var u meta.Upload
	if err := json.Unmarshal(value, &u); err != nil {
		return nil, nil, nil, false, err
	}
	if u.SSE == nil || u.SSE.Type != "AES256" {
		return nil, nil, nil, false, nil
	}
	rest := strings.TrimPrefix(string(key), meta.UploadNamespace)
	i := strings.IndexByte(rest, '/')
	if i < 0 {
		return nil, nil, nil, false, fmt.Errorf("malformed upload key")
	}
	return u.SSE.WrappedKey, sseS3AAD(rest[:i]), func(nw []byte) ([]byte, error) { u.SSE.WrappedKey = nw; return json.Marshal(&u) }, true, nil
}

// Check verifies that some key of the ring unwraps the data directory's
// key-check value, i.e. that the ring belongs to this data directory.
func Check(db kv.Store, ring *kms.Master) error {
	return db.View(func(tx kv.Txn) error {
		b, err := tx.Get([]byte(kms.CheckKey))
		if err != nil {
			return fmt.Errorf("this data directory has no master key-check value: it was never started by a server")
		}
		if _, _, err := ring.UnwrapIndex(b, []byte(kms.CheckAAD)); err != nil {
			return fmt.Errorf("%w: none of the %d keys given unwraps its key-check value", kms.ErrMasterMismatch, ring.Keys())
		}
		return nil
	})
}
