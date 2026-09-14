// Package lifecycle implements S3 bucket lifecycle configuration: parsing
// and validation of the LifecycleConfiguration XML, evaluation of rules
// against object versions, and the background worker that applies the
// resulting actions (expiration, delete-marker cleanup, abort of stale
// multipart uploads and storage-class transitions).
package lifecycle

import (
	"encoding/xml"
	"time"

	"github.com/edward-b-1/OpenS3/internal/s3err"
)

// MaxRules is the AWS limit on rules per configuration.
const MaxRules = 1000

// Config is a LifecycleConfiguration document.
type Config struct {
	XMLName xml.Name `xml:"LifecycleConfiguration"`
	Xmlns   string   `xml:"xmlns,attr,omitempty"`
	Rules   []Rule   `xml:"Rule"`
}

// Rule is one lifecycle rule.
type Rule struct {
	ID     string  `xml:"ID,omitempty"`
	Status string  `xml:"Status"` // Enabled | Disabled
	Prefix *string `xml:"Prefix"` // legacy top-level prefix (exclusive with Filter)
	Filter *Filter `xml:"Filter"`

	Expiration                     *Expiration                     `xml:"Expiration"`
	NoncurrentVersionExpiration    *NoncurrentVersionExpiration    `xml:"NoncurrentVersionExpiration"`
	AbortIncompleteMultipartUpload *AbortIncompleteMultipartUpload `xml:"AbortIncompleteMultipartUpload"`
	Transitions                    []Transition                    `xml:"Transition"`
	NoncurrentVersionTransitions   []NoncurrentVersionTransition   `xml:"NoncurrentVersionTransition"`
}

// Tag is a filter tag.
type Tag struct {
	Key   string `xml:"Key"`
	Value string `xml:"Value"`
}

// Filter selects the objects a rule applies to. Either exactly one of
// Prefix, Tag, ObjectSizeGreaterThan, ObjectSizeLessThan is set, or And
// combines several; an empty Filter matches every object.
type Filter struct {
	Prefix                *string `xml:"Prefix"`
	Tag                   *Tag    `xml:"Tag"`
	ObjectSizeGreaterThan *int64  `xml:"ObjectSizeGreaterThan"`
	ObjectSizeLessThan    *int64  `xml:"ObjectSizeLessThan"`
	And                   *And    `xml:"And"`
}

// And is the conjunction form of a filter.
type And struct {
	Prefix                *string `xml:"Prefix"`
	Tags                  []Tag   `xml:"Tag"`
	ObjectSizeGreaterThan *int64  `xml:"ObjectSizeGreaterThan"`
	ObjectSizeLessThan    *int64  `xml:"ObjectSizeLessThan"`
}

// Expiration expires current versions (or removes expired delete markers).
type Expiration struct {
	Days                      int    `xml:"Days,omitempty"`
	Date                      string `xml:"Date,omitempty"` // ISO 8601, midnight UTC
	ExpiredObjectDeleteMarker *bool  `xml:"ExpiredObjectDeleteMarker"`
}

// NoncurrentVersionExpiration permanently deletes noncurrent versions.
type NoncurrentVersionExpiration struct {
	NoncurrentDays          int `xml:"NoncurrentDays"`
	NewerNoncurrentVersions int `xml:"NewerNoncurrentVersions,omitempty"`
}

// AbortIncompleteMultipartUpload aborts stale multipart uploads.
type AbortIncompleteMultipartUpload struct {
	DaysAfterInitiation int `xml:"DaysAfterInitiation"`
}

// Transition changes the storage class of current versions.
type Transition struct {
	Days         int    `xml:"Days,omitempty"`
	Date         string `xml:"Date,omitempty"`
	StorageClass string `xml:"StorageClass"`
}

// NoncurrentVersionTransition changes the storage class of noncurrent
// versions.
type NoncurrentVersionTransition struct {
	NoncurrentDays          int    `xml:"NoncurrentDays"`
	StorageClass            string `xml:"StorageClass"`
	NewerNoncurrentVersions int    `xml:"NewerNoncurrentVersions,omitempty"`
}

// Parse decodes a LifecycleConfiguration document. It does not validate
// the rules; call Validate for that. A decoding failure is reported as
// MalformedXML.
func Parse(raw []byte) (*Config, error) {
	var c Config
	if err := xml.Unmarshal(raw, &c); err != nil {
		return nil, s3err.New(s3err.MalformedXML)
	}
	return &c, nil
}

// transitionClasses are the storage classes a transition may target.
var transitionClasses = map[string]bool{
	"STANDARD_IA": true, "ONEZONE_IA": true, "INTELLIGENT_TIERING": true,
	"GLACIER": true, "GLACIER_IR": true, "DEEP_ARCHIVE": true,
}

// ValidTransitionClass reports whether sc may be the target of a
// lifecycle transition.
func ValidTransitionClass(sc string) bool { return transitionClasses[sc] }

func malformed() error { return s3err.New(s3err.MalformedXML) }

func invalidArg(msg string) error { return s3err.New(s3err.InvalidArgument).WithMessage("%s", msg) }

func invalidReq(msg string) error { return s3err.New(s3err.InvalidRequest).WithMessage("%s", msg) }

// Validate applies the AWS rules for a lifecycle configuration and returns
// an s3err error describing the first violation.
func Validate(c *Config) error {
	if c == nil || len(c.Rules) == 0 || len(c.Rules) > MaxRules {
		return malformed()
	}
	ids := map[string]bool{}
	for i := range c.Rules {
		r := &c.Rules[i]
		if r.Status != "Enabled" && r.Status != "Disabled" {
			return malformed()
		}
		if r.ID != "" {
			if len(r.ID) > 255 {
				return invalidArg("ID length should not exceed allowed limit of 255")
			}
			if ids[r.ID] {
				return invalidArg("Rule ID must be unique. Found same ID for more than one rule")
			}
			ids[r.ID] = true
		}
		if (r.Prefix == nil) == (r.Filter == nil) {
			// Exactly one of Prefix and Filter must be present.
			return malformed()
		}
		if err := validateFilter(r.Filter); err != nil {
			return err
		}
		if r.Expiration == nil && r.NoncurrentVersionExpiration == nil && r.AbortIncompleteMultipartUpload == nil &&
			len(r.Transitions) == 0 && len(r.NoncurrentVersionTransitions) == 0 {
			return invalidReq("At least one action needs to be specified in a rule")
		}
		hasTags := r.Filter != nil && (r.Filter.Tag != nil || (r.Filter.And != nil && len(r.Filter.And.Tags) > 0))
		if err := validateExpiration(r.Expiration, hasTags); err != nil {
			return err
		}
		if n := r.NoncurrentVersionExpiration; n != nil {
			if n.NoncurrentDays <= 0 {
				return invalidArg("'NoncurrentDays' for NoncurrentVersionExpiration action must be a positive integer")
			}
			if n.NewerNoncurrentVersions < 0 || n.NewerNoncurrentVersions > 100 {
				return invalidArg("'NewerNoncurrentVersions' for NoncurrentVersionExpiration action must be between 0 and 100")
			}
		}
		if a := r.AbortIncompleteMultipartUpload; a != nil {
			if a.DaysAfterInitiation <= 0 {
				return invalidArg("'DaysAfterInitiation' for AbortIncompleteMultipartUpload action must be a positive integer")
			}
			if hasTags {
				return invalidReq("AbortIncompleteMultipartUpload cannot be specified with Tags.")
			}
		}
		if err := validateTransitions(r); err != nil {
			return err
		}
		seen := map[string]bool{}
		for _, t := range r.NoncurrentVersionTransitions {
			if !ValidTransitionClass(t.StorageClass) {
				return malformed()
			}
			if seen[t.StorageClass] {
				return invalidReq("'StorageClass' must be different for 'NoncurrentVersionTransition' actions in same 'Rule'")
			}
			seen[t.StorageClass] = true
			if t.NoncurrentDays < 0 {
				return invalidArg("'NoncurrentDays' for NoncurrentVersionTransition action must be a non-negative integer")
			}
			if t.NewerNoncurrentVersions < 0 || t.NewerNoncurrentVersions > 100 {
				return invalidArg("'NewerNoncurrentVersions' for NoncurrentVersionTransition action must be between 0 and 100")
			}
			if n := r.NoncurrentVersionExpiration; n != nil && n.NoncurrentDays <= t.NoncurrentDays {
				return invalidArg("'NoncurrentDays' in the NoncurrentVersionExpiration action must be greater than 'NoncurrentDays' in the NoncurrentVersionTransition action")
			}
		}
	}
	return nil
}

func validateFilter(f *Filter) error {
	if f == nil {
		return nil
	}
	n := 0
	for _, set := range []bool{f.Prefix != nil, f.Tag != nil, f.ObjectSizeGreaterThan != nil, f.ObjectSizeLessThan != nil, f.And != nil} {
		if set {
			n++
		}
	}
	if n > 1 {
		return malformed()
	}
	if f.Tag != nil {
		if err := validateTags([]Tag{*f.Tag}); err != nil {
			return err
		}
	}
	if err := validateSizes(f.ObjectSizeGreaterThan, f.ObjectSizeLessThan); err != nil {
		return err
	}
	if a := f.And; a != nil {
		if a.Prefix == nil && len(a.Tags) == 0 && a.ObjectSizeGreaterThan == nil && a.ObjectSizeLessThan == nil {
			return malformed()
		}
		if err := validateTags(a.Tags); err != nil {
			return err
		}
		if err := validateSizes(a.ObjectSizeGreaterThan, a.ObjectSizeLessThan); err != nil {
			return err
		}
	}
	return nil
}

func validateTags(tags []Tag) error {
	keys := map[string]bool{}
	for _, t := range tags {
		if t.Key == "" || len(t.Key) > 128 || len(t.Value) > 256 {
			return invalidArg("The TagKey you have provided is invalid")
		}
		if keys[t.Key] {
			return invalidReq("Duplicate Tag Keys are not allowed.")
		}
		keys[t.Key] = true
	}
	return nil
}

func validateSizes(gt, lt *int64) error {
	if gt != nil && *gt < 0 {
		return invalidArg("'ObjectSizeGreaterThan' must be a non-negative integer")
	}
	if lt != nil && *lt <= 0 {
		return invalidArg("'ObjectSizeLessThan' must be a positive integer")
	}
	if gt != nil && lt != nil && *gt >= *lt {
		return invalidArg("'ObjectSizeGreaterThan' must be less than 'ObjectSizeLessThan'")
	}
	return nil
}

// parseDate parses a lifecycle Date and checks that it is at midnight UTC.
func parseDate(s string) (time.Time, error) {
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		// AWS also accepts a bare date.
		t, err = time.Parse("2006-01-02", s)
		if err != nil {
			return time.Time{}, invalidArg("'Date' must be at midnight GMT")
		}
	}
	t = t.UTC()
	if t.Hour() != 0 || t.Minute() != 0 || t.Second() != 0 || t.Nanosecond() != 0 {
		return time.Time{}, invalidArg("'Date' must be at midnight GMT")
	}
	return t, nil
}

func validateExpiration(e *Expiration, hasTags bool) error {
	if e == nil {
		return nil
	}
	n := 0
	if e.Days != 0 {
		n++
	}
	if e.Date != "" {
		n++
	}
	if e.ExpiredObjectDeleteMarker != nil {
		n++
	}
	if n != 1 || e.Days < 0 {
		return malformed()
	}
	if e.Date != "" {
		if _, err := parseDate(e.Date); err != nil {
			return err
		}
	}
	if e.ExpiredObjectDeleteMarker != nil && *e.ExpiredObjectDeleteMarker && hasTags {
		return invalidReq("ExpiredObjectDeleteMarker cannot be specified with tags")
	}
	return nil
}

func validateTransitions(r *Rule) error {
	seen := map[string]bool{}
	byDays, byDate := false, false
	maxDays := -1
	var maxDate time.Time
	for _, t := range r.Transitions {
		if !ValidTransitionClass(t.StorageClass) {
			return malformed()
		}
		if seen[t.StorageClass] {
			return invalidReq("'StorageClass' must be different for 'Transition' actions in same 'Rule'")
		}
		seen[t.StorageClass] = true
		if t.Days < 0 {
			return malformed()
		}
		if t.Date != "" {
			d, err := parseDate(t.Date)
			if err != nil {
				return err
			}
			byDate = true
			if d.After(maxDate) {
				maxDate = d
			}
		} else {
			// Days (0 is allowed: transition at the next midnight).
			byDays = true
			if t.Days > maxDays {
				maxDays = t.Days
			}
		}
	}
	if byDays && byDate {
		return invalidReq("Found mixed 'Date' and 'Days' based Transition actions in lifecycle rule")
	}
	if e := r.Expiration; e != nil && len(r.Transitions) > 0 {
		switch {
		case e.Days != 0 && byDate, e.Date != "" && byDays:
			return invalidReq("Found mixed 'Date' and 'Days' based Expiration and Transition actions in lifecycle rule")
		case e.Days != 0 && e.Days <= maxDays:
			return invalidArg("'Days' in the Expiration action for filter must be greater than 'Days' in the Transition action")
		case e.Date != "":
			d, _ := parseDate(e.Date)
			if !d.After(maxDate) {
				return invalidArg("'Date' in the Expiration action for filter must be later than 'Date' in the Transition action")
			}
		}
	}
	return nil
}
