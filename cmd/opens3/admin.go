package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"text/tabwriter"
	"time"

	"gitlab.com/Birdsall/opens3/internal/admin"
)

func init() { admin.Version = version }

const adminUsage = `usage: opens3 admin [flags] <command> [args]

flags:
  --endpoint URL     server URL (env OPENS3_ENDPOINT, default http://localhost:9000)
  --access-key K     admin access key (env OPENS3_ACCESS_KEY or OPENS3_ROOT_USER)
  --secret-key S     admin secret key (env OPENS3_SECRET_KEY or OPENS3_ROOT_PASSWORD)
  --region R         signing region (env OPENS3_REGION, default us-east-1)
  --json             print raw JSON responses

commands:
  info                                    server version, uptime, usage
  health                                  readiness check
  user list | info NAME
  user add NAME [--secret S] [--policy p1,p2]
  user rm NAME | enable NAME | disable NAME
  user policy NAME [p1,p2]                set attached policies (empty detaches all)
  key list [--user U]
  key add USER [--access-key AK] [--secret S] [--service] [--policy-file F]
                [--expires DUR|RFC3339] [--description D]
  key rm AK | enable AK | disable AK
  key rotate AK [--secret S]
  group list | info NAME
  group add NAME [--members u1,u2] [--policy p1,p2]
  group rm NAME
  group members NAME [--add u1,u2] [--remove u3]
  policy list | get NAME | rm NAME
  policy set NAME FILE                    FILE may be - for stdin
  bucket list [--usage]
  bucket rm NAME [--force]
  kms list | add ID | rm ID
`

// adminCLI holds the parsed global options.
type adminCLI struct {
	c    *admin.Client
	json bool
	out  io.Writer
	ctx  context.Context
}

func runAdmin(args []string) int {
	fs := flag.NewFlagSet("admin", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	env := func(keys ...string) string {
		for _, k := range keys {
			if v := os.Getenv(k); v != "" {
				return v
			}
		}
		return ""
	}
	endpoint := fs.String("endpoint", "", "server URL")
	ak := fs.String("access-key", "", "access key")
	sk := fs.String("secret-key", "", "secret key")
	region := fs.String("region", "", "signing region")
	asJSON := fs.Bool("json", false, "JSON output")
	help := fs.Bool("help", false, "show help")
	fs.BoolVar(help, "h", false, "show help")
	global, rest := splitGlobalFlags(args)
	if err := fs.Parse(global); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		fmt.Fprint(os.Stderr, adminUsage)
		return 2
	}
	if *help || len(rest) == 0 || rest[0] == "help" {
		fmt.Fprint(os.Stderr, adminUsage)
		if *help || len(rest) > 0 {
			return 0
		}
		return 2
	}
	if *endpoint == "" {
		*endpoint = env("OPENS3_ENDPOINT")
	}
	if *endpoint == "" {
		*endpoint = "http://localhost:9000"
	}
	if !strings.Contains(*endpoint, "://") {
		*endpoint = "http://" + *endpoint
	}
	if *ak == "" {
		*ak = env("OPENS3_ACCESS_KEY", "OPENS3_ROOT_USER")
	}
	if *sk == "" {
		*sk = env("OPENS3_SECRET_KEY", "OPENS3_ROOT_PASSWORD")
	}
	if *region == "" {
		*region = env("OPENS3_REGION")
	}
	if *ak == "" || *sk == "" {
		fmt.Fprintln(os.Stderr, "error: credentials required (--access-key/--secret-key or OPENS3_ACCESS_KEY/OPENS3_SECRET_KEY)")
		return 2
	}
	cli := &adminCLI{c: &admin.Client{Endpoint: *endpoint, AccessKey: *ak, SecretKey: *sk, Region: *region}, json: *asJSON, out: os.Stdout, ctx: context.Background()}
	if err := cli.run(rest); err != nil {
		var ue usageError
		if errors.As(err, &ue) {
			fmt.Fprintln(os.Stderr, "error:", err)
			fmt.Fprint(os.Stderr, adminUsage)
			return 2
		}
		fmt.Fprintln(os.Stderr, "error:", err)
		return 1
	}
	return 0
}

type usageError string

// splitGlobalFlags separates the connection/output flags, which may appear
// anywhere on the command line, from the command and its own flags.
func splitGlobalFlags(args []string) (global, rest []string) {
	valued := map[string]bool{"endpoint": true, "access-key": true, "secret-key": true, "region": true}
	boolean := map[string]bool{"json": true, "help": true, "h": true}
	for i := 0; i < len(args); i++ {
		a := args[i]
		if !strings.HasPrefix(a, "-") || a == "-" || a == "--" {
			rest = append(rest, a)
			continue
		}
		name, val, hasVal := strings.Cut(strings.TrimLeft(a, "-"), "=")
		switch {
		case valued[name] && hasVal:
			global = append(global, "-"+name+"="+val)
		case valued[name] && i+1 < len(args):
			global = append(global, "-"+name, args[i+1])
			i++
		case boolean[name]:
			global = append(global, a)
		default:
			rest = append(rest, a)
		}
	}
	return global, rest
}

func (u usageError) Error() string { return string(u) }

// parseInterleaved parses flags that may appear before or after
// positional arguments (the flag package stops at the first positional).
func parseInterleaved(fs *flag.FlagSet, args []string) ([]string, error) {
	var pos []string
	for {
		if err := fs.Parse(args); err != nil {
			return nil, err
		}
		args = fs.Args()
		if len(args) == 0 {
			return pos, nil
		}
		pos = append(pos, args[0])
		args = args[1:]
	}
}

func newFlags(name string) *flag.FlagSet {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	return fs
}

func splitList(s string) []string {
	if s == "" {
		return []string{}
	}
	var out []string
	for _, v := range strings.Split(s, ",") {
		if v = strings.TrimSpace(v); v != "" {
			out = append(out, v)
		}
	}
	return out
}

func (a *adminCLI) run(args []string) error {
	cmd, rest := args[0], args[1:]
	switch cmd {
	case "info":
		info, err := a.c.Info(a.ctx)
		if err != nil {
			return err
		}
		return a.print(info, func(w io.Writer) {
			fmt.Fprintf(w, "Version:\t%s\n", info.Version)
			fmt.Fprintf(w, "Region:\t%s\n", info.Region)
			fmt.Fprintf(w, "Started:\t%s\n", info.StartTime.Local().Format(time.RFC3339))
			fmt.Fprintf(w, "Uptime:\t%s\n", (time.Duration(info.UptimeSeconds) * time.Second).String())
			fmt.Fprintf(w, "Buckets:\t%d\n", info.Buckets)
			fmt.Fprintf(w, "Objects:\t%d\n", info.Objects)
			fmt.Fprintf(w, "Data:\t%s\n", humanBytes(info.TotalBytes))
			fmt.Fprintf(w, "Disk:\t%s used, %s free, %s total\n", humanBytes(info.Disk.UsedBytes), humanBytes(info.Disk.FreeBytes), humanBytes(info.Disk.TotalBytes))
		})
	case "health":
		if err := a.c.Health(a.ctx); err != nil {
			return err
		}
		return a.print(admin.Status{Status: "ok"}, func(w io.Writer) { fmt.Fprintln(w, "ok") })
	case "user":
		return a.user(rest)
	case "key":
		return a.key(rest)
	case "group":
		return a.group(rest)
	case "policy":
		return a.policy(rest)
	case "bucket":
		return a.bucket(rest)
	case "kms":
		return a.kms(rest)
	}
	return usageError("unknown command " + cmd)
}

// print writes v as JSON when --json is set, otherwise calls table with a
// tab-aligned writer.
func (a *adminCLI) print(v any, table func(w io.Writer)) error {
	if a.json {
		enc := json.NewEncoder(a.out)
		enc.SetIndent("", "  ")
		return enc.Encode(v)
	}
	w := tabwriter.NewWriter(a.out, 0, 8, 2, ' ', 0)
	table(w)
	return w.Flush()
}

func humanBytes(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for m := n / unit; m >= unit; m /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", float64(n)/float64(div), "KMGTPE"[exp])
}

func onOff(b bool) string {
	if b {
		return "enabled"
	}
	return "disabled"
}

func sub(args []string) (string, []string, error) {
	if len(args) == 0 {
		return "", nil, usageError("missing subcommand")
	}
	return args[0], args[1:], nil
}

func needArg(pos []string, n int, what string) error {
	if len(pos) < n {
		return usageError("missing " + what)
	}
	return nil
}

// --- users ---------------------------------------------------------------

func (a *adminCLI) printUsers(us []*admin.UserInfo) error {
	return a.print(us, func(w io.Writer) {
		fmt.Fprintln(w, "NAME\tSTATUS\tPOLICIES\tGROUPS\tCREATED")
		for _, u := range us {
			fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\n", u.Name, onOff(u.Enabled), strings.Join(u.Policies, ","), strings.Join(u.Groups, ","), u.Created.Local().Format(time.RFC3339))
		}
	})
}

func (a *adminCLI) user(args []string) error {
	cmd, rest, err := sub(args)
	if err != nil {
		return err
	}
	fs := newFlags("user " + cmd)
	secret := fs.String("secret", "", "secret key")
	policies := fs.String("policy", "", "comma-separated policies")
	pos, err := parseInterleaved(fs, rest)
	if err != nil {
		return usageError(err.Error())
	}
	switch cmd {
	case "list", "ls":
		us, err := a.c.ListUsers(a.ctx)
		if err != nil {
			return err
		}
		return a.printUsers(us)
	case "info", "get":
		if err := needArg(pos, 1, "user name"); err != nil {
			return err
		}
		u, err := a.c.GetUser(a.ctx, pos[0])
		if err != nil {
			return err
		}
		return a.printUsers([]*admin.UserInfo{u})
	case "add":
		if err := needArg(pos, 1, "user name"); err != nil {
			return err
		}
		in := admin.PutUserRequest{SecretKey: *secret}
		if isSet(fs, "policy") {
			in.Policies = splitList(*policies)
		}
		u, err := a.c.PutUser(a.ctx, pos[0], in)
		if err != nil {
			return err
		}
		return a.printUsers([]*admin.UserInfo{u})
	case "rm", "remove", "delete":
		if err := needArg(pos, 1, "user name"); err != nil {
			return err
		}
		return a.c.DeleteUser(a.ctx, pos[0])
	case "enable", "disable":
		if err := needArg(pos, 1, "user name"); err != nil {
			return err
		}
		u, err := a.c.SetUserStatus(a.ctx, pos[0], cmd == "enable")
		if err != nil {
			return err
		}
		return a.printUsers([]*admin.UserInfo{u})
	case "policy":
		if err := needArg(pos, 1, "user name"); err != nil {
			return err
		}
		list := []string{}
		if len(pos) > 1 {
			list = splitList(strings.Join(pos[1:], ","))
		} else if *policies != "" {
			list = splitList(*policies)
		}
		u, err := a.c.SetUserPolicies(a.ctx, pos[0], list)
		if err != nil {
			return err
		}
		return a.printUsers([]*admin.UserInfo{u})
	}
	return usageError("unknown user subcommand " + cmd)
}

func isSet(fs *flag.FlagSet, name string) bool {
	set := false
	fs.Visit(func(f *flag.Flag) {
		if f.Name == name {
			set = true
		}
	})
	return set
}

// --- keys ----------------------------------------------------------------

func (a *adminCLI) printKeys(ks []admin.KeyInfo) error {
	return a.print(ks, func(w io.Writer) {
		fmt.Fprintln(w, "ACCESS KEY\tUSER\tKIND\tSTATUS\tEXPIRES\tDESCRIPTION")
		for _, k := range ks {
			exp := "-"
			if k.Expires != nil {
				exp = k.Expires.Local().Format(time.RFC3339)
			}
			fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\n", k.AccessKey, k.User, k.Kind, onOff(k.Enabled), exp, k.Description)
		}
	})
}

func (a *adminCLI) printCredentials(c *admin.Credentials) error {
	return a.print(c, func(w io.Writer) {
		fmt.Fprintf(w, "Access key:\t%s\n", c.AccessKey)
		fmt.Fprintf(w, "Secret key:\t%s\n", c.SecretKey)
		fmt.Fprintf(w, "User:\t%s\n", c.User)
		fmt.Fprintf(w, "Kind:\t%s\n", c.Kind)
		if c.Expires != nil {
			fmt.Fprintf(w, "Expires:\t%s\n", c.Expires.Local().Format(time.RFC3339))
		}
	})
}

func parseExpiry(s string) (*time.Time, error) {
	if s == "" {
		return nil, nil
	}
	if d, err := time.ParseDuration(s); err == nil {
		t := time.Now().Add(d).UTC()
		return &t, nil
	}
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		return nil, usageError("invalid --expires: use a duration (72h) or RFC3339 time")
	}
	return &t, nil
}

func (a *adminCLI) key(args []string) error {
	cmd, rest, err := sub(args)
	if err != nil {
		return err
	}
	fs := newFlags("key " + cmd)
	user := fs.String("user", "", "owning user")
	accessKey := fs.String("access-key", "", "access key (generated if empty)")
	secret := fs.String("secret", "", "secret key (generated if empty)")
	service := fs.Bool("service", false, "create a service account key")
	policyFile := fs.String("policy-file", "", "session policy JSON file")
	expires := fs.String("expires", "", "expiry as duration or RFC3339 time")
	description := fs.String("description", "", "description")
	pos, err := parseInterleaved(fs, rest)
	if err != nil {
		return usageError(err.Error())
	}
	switch cmd {
	case "list", "ls":
		if *user == "" && len(pos) > 0 {
			*user = pos[0]
		}
		ks, err := a.c.ListKeys(a.ctx, *user)
		if err != nil {
			return err
		}
		return a.printKeys(ks)
	case "add":
		if *user == "" && len(pos) > 0 {
			*user = pos[0]
		}
		if *user == "" {
			return usageError("missing user name")
		}
		in := admin.CreateKeyRequest{User: *user, AccessKey: *accessKey, SecretKey: *secret, Description: *description}
		if *service {
			in.Kind = "service"
		}
		if *policyFile != "" {
			doc, err := readFileOrStdin(*policyFile)
			if err != nil {
				return err
			}
			in.SessionPolicy = doc
		}
		if in.Expires, err = parseExpiry(*expires); err != nil {
			return err
		}
		c, err := a.c.CreateKey(a.ctx, in)
		if err != nil {
			return err
		}
		return a.printCredentials(c)
	case "rm", "remove", "delete":
		if err := needArg(pos, 1, "access key"); err != nil {
			return err
		}
		return a.c.DeleteKey(a.ctx, pos[0])
	case "enable", "disable":
		if err := needArg(pos, 1, "access key"); err != nil {
			return err
		}
		k, err := a.c.SetKeyStatus(a.ctx, pos[0], cmd == "enable")
		if err != nil {
			return err
		}
		return a.printKeys([]admin.KeyInfo{*k})
	case "rotate":
		if err := needArg(pos, 1, "access key"); err != nil {
			return err
		}
		c, err := a.c.RotateKey(a.ctx, pos[0], *secret)
		if err != nil {
			return err
		}
		return a.printCredentials(c)
	}
	return usageError("unknown key subcommand " + cmd)
}

func readFileOrStdin(name string) (json.RawMessage, error) {
	var b []byte
	var err error
	if name == "-" {
		b, err = io.ReadAll(os.Stdin)
	} else {
		b, err = os.ReadFile(name)
	}
	if err != nil {
		return nil, err
	}
	if !json.Valid(b) {
		return nil, fmt.Errorf("%s: not valid JSON", name)
	}
	return json.RawMessage(b), nil
}

// --- groups --------------------------------------------------------------

func (a *adminCLI) printGroups(gs []*admin.GroupInfo) error {
	return a.print(gs, func(w io.Writer) {
		fmt.Fprintln(w, "NAME\tSTATUS\tMEMBERS\tPOLICIES\tCREATED")
		for _, g := range gs {
			fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\n", g.Name, onOff(g.Enabled), strings.Join(g.Members, ","), strings.Join(g.Policies, ","), g.Created.Local().Format(time.RFC3339))
		}
	})
}

func (a *adminCLI) group(args []string) error {
	cmd, rest, err := sub(args)
	if err != nil {
		return err
	}
	fs := newFlags("group " + cmd)
	members := fs.String("members", "", "comma-separated members")
	policies := fs.String("policy", "", "comma-separated policies")
	add := fs.String("add", "", "members to add")
	remove := fs.String("remove", "", "members to remove")
	pos, err := parseInterleaved(fs, rest)
	if err != nil {
		return usageError(err.Error())
	}
	switch cmd {
	case "list", "ls":
		gs, err := a.c.ListGroups(a.ctx)
		if err != nil {
			return err
		}
		return a.printGroups(gs)
	case "info", "get":
		if err := needArg(pos, 1, "group name"); err != nil {
			return err
		}
		g, err := a.c.GetGroup(a.ctx, pos[0])
		if err != nil {
			return err
		}
		return a.printGroups([]*admin.GroupInfo{g})
	case "add":
		if err := needArg(pos, 1, "group name"); err != nil {
			return err
		}
		in := admin.PutGroupRequest{}
		if isSet(fs, "members") {
			in.Members = splitList(*members)
		}
		if isSet(fs, "policy") {
			in.Policies = splitList(*policies)
		}
		g, err := a.c.PutGroup(a.ctx, pos[0], in)
		if err != nil {
			return err
		}
		return a.printGroups([]*admin.GroupInfo{g})
	case "rm", "remove", "delete":
		if err := needArg(pos, 1, "group name"); err != nil {
			return err
		}
		return a.c.DeleteGroup(a.ctx, pos[0])
	case "members":
		if err := needArg(pos, 1, "group name"); err != nil {
			return err
		}
		g, err := a.c.UpdateGroupMembers(a.ctx, pos[0], splitList(*add), splitList(*remove))
		if err != nil {
			return err
		}
		return a.printGroups([]*admin.GroupInfo{g})
	}
	return usageError("unknown group subcommand " + cmd)
}

// --- policies ------------------------------------------------------------

func (a *adminCLI) policy(args []string) error {
	cmd, rest, err := sub(args)
	if err != nil {
		return err
	}
	fs := newFlags("policy " + cmd)
	pos, err := parseInterleaved(fs, rest)
	if err != nil {
		return usageError(err.Error())
	}
	switch cmd {
	case "list", "ls":
		ps, err := a.c.ListPolicies(a.ctx)
		if err != nil {
			return err
		}
		return a.print(ps, func(w io.Writer) {
			fmt.Fprintln(w, "NAME\tBUILT-IN\tUPDATED")
			for _, p := range ps {
				fmt.Fprintf(w, "%s\t%v\t%s\n", p.Name, p.BuiltIn, p.Updated.Local().Format(time.RFC3339))
			}
		})
	case "get", "info":
		if err := needArg(pos, 1, "policy name"); err != nil {
			return err
		}
		p, err := a.c.GetPolicy(a.ctx, pos[0])
		if err != nil {
			return err
		}
		return a.print(p, func(w io.Writer) {
			var buf bytes.Buffer
			if json.Indent(&buf, p.Document, "", "  ") == nil {
				fmt.Fprintln(w, buf.String())
			} else {
				fmt.Fprintln(w, string(p.Document))
			}
		})
	case "set", "add", "create":
		if err := needArg(pos, 2, "policy name and file"); err != nil {
			return err
		}
		doc, err := readFileOrStdin(pos[1])
		if err != nil {
			return err
		}
		p, err := a.c.PutPolicy(a.ctx, pos[0], doc)
		if err != nil {
			return err
		}
		return a.print(p, func(w io.Writer) { fmt.Fprintf(w, "policy %s saved\n", p.Name) })
	case "rm", "remove", "delete":
		if err := needArg(pos, 1, "policy name"); err != nil {
			return err
		}
		return a.c.DeletePolicy(a.ctx, pos[0])
	}
	return usageError("unknown policy subcommand " + cmd)
}

// --- buckets -------------------------------------------------------------

func (a *adminCLI) bucket(args []string) error {
	cmd, rest, err := sub(args)
	if err != nil {
		return err
	}
	fs := newFlags("bucket " + cmd)
	usage := fs.Bool("usage", false, "include object count and size")
	force := fs.Bool("force", false, "delete a non-empty bucket")
	pos, err := parseInterleaved(fs, rest)
	if err != nil {
		return usageError(err.Error())
	}
	switch cmd {
	case "list", "ls":
		bs, err := a.c.ListBuckets(a.ctx, *usage)
		if err != nil {
			return err
		}
		return a.print(bs, func(w io.Writer) {
			if *usage {
				fmt.Fprintln(w, "NAME\tCREATED\tOWNER\tVERSIONING\tLOCK\tOBJECTS\tSIZE")
			} else {
				fmt.Fprintln(w, "NAME\tCREATED\tOWNER\tVERSIONING\tLOCK")
			}
			for _, b := range bs {
				v := b.Versioning
				if v == "" {
					v = "Off"
				}
				fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%v", b.Name, b.Created.Local().Format(time.RFC3339), b.Owner, v, b.ObjectLock)
				if *usage && b.Objects != nil && b.Bytes != nil {
					fmt.Fprintf(w, "\t%d\t%s", *b.Objects, humanBytes(*b.Bytes))
				}
				fmt.Fprintln(w)
			}
		})
	case "rm", "remove", "delete":
		if err := needArg(pos, 1, "bucket name"); err != nil {
			return err
		}
		return a.c.DeleteBucket(a.ctx, pos[0], *force)
	}
	return usageError("unknown bucket subcommand " + cmd)
}

// --- kms -----------------------------------------------------------------

func (a *adminCLI) kms(args []string) error {
	cmd, rest, err := sub(args)
	if err != nil {
		return err
	}
	fs := newFlags("kms " + cmd)
	pos, err := parseInterleaved(fs, rest)
	if err != nil {
		return usageError(err.Error())
	}
	printKeys := func(ks []admin.KMSKeyInfo) error {
		return a.print(ks, func(w io.Writer) {
			fmt.Fprintln(w, "ID\tCREATED\tSTATUS")
			for _, k := range ks {
				fmt.Fprintf(w, "%s\t%s\t%s\n", k.ID, k.Created.Local().Format(time.RFC3339), onOff(!k.Disabled))
			}
		})
	}
	switch cmd {
	case "list", "ls":
		ks, err := a.c.ListKMSKeys(a.ctx)
		if err != nil {
			return err
		}
		return printKeys(ks)
	case "add", "create":
		if err := needArg(pos, 1, "key id"); err != nil {
			return err
		}
		k, err := a.c.CreateKMSKey(a.ctx, pos[0])
		if err != nil {
			return err
		}
		return printKeys([]admin.KMSKeyInfo{*k})
	case "rm", "remove", "delete":
		if err := needArg(pos, 1, "key id"); err != nil {
			return err
		}
		return a.c.DeleteKMSKey(a.ctx, pos[0])
	}
	return usageError("unknown kms subcommand " + cmd)
}
