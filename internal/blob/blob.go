// Package blob stores immutable byte sequences ("blobs") that hold object
// data. Each uploaded object or multipart part becomes one blob. Blobs are
// identified by an opaque ID within a bucket namespace.
package blob

import (
	"context"
	"errors"
	"io"
)

// ErrNotFound is returned when a blob does not exist.
var ErrNotFound = errors.New("blob: not found")

// Store is the blob backend interface.
type Store interface {
	// Create starts a new blob in bucket's namespace. The returned Writer
	// must be committed or aborted.
	Create(ctx context.Context, bucket string) (Writer, error)
	// Open returns a reader over [offset, offset+length) of the blob; a
	// negative length means "to the end".
	Open(ctx context.Context, bucket, id string, offset, length int64) (io.ReadCloser, error)
	// Size returns the stored size of the blob.
	Size(ctx context.Context, bucket, id string) (int64, error)
	// Delete removes a blob. Deleting a missing blob is not an error.
	Delete(ctx context.Context, bucket, id string) error
	// DeleteBucket removes every blob in a bucket's namespace.
	DeleteBucket(ctx context.Context, bucket string) error
	// Stats reports usage information for monitoring.
	Stats(ctx context.Context) (Stats, error)
	Close() error
}

// Writer receives blob bytes.
type Writer interface {
	io.Writer
	// Commit durably stores the blob and returns its ID and size.
	Commit() (id string, size int64, err error)
	// Abort discards the blob.
	Abort() error
}

// Stats is backend usage information.
type Stats struct {
	TotalBytes int64
	FreeBytes  int64
	UsedBytes  int64
}
