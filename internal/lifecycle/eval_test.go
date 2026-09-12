package lifecycle

import (
	"testing"
	"time"

	"gitlab.com/Birdsall/opens3/internal/meta"
)

var (
	// A fixed "now": 2026-06-15 12:00 UTC.
	now = time.Date(2026, 6, 15, 12, 0, 0, 0, time.UTC)
	// Object created 2026-06-10 10:30 UTC: 1 day expires at 06-12 00:00,
	// 5 days at 06-16 00:00.
	created = time.Date(2026, 6, 10, 10, 30, 0, 0, time.UTC)
)

func cfg(t *testing.T, x string) *Config {
	t.Helper()
	c := mustParse(t, `<LifecycleConfiguration>`+x+`</LifecycleConfiguration>`)
	if err := Validate(c); err != nil {
		t.Fatalf("validate: %v", err)
	}
	return c
}

func current(key string) EvalInput {
	return EvalInput{Key: key, Size: 100, ModTime: created, IsLatest: true}
}

func TestExpiresAtRounding(t *testing.T) {
	if got := expiresAt(created, 1); !got.Equal(time.Date(2026, 6, 12, 0, 0, 0, 0, time.UTC)) {
		t.Fatalf("1 day: %v", got)
	}
	midnight := time.Date(2026, 6, 10, 0, 0, 0, 0, time.UTC)
	if got := expiresAt(midnight, 1); !got.Equal(time.Date(2026, 6, 11, 0, 0, 0, 0, time.UTC)) {
		t.Fatalf("exact midnight: %v", got)
	}
	if got := expiresAt(created, 0); !got.Equal(time.Date(2026, 6, 11, 0, 0, 0, 0, time.UTC)) {
		t.Fatalf("0 days: %v", got)
	}
}

func TestEvalExpirationDays(t *testing.T) {
	c := cfg(t, `<Rule><ID>e</ID><Status>Enabled</Status><Filter><Prefix>logs/</Prefix></Filter><Expiration><Days>3</Days></Expiration></Rule>`)
	// 3 days after 06-10 10:30 rounds to 06-14 00:00 -> expired at 06-15 12:00.
	if a := c.Eval(current("logs/a"), now); a.Kind != KindExpireCurrent || a.RuleID != "e" {
		t.Fatalf("got %+v", a)
	}
	// Just before the rounded midnight it is not expired.
	if a := c.Eval(current("logs/a"), time.Date(2026, 6, 13, 23, 59, 59, 0, time.UTC)); a.Kind != KindNone {
		t.Fatalf("early: %+v", a)
	}
	if a := c.Eval(current("logs/a"), time.Date(2026, 6, 14, 0, 0, 0, 0, time.UTC)); a.Kind != KindExpireCurrent {
		t.Fatalf("at midnight: %+v", a)
	}
	// Prefix mismatch.
	if a := c.Eval(current("other/a"), now); a.Kind != KindNone {
		t.Fatalf("prefix: %+v", a)
	}
	// Disabled rules are skipped.
	c.Rules[0].Status = "Disabled"
	if a := c.Eval(current("logs/a"), now); a.Kind != KindNone {
		t.Fatalf("disabled: %+v", a)
	}
}

func TestEvalExpirationDate(t *testing.T) {
	c := cfg(t, `<Rule><Status>Enabled</Status><Prefix></Prefix><Expiration><Date>2026-06-15T00:00:00Z</Date></Expiration></Rule>`)
	if a := c.Eval(current("x"), now); a.Kind != KindExpireCurrent {
		t.Fatalf("got %+v", a)
	}
	if a := c.Eval(current("x"), now.AddDate(0, 0, -1)); a.Kind != KindNone {
		t.Fatalf("before date: %+v", a)
	}
}

func TestEvalTagAndSizeFilters(t *testing.T) {
	c := cfg(t, `<Rule><ID>tag</ID><Status>Enabled</Status><Filter><Tag><Key>tier</Key><Value>tmp</Value></Tag></Filter><Expiration><Days>1</Days></Expiration></Rule>
	<Rule><ID>and</ID><Status>Enabled</Status><Filter><And><Prefix>data/</Prefix><Tag><Key>a</Key><Value>1</Value></Tag><Tag><Key>b</Key><Value>2</Value></Tag><ObjectSizeGreaterThan>10</ObjectSizeGreaterThan><ObjectSizeLessThan>1000</ObjectSizeLessThan></And></Filter><Expiration><Days>1</Days></Expiration></Rule>
	<Rule><ID>small</ID><Status>Enabled</Status><Filter><ObjectSizeLessThan>10</ObjectSizeLessThan></Filter><Expiration><Days>1</Days></Expiration></Rule>`)
	in := current("x")
	if a := c.Eval(in, now); a.Kind != KindNone {
		t.Fatalf("no tags: %+v", a)
	}
	in.Tags = []meta.Tag{{Key: "tier", Value: "tmp"}}
	if a := c.Eval(in, now); a.Kind != KindExpireCurrent || a.RuleID != "tag" {
		t.Fatalf("tag: %+v", a)
	}
	in.Tags = []meta.Tag{{Key: "tier", Value: "other"}}
	if a := c.Eval(in, now); a.Kind != KindNone {
		t.Fatalf("tag value: %+v", a)
	}
	// And: all tags, prefix and both size bounds must hold.
	in = current("data/x")
	in.Tags = []meta.Tag{{Key: "b", Value: "2"}, {Key: "a", Value: "1"}, {Key: "extra", Value: "z"}}
	if a := c.Eval(in, now); a.Kind != KindExpireCurrent || a.RuleID != "and" {
		t.Fatalf("and: %+v", a)
	}
	in.Size = 10
	if a := c.Eval(in, now); a.Kind != KindNone {
		t.Fatalf("size not greater: %+v", a)
	}
	in.Size = 1000
	if a := c.Eval(in, now); a.Kind != KindNone {
		t.Fatalf("size not less: %+v", a)
	}
	in.Size = 500
	in.Tags = in.Tags[1:]
	if a := c.Eval(in, now); a.Kind != KindNone {
		t.Fatalf("missing tag: %+v", a)
	}
	in.Tags = []meta.Tag{{Key: "b", Value: "2"}, {Key: "a", Value: "1"}}
	in.Key = "x"
	if a := c.Eval(in, now); a.Kind != KindNone {
		t.Fatalf("and prefix: %+v", a)
	}
	// Single size filter.
	in = current("x")
	in.Size = 5
	if a := c.Eval(in, now); a.Kind != KindExpireCurrent || a.RuleID != "small" {
		t.Fatalf("small: %+v", a)
	}
}

func TestEvalTransitions(t *testing.T) {
	c := cfg(t, `<Rule><ID>t</ID><Status>Enabled</Status><Filter></Filter>
	<Transition><Days>1</Days><StorageClass>STANDARD_IA</StorageClass></Transition>
	<Transition><Days>4</Days><StorageClass>GLACIER</StorageClass></Transition>
	<Expiration><Days>30</Days></Expiration></Rule>`)
	// 1 day -> 06-12; 4 days -> 06-15 00:00. At now both are due: coldest wins.
	if a := c.Eval(current("x"), now); a.Kind != KindTransition || a.StorageClass != "GLACIER" {
		t.Fatalf("got %+v", a)
	}
	if a := c.Eval(current("x"), time.Date(2026, 6, 13, 0, 0, 0, 0, time.UTC)); a.Kind != KindTransition || a.StorageClass != "STANDARD_IA" {
		t.Fatalf("first step: %+v", a)
	}
	// Already in GLACIER: nothing to do; already in DEEP_ARCHIVE: never warm up.
	in := current("x")
	in.StorageClass = "GLACIER"
	if a := c.Eval(in, now); a.Kind != KindNone {
		t.Fatalf("same class: %+v", a)
	}
	in.StorageClass = "DEEP_ARCHIVE"
	if a := c.Eval(in, now); a.Kind != KindNone {
		t.Fatalf("colder class: %+v", a)
	}
	// Expiration wins over transition when both are due.
	c2 := cfg(t, `<Rule><ID>t</ID><Status>Enabled</Status><Filter></Filter><Transition><Days>1</Days><StorageClass>GLACIER</StorageClass></Transition></Rule>
	<Rule><ID>e</ID><Status>Enabled</Status><Filter></Filter><Expiration><Days>2</Days></Expiration></Rule>`)
	if a := c2.Eval(current("x"), now); a.Kind != KindExpireCurrent || a.RuleID != "e" {
		t.Fatalf("precedence: %+v", a)
	}
	// Date-based transition.
	c3 := cfg(t, `<Rule><Status>Enabled</Status><Filter></Filter><Transition><Date>2026-06-15T00:00:00Z</Date><StorageClass>ONEZONE_IA</StorageClass></Transition></Rule>`)
	if a := c3.Eval(current("x"), now); a.Kind != KindTransition || a.StorageClass != "ONEZONE_IA" {
		t.Fatalf("date transition: %+v", a)
	}
}

func TestEvalNoncurrent(t *testing.T) {
	c := cfg(t, `<Rule><ID>n</ID><Status>Enabled</Status><Filter></Filter>
	<NoncurrentVersionTransition><NoncurrentDays>1</NoncurrentDays><StorageClass>GLACIER_IR</StorageClass></NoncurrentVersionTransition>
	<NoncurrentVersionExpiration><NoncurrentDays>3</NoncurrentDays><NewerNoncurrentVersions>2</NewerNoncurrentVersions></NoncurrentVersionExpiration></Rule>`)
	nc := EvalInput{Key: "x", Size: 1, ModTime: created.AddDate(0, 0, -10), NoncurrentSince: created, NewerNoncurrentCount: 2}
	// Noncurrent since 06-10 10:30: 3 days -> 06-14 00:00, expired at now.
	if a := c.Eval(nc, now); a.Kind != KindExpireNoncurrentVersion {
		t.Fatalf("got %+v", a)
	}
	// Fewer newer noncurrent versions than NewerNoncurrentVersions: retained, but transitioned.
	nc.NewerNoncurrentCount = 1
	if a := c.Eval(nc, now); a.Kind != KindNoncurrentTransition || a.StorageClass != "GLACIER_IR" {
		t.Fatalf("retained: %+v", a)
	}
	// Not yet 3 days noncurrent (only ModTime is old): transition only.
	nc.NewerNoncurrentCount = 5
	if a := c.Eval(nc, time.Date(2026, 6, 13, 0, 0, 0, 0, time.UTC)); a.Kind != KindNoncurrentTransition {
		t.Fatalf("transition: %+v", a)
	}
	if a := c.Eval(nc, time.Date(2026, 6, 11, 0, 0, 0, 0, time.UTC)); a.Kind != KindNone {
		t.Fatalf("too early: %+v", a)
	}
	// Noncurrent delete markers are expired but never transitioned.
	dm := nc
	dm.DeleteMarker = true
	if a := c.Eval(dm, now); a.Kind != KindExpireNoncurrentVersion {
		t.Fatalf("noncurrent marker: %+v", a)
	}
	dm.NewerNoncurrentCount = 0
	if a := c.Eval(dm, now); a.Kind != KindNone {
		t.Fatalf("noncurrent marker retained: %+v", a)
	}
	// Current-version rules do not touch noncurrent versions.
	c2 := cfg(t, `<Rule><Status>Enabled</Status><Filter></Filter><Expiration><Days>1</Days></Expiration><Transition><Days>0</Days><StorageClass>GLACIER</StorageClass></Transition></Rule>`)
	if a := c2.Eval(nc, now); a.Kind != KindNone {
		t.Fatalf("current rule on noncurrent: %+v", a)
	}
}

func TestEvalDeleteMarkers(t *testing.T) {
	eodm := cfg(t, `<Rule><ID>dm</ID><Status>Enabled</Status><Filter></Filter><Expiration><ExpiredObjectDeleteMarker>true</ExpiredObjectDeleteMarker></Expiration></Rule>`)
	marker := EvalInput{Key: "x", ModTime: now, IsLatest: true, DeleteMarker: true}
	if a := eodm.Eval(marker, now); a.Kind != KindRemoveDeleteMarker || a.RuleID != "dm" {
		t.Fatalf("got %+v", a)
	}
	marker.HasOlderVersions = true
	if a := eodm.Eval(marker, now); a.Kind != KindNone {
		t.Fatalf("has versions: %+v", a)
	}
	off := cfg(t, `<Rule><Status>Enabled</Status><Filter></Filter><Expiration><ExpiredObjectDeleteMarker>false</ExpiredObjectDeleteMarker></Expiration></Rule>`)
	marker.HasOlderVersions = false
	if a := off.Eval(marker, now); a.Kind != KindNone {
		t.Fatalf("eodm false: %+v", a)
	}
	// Days-based expiration removes expired delete markers once the days
	// have elapsed since the marker was created.
	days := cfg(t, `<Rule><Status>Enabled</Status><Filter></Filter><Expiration><Days>1</Days></Expiration></Rule>`)
	if a := days.Eval(marker, now); a.Kind != KindNone {
		t.Fatalf("fresh marker: %+v", a)
	}
	marker.ModTime = created
	if a := days.Eval(marker, now); a.Kind != KindRemoveDeleteMarker {
		t.Fatalf("old marker: %+v", a)
	}
	// Delete markers are never transitioned or "expired" into a new marker.
	tr := cfg(t, `<Rule><Status>Enabled</Status><Filter></Filter><Transition><Days>0</Days><StorageClass>GLACIER</StorageClass></Transition></Rule>`)
	if a := tr.Eval(marker, now); a.Kind != KindNone {
		t.Fatalf("transition marker: %+v", a)
	}
}

func TestAbortAfter(t *testing.T) {
	c := cfg(t, `<Rule><Status>Enabled</Status><Filter><Prefix>up/</Prefix></Filter><AbortIncompleteMultipartUpload><DaysAfterInitiation>2</DaysAfterInitiation></AbortIncompleteMultipartUpload></Rule>
	<Rule><Status>Disabled</Status><Filter></Filter><AbortIncompleteMultipartUpload><DaysAfterInitiation>1</DaysAfterInitiation></AbortIncompleteMultipartUpload></Rule>`)
	// 2 days after 06-10 10:30 -> 06-13 00:00.
	if !c.AbortAfter(created, "up/x", now) {
		t.Fatal("should abort")
	}
	if c.AbortAfter(created, "up/x", time.Date(2026, 6, 12, 23, 0, 0, 0, time.UTC)) {
		t.Fatal("too early")
	}
	if c.AbortAfter(created, "other/x", now) {
		t.Fatal("prefix / disabled rule")
	}
}
