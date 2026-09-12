// Package kv defines the ordered key-value store abstraction that the
// metadata layer is built on. Keys are byte strings compared
// lexicographically; iteration is in key order.
//
// The interface is deliberately small so that the default embedded engine
// (bbolt) can later be swapped for pebble or a distributed store.
package kv

import "errors"

// ErrNotFound is returned by Txn.Get when a key does not exist.
var ErrNotFound = errors.New("kv: key not found")

// Store is an ordered, transactional key-value store.
type Store interface {
	// View runs fn in a read-only transaction.
	View(fn func(Txn) error) error
	// Update runs fn in a read-write transaction; the transaction is
	// committed if fn returns nil and rolled back otherwise.
	Update(fn func(Txn) error) error
	Close() error
}

// Txn is a transaction. Byte slices returned by Get, Key and Value are only
// valid until the transaction ends; callers must copy them if needed.
type Txn interface {
	Get(key []byte) ([]byte, error)
	Put(key, value []byte) error
	Delete(key []byte) error
	// Seek returns an iterator positioned at the first key >= start.
	Seek(start []byte) Iterator
}

// Iterator walks keys in ascending order.
type Iterator interface {
	Valid() bool
	Key() []byte
	Value() []byte
	Next()
	// Seek repositions the iterator at the first key >= key.
	Seek(key []byte)
	Close()
}

// PrefixSuccessor returns the smallest key that is greater than every key
// having the given prefix, or nil if no such key exists (prefix is all 0xff).
func PrefixSuccessor(prefix []byte) []byte {
	out := make([]byte, len(prefix))
	copy(out, prefix)
	for i := len(out) - 1; i >= 0; i-- {
		if out[i] != 0xff {
			out[i]++
			return out[:i+1]
		}
	}
	return nil
}
