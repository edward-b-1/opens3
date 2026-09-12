package blob

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"syscall"

	"gitlab.com/Birdsall/opens3/internal/meta"
)

// FS stores blobs as files: <root>/data/<bucket>/<id[:2]>/<id>, written to
// <root>/tmp first and renamed into place after fsync. See docs/FORMAT.md.
type FS struct {
	root string
	sync bool
}

// OpenFS opens a filesystem blob store rooted at root. If fsync is false
// commits do not call fsync (only for tests and benchmarks).
func OpenFS(root string, fsync bool) (*FS, error) {
	for _, d := range []string{"data", "tmp"} {
		if err := os.MkdirAll(filepath.Join(root, d), 0o755); err != nil {
			return nil, err
		}
	}
	// Clear leftovers from interrupted writes.
	entries, _ := os.ReadDir(filepath.Join(root, "tmp"))
	for _, e := range entries {
		_ = os.Remove(filepath.Join(root, "tmp", e.Name()))
	}
	return &FS{root: root, sync: fsync}, nil
}

func (f *FS) path(bucket, id string) string {
	if len(id) < 2 {
		id = "00" + id
	}
	return filepath.Join(f.root, "data", bucket, id[:2], id)
}

func (f *FS) Create(ctx context.Context, bucket string) (Writer, error) {
	id := meta.NewID()
	tmp := filepath.Join(f.root, "tmp", id)
	file, err := os.OpenFile(tmp, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
	if err != nil {
		return nil, err
	}
	return &fsWriter{fs: f, bucket: bucket, id: id, tmp: tmp, f: file}, nil
}

type fsWriter struct {
	fs     *FS
	bucket string
	id     string
	tmp    string
	f      *os.File
	n      int64
	done   bool
}

func (w *fsWriter) Write(p []byte) (int, error) {
	n, err := w.f.Write(p)
	w.n += int64(n)
	return n, err
}

func (w *fsWriter) Commit() (string, int64, error) {
	if w.done {
		return "", 0, errors.New("blob: writer already finished")
	}
	w.done = true
	if w.fs.sync {
		if err := w.f.Sync(); err != nil {
			w.f.Close()
			os.Remove(w.tmp)
			return "", 0, err
		}
	}
	if err := w.f.Close(); err != nil {
		os.Remove(w.tmp)
		return "", 0, err
	}
	dst := w.fs.path(w.bucket, w.id)
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		os.Remove(w.tmp)
		return "", 0, err
	}
	if err := os.Rename(w.tmp, dst); err != nil {
		os.Remove(w.tmp)
		return "", 0, err
	}
	if w.fs.sync {
		if d, err := os.Open(filepath.Dir(dst)); err == nil {
			_ = d.Sync()
			d.Close()
		}
	}
	return w.id, w.n, nil
}

func (w *fsWriter) Abort() error {
	if w.done {
		return nil
	}
	w.done = true
	w.f.Close()
	return os.Remove(w.tmp)
}

func (f *FS) Open(ctx context.Context, bucket, id string, offset, length int64) (io.ReadCloser, error) {
	file, err := os.Open(f.path(bucket, id))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	if offset > 0 {
		if _, err := file.Seek(offset, io.SeekStart); err != nil {
			file.Close()
			return nil, err
		}
	}
	if length < 0 {
		return file, nil
	}
	return &limitedFile{Reader: io.LimitReader(file, length), f: file}, nil
}

type limitedFile struct {
	io.Reader
	f *os.File
}

func (l *limitedFile) Close() error { return l.f.Close() }

func (f *FS) Size(ctx context.Context, bucket, id string) (int64, error) {
	st, err := os.Stat(f.path(bucket, id))
	if err != nil {
		if os.IsNotExist(err) {
			return 0, ErrNotFound
		}
		return 0, err
	}
	return st.Size(), nil
}

func (f *FS) Delete(ctx context.Context, bucket, id string) error {
	err := os.Remove(f.path(bucket, id))
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	// Opportunistically remove the empty shard directory.
	_ = os.Remove(filepath.Dir(f.path(bucket, id)))
	return nil
}

func (f *FS) DeleteBucket(ctx context.Context, bucket string) error {
	return os.RemoveAll(filepath.Join(f.root, "data", bucket))
}

func (f *FS) Stats(ctx context.Context) (Stats, error) {
	var st syscall.Statfs_t
	if err := syscall.Statfs(f.root, &st); err != nil {
		return Stats{}, err
	}
	total := int64(st.Blocks) * int64(st.Bsize)
	free := int64(st.Bavail) * int64(st.Bsize)
	return Stats{TotalBytes: total, FreeBytes: free, UsedBytes: total - free}, nil
}

func (f *FS) Close() error { return nil }

// Root returns the root directory (for diagnostics).
func (f *FS) Root() string { return f.root }

var _ Store = (*FS)(nil)

func (f *FS) String() string { return fmt.Sprintf("fs(%s)", f.root) }
