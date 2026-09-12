package lifecycle

import (
	"strings"
	"time"

	"gitlab.com/Birdsall/opens3/internal/meta"
)

// Kind is the type of a lifecycle action.
type Kind int

// Action kinds, in order of precedence (higher wins when several rules
// match the same version).
const (
	KindNone Kind = iota
	// NoncurrentTransition records a storage-class change on a noncurrent version.
	KindNoncurrentTransition
	// Transition records a storage-class change on the current version.
	KindTransition
	// RemoveDeleteMarker permanently removes an expired delete marker (a
	// current delete marker with no other versions).
	KindRemoveDeleteMarker
	// ExpireNoncurrentVersion permanently deletes a noncurrent version.
	KindExpireNoncurrentVersion
	// ExpireCurrent deletes the current version: permanently in an
	// unversioned bucket, by inserting a delete marker otherwise.
	KindExpireCurrent
)

func (k Kind) String() string {
	switch k {
	case KindNone:
		return "None"
	case KindNoncurrentTransition:
		return "NoncurrentTransition"
	case KindTransition:
		return "Transition"
	case KindRemoveDeleteMarker:
		return "RemoveDeleteMarker"
	case KindExpireNoncurrentVersion:
		return "ExpireNoncurrentVersion"
	case KindExpireCurrent:
		return "ExpireCurrent"
	}
	return "Kind(?)"
}

// Action is what the lifecycle rules decide for one object version.
type Action struct {
	Kind Kind
	// StorageClass is the target class for transitions.
	StorageClass string
	// RuleID is the ID of the rule that produced the action.
	RuleID string
}

// EvalInput describes one object version as the rules see it.
type EvalInput struct {
	Key          string
	Size         int64
	Tags         []meta.Tag
	ModTime      time.Time
	StorageClass string // current class ("" = STANDARD)
	IsLatest     bool
	DeleteMarker bool
	IsNull       bool
	// HasOlderVersions is set when other (noncurrent) versions of the key
	// exist; a current delete marker without any is an "expired object
	// delete marker".
	HasOlderVersions bool
	// NoncurrentSince is when the version became noncurrent (the ModTime
	// of the next newer version). Ignored for the current version.
	NoncurrentSince time.Time
	// NewerNoncurrentCount is the number of noncurrent versions newer
	// than this one.
	NewerNoncurrentCount int
}

// classRank orders storage classes from hottest to coldest; a transition
// only ever moves an object to a colder class, and when several rules
// transition the same version the coldest target wins (as on AWS).
var classRank = map[string]int{
	"": 0, "STANDARD": 0, "REDUCED_REDUNDANCY": 0, "EXPRESS_ONEZONE": 0, "OUTPOSTS": 0,
	"STANDARD_IA": 1, "INTELLIGENT_TIERING": 2, "ONEZONE_IA": 3,
	"GLACIER_IR": 4, "GLACIER": 5, "DEEP_ARCHIVE": 6,
}

// expiresAt returns the moment days days after t, rounded up to the next
// midnight UTC as AWS does.
func expiresAt(t time.Time, days int) time.Time {
	x := t.UTC().Add(time.Duration(days) * 24 * time.Hour)
	m := x.Truncate(24 * time.Hour)
	if m.Before(x) {
		m = m.Add(24 * time.Hour)
	}
	return m
}

// dateReached reports whether a Date-based action has come due.
func dateReached(date string, now time.Time) bool {
	d, err := parseDate(date)
	if err != nil {
		return false
	}
	return !now.Before(d)
}

// filterPrefix returns the prefix a rule applies to.
func (r *Rule) filterPrefix() string {
	switch {
	case r.Prefix != nil:
		return *r.Prefix
	case r.Filter == nil:
		return ""
	case r.Filter.Prefix != nil:
		return *r.Filter.Prefix
	case r.Filter.And != nil && r.Filter.And.Prefix != nil:
		return *r.Filter.And.Prefix
	}
	return ""
}

// matches reports whether the rule's filter selects the object.
func (r *Rule) matches(o *EvalInput) bool {
	if !strings.HasPrefix(o.Key, r.filterPrefix()) {
		return false
	}
	f := r.Filter
	if f == nil {
		return true
	}
	var tags []Tag
	gt, lt := f.ObjectSizeGreaterThan, f.ObjectSizeLessThan
	if f.Tag != nil {
		tags = []Tag{*f.Tag}
	}
	if f.And != nil {
		tags, gt, lt = f.And.Tags, f.And.ObjectSizeGreaterThan, f.And.ObjectSizeLessThan
	}
	for _, t := range tags {
		if !hasTag(o.Tags, t) {
			return false
		}
	}
	if gt != nil && !(o.Size > *gt) {
		return false
	}
	if lt != nil && !(o.Size < *lt) {
		return false
	}
	return true
}

func hasTag(tags []meta.Tag, want Tag) bool {
	for _, t := range tags {
		if t.Key == want.Key && t.Value == want.Value {
			return true
		}
	}
	return false
}

// Eval decides what, if anything, the lifecycle configuration does to the
// version described by o at time now. Expiration takes precedence over
// transition; among transitions the coldest class wins.
func (c *Config) Eval(o EvalInput, now time.Time) Action {
	best := Action{Kind: KindNone}
	consider := func(a Action) {
		if a.Kind == KindNone {
			return
		}
		if a.Kind > best.Kind || (a.Kind == best.Kind && classRank[a.StorageClass] > classRank[best.StorageClass]) {
			best = a
		}
	}
	for i := range c.Rules {
		r := &c.Rules[i]
		if r.Status != "Enabled" || !r.matches(&o) {
			continue
		}
		switch {
		case o.IsLatest && o.DeleteMarker:
			consider(r.evalDeleteMarker(&o, now))
		case o.IsLatest:
			consider(r.evalCurrent(&o, now))
		default:
			consider(r.evalNoncurrent(&o, now))
		}
	}
	return best
}

func (r *Rule) evalCurrent(o *EvalInput, now time.Time) Action {
	if e := r.Expiration; e != nil {
		if (e.Days > 0 && !now.Before(expiresAt(o.ModTime, e.Days))) || (e.Date != "" && dateReached(e.Date, now)) {
			return Action{Kind: KindExpireCurrent, RuleID: r.ID}
		}
	}
	best := Action{Kind: KindNone}
	for _, t := range r.Transitions {
		due := false
		if t.Date != "" {
			due = dateReached(t.Date, now)
		} else {
			due = !now.Before(expiresAt(o.ModTime, t.Days))
		}
		if due && classRank[t.StorageClass] > classRank[o.StorageClass] && classRank[t.StorageClass] > classRank[best.StorageClass] {
			best = Action{Kind: KindTransition, StorageClass: t.StorageClass, RuleID: r.ID}
		}
	}
	return best
}

// evalDeleteMarker handles a current delete marker. It is only removed
// when it is an expired object delete marker (no other versions remain):
// immediately for ExpiredObjectDeleteMarker rules and once the Days/Date
// have passed for ordinary expiration rules (AWS removes expired delete
// markers automatically for those).
func (r *Rule) evalDeleteMarker(o *EvalInput, now time.Time) Action {
	e := r.Expiration
	if e == nil || o.HasOlderVersions {
		return Action{Kind: KindNone}
	}
	switch {
	case e.ExpiredObjectDeleteMarker != nil:
		if *e.ExpiredObjectDeleteMarker {
			return Action{Kind: KindRemoveDeleteMarker, RuleID: r.ID}
		}
	case e.Days > 0:
		if !now.Before(expiresAt(o.ModTime, e.Days)) {
			return Action{Kind: KindRemoveDeleteMarker, RuleID: r.ID}
		}
	case e.Date != "":
		if dateReached(e.Date, now) {
			return Action{Kind: KindRemoveDeleteMarker, RuleID: r.ID}
		}
	}
	return Action{Kind: KindNone}
}

func (r *Rule) evalNoncurrent(o *EvalInput, now time.Time) Action {
	if n := r.NoncurrentVersionExpiration; n != nil {
		if o.NewerNoncurrentCount >= n.NewerNoncurrentVersions && !now.Before(expiresAt(o.NoncurrentSince, n.NoncurrentDays)) {
			return Action{Kind: KindExpireNoncurrentVersion, RuleID: r.ID}
		}
	}
	best := Action{Kind: KindNone}
	if o.DeleteMarker {
		return best
	}
	for _, t := range r.NoncurrentVersionTransitions {
		if o.NewerNoncurrentCount < t.NewerNoncurrentVersions || now.Before(expiresAt(o.NoncurrentSince, t.NoncurrentDays)) {
			continue
		}
		if classRank[t.StorageClass] > classRank[o.StorageClass] && classRank[t.StorageClass] > classRank[best.StorageClass] {
			best = Action{Kind: KindNoncurrentTransition, StorageClass: t.StorageClass, RuleID: r.ID}
		}
	}
	return best
}

// AbortAfter reports whether an incomplete multipart upload of key
// initiated at initiated should be aborted at time now. Only the prefix
// part of a rule's filter applies to uploads.
func (c *Config) AbortAfter(initiated time.Time, key string, now time.Time) bool {
	for i := range c.Rules {
		r := &c.Rules[i]
		a := r.AbortIncompleteMultipartUpload
		if r.Status != "Enabled" || a == nil || !strings.HasPrefix(key, r.filterPrefix()) {
			continue
		}
		if !now.Before(expiresAt(initiated, a.DaysAfterInitiation)) {
			return true
		}
	}
	return false
}
