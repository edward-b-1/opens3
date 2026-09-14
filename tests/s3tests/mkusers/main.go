// Command mkusers prepares an OpenS3 data directory for the Ceph s3-tests
// conformance suite: it opens the metadata store directly (the server must
// not be running), creates the extra "alt" and "tenant" users the suite
// needs, and prints the canonical IDs of all three users as shell
// assignments so run.sh can generate s3tests.conf.
//
// It builds the same KV / KMS / IAM stack as internal/server.New so the
// users are visible to the server started afterwards.
package main

import (
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"

	"github.com/edward-b-1/OpenS3/internal/iam"
	"github.com/edward-b-1/OpenS3/internal/kms"
	"github.com/edward-b-1/OpenS3/internal/kv"
)

func main() {
	root := flag.String("root", "", "data directory (same as the server's --root)")
	rootUser := flag.String("root-user", os.Getenv("OPENS3_ROOT_USER"), "root access key")
	rootPass := flag.String("root-password", os.Getenv("OPENS3_ROOT_PASSWORD"), "root secret key")
	altUser := flag.String("alt-user", "s3testsalt", "alt user name / access key")
	altSecret := flag.String("alt-secret", "s3testsaltsecretkey0000000000000", "alt user secret key")
	tenantUser := flag.String("tenant-user", "s3teststenant", "tenant user name / access key")
	tenantSecret := flag.String("tenant-secret", "s3teststenantsecretkey000000000", "tenant user secret key")
	flag.Parse()
	if *root == "" || *rootUser == "" || *rootPass == "" {
		fmt.Fprintln(os.Stderr, "mkusers: -root, -root-user and -root-password (or OPENS3_ROOT_USER/OPENS3_ROOT_PASSWORD) are required")
		os.Exit(2)
	}
	if err := run(*root, *rootUser, *rootPass, *altUser, *altSecret, *tenantUser, *tenantSecret); err != nil {
		fmt.Fprintln(os.Stderr, "mkusers:", err)
		os.Exit(1)
	}
}

func run(root, rootUser, rootPass, altUser, altSecret, tenantUser, tenantSecret string) error {
	if err := os.MkdirAll(filepath.Join(root, "meta"), 0o755); err != nil {
		return err
	}
	db, err := kv.OpenBolt(filepath.Join(root, "meta", "opens3.db"))
	if err != nil {
		return err
	}
	defer db.Close()
	// Mirror internal/server.New: the KMS master key ring lives in <root>/meta/master.keys
	// (OPENS3_MASTER_KEY overrides), exactly as server.New does.
	var master *kms.Master
	if m := os.Getenv("OPENS3_MASTER_KEY"); m != "" {
		master, err = kms.MasterFromMaterial([]byte(m))
	} else {
		master, _, err = kms.LoadOrCreateMasterFile(filepath.Join(root, "meta", "master.keys"))
	}
	if err != nil {
		return err
	}
	k, err := kms.NewLocal(db, master)
	if err != nil {
		return err
	}
	ia, err := iam.Open(db, iam.Config{RootAccessKey: rootUser, RootSecretKey: rootPass, Wrapper: master, AccountID: os.Getenv("OPENS3_ACCOUNT_ID")})
	if err != nil {
		return err
	}
	// The suite treats alt and tenant as separate AWS accounts whose access
	// to the main user's buckets is governed purely by ACLs and bucket
	// policies, so they get no blanket identity policy: they may create
	// buckets (and, as owners, fully control those) but nothing else.
	const accountPolicy = `{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":["s3:CreateBucket","s3:ListAllMyBuckets"],"Resource":["arn:aws:s3:::*"]}]}`
	if err := ia.PutPolicy("s3tests-account", []byte(accountPolicy)); err != nil {
		return fmt.Errorf("create policy: %w", err)
	}
	for _, u := range []struct{ name, secret string }{{altUser, altSecret}, {tenantUser, tenantSecret}} {
		err := ia.CreateUser(u.name, u.secret, []string{"s3tests-account"})
		if err != nil && !errors.Is(err, iam.ErrExists) {
			return fmt.Errorf("create user %s: %w", u.name, err)
		}
	}
	// KMS keys referenced by the [s3 main] kms_keyid / kms_keyid2 settings.
	for _, id := range []string{"testkey-1", "testkey-2"} {
		if !k.KeyExists(id) {
			if err := k.CreateKey(id); err != nil {
				return fmt.Errorf("create kms key %s: %w", id, err)
			}
		}
	}
	// The root identity's canonical ID is derived from the name "root",
	// not from its access key (see iam.Identity.CanonicalID).
	fmt.Printf("MAIN_USER_ID=%s\n", iam.CanonicalID("root"))
	fmt.Printf("ALT_USER_ID=%s\n", iam.CanonicalID(altUser))
	fmt.Printf("TENANT_USER_ID=%s\n", iam.CanonicalID(tenantUser))
	fmt.Printf("ACCOUNT_ID=%s\n", ia.AccountID())
	return nil
}
