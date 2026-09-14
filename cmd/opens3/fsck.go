package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"
	"text/tabwriter"

	"github.com/edward-b-1/opens3/internal/fsck"
)

const fsckUsage = `usage: opens3 fsck <command> [--root DIR] [flags]

Checks a data directory against its metadata and repairs what can be
repaired without losing data. The server must be stopped, except that
check can run on a snapshot or copy.

commands:
  check     report problems; changes nothing (exit 1 when any are found)
  repair    fix the repairable problems found by check

flags:
  --root DIR      data directory (env OPENS3_ROOT, default ./data)
  --bucket NAME   only this bucket
  --verify        check: read every object and compare its content with
                  the recorded ETag (slow; decrypts with the master key)
  --dry-run       repair: list what would be done without doing it
  --examples N    problems listed per kind (default 10; 0 = all)

What repair does and does not do:
  - deletes orphan files and leftover temporary files
  - deletes or recreates dangling null-version pointers and upload indexes
  - deletes part records for uploads that no longer exist, or whose file
    is missing, and finishes interrupted bucket deletions
  - recreates the bucket record for objects whose bucket record is gone
  - never removes an object whose file is missing or damaged: restore it
    from a backup, or delete it with an S3 client
`

func runFsck(args []string) int { return runFsckIO(args, os.Stdout, os.Stderr) }

func runFsckIO(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("fsck", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	root := "./data"
	if v := os.Getenv("OPENS3_ROOT"); v != "" {
		root = v
	}
	fs.StringVar(&root, "root", root, "data directory")
	bucket := fs.String("bucket", "", "only this bucket")
	verify := fs.Bool("verify", false, "verify content")
	dryRun := fs.Bool("dry-run", false, "list repairs only")
	examples := fs.Int("examples", 10, "problems listed per kind")
	help := fs.Bool("help", false, "show help")
	fs.BoolVar(help, "h", false, "show help")
	if err := fs.Parse(args); err != nil {
		fmt.Fprintln(stderr, err)
		fmt.Fprint(stderr, fsckUsage)
		return 2
	}
	cmd := fs.Arg(0)
	if fs.NArg() > 1 {
		if err := fs.Parse(fs.Args()[1:]); err != nil || fs.NArg() > 0 {
			fmt.Fprint(stderr, fsckUsage)
			return 2
		}
	}
	if *help || cmd == "" || cmd == "help" {
		fmt.Fprint(stderr, fsckUsage)
		if cmd == "" && !*help {
			return 2
		}
		return 0
	}
	if cmd != "check" && cmd != "repair" {
		fmt.Fprintf(stderr, "unknown command %q\n", cmd)
		fmt.Fprint(stderr, fsckUsage)
		return 2
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	o, err := openOffline(root)
	if err != nil {
		fmt.Fprintln(stderr, "opens3 fsck:", err)
		return 2
	}
	defer o.Close()
	if o.keyNote != "" {
		fmt.Fprintln(stderr, "note:", o.keyNote)
	}
	rep, err := fsck.Check(ctx, o.db, root, fsck.Options{Bucket: *bucket, Verify: *verify, Objects: o.obj,
		Progress: func(line string) { fmt.Fprintln(stderr, " ", line) }})
	if err != nil {
		fmt.Fprintln(stderr, "opens3 fsck:", err)
		return 2
	}
	printReport(stdout, rep, *examples)
	if cmd == "check" {
		if len(rep.Problems) > 0 {
			return 1
		}
		return 0
	}
	// repair
	if rep.Repairable() == 0 {
		fmt.Fprintln(stdout, "Nothing to repair.")
		if len(rep.Problems) > 0 {
			return 1
		}
		return 0
	}
	res, err := fsck.Repair(ctx, o.db, root, rep, *dryRun)
	if res != nil {
		fmt.Fprintln(stdout)
		verb := "Repaired"
		if *dryRun {
			verb = "Would repair"
		}
		fmt.Fprintf(stdout, "%s %d of %d problems:\n", verb, len(res.Actions), len(rep.Problems))
		for i, a := range res.Actions {
			if *examples > 0 && i >= *examples {
				fmt.Fprintf(stdout, "  ... and %d more\n", len(res.Actions)-i)
				break
			}
			fmt.Fprintf(stdout, "  %s: %s/%s: %s\n", a.Kind, a.Bucket, a.Item, a.What)
		}
	}
	if err != nil {
		fmt.Fprintln(stderr, "opens3 fsck: repair stopped:", err)
		return 2
	}
	if left := len(rep.Problems) - rep.Repairable(); left > 0 {
		fmt.Fprintf(stdout, "%d problems need a backup or a decision; see the report above.\n", left)
		return 1
	}
	return 0
}

func printReport(w io.Writer, rep *fsck.Report, examples int) {
	fmt.Fprintf(w, "Data directory: %s\n", rep.Root)
	fmt.Fprintf(w, "  %d buckets, %d object versions, %d multipart uploads (%d parts), %d files, %s", rep.Buckets, rep.Objects, rep.Uploads, rep.Parts, rep.Files, humanBytes(rep.Bytes))
	if rep.Verified > 0 {
		fmt.Fprintf(w, ", %d objects verified", rep.Verified)
	}
	fmt.Fprintf(w, " (%s)\n", rep.Duration.Round(1e6))
	if len(rep.Problems) == 0 {
		fmt.Fprintln(w, "No problems found.")
		return
	}
	fmt.Fprintln(w)
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "PROBLEM\tCOUNT\tREPAIR")
	for _, k := range rep.Kinds() {
		fmt.Fprintf(tw, "%s\t%d\t%s\n", k, rep.Count(k), fsck.RepairAction(k))
	}
	tw.Flush()
	fmt.Fprintln(w)
	for _, k := range rep.Kinds() {
		n := 0
		for _, p := range rep.Problems {
			if p.Kind != k {
				continue
			}
			n++
			if examples > 0 && n > examples {
				fmt.Fprintf(w, "  ... and %d more %s\n", rep.Count(k)-examples, k)
				break
			}
			where := p.Item
			if p.Bucket != "" {
				where = p.Bucket + "/" + p.Item
			}
			fmt.Fprintf(w, "  %s: %s: %s\n", k, where, p.Detail)
		}
	}
	fmt.Fprintf(w, "\n%d problems, %d repairable", len(rep.Problems), rep.Repairable())
	if rep.Repairable() > 0 {
		fmt.Fprint(w, ": run `opens3 fsck repair` (with --dry-run first to see what it would do)")
	}
	fmt.Fprintln(w, ".")
}
