package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"text/tabwriter"

	"github.com/edward-b-1/opens3/internal/kms"
	"github.com/edward-b-1/opens3/internal/kv"
	"github.com/edward-b-1/opens3/internal/masterkey"
)

const masterUsage = `usage: opens3 master <command> [--root DIR]

Manages the master key ring that protects encrypted objects, named
encryption keys and stored access-key secrets. The server must be stopped:
these commands open the data directory directly.

commands:
  status    show the keys in the ring and how many records each protects
  rotate    add a new key to the ring and re-wrap every record under it
  rewrap    re-wrap every record under the current key
            (finishes an interrupted rotate; --dry-run only counts)
  retire    remove the older keys from the ring once no record needs them

flags:
  --root DIR   data directory (env OPENS3_ROOT, default ./data)
  --dry-run    rewrap: report what would be re-wrapped without writing

environment:
  OPENS3_MASTER_KEY      when set, the server takes its master key from the
                         environment instead of <root>/meta/master.keys;
                         rotate then needs OPENS3_MASTER_KEY_NEW and prints
                         what to do afterwards
  OPENS3_MASTER_KEY_OLD  a previous environment key, used only to read
                         records still wrapped under it (moving from the
                         environment to the key file, or finishing an
                         interrupted rotate)
`

// masterCmd is the parsed invocation.
type masterCmd struct {
	root   string
	dryRun bool
	out    io.Writer
	err    io.Writer
}

func runMaster(args []string) int {
	return runMasterIO(args, os.Stdout, os.Stderr)
}

func runMasterIO(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("master", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	c := &masterCmd{out: stdout, err: stderr}
	root := "./data"
	if v := os.Getenv("OPENS3_ROOT"); v != "" {
		root = v
	}
	fs.StringVar(&c.root, "root", root, "data directory")
	fs.BoolVar(&c.dryRun, "dry-run", false, "only report")
	help := fs.Bool("help", false, "show help")
	fs.BoolVar(help, "h", false, "show help")
	// Accept flags before or after the command.
	if err := fs.Parse(args); err != nil {
		fmt.Fprintln(stderr, err)
		fmt.Fprint(stderr, masterUsage)
		return 2
	}
	cmd := fs.Arg(0)
	if fs.NArg() > 1 {
		if err := fs.Parse(fs.Args()[1:]); err != nil {
			fmt.Fprintln(stderr, err)
			fmt.Fprint(stderr, masterUsage)
			return 2
		}
		if fs.NArg() > 0 {
			fmt.Fprintf(stderr, "unexpected argument %q\n", fs.Arg(0))
			fmt.Fprint(stderr, masterUsage)
			return 2
		}
	}
	if *help || cmd == "" || cmd == "help" {
		fmt.Fprint(stderr, masterUsage)
		if cmd == "" && !*help {
			return 2
		}
		return 0
	}
	var err error
	switch cmd {
	case "status":
		err = c.status()
	case "rotate":
		err = c.rotate()
	case "rewrap":
		err = c.rewrap()
	case "retire":
		err = c.retire()
	default:
		fmt.Fprintf(stderr, "unknown command %q\n", cmd)
		fmt.Fprint(stderr, masterUsage)
		return 2
	}
	if err != nil {
		fmt.Fprintln(stderr, "opens3 master:", err)
		return 1
	}
	return 0
}

// ring is what a command works with: the key that wraps (the first key of
// keys), every key that may unwrap, and where the primary came from.
type ring struct {
	keys  *kms.Master
	file  *kms.Master // the key file, when it exists (also the primary in file mode)
	env   bool        // the server's key comes from OPENS3_MASTER_KEY
	old   *kms.Master // OPENS3_MASTER_KEY_OLD, a fallback key, when set
	fresh bool        // the key file was created by this command
}

func (c *masterCmd) keyFile() string { return filepath.Join(c.root, "meta", "master.keys") }

// openRing assembles the ring the way the server would (environment key,
// else key file) plus the fallbacks that only these commands know about.
// For rotate, forRotate makes OPENS3_MASTER_KEY_NEW the wrapping key in
// environment mode and creates the key file when it is missing in file
// mode.
func (c *masterCmd) openRing(forRotate bool) (*ring, error) {
	cur, err := kms.MasterFromEnv("OPENS3_MASTER_KEY")
	if err != nil {
		return nil, err
	}
	old, err := kms.MasterFromEnv("OPENS3_MASTER_KEY_OLD")
	if err != nil {
		return nil, err
	}
	next, err := kms.MasterFromEnv("OPENS3_MASTER_KEY_NEW")
	if err != nil {
		return nil, err
	}
	file, err := kms.LoadMasterFile(c.keyFile())
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	r := &ring{file: file, old: old}
	if cur != nil {
		r.env = true
		if forRotate {
			if next == nil {
				return nil, errors.New("OPENS3_MASTER_KEY is set, so the server takes its master key from the environment: put the new key material in OPENS3_MASTER_KEY_NEW and run rotate again")
			}
			r.keys = next.WithFallback(cur, old, file)
			return r, nil
		}
		if next != nil {
			return nil, errors.New("OPENS3_MASTER_KEY_NEW is only used by rotate; unset it")
		}
		r.keys = cur.WithFallback(old, file)
		return r, nil
	}
	if next != nil {
		return nil, errors.New("OPENS3_MASTER_KEY_NEW is only used by rotate with OPENS3_MASTER_KEY set; in file mode rotate generates the new key itself")
	}
	if file == nil {
		if !forRotate {
			return nil, fmt.Errorf("no master key file at %s (set OPENS3_MASTER_KEY if the server takes its key from the environment)", c.keyFile())
		}
		if old == nil {
			return nil, fmt.Errorf("no master key file at %s: this is not an initialised data directory (set OPENS3_MASTER_KEY_OLD to move a data directory from an environment key to a key file)", c.keyFile())
		}
		// rotate creates the file once the database is open and the old
		// key has been checked against it.
		r.keys, r.fresh = old, true
		return r, nil
	}
	r.keys = file.WithFallback(old)
	return r, nil
}

func (c *masterCmd) openDB() (kv.Store, error) {
	path := filepath.Join(c.root, "meta", "opens3.db")
	if _, err := os.Stat(path); err != nil {
		return nil, fmt.Errorf("no metadata database at %s: is --root the server's data directory?", path)
	}
	db, err := kv.OpenBolt(path)
	if err != nil {
		if strings.Contains(err.Error(), "timeout") {
			return nil, fmt.Errorf("the metadata database %s is locked: stop the server first", path)
		}
		return nil, err
	}
	return db, nil
}

func (c *masterCmd) run(r *ring, db kv.Store, dryRun bool) (masterkey.Report, error) {
	if err := masterkey.Check(db, r.keys); err != nil {
		return masterkey.Report{}, err
	}
	return masterkey.Run(db, r.keys, masterkey.Options{DryRun: dryRun, Progress: func(kind string, scanned int) {
		if scanned > 0 && scanned%50000 == 0 {
			fmt.Fprintf(c.err, "  %s: %d scanned\n", kind, scanned)
		}
	}})
}

func (c *masterCmd) status() error {
	r, err := c.openRing(false)
	if err != nil {
		return err
	}
	db, err := c.openDB()
	if err != nil {
		return err
	}
	defer db.Close()
	rep, err := c.run(r, db, true)
	if err != nil {
		return err
	}
	c.printRing(r, rep)
	c.printReport(rep)
	c.advise(r, rep)
	return nil
}

func (c *masterCmd) rotate() error {
	r, err := c.openRing(true)
	if err != nil {
		return err
	}
	db, err := c.openDB()
	if err != nil {
		return err
	}
	defer db.Close()
	// Refuse before touching the ring when the keys do not match the data
	// directory at all.
	if err := masterkey.Check(db, r.keys); err != nil {
		return err
	}
	switch {
	case r.fresh:
		file, _, err := kms.LoadOrCreateMasterFile(c.keyFile())
		if err != nil {
			return err
		}
		r.file = file
		r.keys = file.WithFallback(r.old)
		fmt.Fprintf(c.out, "Created %s with key %s (fingerprint %s).\n", file.Path(), file.Info()[0].ID, file.Fingerprint())
	case !r.env:
		info, err := r.file.AddKey()
		if err != nil {
			return err
		}
		r.keys = r.file.WithFallback(r.old) // the new key first; the rest only unwrap
		fmt.Fprintf(c.out, "Added key %s (fingerprint %s) to %s.\n", info.ID, info.Fingerprint, r.file.Path())
	}
	rep, err := c.run(r, db, false)
	if err != nil {
		return err
	}
	c.printReport(rep)
	fmt.Fprintf(c.out, "\nRe-wrapped %d records under the new key", rep.Rewrapped())
	if s := rep.Stale(); s > 0 {
		fmt.Fprintf(c.out, "; %d could not be re-wrapped, run `opens3 master rewrap` again", s)
	}
	fmt.Fprintln(c.out, ".")
	switch {
	case r.env:
		fmt.Fprintln(c.out, "Set OPENS3_MASTER_KEY to the value of OPENS3_MASTER_KEY_NEW before starting the server, and unset OPENS3_MASTER_KEY_NEW.")
		if r.file != nil {
			fmt.Fprintf(c.out, "The key file %s is no longer needed by the server; `opens3 master retire` removes it once nothing depends on it.\n", r.file.Path())
		}
		fmt.Fprintln(c.out, "Keep the previous key (as OPENS3_MASTER_KEY_OLD) as long as you may need to restore a metadata backup taken before this rotation.")
	case r.fresh:
		fmt.Fprintf(c.out, "Start the server without OPENS3_MASTER_KEY; it will use %s. Back the file up now.\n", r.file.Path())
		fmt.Fprintln(c.out, "Keep the previous key (as OPENS3_MASTER_KEY_OLD) as long as you may need to restore a metadata backup taken before this change.")
	default:
		fmt.Fprintf(c.out, "Back up %s again: it now holds a key that earlier copies lack.\n", r.file.Path())
		fmt.Fprintln(c.out, "The older keys stay in the ring so that metadata backups taken before this rotation remain restorable; `opens3 master retire` removes them when you no longer need that.")
	}
	return nil
}

func (c *masterCmd) rewrap() error {
	r, err := c.openRing(false)
	if err != nil {
		return err
	}
	db, err := c.openDB()
	if err != nil {
		return err
	}
	defer db.Close()
	rep, err := c.run(r, db, c.dryRun)
	if err != nil {
		return err
	}
	c.printReport(rep)
	if c.dryRun {
		fmt.Fprintf(c.out, "\nDry run: %d records would be re-wrapped under the current key (%s).\n", rep.Stale(), r.keys.Fingerprint())
	} else {
		fmt.Fprintf(c.out, "\nRe-wrapped %d records under the current key (%s).\n", rep.Rewrapped(), r.keys.Fingerprint())
	}
	c.advise(r, rep)
	return nil
}

func (c *masterCmd) retire() error {
	r, err := c.openRing(false)
	if err != nil {
		return err
	}
	db, err := c.openDB()
	if err != nil {
		return err
	}
	defer db.Close()
	rep, err := c.run(r, db, true)
	if err != nil {
		return err
	}
	if s := rep.Stale(); s > 0 {
		c.printReport(rep)
		return fmt.Errorf("%d records are still wrapped under older keys; run `opens3 master rewrap` first", s)
	}
	if u := rep.Unreadable(); u > 0 {
		fmt.Fprintf(c.out, "Warning: %d records are readable by no key in the ring; retiring keys does not change that.\n", u)
	}
	switch {
	case r.env && r.file != nil:
		if err := os.Remove(r.file.Path()); err != nil {
			return err
		}
		fmt.Fprintf(c.out, "Removed %s: every record is wrapped under OPENS3_MASTER_KEY.\n", r.file.Path())
	case r.env:
		fmt.Fprintln(c.out, "Nothing to retire: the ring is the single key in OPENS3_MASTER_KEY.")
	default:
		removed, err := r.file.Prune()
		if err != nil {
			return err
		}
		if len(removed) == 0 {
			fmt.Fprintf(c.out, "Nothing to retire: %s holds a single key.\n", r.file.Path())
		} else {
			ids := make([]string, len(removed))
			for i, k := range removed {
				ids[i] = k.ID + " (" + k.Fingerprint + ")"
			}
			fmt.Fprintf(c.out, "Removed %s from %s. Metadata backups taken before the rotation can no longer be read with this file.\n", strings.Join(ids, ", "), r.file.Path())
		}
	}
	if r.old != nil {
		fmt.Fprintln(c.out, "OPENS3_MASTER_KEY_OLD is no longer needed by this data directory.")
	}
	return nil
}

func (c *masterCmd) printRing(r *ring, rep masterkey.Report) {
	src := "OPENS3_MASTER_KEY"
	if !r.env {
		src = r.file.Path()
	}
	fmt.Fprintf(c.out, "Master key ring: %s\n", src)
	tw := tabwriter.NewWriter(c.out, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "  ID\tSOURCE\tCREATED\tFINGERPRINT\tRECORDS")
	for i, k := range r.keys.Info() {
		created := "-"
		if !k.Created.IsZero() {
			created = k.Created.UTC().Format("2006-01-02 15:04 UTC")
		}
		role := fmt.Sprintf("%d", rep.ByKey(i))
		if i == 0 {
			role += " (current)"
		}
		source := k.Source
		if strings.HasPrefix(source, "file:") {
			source = "file"
		}
		fmt.Fprintf(tw, "  %s\t%s\t%s\t%s\t%s\n", k.ID, source, created, k.Fingerprint, role)
	}
	tw.Flush()
	fmt.Fprintln(c.out)
}

func (c *masterCmd) printReport(rep masterkey.Report) {
	fmt.Fprintf(c.out, "%-28s %7s %8s %11s %11s %11s\n", "RECORDS", "TOTAL", "CURRENT", "OLDER KEYS", "UNREADABLE", "RE-WRAPPED")
	for _, k := range rep.Kinds {
		fmt.Fprintf(c.out, "%-28s %7d %8d %11d %11d %11d\n", k.Name, k.Total, k.Current(), k.Stale(), k.Unreadable, k.Rewrapped)
	}
}

func (c *masterCmd) advise(r *ring, rep masterkey.Report) {
	if s := rep.Stale(); s > 0 && c.dryRun || s > 0 && rep.Rewrapped() == 0 {
		fmt.Fprintf(c.out, "\n%d records are wrapped under older keys: run `opens3 master rewrap`.\n", s)
	}
	if u := rep.Unreadable(); u > 0 {
		fmt.Fprintf(c.out, "\n%d records are readable by no key in the ring. If they were wrapped under a previous OPENS3_MASTER_KEY, set OPENS3_MASTER_KEY_OLD to it and run `opens3 master rewrap`.\n", u)
	}
	if rep.Stale() == 0 && rep.Unreadable() == 0 && (r.keys.Keys() > 1) {
		fmt.Fprintln(c.out, "\nEvery record is under the current key; `opens3 master retire` removes the older keys from the ring.")
	}
}
