// Package policy parses and evaluates AWS IAM policy documents as used by
// S3 bucket policies and identity policies: Version, Statement, Effect,
// Principal, Action, Resource and Condition with the standard operators,
// set modifiers (ForAnyValue/ForAllValues), IfExists and policy variables.
package policy

import (
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"path"
	"strconv"
	"strings"
	"time"
)

// Effect is Allow or Deny.
type Effect string

const (
	Allow Effect = "Allow"
	Deny  Effect = "Deny"
)

// Document is a parsed policy.
type Document struct {
	Version    string
	ID         string
	Statements []Statement
}

// Statement is one policy statement.
type Statement struct {
	SID          string
	Effect       Effect
	Principal    *Principal // nil for identity policies
	NotPrincipal *Principal
	Actions      []string
	NotActions   []string
	Resources    []string
	NotResources []string
	Conditions   map[string]map[string][]string // operator → key → values
}

// Principal is the Principal element.
type Principal struct {
	All       bool // "*" or {"AWS":"*"}
	AWS       []string
	Federated []string
	Service   []string
	Canonical []string
}

// Errors.
var (
	ErrMalformed = errors.New("policy: malformed policy document")
)

// Parse parses a policy JSON document.
func Parse(data []byte) (*Document, error) {
	var raw struct {
		Version   string          `json:"Version"`
		ID        string          `json:"Id"`
		Statement json.RawMessage `json:"Statement"`
	}
	dec := json.NewDecoder(strings.NewReader(string(data)))
	if err := dec.Decode(&raw); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrMalformed, err)
	}
	if raw.Version != "" && raw.Version != "2012-10-17" && raw.Version != "2008-10-17" {
		return nil, fmt.Errorf("%w: unsupported Version %q", ErrMalformed, raw.Version)
	}
	if len(raw.Statement) == 0 {
		return nil, fmt.Errorf("%w: missing Statement", ErrMalformed)
	}
	var stmts []json.RawMessage
	if raw.Statement[0] == '[' {
		if err := json.Unmarshal(raw.Statement, &stmts); err != nil {
			return nil, fmt.Errorf("%w: %v", ErrMalformed, err)
		}
	} else {
		stmts = []json.RawMessage{raw.Statement}
	}
	doc := &Document{Version: raw.Version, ID: raw.ID}
	for _, s := range stmts {
		st, err := parseStatement(s)
		if err != nil {
			return nil, err
		}
		doc.Statements = append(doc.Statements, *st)
	}
	return doc, nil
}

func parseStatement(data json.RawMessage) (*Statement, error) {
	var raw struct {
		SID          string          `json:"Sid"`
		Effect       string          `json:"Effect"`
		Principal    json.RawMessage `json:"Principal"`
		NotPrincipal json.RawMessage `json:"NotPrincipal"`
		Action       json.RawMessage `json:"Action"`
		NotAction    json.RawMessage `json:"NotAction"`
		Resource     json.RawMessage `json:"Resource"`
		NotResource  json.RawMessage `json:"NotResource"`
		Condition    map[string]map[string]json.RawMessage
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrMalformed, err)
	}
	st := &Statement{SID: raw.SID, Effect: Effect(raw.Effect)}
	if st.Effect != Allow && st.Effect != Deny {
		return nil, fmt.Errorf("%w: Effect must be Allow or Deny", ErrMalformed)
	}
	var err error
	if st.Actions, err = stringOrList(raw.Action); err != nil {
		return nil, err
	}
	if st.NotActions, err = stringOrList(raw.NotAction); err != nil {
		return nil, err
	}
	if len(st.Actions) == 0 && len(st.NotActions) == 0 {
		return nil, fmt.Errorf("%w: missing Action", ErrMalformed)
	}
	if st.Resources, err = stringOrList(raw.Resource); err != nil {
		return nil, err
	}
	if st.NotResources, err = stringOrList(raw.NotResource); err != nil {
		return nil, err
	}
	if len(st.Resources) == 0 && len(st.NotResources) == 0 {
		return nil, fmt.Errorf("%w: missing Resource", ErrMalformed)
	}
	if st.Principal, err = parsePrincipal(raw.Principal); err != nil {
		return nil, err
	}
	if st.NotPrincipal, err = parsePrincipal(raw.NotPrincipal); err != nil {
		return nil, err
	}
	if raw.Condition != nil {
		st.Conditions = map[string]map[string][]string{}
		for op, keys := range raw.Condition {
			st.Conditions[op] = map[string][]string{}
			for k, v := range keys {
				vals, err := anyOrList(v)
				if err != nil {
					return nil, err
				}
				st.Conditions[op][k] = vals
			}
			if _, _, _, ok := splitOperator(op); !ok {
				return nil, fmt.Errorf("%w: unknown condition operator %s", ErrMalformed, op)
			}
		}
	}
	return st, nil
}

func stringOrList(raw json.RawMessage) ([]string, error) {
	if len(raw) == 0 || string(raw) == "null" {
		return nil, nil
	}
	if raw[0] == '"' {
		var s string
		if err := json.Unmarshal(raw, &s); err != nil {
			return nil, fmt.Errorf("%w: %v", ErrMalformed, err)
		}
		return []string{s}, nil
	}
	var l []string
	if err := json.Unmarshal(raw, &l); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrMalformed, err)
	}
	return l, nil
}

// anyOrList accepts strings, numbers and bools (condition values).
func anyOrList(raw json.RawMessage) ([]string, error) {
	if len(raw) == 0 {
		return nil, nil
	}
	if raw[0] == '[' {
		var l []any
		if err := json.Unmarshal(raw, &l); err != nil {
			return nil, fmt.Errorf("%w: %v", ErrMalformed, err)
		}
		out := make([]string, 0, len(l))
		for _, v := range l {
			out = append(out, fmt.Sprint(v))
		}
		return out, nil
	}
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrMalformed, err)
	}
	return []string{fmt.Sprint(v)}, nil
}

func parsePrincipal(raw json.RawMessage) (*Principal, error) {
	if len(raw) == 0 || string(raw) == "null" {
		return nil, nil
	}
	if string(raw) == `"*"` {
		return &Principal{All: true}, nil
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil, fmt.Errorf("%w: invalid Principal", ErrMalformed)
	}
	p := &Principal{}
	for k, v := range m {
		l, err := stringOrList(v)
		if err != nil {
			return nil, err
		}
		switch k {
		case "AWS":
			for _, s := range l {
				if s == "*" {
					p.All = true
				}
			}
			p.AWS = l
		case "Federated":
			p.Federated = l
		case "Service":
			p.Service = l
		case "CanonicalUser":
			p.Canonical = l
		default:
			return nil, fmt.Errorf("%w: unknown Principal type %s", ErrMalformed, k)
		}
	}
	return p, nil
}

// Args describes a request being authorised.
type Args struct {
	Action   string // e.g. s3:GetObject
	Resource string // e.g. arn:aws:s3:::bucket/key
	// Principal identifiers for the caller. ARN is the IAM-style ARN
	// (arn:aws:iam::<account>:user/<name>); empty for anonymous.
	PrincipalARN string
	CanonicalID  string
	Anonymous    bool
	// Conditions supplies context keys (lower-cased) → values.
	Conditions map[string][]string
	// Vars supplies policy variables (e.g. "aws:username").
	Vars map[string]string
}

// Decision is the result of evaluating a document.
type Decision int

const (
	NoMatch Decision = iota
	Allowed
	Denied
)

// Evaluate applies the standard algorithm: any matching Deny wins,
// otherwise any matching Allow, otherwise NoMatch.
func (d *Document) Evaluate(a Args) Decision {
	res := NoMatch
	for i := range d.Statements {
		st := &d.Statements[i]
		if !st.matches(a) {
			continue
		}
		if st.Effect == Deny {
			return Denied
		}
		res = Allowed
	}
	return res
}

func (st *Statement) matches(a Args) bool {
	if st.Principal != nil && !st.Principal.matches(a) {
		return false
	}
	if st.NotPrincipal != nil && st.NotPrincipal.matches(a) {
		return false
	}
	if len(st.Actions) > 0 && !matchAny(st.Actions, a.Action, false) {
		return false
	}
	if len(st.NotActions) > 0 && matchAny(st.NotActions, a.Action, false) {
		return false
	}
	if len(st.Resources) > 0 && !matchAnyVars(st.Resources, a.Resource, a.Vars) {
		return false
	}
	if len(st.NotResources) > 0 && matchAnyVars(st.NotResources, a.Resource, a.Vars) {
		return false
	}
	for op, keys := range st.Conditions {
		if !evalCondition(op, keys, a) {
			return false
		}
	}
	return true
}

func (p *Principal) matches(a Args) bool {
	if p.All {
		return true
	}
	if a.Anonymous {
		return false
	}
	for _, s := range p.AWS {
		if s == a.PrincipalARN || (a.PrincipalARN != "" && Match(s, a.PrincipalARN)) {
			return true
		}
	}
	for _, s := range p.Canonical {
		if s == a.CanonicalID {
			return true
		}
	}
	for _, s := range p.Federated {
		if s == a.PrincipalARN {
			return true
		}
	}
	return false
}

// Match performs IAM wildcard matching: '*' matches any sequence, '?' one
// character. Actions are matched case-insensitively by the caller.
func Match(pattern, s string) bool {
	// Iterative glob with backtracking.
	px, sx := 0, 0
	starP, starS := -1, -1
	for sx < len(s) {
		if px < len(pattern) && (pattern[px] == '?' || pattern[px] == s[sx]) {
			px++
			sx++
		} else if px < len(pattern) && pattern[px] == '*' {
			starP, starS = px, sx
			px++
		} else if starP >= 0 {
			px = starP + 1
			starS++
			sx = starS
		} else {
			return false
		}
	}
	for px < len(pattern) && pattern[px] == '*' {
		px++
	}
	return px == len(pattern)
}

func matchAny(patterns []string, s string, caseSensitive bool) bool {
	for _, p := range patterns {
		if caseSensitive {
			if Match(p, s) {
				return true
			}
		} else if Match(strings.ToLower(p), strings.ToLower(s)) {
			return true
		}
	}
	return false
}

func matchAnyVars(patterns []string, s string, vars map[string]string) bool {
	for _, p := range patterns {
		if Match(SubstituteVars(p, vars), s) {
			return true
		}
	}
	return false
}

// SubstituteVars replaces ${name} policy variables. Unknown variables are
// left in place (so they never match). "${*}", "${?}" and "${$}" are the
// documented escapes.
func SubstituteVars(s string, vars map[string]string) string {
	if !strings.Contains(s, "${") {
		return s
	}
	var b strings.Builder
	for {
		i := strings.Index(s, "${")
		if i < 0 {
			b.WriteString(s)
			break
		}
		j := strings.IndexByte(s[i:], '}')
		if j < 0 {
			b.WriteString(s)
			break
		}
		b.WriteString(s[:i])
		name := s[i+2 : i+j]
		switch name {
		case "*", "?", "$":
			b.WriteString(name)
		default:
			if v, ok := vars[strings.ToLower(name)]; ok {
				b.WriteString(v)
			} else {
				b.WriteString(s[i : i+j+1])
			}
		}
		s = s[i+j+1:]
	}
	return b.String()
}

// splitOperator parses "ForAnyValue:StringEqualsIfExists" into its parts.
func splitOperator(op string) (base string, set string, ifExists bool, ok bool) {
	if i := strings.IndexByte(op, ':'); i >= 0 {
		set = op[:i]
		op = op[i+1:]
		if set != "ForAnyValue" && set != "ForAllValues" {
			return "", "", false, false
		}
	}
	if strings.HasSuffix(op, "IfExists") {
		ifExists = true
		op = strings.TrimSuffix(op, "IfExists")
	}
	switch op {
	case "StringEquals", "StringNotEquals", "StringEqualsIgnoreCase", "StringNotEqualsIgnoreCase", "StringLike", "StringNotLike",
		"NumericEquals", "NumericNotEquals", "NumericLessThan", "NumericLessThanEquals", "NumericGreaterThan", "NumericGreaterThanEquals",
		"DateEquals", "DateNotEquals", "DateLessThan", "DateLessThanEquals", "DateGreaterThan", "DateGreaterThanEquals",
		"Bool", "BinaryEquals", "IpAddress", "NotIpAddress", "ArnEquals", "ArnLike", "ArnNotEquals", "ArnNotLike", "Null":
		return op, set, ifExists, true
	}
	return "", "", false, false
}

func evalCondition(op string, keys map[string][]string, a Args) bool {
	base, set, ifExists, _ := splitOperator(op)
	for key, want := range keys {
		have, present := a.Conditions[strings.ToLower(key)]
		// Substitute variables in wanted values.
		wv := make([]string, len(want))
		for i, w := range want {
			wv[i] = SubstituteVars(w, a.Vars)
		}
		if base == "Null" {
			wantNull := len(wv) > 0 && strings.EqualFold(wv[0], "true")
			if wantNull == (present && len(have) > 0) {
				return false
			}
			continue
		}
		if !present || len(have) == 0 {
			if ifExists || set == "ForAllValues" {
				continue
			}
			return false
		}
		switch set {
		case "ForAllValues":
			for _, h := range have {
				if !matchOne(base, h, wv) {
					return false
				}
			}
		case "ForAnyValue":
			any := false
			for _, h := range have {
				if matchOne(base, h, wv) {
					any = true
					break
				}
			}
			if !any {
				return false
			}
		default:
			// Single-valued: use the first value.
			if !matchOne(base, have[0], wv) {
				return false
			}
		}
	}
	return true
}

// matchOne evaluates one operator for a single request value against the
// policy's list of values (OR semantics, negated operators use AND).
func matchOne(op, have string, want []string) bool {
	switch op {
	case "StringEquals":
		return anyOf(want, func(w string) bool { return have == w })
	case "StringNotEquals":
		return !anyOf(want, func(w string) bool { return have == w })
	case "StringEqualsIgnoreCase":
		return anyOf(want, func(w string) bool { return strings.EqualFold(have, w) })
	case "StringNotEqualsIgnoreCase":
		return !anyOf(want, func(w string) bool { return strings.EqualFold(have, w) })
	case "StringLike":
		return anyOf(want, func(w string) bool { return Match(w, have) })
	case "StringNotLike":
		return !anyOf(want, func(w string) bool { return Match(w, have) })
	case "NumericEquals", "NumericNotEquals", "NumericLessThan", "NumericLessThanEquals", "NumericGreaterThan", "NumericGreaterThanEquals":
		h, err := strconv.ParseFloat(have, 64)
		if err != nil {
			return false
		}
		res := anyOf(want, func(w string) bool {
			x, err := strconv.ParseFloat(w, 64)
			if err != nil {
				return false
			}
			switch op {
			case "NumericEquals", "NumericNotEquals":
				return h == x
			case "NumericLessThan":
				return h < x
			case "NumericLessThanEquals":
				return h <= x
			case "NumericGreaterThan":
				return h > x
			default:
				return h >= x
			}
		})
		if op == "NumericNotEquals" {
			return !res
		}
		return res
	case "DateEquals", "DateNotEquals", "DateLessThan", "DateLessThanEquals", "DateGreaterThan", "DateGreaterThanEquals":
		h, ok := parseDate(have)
		if !ok {
			return false
		}
		res := anyOf(want, func(w string) bool {
			x, ok := parseDate(w)
			if !ok {
				return false
			}
			switch op {
			case "DateEquals", "DateNotEquals":
				return h.Equal(x)
			case "DateLessThan":
				return h.Before(x)
			case "DateLessThanEquals":
				return !h.After(x)
			case "DateGreaterThan":
				return h.After(x)
			default:
				return !h.Before(x)
			}
		})
		if op == "DateNotEquals" {
			return !res
		}
		return res
	case "Bool":
		return anyOf(want, func(w string) bool { return strings.EqualFold(have, w) })
	case "BinaryEquals":
		return anyOf(want, func(w string) bool { return have == w })
	case "IpAddress", "NotIpAddress":
		ip := net.ParseIP(have)
		res := ip != nil && anyOf(want, func(w string) bool {
			if !strings.Contains(w, "/") {
				if ip.To4() != nil {
					w += "/32"
				} else {
					w += "/128"
				}
			}
			_, n, err := net.ParseCIDR(w)
			return err == nil && n.Contains(ip)
		})
		if op == "NotIpAddress" {
			return !res
		}
		return res
	case "ArnEquals", "ArnLike":
		return anyOf(want, func(w string) bool { return arnMatch(w, have) })
	case "ArnNotEquals", "ArnNotLike":
		return !anyOf(want, func(w string) bool { return arnMatch(w, have) })
	}
	return false
}

func anyOf(l []string, f func(string) bool) bool {
	for _, s := range l {
		if f(s) {
			return true
		}
	}
	return false
}

func parseDate(s string) (time.Time, bool) {
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return t, true
	}
	if n, err := strconv.ParseInt(s, 10, 64); err == nil {
		return time.Unix(n, 0), true
	}
	return time.Time{}, false
}

// arnMatch compares ARNs component-wise, with wildcards in each of the
// six colon-separated fields (the resource field may contain '/').
func arnMatch(pattern, arn string) bool {
	pp := strings.SplitN(pattern, ":", 6)
	ap := strings.SplitN(arn, ":", 6)
	if len(pp) != 6 || len(ap) != 6 {
		return false
	}
	for i := 0; i < 6; i++ {
		if i < 5 {
			if !Match(strings.ToLower(pp[i]), strings.ToLower(ap[i])) {
				return false
			}
		} else if !Match(pp[i], ap[i]) {
			return false
		}
	}
	return true
}

// ValidateBucketPolicy checks the constraints S3 imposes on bucket
// policies: every statement needs a Principal and every resource must
// belong to bucket.
func ValidateBucketPolicy(d *Document, bucket string) error {
	for _, st := range d.Statements {
		if st.Principal == nil && st.NotPrincipal == nil {
			return fmt.Errorf("%w: missing Principal", ErrMalformed)
		}
		for _, r := range append(append([]string{}, st.Resources...), st.NotResources...) {
			if !strings.HasPrefix(r, "arn:aws:s3:::") {
				return fmt.Errorf("%w: invalid resource %s", ErrMalformed, r)
			}
			rest := strings.TrimPrefix(r, "arn:aws:s3:::")
			b := rest
			if i := strings.IndexByte(rest, '/'); i >= 0 {
				b = rest[:i]
			}
			if !Match(b, bucket) && b != bucket {
				return fmt.Errorf("%w: resource %s does not belong to bucket", ErrMalformed, r)
			}
		}
		for _, a := range append(append([]string{}, st.Actions...), st.NotActions...) {
			if a != "*" && !strings.HasPrefix(strings.ToLower(a), "s3:") {
				return fmt.Errorf("%w: invalid action %s", ErrMalformed, a)
			}
		}
	}
	return nil
}

// IsPublic reports whether the policy grants anything to everyone (used
// for GetBucketPolicyStatus and Block Public Access).
func (d *Document) IsPublic() bool {
	for _, st := range d.Statements {
		if st.Effect != Allow || st.Principal == nil || !st.Principal.All {
			continue
		}
		// A public statement restricted by a limiting condition is not
		// considered public by AWS; approximate: any IP/ARN/principal
		// condition makes it non-public.
		limiting := false
		for op := range st.Conditions {
			base, _, _, _ := splitOperator(op)
			switch base {
			case "IpAddress", "ArnEquals", "ArnLike", "StringEquals", "StringLike", "StringEqualsIgnoreCase", "Bool", "BinaryEquals", "NumericEquals", "DateEquals":
				for k := range st.Conditions[op] {
					lk := strings.ToLower(k)
					if strings.HasPrefix(lk, "aws:sourcevpc") || lk == "aws:sourceip" || strings.HasPrefix(lk, "aws:principal") || lk == "aws:userid" || lk == "aws:sourcearn" || lk == "aws:sourceaccount" || strings.HasPrefix(lk, "s3:dataaccesspoint") {
						limiting = true
					}
				}
			}
		}
		if !limiting {
			return true
		}
	}
	return false
}

// ResourceARN builds an S3 resource ARN.
func ResourceARN(bucket, key string) string {
	if key == "" {
		return "arn:aws:s3:::" + bucket
	}
	return "arn:aws:s3:::" + path.Join(bucket, key)
}
