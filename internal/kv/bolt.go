package kv

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"time"

	bolt "go.etcd.io/bbolt"
)

var rootBucket = []byte("kv")

// Bolt is a Store backed by an embedded bbolt database file.
type Bolt struct {
	db *bolt.DB
}

// OpenBolt opens (creating if needed) the database file at path.
func OpenBolt(path string) (*Bolt, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	db, err := bolt.Open(path, 0o600, &bolt.Options{
		Timeout:      5 * time.Second,
		FreelistType: bolt.FreelistMapType,
		NoSync:       false,
	})
	if err != nil {
		return nil, fmt.Errorf("kv: open %s: %w", path, err)
	}
	if err := db.Update(func(tx *bolt.Tx) error {
		_, err := tx.CreateBucketIfNotExists(rootBucket)
		return err
	}); err != nil {
		db.Close()
		return nil, err
	}
	return &Bolt{db: db}, nil
}

func (b *Bolt) View(fn func(Txn) error) error {
	return b.db.View(func(tx *bolt.Tx) error {
		return fn(&boltTxn{b: tx.Bucket(rootBucket)})
	})
}

func (b *Bolt) Update(fn func(Txn) error) error {
	return b.db.Update(func(tx *bolt.Tx) error {
		return fn(&boltTxn{b: tx.Bucket(rootBucket)})
	})
}

func (b *Bolt) Close() error { return b.db.Close() }

type boltTxn struct {
	b *bolt.Bucket
}

func (t *boltTxn) Get(key []byte) ([]byte, error) {
	v := t.b.Get(key)
	if v == nil {
		return nil, ErrNotFound
	}
	return v, nil
}

func (t *boltTxn) Put(key, value []byte) error {
	if !t.b.Writable() {
		return bolt.ErrTxNotWritable
	}
	return t.b.Put(key, value)
}

func (t *boltTxn) Delete(key []byte) error {
	if !t.b.Writable() {
		return bolt.ErrTxNotWritable
	}
	return t.b.Delete(key)
}

func (t *boltTxn) Seek(start []byte) Iterator {
	c := t.b.Cursor()
	it := &boltIter{c: c}
	it.k, it.v = c.Seek(start)
	return it
}

type boltIter struct {
	c    *bolt.Cursor
	k, v []byte
}

func (it *boltIter) Valid() bool   { return it.k != nil }
func (it *boltIter) Key() []byte   { return it.k }
func (it *boltIter) Value() []byte { return it.v }
func (it *boltIter) Next()         { it.k, it.v = it.c.Next() }
func (it *boltIter) Seek(key []byte) {
	it.k, it.v = it.c.Seek(key)
}
func (it *boltIter) Close() {}

// HasPrefix reports whether key starts with prefix. Helper for iteration loops.
func HasPrefix(key, prefix []byte) bool { return bytes.HasPrefix(key, prefix) }
