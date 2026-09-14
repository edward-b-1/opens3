package policy

import "testing"

func TestMatch(t *testing.T) {
	cases := []struct {
		p, s string
		want bool
	}{
		{"s3:*", "s3:GetObject", true}, {"s3:Get*", "s3:GetObject", true}, {"s3:Get*", "s3:PutObject", false},
		{"arn:aws:s3:::b/*", "arn:aws:s3:::b/x/y", true}, {"arn:aws:s3:::b/*", "arn:aws:s3:::b", false},
		{"a?c", "abc", true}, {"a?c", "ac", false}, {"*", "", true}, {"a*b*c", "aXXbYYc", true}, {"a*b*c", "aXXbYY", false},
	}
	for _, c := range cases {
		if got := Match(c.p, c.s); got != c.want {
			t.Errorf("Match(%q,%q)=%v", c.p, c.s, got)
		}
	}
}

const bucketPolicy = `{
  "Version": "2012-10-17",
  "Statement": [
    {"Sid":"PublicRead","Effect":"Allow","Principal":"*","Action":["s3:GetObject"],"Resource":"arn:aws:s3:::pub/*"},
    {"Sid":"DenyOffice","Effect":"Deny","Principal":{"AWS":"*"},"Action":"s3:*","Resource":["arn:aws:s3:::pub","arn:aws:s3:::pub/*"],
     "Condition":{"NotIpAddress":{"aws:SourceIp":["10.0.0.0/8","192.168.1.5"]}}},
    {"Sid":"Home","Effect":"Allow","Principal":{"AWS":"arn:aws:iam::123456789012:user/alice"},"Action":"s3:*","Resource":"arn:aws:s3:::pub/home/${aws:username}/*"},
    {"Sid":"Tagged","Effect":"Allow","Principal":{"AWS":["arn:aws:iam::123456789012:user/bob"]},"Action":"s3:GetObject","Resource":"arn:aws:s3:::pub/*",
     "Condition":{"StringEquals":{"s3:ExistingObjectTag/team":"blue"},"Bool":{"aws:SecureTransport":"true"}}},
    {"Sid":"List","Effect":"Allow","Principal":"*","Action":"s3:ListBucket","Resource":"arn:aws:s3:::pub",
     "Condition":{"StringLike":{"s3:prefix":["public/*"]},"NumericLessThanEquals":{"s3:max-keys":"100"}}}
  ]}`

func TestEvaluate(t *testing.T) {
	d, err := Parse([]byte(bucketPolicy))
	if err != nil {
		t.Fatal(err)
	}
	if err := ValidateBucketPolicy(d, "pub"); err != nil {
		t.Fatal(err)
	}
	if err := ValidateBucketPolicy(d, "other"); err == nil {
		t.Fatal("expected resource mismatch")
	}
	if !d.IsPublic() {
		t.Fatal("policy should be public")
	}
	inside := map[string][]string{"aws:sourceip": {"10.1.2.3"}}
	outside := map[string][]string{"aws:sourceip": {"8.8.8.8"}}

	anon := Args{Action: "s3:GetObject", Resource: "arn:aws:s3:::pub/file", Anonymous: true, Conditions: inside}
	if d.Evaluate(anon) != Allowed {
		t.Fatal("anonymous read from office should be allowed")
	}
	anon.Conditions = outside
	if d.Evaluate(anon) != Denied {
		t.Fatal("outside office should be denied")
	}
	anon.Conditions = inside
	anon.Action = "s3:PutObject"
	if d.Evaluate(anon) != NoMatch {
		t.Fatal("anonymous put should not match")
	}

	alice := Args{Action: "s3:PutObject", Resource: "arn:aws:s3:::pub/home/alice/doc", PrincipalARN: "arn:aws:iam::123456789012:user/alice",
		Conditions: inside, Vars: map[string]string{"aws:username": "alice"}}
	if d.Evaluate(alice) != Allowed {
		t.Fatal("alice home dir")
	}
	alice.Resource = "arn:aws:s3:::pub/home/bob/doc"
	if d.Evaluate(alice) != NoMatch {
		t.Fatal("alice must not write to bob's home")
	}

	bob := Args{Action: "s3:getobject", Resource: "arn:aws:s3:::pub/x", PrincipalARN: "arn:aws:iam::123456789012:user/bob",
		Conditions: map[string][]string{"aws:sourceip": {"10.0.0.1"}, "s3:existingobjecttag/team": {"blue"}, "aws:securetransport": {"true"}}}
	if d.Evaluate(bob) != Allowed {
		t.Fatal("bob tagged read (case-insensitive action)")
	}
	bob.Conditions["aws:securetransport"] = []string{"false"}
	// Falls back to the public-read statement.
	if d.Evaluate(bob) != Allowed {
		t.Fatal("public read still applies")
	}
	bob.Action = "s3:GetObjectVersion"
	if d.Evaluate(bob) != NoMatch {
		t.Fatal("no statement for GetObjectVersion over http")
	}

	list := Args{Action: "s3:ListBucket", Resource: "arn:aws:s3:::pub", Anonymous: true,
		Conditions: map[string][]string{"aws:sourceip": {"10.0.0.1"}, "s3:prefix": {"public/2024"}, "s3:max-keys": {"50"}}}
	if d.Evaluate(list) != Allowed {
		t.Fatal("list with prefix")
	}
	list.Conditions["s3:prefix"] = []string{"private/"}
	if d.Evaluate(list) != NoMatch {
		t.Fatal("list wrong prefix")
	}
	delete(list.Conditions, "s3:prefix")
	if d.Evaluate(list) != NoMatch {
		t.Fatal("missing prefix key must not match StringLike")
	}
}

func TestConditionOperators(t *testing.T) {
	p := `{"Statement":{"Effect":"Allow","Action":"s3:*","Resource":"*","Condition":{
		"ForAllValues:StringEquals":{"aws:TagKeys":["a","b"]},
		"StringEqualsIfExists":{"s3:x-amz-acl":"private"},
		"DateGreaterThan":{"aws:CurrentTime":"2020-01-01T00:00:00Z"},
		"Null":{"s3:versionid":"true"}}}}`
	d, err := Parse([]byte(p))
	if err != nil {
		t.Fatal(err)
	}
	ok := Args{Action: "s3:PutObject", Resource: "x", Conditions: map[string][]string{"aws:tagkeys": {"a"}, "aws:currenttime": {"2024-05-05T00:00:00Z"}}}
	if d.Evaluate(ok) != Allowed {
		t.Fatal("should allow")
	}
	ok.Conditions["aws:tagkeys"] = []string{"a", "c"}
	if d.Evaluate(ok) != NoMatch {
		t.Fatal("ForAllValues should fail on c")
	}
	ok.Conditions["aws:tagkeys"] = []string{"b"}
	ok.Conditions["s3:x-amz-acl"] = []string{"public-read"}
	if d.Evaluate(ok) != NoMatch {
		t.Fatal("IfExists with wrong value should fail")
	}
	delete(ok.Conditions, "s3:x-amz-acl")
	ok.Conditions["s3:versionid"] = []string{"v1"}
	if d.Evaluate(ok) != NoMatch {
		t.Fatal("Null:true with key present should fail")
	}
}

func TestParseErrors(t *testing.T) {
	bad := []string{`{}`, `{"Statement":[{"Effect":"Maybe","Action":"s3:*","Resource":"*"}]}`,
		`{"Statement":[{"Effect":"Allow","Resource":"*"}]}`, `{"Statement":[{"Effect":"Allow","Action":"s3:*","Resource":"*","Condition":{"Bogus":{"k":"v"}}}]}`, `not json`}
	for _, b := range bad {
		if _, err := Parse([]byte(b)); err == nil {
			t.Errorf("expected error for %s", b)
		}
	}
}

func TestSubstituteVars(t *testing.T) {
	got := SubstituteVars("home/${aws:username}/${*}x${unknown}", map[string]string{"aws:username": "al"})
	if got != "home/al/*x${unknown}" {
		t.Fatalf("got %q", got)
	}
}

func TestNegatedOperatorsMatchWhenKeyAbsent(t *testing.T) {
	// "Deny unless the encryption header is AES256" must fire when the
	// header is missing altogether (AWS semantics for negated operators).
	d, err := Parse([]byte(`{"Statement":[{"Effect":"Deny","Action":"s3:PutObject","Resource":"*",
		"Condition":{"StringNotEquals":{"s3:x-amz-server-side-encryption":"AES256"}}}]}`))
	if err != nil {
		t.Fatal(err)
	}
	if d.Evaluate(Args{Action: "s3:PutObject", Resource: "x", Conditions: map[string][]string{}}) != Denied {
		t.Fatal("missing key must match StringNotEquals")
	}
	if d.Evaluate(Args{Action: "s3:PutObject", Resource: "x", Conditions: map[string][]string{"s3:x-amz-server-side-encryption": {"AES256"}}}) != NoMatch {
		t.Fatal("matching value must not be denied")
	}
	if d.Evaluate(Args{Action: "s3:PutObject", Resource: "x", Conditions: map[string][]string{"s3:x-amz-server-side-encryption": {"aws:kms"}}}) != Denied {
		t.Fatal("other value must be denied")
	}
	// Positive operators still need the key; NotIpAddress and ArnNotLike
	// behave like StringNotEquals.
	for _, tc := range []struct {
		cond string
		want Decision
	}{
		{`{"StringEquals":{"aws:sourceip":"10.0.0.1"}}`, NoMatch},
		{`{"NotIpAddress":{"aws:sourceip":"10.0.0.0/8"}}`, Allowed},
		{`{"ArnNotLike":{"aws:principalarn":"arn:aws:iam::*:user/admin"}}`, Allowed},
		{`{"Bool":{"aws:securetransport":"false"}}`, NoMatch},
		{`{"ForAnyValue:StringNotEquals":{"aws:tagkeys":"x"}}`, NoMatch},
	} {
		d, err := Parse([]byte(`{"Statement":[{"Effect":"Allow","Action":"s3:*","Resource":"*","Condition":` + tc.cond + `}]}`))
		if err != nil {
			t.Fatal(err)
		}
		if got := d.Evaluate(Args{Action: "s3:GetObject", Resource: "x", Conditions: map[string][]string{}}); got != tc.want {
			t.Errorf("%s with key absent: %v, want %v", tc.cond, got, tc.want)
		}
	}
}

func TestResourceARNKeepsKeyVerbatim(t *testing.T) {
	for key, want := range map[string]string{
		"a/b": "arn:aws:s3:::b/a/b", "private/../public/x": "arn:aws:s3:::b/private/../public/x",
		"dir/": "arn:aws:s3:::b/dir/", "a//b": "arn:aws:s3:::b/a//b", "./x": "arn:aws:s3:::b/./x",
	} {
		if got := ResourceARN("b", key); got != want {
			t.Errorf("ResourceARN(%q) = %q, want %q", key, got, want)
		}
	}
	if ResourceARN("b", "") != "arn:aws:s3:::b" {
		t.Fatal("bucket ARN")
	}
	// A prefix policy on public/* must not cover a key that only
	// normalises to public/.
	d, _ := Parse([]byte(`{"Statement":[{"Effect":"Allow","Action":"s3:GetObject","Resource":"arn:aws:s3:::b/public/*"}]}`))
	if d.Evaluate(Args{Action: "s3:GetObject", Resource: ResourceARN("b", "private/../public/x")}) != NoMatch {
		t.Fatal("dot-dot key authorised as public")
	}
}
