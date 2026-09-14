package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"

	"github.com/edward-b-1/opens3/internal/fsck"
)

const exportUsage = `usage: opens3 export --to OUTDIR [--root DIR] [flags]

Writes objects out as plain files: OUTDIR/<bucket>/<key>, decrypting
SSE-S3 and SSE-KMS objects with the master key. A manifest
(OUTDIR/manifest.jsonl, one JSON line per object) records key, version,
size, ETag, checksum, content type, metadata and tags, and says why an
object was not written (delete marker; SSE-C, whose key the server does
not hold). The server must be stopped, or export from a snapshot.

flags:
  --to OUTDIR      output directory (required; created if missing)
  --root DIR       data directory (env OPENS3_ROOT, default ./data)
  --bucket NAME    only this bucket
  --prefix P       only keys starting with P
  --all-versions   every version: older ones are written as <key>@<versionId>
  --overwrite      replace files already in OUTDIR (default: keep them)
`

func runExport(args []string) int { return runExportIO(args, os.Stdout, os.Stderr) }

func runExportIO(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("export", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	root := "./data"
	if v := os.Getenv("OPENS3_ROOT"); v != "" {
		root = v
	}
	fs.StringVar(&root, "root", root, "data directory")
	to := fs.String("to", "", "output directory")
	bucket := fs.String("bucket", "", "only this bucket")
	prefix := fs.String("prefix", "", "only this key prefix")
	all := fs.Bool("all-versions", false, "every version")
	overwrite := fs.Bool("overwrite", false, "replace existing files")
	help := fs.Bool("help", false, "show help")
	fs.BoolVar(help, "h", false, "show help")
	if err := fs.Parse(args); err != nil || fs.NArg() > 0 {
		fmt.Fprint(stderr, exportUsage)
		return 2
	}
	if *help {
		fmt.Fprint(stderr, exportUsage)
		return 0
	}
	if *to == "" {
		fmt.Fprintln(stderr, "opens3 export: --to OUTDIR is required")
		fmt.Fprint(stderr, exportUsage)
		return 2
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	o, err := openOffline(root)
	if err != nil {
		fmt.Fprintln(stderr, "opens3 export:", err)
		return 2
	}
	defer o.Close()
	if o.obj == nil {
		fmt.Fprintln(stderr, "opens3 export:", o.keyNote)
		return 2
	}
	res, err := fsck.Export(ctx, o.obj, *to, fsck.ExportOptions{Bucket: *bucket, Prefix: *prefix, AllVersions: *all, Overwrite: *overwrite,
		Progress: func(line string) { fmt.Fprintln(stderr, " ", line) }})
	if res != nil {
		fmt.Fprintf(stdout, "Exported %d objects (%s) from %d buckets to %s; %d not written (see %s).\n",
			res.Exported, humanBytes(res.Bytes), res.Buckets, *to, res.Skipped, res.Manifest)
	}
	if err != nil {
		fmt.Fprintln(stderr, "opens3 export:", err)
		return 2
	}
	return 0
}
