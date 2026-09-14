// Command opens3 is the OpenS3 server and administration CLI.
package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"runtime"
	"runtime/debug"
	"syscall"

	"github.com/edward-b-1/opens3/internal/admin"
	"github.com/edward-b-1/opens3/internal/server"
)

// version is set at link time by the Makefile and GoReleaser
// (-X main.version=...); "dev" otherwise. It is reported by `opens3
// version`, the startup log, `opens3 admin info` and the console.
var version = "dev"

func init() {
	admin.Version = version
	server.Version = version
}

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}
	switch os.Args[1] {
	case "server":
		os.Exit(runServer(os.Args[2:]))
	case "admin":
		os.Exit(runAdmin(os.Args[2:]))
	case "version":
		fmt.Println(versionString())
	case "-h", "--help", "help":
		usage()
	default:
		fmt.Fprintf(os.Stderr, "unknown command %q\n", os.Args[1])
		usage()
		os.Exit(2)
	}
}

// versionString is "opens3 <version> (<go version> <os>/<arch>, commit
// <rev>)"; the commit comes from the VCS stamp the Go toolchain embeds and
// is omitted when the binary was built outside a git checkout.
func versionString() string {
	s := fmt.Sprintf("opens3 %s (%s %s/%s", version, runtime.Version(), runtime.GOOS, runtime.GOARCH)
	if bi, ok := debug.ReadBuildInfo(); ok {
		var rev, modified string
		for _, kv := range bi.Settings {
			switch kv.Key {
			case "vcs.revision":
				rev = kv.Value
			case "vcs.modified":
				if kv.Value == "true" {
					modified = "-dirty"
				}
			}
		}
		if len(rev) > 12 {
			rev = rev[:12]
		}
		if rev != "" {
			s += ", commit " + rev + modified
		}
	}
	return s + ")"
}

func usage() {
	fmt.Fprintln(os.Stderr, `usage: opens3 <command> [flags]

commands:
  server    run the object storage server
  version   print the version

environment:
  OPENS3_ROOT_USER, OPENS3_ROOT_PASSWORD   root credentials (required)
  OPENS3_MASTER_KEY                        KMS master key material (recommended)
  OPENS3_ROOT, OPENS3_ADDRESS, OPENS3_REGION, OPENS3_DOMAINS, OPENS3_TLS_CERT, OPENS3_TLS_KEY`)
}

func runServer(args []string) int {
	fs := flag.NewFlagSet("server", flag.ContinueOnError)
	cfg := server.Config{}
	fs.StringVar(&cfg.Root, "root", "./data", "data directory")
	fs.StringVar(&cfg.Address, "address", ":9000", "listen address")
	fs.StringVar(&cfg.Region, "region", "us-east-1", "region name reported to clients")
	fs.BoolVar(&cfg.EnforceRegion, "enforce-region", false, "reject signatures for other regions")
	fs.BoolVar(&cfg.NoFsync, "no-fsync", false, "skip fsync on writes (unsafe; benchmarks only)")
	fs.StringVar(&cfg.TLSCert, "tls-cert", "", "TLS certificate file")
	fs.StringVar(&cfg.TLSKey, "tls-key", "", "TLS key file")
	logLevel := fs.String("log-level", "info", "log level: debug, info, warn, error")
	logJSON := fs.Bool("log-json", false, "log in JSON")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	cfg = server.ConfigFromEnv(cfg)
	var lvl slog.Level
	if err := lvl.UnmarshalText([]byte(*logLevel)); err != nil {
		lvl = slog.LevelInfo
	}
	var h slog.Handler
	if *logJSON {
		h = slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{Level: lvl})
	} else {
		h = slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: lvl})
	}
	cfg.Log = slog.New(h)
	slog.SetDefault(cfg.Log)
	srv, err := server.New(cfg)
	if err != nil {
		cfg.Log.Error("startup failed", "err", err)
		return 1
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	cfg.Log.Info("starting opens3", "version", version, "config", cfg.String())
	if err := srv.ListenAndServe(ctx); err != nil {
		cfg.Log.Error("server error", "err", err)
		return 1
	}
	return 0
}
