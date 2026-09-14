package main

import (
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	"github.com/edward-b-1/opens3/internal/blob"
	"github.com/edward-b-1/opens3/internal/kms"
	"github.com/edward-b-1/opens3/internal/kv"
	"github.com/edward-b-1/opens3/internal/object"
)

// offline is a data directory opened without a server: the metadata
// database, the object files and, when the master key is available, the
// KMS and an object service that can decrypt. `opens3 fsck` and
// `opens3 export` use it; `opens3 master` opens its own since it edits
// the key ring.
type offline struct {
	root  string
	db    kv.Store
	blobs *blob.FS
	// obj is nil when no master key was found (plain objects still readable
	// through the files; encrypted ones not).
	obj     *object.Service
	keyNote string // why obj is nil, for the operator
}

// openOffline opens root. The database is locked by a running server, in
// which case the error says so.
func openOffline(root string) (*offline, error) {
	dbPath := filepath.Join(root, "meta", "opens3.db")
	if _, err := os.Stat(dbPath); err != nil {
		return nil, fmt.Errorf("no metadata database at %s: is --root the server's data directory?", dbPath)
	}
	db, err := kv.OpenBolt(dbPath)
	if err != nil {
		if strings.Contains(err.Error(), "timeout") {
			return nil, fmt.Errorf("the metadata database %s is locked: stop the server first (or check a snapshot)", dbPath)
		}
		return nil, err
	}
	o := &offline{root: root, db: db}
	o.blobs, err = blob.OpenFS(root, false)
	if err != nil {
		db.Close()
		return nil, err
	}
	// The master key: the environment, else the key file; neither is
	// created here (a copy of a data directory must not gain a key).
	var master *kms.Master
	if m, err := kms.MasterFromEnv("OPENS3_MASTER_KEY"); err != nil {
		db.Close()
		return nil, err
	} else if m != nil {
		master = m
	} else if m, err := kms.LoadMasterFile(filepath.Join(root, "meta", "master.keys")); err == nil {
		master = m
	} else if errors.Is(err, os.ErrNotExist) {
		o.keyNote = "no master key (OPENS3_MASTER_KEY unset and no meta/master.keys): encrypted objects cannot be read"
	} else {
		db.Close()
		return nil, err
	}
	if master != nil {
		k, err := kms.NewLocal(db, master)
		if err != nil {
			if errors.Is(err, kms.ErrMasterMismatch) {
				o.keyNote = "the master key does not match this data directory: encrypted objects cannot be read"
			} else {
				db.Close()
				return nil, err
			}
		} else {
			o.obj = object.New(db, o.blobs, k, "", slog.New(slog.NewTextHandler(io.Discard, nil)))
		}
	}
	return o, nil
}

func (o *offline) Close() { o.db.Close() }
