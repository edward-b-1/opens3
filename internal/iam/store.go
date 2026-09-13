package iam

import (
	"bytes"
	"crypto/pbkdf2"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"gitlab.com/Birdsall/opens3/internal/kv"
	"gitlab.com/Birdsall/opens3/internal/policy"
	"gitlab.com/Birdsall/opens3/internal/sse"
)

// Errors.
var (
	ErrNotFound    = errors.New("iam: not found")
	ErrExists      = errors.New("iam: already exists")
	ErrInvalid     = errors.New("iam: invalid argument")
	ErrDisabled    = errors.New("iam: credentials disabled")
	ErrExpired     = errors.New("iam: credentials expired")
	ErrBadToken    = errors.New("iam: invalid session token")
	ErrBuiltin     = errors.New("iam: built-in policy cannot be modified")
	ErrRoot        = errors.New("iam: root account cannot be modified")
	ErrBadPassword = errors.New("iam: invalid user name or password")
	ErrNoPassword  = errors.New("iam: user has no console password")
)

// Credential formats: a 20-character upper-case alphanumeric access key ID
// and a 40-character secret, both random, matching the shape AWS uses.
const (
	AccessKeyLength   = 20
	SecretKeyLength   = 40
	accessKeyAlphabet = "ABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789"
	pbkdf2Iterations  = 600000
	minPasswordLength = 8
)

// GenerateAccessKey returns a random access key ID.
func GenerateAccessKey() string { return randomToken(AccessKeyLength, accessKeyAlphabet) }

// GenerateSecretKey returns a random secret access key.
func GenerateSecretKey() string { return randomToken(SecretKeyLength, "") }

const (
	nsUser   = "i/u/"
	nsKey    = "i/k/"
	nsGroup  = "i/g/"
	nsPolicy = "i/p/"
)

// Config for the IAM store.
type Config struct {
	RootAccessKey string
	RootSecretKey string
	// MasterKey (32 bytes) encrypts stored secret keys.
	MasterKey []byte
	// AccountID appears in principal ARNs.
	AccountID string
}

// Store persists identities in the KV store.
type Store struct {
	kv  kv.Store
	cfg Config
	mu  sync.RWMutex
	// cache of parsed policy documents by name
	pcache  map[string]*policy.Document
	started time.Time
}

// Open creates the store and ensures the built-in policies exist.
func Open(db kv.Store, cfg Config) (*Store, error) {
	if len(cfg.MasterKey) != sse.KeySize {
		return nil, fmt.Errorf("%w: master key must be %d bytes", ErrInvalid, sse.KeySize)
	}
	if cfg.RootAccessKey == "" || cfg.RootSecretKey == "" {
		return nil, fmt.Errorf("%w: root credentials are required", ErrInvalid)
	}
	if len(cfg.RootAccessKey) < 3 || len(cfg.RootSecretKey) < 8 {
		return nil, fmt.Errorf("%w: root access key must be >= 3 and secret >= 8 characters", ErrInvalid)
	}
	if cfg.AccountID == "" {
		cfg.AccountID = "000000000000"
	}
	s := &Store{kv: db, cfg: cfg, pcache: map[string]*policy.Document{}, started: time.Now().UTC()}
	err := db.Update(func(tx kv.Txn) error {
		for name, doc := range builtinPolicies {
			k := []byte(nsPolicy + name)
			// Built-in documents are owned by the server: rewrite them on every
			// start so upgrades take effect (they cannot be edited by users).
			created := time.Now().UTC()
			if b, err := tx.Get(k); err == nil {
				var old Policy
				if json.Unmarshal(b, &old) == nil && !old.Created.IsZero() {
					created = old.Created
				}
			}
			p := &Policy{Name: name, Document: json.RawMessage(doc), Created: created, Updated: time.Now().UTC(), BuiltIn: true}
			if err := tx.Put(k, mustJSON(p)); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return s, nil
}

// AccountID returns the configured account ID.
func (s *Store) AccountID() string { return s.cfg.AccountID }

func mustJSON(v any) []byte {
	b, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return b
}

func (s *Store) wrapSecret(secret string) []byte {
	w, err := sse.Wrap(s.cfg.MasterKey, []byte(secret), []byte("iam-secret"))
	if err != nil {
		panic(err)
	}
	return w
}

func (s *Store) unwrapSecret(w []byte) (string, error) {
	b, err := sse.Unwrap(s.cfg.MasterKey, w, []byte("iam-secret"))
	if err != nil {
		return "", err
	}
	return string(b), nil
}

// --- credential lookup ---------------------------------------------------

// LookupSecret resolves an access key to its secret (for SigV4). Disabled
// or expired keys return errors; the caller should map them to
// InvalidAccessKeyId.
func (s *Store) LookupSecret(accessKey string) (string, error) {
	if accessKey == s.cfg.RootAccessKey {
		return s.cfg.RootSecretKey, nil
	}
	var k *Key
	err := s.kv.View(func(tx kv.Txn) error {
		var err error
		k, err = getKey(tx, accessKey)
		return err
	})
	if err != nil {
		return "", err
	}
	if !k.Enabled {
		return "", ErrDisabled
	}
	if k.Expires != nil && time.Now().After(*k.Expires) {
		return "", ErrExpired
	}
	return s.unwrapSecret(k.SecretWrapped)
}

// Resolve builds the Identity for an authenticated access key, verifying
// the session token for STS keys.
func (s *Store) Resolve(accessKey, sessionToken string) (*Identity, error) {
	if accessKey == s.cfg.RootAccessKey {
		return &Identity{IsRoot: true, AccountID: s.cfg.AccountID}, nil
	}
	var id *Identity
	err := s.kv.View(func(tx kv.Txn) error {
		k, err := getKey(tx, accessKey)
		if err != nil {
			return err
		}
		if !k.Enabled {
			return ErrDisabled
		}
		if k.Expires != nil && time.Now().After(*k.Expires) {
			return ErrExpired
		}
		if k.Kind == KindSTS {
			if subtle.ConstantTimeCompare([]byte(k.SessionToken), []byte(sessionToken)) != 1 {
				return ErrBadToken
			}
		}
		u, err := getUser(tx, k.User)
		if err != nil {
			return err
		}
		if !u.Enabled {
			return ErrDisabled
		}
		id = &Identity{User: u, Key: k, AccountID: s.cfg.AccountID, SessionPolicy: k.SessionPolicy}
		id.Policies, err = s.effectivePolicies(tx, u)
		return err
	})
	if err != nil {
		return nil, err
	}
	return id, nil
}

// effectivePolicies collects the documents attached to the user and its groups.
func (s *Store) effectivePolicies(tx kv.Txn, u *User) ([]json.RawMessage, error) {
	seen := map[string]bool{}
	var out []json.RawMessage
	add := func(names []string) error {
		for _, n := range names {
			if seen[n] {
				continue
			}
			seen[n] = true
			p, err := getPolicy(tx, n)
			if errors.Is(err, ErrNotFound) {
				continue
			}
			if err != nil {
				return err
			}
			out = append(out, p.Document)
		}
		return nil
	}
	if err := add(u.Policies); err != nil {
		return nil, err
	}
	for _, g := range u.Groups {
		grp, err := getGroup(tx, g)
		if errors.Is(err, ErrNotFound) {
			continue
		}
		if err != nil {
			return nil, err
		}
		if !grp.Enabled {
			continue
		}
		if err := add(grp.Policies); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// ParsedPolicy returns the parsed document for raw JSON, cached by content.
func (s *Store) ParsedPolicy(raw json.RawMessage) (*policy.Document, error) {
	key := string(raw)
	s.mu.RLock()
	d, ok := s.pcache[key]
	s.mu.RUnlock()
	if ok {
		return d, nil
	}
	d, err := policy.Parse(raw)
	if err != nil {
		return nil, err
	}
	s.mu.Lock()
	if len(s.pcache) > 4096 {
		s.pcache = map[string]*policy.Document{}
	}
	s.pcache[key] = d
	s.mu.Unlock()
	return d, nil
}

// --- users ---------------------------------------------------------------

func getUser(tx kv.Txn, name string) (*User, error) {
	b, err := tx.Get([]byte(nsUser + name))
	if errors.Is(err, kv.ErrNotFound) {
		return nil, ErrNotFound
	} else if err != nil {
		return nil, err
	}
	var u User
	if err := json.Unmarshal(b, &u); err != nil {
		return nil, err
	}
	return &u, nil
}

func getKey(tx kv.Txn, ak string) (*Key, error) {
	b, err := tx.Get([]byte(nsKey + ak))
	if errors.Is(err, kv.ErrNotFound) {
		return nil, ErrNotFound
	} else if err != nil {
		return nil, err
	}
	var k Key
	if err := json.Unmarshal(b, &k); err != nil {
		return nil, err
	}
	return &k, nil
}

func getGroup(tx kv.Txn, name string) (*Group, error) {
	b, err := tx.Get([]byte(nsGroup + name))
	if errors.Is(err, kv.ErrNotFound) {
		return nil, ErrNotFound
	} else if err != nil {
		return nil, err
	}
	var g Group
	if err := json.Unmarshal(b, &g); err != nil {
		return nil, err
	}
	return &g, nil
}

func getPolicy(tx kv.Txn, name string) (*Policy, error) {
	b, err := tx.Get([]byte(nsPolicy + name))
	if errors.Is(err, kv.ErrNotFound) {
		return nil, ErrNotFound
	} else if err != nil {
		return nil, err
	}
	var p Policy
	if err := json.Unmarshal(b, &p); err != nil {
		return nil, err
	}
	return &p, nil
}

func validName(n string) bool {
	if n == "" || len(n) > 128 || n == "root" {
		return false
	}
	for _, c := range n {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-' || c == '_' || c == '.' || c == '@' || c == '+' || c == '=' || c == ',') {
			return false
		}
	}
	return true
}

// CreateUser creates a user with an initial access key equal to the name
// (MinIO convention) and the given secret. Pass secret "" to create the
// user with no key.
func (s *Store) CreateUser(name, secret string, policies []string) error {
	if !validName(name) || name == s.cfg.RootAccessKey {
		return fmt.Errorf("%w: invalid user name", ErrInvalid)
	}
	if secret != "" && len(secret) < 8 {
		return fmt.Errorf("%w: secret key must be at least 8 characters", ErrInvalid)
	}
	now := time.Now().UTC()
	return s.kv.Update(func(tx kv.Txn) error {
		if _, err := getUser(tx, name); err == nil {
			return ErrExists
		}
		if _, err := getKey(tx, name); err == nil {
			return ErrExists
		}
		for _, p := range policies {
			if _, err := getPolicy(tx, p); err != nil {
				return fmt.Errorf("%w: policy %s", ErrNotFound, p)
			}
		}
		u := &User{Name: name, Enabled: true, Policies: policies, Created: now}
		if err := tx.Put([]byte(nsUser+name), mustJSON(u)); err != nil {
			return err
		}
		if secret != "" {
			k := &Key{AccessKey: name, SecretWrapped: s.wrapSecret(secret), User: name, Kind: KindUser, Enabled: true, Created: now}
			if err := tx.Put([]byte(nsKey+name), mustJSON(k)); err != nil {
				return err
			}
		}
		return nil
	})
}

// GetUser returns a user.
func (s *Store) GetUser(name string) (*User, error) {
	var u *User
	err := s.kv.View(func(tx kv.Txn) error {
		var err error
		u, err = getUser(tx, name)
		return err
	})
	return u, err
}

// ListUsers returns all users.
func (s *Store) ListUsers() ([]*User, error) {
	var out []*User
	err := s.kv.View(func(tx kv.Txn) error {
		it := tx.Seek([]byte(nsUser))
		defer it.Close()
		for ; it.Valid() && bytes.HasPrefix(it.Key(), []byte(nsUser)); it.Next() {
			var u User
			if err := json.Unmarshal(it.Value(), &u); err != nil {
				return err
			}
			out = append(out, &u)
		}
		return nil
	})
	return out, err
}

// UpdateUser applies fn to the user record.
func (s *Store) UpdateUser(name string, fn func(u *User) error) error {
	return s.kv.Update(func(tx kv.Txn) error {
		u, err := getUser(tx, name)
		if err != nil {
			return err
		}
		if err := fn(u); err != nil {
			return err
		}
		for _, p := range u.Policies {
			if _, err := getPolicy(tx, p); err != nil {
				return fmt.Errorf("%w: policy %s", ErrNotFound, p)
			}
		}
		return tx.Put([]byte(nsUser+name), mustJSON(u))
	})
}

// DeleteUser removes a user, its keys and its group memberships.
func (s *Store) DeleteUser(name string) error {
	return s.kv.Update(func(tx kv.Txn) error {
		u, err := getUser(tx, name)
		if err != nil {
			return err
		}
		// Delete keys owned by the user.
		it := tx.Seek([]byte(nsKey))
		var dead [][]byte
		for ; it.Valid() && bytes.HasPrefix(it.Key(), []byte(nsKey)); it.Next() {
			var k Key
			if json.Unmarshal(it.Value(), &k) == nil && k.User == name {
				dead = append(dead, append([]byte(nil), it.Key()...))
			}
		}
		it.Close()
		for _, k := range dead {
			if err := tx.Delete(k); err != nil {
				return err
			}
		}
		for _, g := range u.Groups {
			grp, err := getGroup(tx, g)
			if err != nil {
				continue
			}
			grp.Members = remove(grp.Members, name)
			if err := tx.Put([]byte(nsGroup+g), mustJSON(grp)); err != nil {
				return err
			}
		}
		return tx.Delete([]byte(nsUser + name))
	})
}

func remove(l []string, s string) []string {
	out := l[:0]
	for _, v := range l {
		if v != s {
			out = append(out, v)
		}
	}
	return out
}

// --- console passwords ---------------------------------------------------

func hashPassword(pw string, salt []byte) []byte {
	h, err := pbkdf2.Key(sha256.New, pw, salt, pbkdf2Iterations, 32)
	if err != nil {
		panic(err)
	}
	return h
}

// SetPassword sets a user's console password.
func (s *Store) SetPassword(name, password string) error {
	if len(password) < minPasswordLength {
		return fmt.Errorf("%w: password must be at least %d characters", ErrInvalid, minPasswordLength)
	}
	if name == "root" || name == s.cfg.RootAccessKey {
		return ErrRoot
	}
	salt := make([]byte, 16)
	if _, err := rand.Read(salt); err != nil {
		return err
	}
	hash := hashPassword(password, salt)
	return s.UpdateUser(name, func(u *User) error {
		u.PasswordHash, u.PasswordSalt, u.PasswordSet = hash, salt, time.Now().UTC()
		return nil
	})
}

// ClearPassword removes a user's console password.
func (s *Store) ClearPassword(name string) error {
	return s.UpdateUser(name, func(u *User) error {
		u.PasswordHash, u.PasswordSalt, u.PasswordSet = nil, nil, time.Time{}
		return nil
	})
}

// VerifyPassword checks a console sign-in. The root account signs in with
// the configured root user name and password. Failures take comparable time
// whether or not the user exists.
func (s *Store) VerifyPassword(name, password string) (*Identity, error) {
	if name == s.cfg.RootAccessKey {
		if subtle.ConstantTimeCompare([]byte(password), []byte(s.cfg.RootSecretKey)) == 1 {
			return &Identity{IsRoot: true, AccountID: s.cfg.AccountID}, nil
		}
		return nil, ErrBadPassword
	}
	var u *User
	var polices []json.RawMessage
	err := s.kv.View(func(tx kv.Txn) error {
		var err error
		if u, err = getUser(tx, name); err != nil {
			return err
		}
		polices, err = s.effectivePolicies(tx, u)
		return err
	})
	if err != nil || !u.HasPassword() {
		hashPassword(password, make([]byte, 16)) // burn comparable time
		return nil, ErrBadPassword
	}
	if subtle.ConstantTimeCompare(hashPassword(password, u.PasswordSalt), u.PasswordHash) != 1 {
		return nil, ErrBadPassword
	}
	if !u.Enabled {
		return nil, ErrDisabled
	}
	return &Identity{User: u, AccountID: s.cfg.AccountID, Policies: polices}, nil
}

// --- keys ----------------------------------------------------------------

// CreateKey adds an access key to a user. kind is KindUser or KindService.
// Empty accessKey/secret are generated.
func (s *Store) CreateKey(user, accessKey, secret, kind string, sessionPolicy json.RawMessage, expires *time.Time, description string) (*Key, string, error) {
	if kind != KindUser && kind != KindService {
		return nil, "", fmt.Errorf("%w: bad key kind", ErrInvalid)
	}
	if accessKey == "" {
		accessKey = GenerateAccessKey()
	}
	if secret == "" {
		secret = GenerateSecretKey()
	}
	if len(accessKey) < 3 || len(secret) < 8 || accessKey == s.cfg.RootAccessKey {
		return nil, "", fmt.Errorf("%w: access key must be >= 3 and secret >= 8 characters", ErrInvalid)
	}
	if len(sessionPolicy) > 0 {
		if _, err := policy.Parse(sessionPolicy); err != nil {
			return nil, "", fmt.Errorf("%w: %v", ErrInvalid, err)
		}
	}
	k := &Key{AccessKey: accessKey, SecretWrapped: s.wrapSecret(secret), User: user, Kind: kind, Enabled: true,
		SessionPolicy: sessionPolicy, Expires: expires, Description: description, Created: time.Now().UTC()}
	err := s.kv.Update(func(tx kv.Txn) error {
		if _, err := getUser(tx, user); err != nil {
			return err
		}
		if _, err := getKey(tx, accessKey); err == nil {
			return ErrExists
		}
		if _, err := getUser(tx, accessKey); err == nil {
			return ErrExists
		}
		return tx.Put([]byte(nsKey+accessKey), mustJSON(k))
	})
	if err != nil {
		return nil, "", err
	}
	return k, secret, nil
}

// GetKey returns a key record (secret not included).
func (s *Store) GetKey(accessKey string) (*Key, error) {
	var k *Key
	err := s.kv.View(func(tx kv.Txn) error {
		var err error
		k, err = getKey(tx, accessKey)
		return err
	})
	return k, err
}

// ListKeys returns the keys of a user (or all keys if user is "").
func (s *Store) ListKeys(user string) ([]*Key, error) {
	var out []*Key
	err := s.kv.View(func(tx kv.Txn) error {
		it := tx.Seek([]byte(nsKey))
		defer it.Close()
		for ; it.Valid() && bytes.HasPrefix(it.Key(), []byte(nsKey)); it.Next() {
			var k Key
			if err := json.Unmarshal(it.Value(), &k); err != nil {
				return err
			}
			if user == "" || k.User == user {
				out = append(out, &k)
			}
		}
		return nil
	})
	return out, err
}

// UpdateKey applies fn to a key record (enable/disable, rotate secret via
// SetSecret, change session policy).
func (s *Store) UpdateKey(accessKey string, fn func(k *Key) error) error {
	return s.kv.Update(func(tx kv.Txn) error {
		k, err := getKey(tx, accessKey)
		if err != nil {
			return err
		}
		if err := fn(k); err != nil {
			return err
		}
		return tx.Put([]byte(nsKey+accessKey), mustJSON(k))
	})
}

// SetSecret rotates the secret of a key.
func (s *Store) SetSecret(accessKey, secret string) error {
	if len(secret) < 8 {
		return fmt.Errorf("%w: secret key must be at least 8 characters", ErrInvalid)
	}
	return s.UpdateKey(accessKey, func(k *Key) error { k.SecretWrapped = s.wrapSecret(secret); return nil })
}

// DeleteKey removes a key.
func (s *Store) DeleteKey(accessKey string) error {
	return s.kv.Update(func(tx kv.Txn) error {
		if _, err := getKey(tx, accessKey); err != nil {
			return err
		}
		return tx.Delete([]byte(nsKey + accessKey))
	})
}

// --- STS -----------------------------------------------------------------

// AssumeRole issues temporary credentials for the identity, optionally
// restricted by an inline session policy. Returns access key, secret,
// session token, expiry.
func (s *Store) AssumeRole(id *Identity, sessionPolicy json.RawMessage, duration time.Duration) (ak, sk, token string, exp time.Time, err error) {
	if duration < 15*time.Minute {
		duration = time.Hour
	}
	if duration > 7*24*time.Hour {
		duration = 7 * 24 * time.Hour
	}
	if len(sessionPolicy) > 0 {
		if _, err := policy.Parse(sessionPolicy); err != nil {
			return "", "", "", time.Time{}, fmt.Errorf("%w: %v", ErrInvalid, err)
		}
	}
	user := id.Name()
	if id.IsRoot {
		// Root has no user record; STS keys for root reference a synthetic
		// user that inherits full access via IsRoot handling in Resolve.
		user = "root"
	}
	ak = GenerateAccessKey()
	sk = GenerateSecretKey()
	token = randomToken(64, "")
	exp = time.Now().UTC().Add(duration)
	// Chain session policies: an STS session created from a service
	// account keeps that account's restriction.
	if len(sessionPolicy) == 0 && id.Key != nil && len(id.Key.SessionPolicy) > 0 {
		sessionPolicy = id.Key.SessionPolicy
	}
	k := &Key{AccessKey: ak, SecretWrapped: s.wrapSecret(sk), User: user, Kind: KindSTS, Enabled: true,
		SessionPolicy: sessionPolicy, SessionToken: token, Expires: &exp, Created: time.Now().UTC()}
	err = s.kv.Update(func(tx kv.Txn) error {
		if id.IsRoot {
			if _, err := getUser(tx, "root"); errors.Is(err, ErrNotFound) {
				// Sessions derived from root inherit full access; the
				// session policy (if any) narrows it.
				if err := tx.Put([]byte(nsUser+"root"), mustJSON(&User{Name: "root", Enabled: true, Policies: []string{"consoleAdmin"}, Created: time.Now().UTC()})); err != nil {
					return err
				}
			}
		}
		return tx.Put([]byte(nsKey+ak), mustJSON(k))
	})
	return
}

// PurgeExpired deletes expired STS keys; called periodically.
func (s *Store) PurgeExpired() (int, error) {
	n := 0
	err := s.kv.Update(func(tx kv.Txn) error {
		it := tx.Seek([]byte(nsKey))
		var dead [][]byte
		now := time.Now()
		for ; it.Valid() && bytes.HasPrefix(it.Key(), []byte(nsKey)); it.Next() {
			var k Key
			if json.Unmarshal(it.Value(), &k) == nil && k.Expires != nil && now.After(k.Expires.Add(time.Hour)) {
				dead = append(dead, append([]byte(nil), it.Key()...))
			}
		}
		it.Close()
		for _, k := range dead {
			if err := tx.Delete(k); err != nil {
				return err
			}
			n++
		}
		return nil
	})
	return n, err
}

// --- groups --------------------------------------------------------------

// CreateGroup creates a group.
func (s *Store) CreateGroup(name string, members, policies []string) error {
	if !validName(name) {
		return fmt.Errorf("%w: invalid group name", ErrInvalid)
	}
	return s.kv.Update(func(tx kv.Txn) error {
		if _, err := getGroup(tx, name); err == nil {
			return ErrExists
		}
		for _, p := range policies {
			if _, err := getPolicy(tx, p); err != nil {
				return fmt.Errorf("%w: policy %s", ErrNotFound, p)
			}
		}
		g := &Group{Name: name, Enabled: true, Policies: policies, Created: time.Now().UTC()}
		for _, m := range members {
			u, err := getUser(tx, m)
			if err != nil {
				return fmt.Errorf("%w: user %s", ErrNotFound, m)
			}
			g.Members = append(g.Members, m)
			u.Groups = appendUnique(u.Groups, name)
			if err := tx.Put([]byte(nsUser+m), mustJSON(u)); err != nil {
				return err
			}
		}
		return tx.Put([]byte(nsGroup+name), mustJSON(g))
	})
}

func appendUnique(l []string, s string) []string {
	for _, v := range l {
		if v == s {
			return l
		}
	}
	return append(l, s)
}

// GetGroup returns a group.
func (s *Store) GetGroup(name string) (*Group, error) {
	var g *Group
	err := s.kv.View(func(tx kv.Txn) error {
		var err error
		g, err = getGroup(tx, name)
		return err
	})
	return g, err
}

// ListGroups returns all groups.
func (s *Store) ListGroups() ([]*Group, error) {
	var out []*Group
	err := s.kv.View(func(tx kv.Txn) error {
		it := tx.Seek([]byte(nsGroup))
		defer it.Close()
		for ; it.Valid() && bytes.HasPrefix(it.Key(), []byte(nsGroup)); it.Next() {
			var g Group
			if err := json.Unmarshal(it.Value(), &g); err != nil {
				return err
			}
			out = append(out, &g)
		}
		return nil
	})
	return out, err
}

// UpdateGroup applies fn to the group and keeps member back-references in sync.
func (s *Store) UpdateGroup(name string, fn func(g *Group) error) error {
	return s.kv.Update(func(tx kv.Txn) error {
		g, err := getGroup(tx, name)
		if err != nil {
			return err
		}
		before := map[string]bool{}
		for _, m := range g.Members {
			before[m] = true
		}
		if err := fn(g); err != nil {
			return err
		}
		for _, p := range g.Policies {
			if _, err := getPolicy(tx, p); err != nil {
				return fmt.Errorf("%w: policy %s", ErrNotFound, p)
			}
		}
		after := map[string]bool{}
		for _, m := range g.Members {
			after[m] = true
			u, err := getUser(tx, m)
			if err != nil {
				return fmt.Errorf("%w: user %s", ErrNotFound, m)
			}
			if !before[m] {
				u.Groups = appendUnique(u.Groups, name)
				if err := tx.Put([]byte(nsUser+m), mustJSON(u)); err != nil {
					return err
				}
			}
		}
		for m := range before {
			if !after[m] {
				if u, err := getUser(tx, m); err == nil {
					u.Groups = remove(u.Groups, name)
					if err := tx.Put([]byte(nsUser+m), mustJSON(u)); err != nil {
						return err
					}
				}
			}
		}
		return tx.Put([]byte(nsGroup+name), mustJSON(g))
	})
}

// DeleteGroup removes a group.
func (s *Store) DeleteGroup(name string) error {
	return s.kv.Update(func(tx kv.Txn) error {
		g, err := getGroup(tx, name)
		if err != nil {
			return err
		}
		for _, m := range g.Members {
			if u, err := getUser(tx, m); err == nil {
				u.Groups = remove(u.Groups, name)
				if err := tx.Put([]byte(nsUser+m), mustJSON(u)); err != nil {
					return err
				}
			}
		}
		return tx.Delete([]byte(nsGroup + name))
	})
}

// --- policies ------------------------------------------------------------

// PutPolicy creates or replaces a named policy.
func (s *Store) PutPolicy(name string, doc json.RawMessage) error {
	if !validName(name) {
		return fmt.Errorf("%w: invalid policy name", ErrInvalid)
	}
	d, err := policy.Parse(doc)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrInvalid, err)
	}
	if err := checkActions(d); err != nil {
		return err
	}
	if _, ok := builtinPolicies[name]; ok {
		return ErrBuiltin
	}
	now := time.Now().UTC()
	return s.kv.Update(func(tx kv.Txn) error {
		p := &Policy{Name: name, Document: doc, Created: now, Updated: now}
		if old, err := getPolicy(tx, name); err == nil {
			p.Created = old.Created
		}
		return tx.Put([]byte(nsPolicy+name), mustJSON(p))
	})
}

// GetPolicy returns a named policy.
func (s *Store) GetPolicy(name string) (*Policy, error) {
	var p *Policy
	err := s.kv.View(func(tx kv.Txn) error {
		var err error
		p, err = getPolicy(tx, name)
		return err
	})
	return p, err
}

// ListPolicies returns all named policies.
func (s *Store) ListPolicies() ([]*Policy, error) {
	var out []*Policy
	err := s.kv.View(func(tx kv.Txn) error {
		it := tx.Seek([]byte(nsPolicy))
		defer it.Close()
		for ; it.Valid() && bytes.HasPrefix(it.Key(), []byte(nsPolicy)); it.Next() {
			var p Policy
			if err := json.Unmarshal(it.Value(), &p); err != nil {
				return err
			}
			out = append(out, &p)
		}
		return nil
	})
	return out, err
}

// DeletePolicy removes a named policy (detaching it everywhere).
func (s *Store) DeletePolicy(name string) error {
	if _, ok := builtinPolicies[name]; ok {
		return ErrBuiltin
	}
	return s.kv.Update(func(tx kv.Txn) error {
		if _, err := getPolicy(tx, name); err != nil {
			return err
		}
		for _, ns := range []string{nsUser, nsGroup} {
			it := tx.Seek([]byte(ns))
			var upd [][2][]byte
			for ; it.Valid() && bytes.HasPrefix(it.Key(), []byte(ns)); it.Next() {
				if ns == nsUser {
					var u User
					if json.Unmarshal(it.Value(), &u) == nil && contains(u.Policies, name) {
						u.Policies = remove(u.Policies, name)
						upd = append(upd, [2][]byte{append([]byte(nil), it.Key()...), mustJSON(&u)})
					}
				} else {
					var g Group
					if json.Unmarshal(it.Value(), &g) == nil && contains(g.Policies, name) {
						g.Policies = remove(g.Policies, name)
						upd = append(upd, [2][]byte{append([]byte(nil), it.Key()...), mustJSON(&g)})
					}
				}
			}
			it.Close()
			for _, kv := range upd {
				if err := tx.Put(kv[0], kv[1]); err != nil {
					return err
				}
			}
		}
		return tx.Delete([]byte(nsPolicy + name))
	})
}

func contains(l []string, s string) bool {
	for _, v := range l {
		if v == s {
			return true
		}
	}
	return false
}

func randomToken(n int, alphabet string) string {
	if alphabet == "" {
		b := make([]byte, n)
		rand.Read(b)
		return strings.TrimRight(base64.RawURLEncoding.EncodeToString(b), "=")[:n]
	}
	b := make([]byte, n)
	rand.Read(b)
	out := make([]byte, n)
	for i := range b {
		out[i] = alphabet[int(b[i])%len(alphabet)]
	}
	return string(out)
}

// checkActions rejects removed action namespaces in a policy document.
func checkActions(d *policy.Document) error {
	for _, st := range d.Statements {
		for _, a := range append(append([]string{}, st.Actions...), st.NotActions...) {
			if err := LegacyActionError(a); err != nil {
				return err
			}
		}
	}
	return nil
}

// Error classification helpers for API layers.
func IsNotFound(err error) bool { return errors.Is(err, ErrNotFound) }
func IsExists(err error) bool   { return errors.Is(err, ErrExists) }
func IsInvalid(err error) bool  { return errors.Is(err, ErrInvalid) }
func IsBuiltin(err error) bool  { return errors.Is(err, ErrBuiltin) }
func IsRoot(err error) bool     { return errors.Is(err, ErrRoot) }

// Started returns when the store was opened (used as root's creation time).
func (s *Store) Started() time.Time { return s.started }

// SetPolicyPath records the IAM path of a named policy.
func (s *Store) SetPolicyPath(name, path string) error {
	return s.kv.Update(func(tx kv.Txn) error {
		p, err := getPolicy(tx, name)
		if err != nil {
			return err
		}
		p.Path = path
		return tx.Put([]byte(nsPolicy+name), mustJSON(p))
	})
}
