package lifecycle

import (
	"errors"
	"strings"
	"testing"

	"gitlab.com/Birdsall/opens3/internal/s3err"
)

func code(err error) s3err.Code {
	var e *s3err.Error
	if errors.As(err, &e) {
		return e.Code
	}
	return ""
}

func mustParse(t *testing.T, x string) *Config {
	t.Helper()
	c, err := Parse([]byte(x))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	return c
}

func rule(body string) string {
	return `<LifecycleConfiguration xmlns="http://s3.amazonaws.com/doc/2006-03-01/"><Rule>` + body + `</Rule></LifecycleConfiguration>`
}

func TestParseValid(t *testing.T) {
	c := mustParse(t, `<LifecycleConfiguration>
	<Rule><ID>a</ID><Status>Enabled</Status><Filter><And><Prefix>logs/</Prefix><Tag><Key>k</Key><Value>v</Value></Tag><ObjectSizeGreaterThan>10</ObjectSizeGreaterThan></And></Filter>
	  <Transition><Days>30</Days><StorageClass>STANDARD_IA</StorageClass></Transition>
	  <Transition><Days>90</Days><StorageClass>GLACIER</StorageClass></Transition>
	  <Expiration><Days>365</Days></Expiration>
	  <NoncurrentVersionTransition><NoncurrentDays>7</NoncurrentDays><StorageClass>GLACIER_IR</StorageClass></NoncurrentVersionTransition>
	  <NoncurrentVersionExpiration><NoncurrentDays>30</NoncurrentDays><NewerNoncurrentVersions>3</NewerNoncurrentVersions></NoncurrentVersionExpiration></Rule>
	<Rule><ID>b</ID><Status>Disabled</Status><Prefix>tmp/</Prefix><Expiration><Date>2030-01-01T00:00:00Z</Date></Expiration></Rule>
	<Rule><ID>c</ID><Status>Enabled</Status><Filter></Filter><Expiration><ExpiredObjectDeleteMarker>true</ExpiredObjectDeleteMarker></Expiration></Rule>
	<Rule><ID>d</ID><Status>Enabled</Status><Filter><ObjectSizeLessThan>5</ObjectSizeLessThan></Filter><AbortIncompleteMultipartUpload><DaysAfterInitiation>1</DaysAfterInitiation></AbortIncompleteMultipartUpload></Rule>
	</LifecycleConfiguration>`)
	if err := Validate(c); err != nil {
		t.Fatalf("validate: %v", err)
	}
	if len(c.Rules) != 4 || c.Rules[0].Filter.And.Tags[0].Key != "k" || *c.Rules[0].Filter.And.ObjectSizeGreaterThan != 10 {
		t.Fatalf("parsed: %+v", c.Rules[0])
	}
	if c.Rules[1].Prefix == nil || *c.Rules[1].Prefix != "tmp/" || c.Rules[2].Filter == nil || c.Rules[2].Filter.Prefix != nil {
		t.Fatalf("prefix/filter parse: %+v %+v", c.Rules[1], c.Rules[2])
	}
}

func TestParseMalformed(t *testing.T) {
	if _, err := Parse([]byte("<Lifecycle")); code(err) != s3err.MalformedXML {
		t.Fatalf("got %v", err)
	}
}

func TestValidateErrors(t *testing.T) {
	long := strings.Repeat("x", 256)
	cases := []struct {
		name string
		xml  string
		code s3err.Code
		msg  string
	}{
		{"no rules", `<LifecycleConfiguration></LifecycleConfiguration>`, s3err.MalformedXML, ""},
		{"bad status", rule(`<Status>On</Status><Prefix></Prefix><Expiration><Days>1</Days></Expiration>`), s3err.MalformedXML, ""},
		{"no prefix or filter", rule(`<Status>Enabled</Status><Expiration><Days>1</Days></Expiration>`), s3err.MalformedXML, ""},
		{"prefix and filter", rule(`<Status>Enabled</Status><Prefix>a</Prefix><Filter></Filter><Expiration><Days>1</Days></Expiration>`), s3err.MalformedXML, ""},
		{"no action", rule(`<Status>Enabled</Status><Filter></Filter>`), s3err.InvalidRequest, "At least one action"},
		{"days and date", rule(`<Status>Enabled</Status><Filter></Filter><Expiration><Days>1</Days><Date>2030-01-01T00:00:00Z</Date></Expiration>`), s3err.MalformedXML, ""},
		{"days and eodm", rule(`<Status>Enabled</Status><Filter></Filter><Expiration><Days>1</Days><ExpiredObjectDeleteMarker>true</ExpiredObjectDeleteMarker></Expiration>`), s3err.MalformedXML, ""},
		{"empty expiration", rule(`<Status>Enabled</Status><Filter></Filter><Expiration></Expiration>`), s3err.MalformedXML, ""},
		{"negative days", rule(`<Status>Enabled</Status><Filter></Filter><Expiration><Days>-1</Days></Expiration>`), s3err.MalformedXML, ""},
		{"date not midnight", rule(`<Status>Enabled</Status><Filter></Filter><Expiration><Date>2030-01-01T10:00:00Z</Date></Expiration>`), s3err.InvalidArgument, "midnight GMT"},
		{"date garbage", rule(`<Status>Enabled</Status><Filter></Filter><Expiration><Date>tomorrow</Date></Expiration>`), s3err.InvalidArgument, "midnight GMT"},
		{"eodm with tag", rule(`<Status>Enabled</Status><Filter><Tag><Key>a</Key><Value>b</Value></Tag></Filter><Expiration><ExpiredObjectDeleteMarker>true</ExpiredObjectDeleteMarker></Expiration>`), s3err.InvalidRequest, "ExpiredObjectDeleteMarker"},
		{"filter two elements", rule(`<Status>Enabled</Status><Filter><Prefix>a</Prefix><Tag><Key>a</Key><Value>b</Value></Tag></Filter><Expiration><Days>1</Days></Expiration>`), s3err.MalformedXML, ""},
		{"filter and plus prefix", rule(`<Status>Enabled</Status><Filter><Prefix>a</Prefix><And><Prefix>b</Prefix></And></Filter><Expiration><Days>1</Days></Expiration>`), s3err.MalformedXML, ""},
		{"empty and", rule(`<Status>Enabled</Status><Filter><And></And></Filter><Expiration><Days>1</Days></Expiration>`), s3err.MalformedXML, ""},
		{"empty tag key", rule(`<Status>Enabled</Status><Filter><Tag><Key></Key><Value>b</Value></Tag></Filter><Expiration><Days>1</Days></Expiration>`), s3err.InvalidArgument, ""},
		{"dup tag key", rule(`<Status>Enabled</Status><Filter><And><Tag><Key>a</Key><Value>b</Value></Tag><Tag><Key>a</Key><Value>c</Value></Tag></And></Filter><Expiration><Days>1</Days></Expiration>`), s3err.InvalidRequest, "Duplicate"},
		{"size bounds", rule(`<Status>Enabled</Status><Filter><And><ObjectSizeGreaterThan>10</ObjectSizeGreaterThan><ObjectSizeLessThan>5</ObjectSizeLessThan></And></Filter><Expiration><Days>1</Days></Expiration>`), s3err.InvalidArgument, "ObjectSizeGreaterThan"},
		{"negative size", rule(`<Status>Enabled</Status><Filter><ObjectSizeGreaterThan>-1</ObjectSizeGreaterThan></Filter><Expiration><Days>1</Days></Expiration>`), s3err.InvalidArgument, ""},
		{"bad storage class", rule(`<Status>Enabled</Status><Filter></Filter><Transition><Days>1</Days><StorageClass>COLD</StorageClass></Transition>`), s3err.MalformedXML, ""},
		{"transition to standard", rule(`<Status>Enabled</Status><Filter></Filter><Transition><Days>1</Days><StorageClass>STANDARD</StorageClass></Transition>`), s3err.MalformedXML, ""},
		{"dup transition class", rule(`<Status>Enabled</Status><Filter></Filter><Transition><Days>1</Days><StorageClass>GLACIER</StorageClass></Transition><Transition><Days>2</Days><StorageClass>GLACIER</StorageClass></Transition>`), s3err.InvalidRequest, "StorageClass"},
		{"mixed days date transitions", rule(`<Status>Enabled</Status><Filter></Filter><Transition><Days>1</Days><StorageClass>GLACIER</StorageClass></Transition><Transition><Date>2030-01-01T00:00:00Z</Date><StorageClass>DEEP_ARCHIVE</StorageClass></Transition>`), s3err.InvalidRequest, "mixed"},
		{"mixed expiration transition", rule(`<Status>Enabled</Status><Filter></Filter><Transition><Days>1</Days><StorageClass>GLACIER</StorageClass></Transition><Expiration><Date>2030-01-01T00:00:00Z</Date></Expiration>`), s3err.InvalidRequest, "mixed"},
		{"expiration before transition", rule(`<Status>Enabled</Status><Filter></Filter><Transition><Days>10</Days><StorageClass>GLACIER</StorageClass></Transition><Expiration><Days>5</Days></Expiration>`), s3err.InvalidArgument, "greater"},
		{"noncurrent days zero", rule(`<Status>Enabled</Status><Filter></Filter><NoncurrentVersionExpiration><NoncurrentDays>0</NoncurrentDays></NoncurrentVersionExpiration>`), s3err.InvalidArgument, "NoncurrentDays"},
		{"newer noncurrent too big", rule(`<Status>Enabled</Status><Filter></Filter><NoncurrentVersionExpiration><NoncurrentDays>1</NoncurrentDays><NewerNoncurrentVersions>101</NewerNoncurrentVersions></NoncurrentVersionExpiration>`), s3err.InvalidArgument, "NewerNoncurrentVersions"},
		{"noncurrent expiry before transition", rule(`<Status>Enabled</Status><Filter></Filter><NoncurrentVersionExpiration><NoncurrentDays>5</NoncurrentDays></NoncurrentVersionExpiration><NoncurrentVersionTransition><NoncurrentDays>5</NoncurrentDays><StorageClass>GLACIER</StorageClass></NoncurrentVersionTransition>`), s3err.InvalidArgument, "NoncurrentDays"},
		{"abort zero days", rule(`<Status>Enabled</Status><Filter></Filter><AbortIncompleteMultipartUpload><DaysAfterInitiation>0</DaysAfterInitiation></AbortIncompleteMultipartUpload>`), s3err.InvalidArgument, "DaysAfterInitiation"},
		{"abort with tags", rule(`<Status>Enabled</Status><Filter><Tag><Key>a</Key><Value>b</Value></Tag></Filter><AbortIncompleteMultipartUpload><DaysAfterInitiation>1</DaysAfterInitiation></AbortIncompleteMultipartUpload>`), s3err.InvalidRequest, "Tags"},
		{"long id", rule(`<ID>` + long + `</ID><Status>Enabled</Status><Filter></Filter><Expiration><Days>1</Days></Expiration>`), s3err.InvalidArgument, "255"},
		{"dup id", `<LifecycleConfiguration><Rule><ID>x</ID><Status>Enabled</Status><Filter></Filter><Expiration><Days>1</Days></Expiration></Rule><Rule><ID>x</ID><Status>Enabled</Status><Filter></Filter><Expiration><Days>2</Days></Expiration></Rule></LifecycleConfiguration>`, s3err.InvalidArgument, "unique"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := mustParse(t, tc.xml)
			err := Validate(c)
			if code(err) != tc.code {
				t.Fatalf("code = %v (%v), want %v", code(err), err, tc.code)
			}
			if tc.msg != "" && !strings.Contains(err.Error(), tc.msg) {
				t.Fatalf("message %q does not contain %q", err.Error(), tc.msg)
			}
		})
	}
}

func TestValidateTooManyRules(t *testing.T) {
	var sb strings.Builder
	sb.WriteString("<LifecycleConfiguration>")
	for i := 0; i <= MaxRules; i++ {
		sb.WriteString(`<Rule><Status>Enabled</Status><Filter></Filter><Expiration><Days>1</Days></Expiration></Rule>`)
	}
	sb.WriteString("</LifecycleConfiguration>")
	if err := Validate(mustParse(t, sb.String())); code(err) != s3err.MalformedXML {
		t.Fatalf("got %v", err)
	}
}
