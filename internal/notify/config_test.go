package notify

import (
	"errors"
	"testing"
)

const testXML = `<NotificationConfiguration xmlns="http://s3.amazonaws.com/doc/2006-03-01/">
  <QueueConfiguration>
    <Id>all-created</Id>
    <Queue>arn:opens3:sqs::hook:webhook</Queue>
    <Event>s3:ObjectCreated:*</Event>
  </QueueConfiguration>
  <TopicConfiguration>
    <Id>jpg-deletes</Id>
    <Topic>arn:aws:sns:us-east-1:123456789012:hook</Topic>
    <Event>s3:ObjectRemoved:Delete</Event>
    <Filter><S3Key>
      <FilterRule><Name>prefix</Name><Value>photos/</Value></FilterRule>
      <FilterRule><Name>Suffix</Name><Value>.jpg</Value></FilterRule>
    </S3Key></Filter>
  </TopicConfiguration>
  <CloudFunctionConfiguration>
    <Id>wild</Id>
    <CloudFunction>arn:aws:lambda:us-east-1:123456789012:function:hook</CloudFunction>
    <Event>s3:ObjectCreated:Put</Event>
    <Filter><S3Key>
      <FilterRule><Name>suffix</Name><Value>*.tar.??</Value></FilterRule>
    </S3Key></Filter>
  </CloudFunctionConfiguration>
</NotificationConfiguration>`

func ids(rules []Rule) []string {
	var out []string
	for _, r := range rules {
		out = append(out, r.ID)
	}
	return out
}

func eq(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func TestMatcher(t *testing.T) {
	cfg, err := ParseConfiguration([]byte(testXML))
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Rules) != 3 || cfg.Rules[0].ID != "jpg-deletes" || cfg.Rules[1].ID != "all-created" {
		t.Fatalf("rules: %+v", cfg.Rules)
	}
	cases := []struct {
		event, key string
		want       []string
	}{
		{"s3:ObjectCreated:Put", "a.txt", []string{"all-created"}},
		{"s3:ObjectCreated:CompleteMultipartUpload", "photos/x.jpg", []string{"all-created"}},
		{"s3:ObjectCreated:Put", "dir/x.tar.gz", []string{"all-created", "wild"}},
		{"s3:ObjectCreated:Copy", "dir/x.tar.gz", []string{"all-created"}},
		{"s3:ObjectCreated:Put", "x.tar.gzip", []string{"all-created"}},
		{"s3:ObjectRemoved:Delete", "photos/x.jpg", []string{"jpg-deletes"}},
		{"s3:ObjectRemoved:Delete", "photos/x.png", nil},
		{"s3:ObjectRemoved:Delete", "other/x.jpg", nil},
		{"s3:ObjectRemoved:DeleteMarkerCreated", "photos/x.jpg", nil},
		{"s3:ObjectTagging:Put", "photos/x.jpg", nil},
	}
	for _, c := range cases {
		got := ids(cfg.Match(c.event, c.key))
		if !eq(got, c.want) {
			t.Errorf("%s %s: got %v want %v", c.event, c.key, got, c.want)
		}
	}
	if !EventMatches("ObjectCreated:*", "s3:ObjectCreated:Put") || EventMatches("s3:ObjectCreated:Put", "s3:ObjectCreated:Post") {
		t.Fatal("event pattern")
	}
	for _, c := range []struct {
		p, s string
		ok   bool
	}{{"*.jpg", "a/b.jpg", true}, {"a?c", "abc", true}, {"a?c", "abbc", false}, {"a*", "", false}, {"*", "", true}, {"x*y*z", "xaybz", true}, {"x*y*z", "xz", false}} {
		if wildMatch(c.p, c.s) != c.ok {
			t.Errorf("wildMatch(%q,%q) != %v", c.p, c.s, c.ok)
		}
	}
}

func TestParseErrors(t *testing.T) {
	bad := []string{
		`<NotificationConfiguration><QueueConfiguration><Queue>arn:opens3:sqs::a:webhook</Queue><Event>s3:ObjectCreated:*</Event>
		<Filter><S3Key><FilterRule><Name>prefix</Name><Value>a</Value></FilterRule><FilterRule><Name>prefix</Name><Value>b</Value></FilterRule></S3Key></Filter></QueueConfiguration></NotificationConfiguration>`,
		`<NotificationConfiguration><QueueConfiguration><Queue>arn:opens3:sqs::a:webhook</Queue><Event>s3:ObjectCreated:*</Event>
		<Filter><S3Key><FilterRule><Name>regex</Name><Value>a</Value></FilterRule></S3Key></Filter></QueueConfiguration></NotificationConfiguration>`,
		`<Nope>`,
	}
	for _, x := range bad {
		if _, err := ParseConfiguration([]byte(x)); err == nil {
			t.Errorf("expected error for %s", x)
		}
	}
	cfg, _ := ParseConfiguration([]byte(testXML))
	resolve := func(arn string) error {
		if a, err := ParseARN(arn); err != nil || a.Name != "hook" {
			return ErrUnknownTarget
		}
		return nil
	}
	if err := Validate(cfg, resolve); err != nil {
		t.Fatal(err)
	}
	cfg.Rules[0].ARN = "arn:opens3:sqs::other:webhook"
	if err := Validate(cfg, resolve); !errors.Is(err, ErrUnknownTarget) {
		t.Fatalf("want unknown target, got %v", err)
	}
	cfg.Rules[0].ARN = "arn:opens3:sqs::hook:webhook"
	cfg.Rules[0].Events = []string{"s3:Bogus"}
	if err := Validate(cfg, resolve); err == nil {
		t.Fatal("want bad event error")
	}
	empty, err := ParseConfiguration(nil)
	if err != nil || len(empty.Rules) != 0 {
		t.Fatal("empty config")
	}
}

func TestParseARN(t *testing.T) {
	good := map[string]ARN{
		"arn:opens3:sqs::hook:webhook":                            {Partition: "opens3", Service: "sqs", Name: "hook", Type: "webhook"},
		"arn:minio:sqs:us-east-1:PRIMARY:kafka":                   {Partition: "minio", Service: "sqs", Region: "us-east-1", Name: "PRIMARY", Type: "kafka"},
		"arn:aws:sqs:eu-west-1:123456789012:q1":                   {Partition: "aws", Service: "sqs", Region: "eu-west-1", Name: "q1"},
		"arn:aws:sns:eu-west-1:123456789012:t1":                   {Partition: "aws", Service: "sns", Region: "eu-west-1", Name: "t1"},
		"arn:aws:lambda:eu-west-1:123456789012:function:fn":       {Partition: "aws", Service: "lambda", Region: "eu-west-1", Name: "fn"},
		"arn:aws:lambda:eu-west-1:123456789012:function:fn:alias": {Partition: "aws", Service: "lambda", Region: "eu-west-1", Name: "fn"},
	}
	for s, want := range good {
		got, err := ParseARN(s)
		if err != nil || got != want {
			t.Errorf("%s: got %+v (%v) want %+v", s, got, err, want)
		}
	}
	for _, s := range []string{"", "hook", "arn:opens3:sqs::hook", "arn:opens3:sns::hook:webhook", "arn:opens3:sqs:::webhook", "arn:aws:s3:::bucket", "arn:gcp:sqs::a:b", "arn:aws:lambda:r:a:fn"} {
		if _, err := ParseARN(s); err == nil {
			t.Errorf("%q: expected error", s)
		}
	}
}

func TestParseEnv(t *testing.T) {
	cfg, err := ParseEnv([]string{
		"OPENS3_NOTIFY_WEBHOOK_MY_HOOK_ENDPOINT=http://h/x",
		"OPENS3_NOTIFY_WEBHOOK_MY_HOOK_AUTH_TOKEN=tok",
		"OPENS3_NOTIFY_WEBHOOK_MY_HOOK_QUEUE_DIR=/q",
		"OPENS3_NOTIFY_WEBHOOK_MY_HOOK_QUEUE_LIMIT=42",
		"OPENS3_NOTIFY_WEBHOOK_B_ENDPOINT=http://h/b",
		"OPENS3_ROOT_USER=x",
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Targets) != 2 || cfg.Targets[0].Name != "B" || cfg.Targets[1] != (TargetConfig{Name: "MY_HOOK", Type: "webhook", Endpoint: "http://h/x", AuthToken: "tok", QueueDir: "/q", QueueLimit: 42}) {
		t.Fatalf("targets: %+v", cfg.Targets)
	}
	for _, bad := range [][]string{
		{"OPENS3_NOTIFY_WEBHOOK_A_AUTH_TOKEN=t"},
		{"OPENS3_NOTIFY_WEBHOOK_A_ENDPOINT=http://h", "OPENS3_NOTIFY_WEBHOOK_A_QUEUE_LIMIT=x"},
		{"OPENS3_NOTIFY_KAFKA_A_ENDPOINT=http://h"},
		{"OPENS3_NOTIFY_WEBHOOK_A_BOGUS=1"},
	} {
		if _, err := ParseEnv(bad); err == nil {
			t.Errorf("expected error for %v", bad)
		}
	}
}
