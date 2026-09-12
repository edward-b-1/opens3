package sigv4

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"hash/crc32"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

const (
	testAccess = "AKIAIOSFODNN7EXAMPLE"
	testSecret = "wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY"
)

func lookup(ak string) (string, error) {
	if ak == testAccess {
		return testSecret, nil
	}
	return "", ErrUnknownAccessKey
}

// Example from the AWS SigV4 documentation ("GET Object" example).
func TestAWSDocExampleGet(t *testing.T) {
	r := httptest.NewRequest("GET", "http://examplebucket.s3.amazonaws.com/test.txt", nil)
	r.Host = "examplebucket.s3.amazonaws.com"
	r.Header.Set("Range", "bytes=0-9")
	r.Header.Set("x-amz-content-sha256", EmptySHA256)
	r.Header.Set("x-amz-date", "20130524T000000Z")
	r.Header.Set("Authorization", "AWS4-HMAC-SHA256 Credential=AKIAIOSFODNN7EXAMPLE/20130524/us-east-1/s3/aws4_request,SignedHeaders=host;range;x-amz-content-sha256;x-amz-date,Signature=f0e8bdb87c964420e857bd35b5d6ed310bd44f0170aba48dd91039c6036bdb41")
	p, err := ParseRequest(r)
	if err != nil {
		t.Fatal(err)
	}
	now := func() time.Time { return time.Date(2013, 5, 24, 0, 0, 5, 0, time.UTC) }
	if _, err := Verify(r, p, lookup, Options{Now: now}); err != nil {
		t.Fatalf("verify: %v\ncanonical:\n%s", err, CanonicalRequest(r, p))
	}
	// Tamper.
	r.Header.Set("Range", "bytes=0-10")
	if _, err := Verify(r, p, lookup, Options{Now: now}); err != ErrSignatureMismatch {
		t.Fatalf("expected mismatch, got %v", err)
	}
}

// AWS doc example: presigned GET.
func TestAWSDocExamplePresigned(t *testing.T) {
	u := "http://examplebucket.s3.amazonaws.com/test.txt?X-Amz-Algorithm=AWS4-HMAC-SHA256&X-Amz-Credential=AKIAIOSFODNN7EXAMPLE%2F20130524%2Fus-east-1%2Fs3%2Faws4_request&X-Amz-Date=20130524T000000Z&X-Amz-Expires=86400&X-Amz-SignedHeaders=host&X-Amz-Signature=aeeed9bbccd4d02ee5c0109b86d86835f995330da4c265957d157751f604d404"
	r := httptest.NewRequest("GET", u, nil)
	r.Host = "examplebucket.s3.amazonaws.com"
	p, err := ParseRequest(r)
	if err != nil {
		t.Fatal(err)
	}
	if !p.Presigned || p.Expires != 86400*time.Second {
		t.Fatalf("parsed: %+v", p)
	}
	now := func() time.Time { return time.Date(2013, 5, 24, 1, 0, 0, 0, time.UTC) }
	if _, err := Verify(r, p, lookup, Options{Now: now}); err != nil {
		t.Fatalf("verify: %v\n%s", err, CanonicalRequest(r, p))
	}
	late := func() time.Time { return time.Date(2013, 5, 26, 1, 0, 0, 0, time.UTC) }
	if _, err := Verify(r, p, lookup, Options{Now: late}); err != ErrExpired {
		t.Fatalf("expected expired, got %v", err)
	}
}

func TestSignAndVerifyRoundTrip(t *testing.T) {
	body := []byte("hello")
	sum := sha256.Sum256(body)
	r := httptest.NewRequest("PUT", "http://localhost:9000/my-bucket/some%20key+with%2Bplus/%C3%A9?tagging=&versionId=abc", bytes.NewReader(body))
	r.Host = "localhost:9000"
	r.Header.Set("Content-Type", "text/plain")
	r.Header.Set("X-Amz-Meta-Foo", "  bar   baz ")
	now := time.Now()
	Sign(r, testAccess, testSecret, "us-east-1", now, hex.EncodeToString(sum[:]))
	p, err := ParseRequest(r)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Verify(r, p, lookup, Options{}); err != nil {
		t.Fatalf("verify: %v\n%s", err, CanonicalRequest(r, p))
	}
	if _, err := Verify(r, p, lookup, Options{Region: "eu-west-1"}); err != ErrBadRegion {
		t.Fatalf("region check: %v", err)
	}
	skew := func() time.Time { return now.Add(20 * time.Minute) }
	if _, err := Verify(r, p, lookup, Options{Now: skew}); err != ErrTimeSkew {
		t.Fatalf("skew: %v", err)
	}
}

func TestPresignRoundTrip(t *testing.T) {
	r := httptest.NewRequest("GET", "http://localhost:9000/b/k?response-content-type=text%2Fplain", nil)
	r.Host = "localhost:9000"
	Presign(r, testAccess, testSecret, "us-east-1", time.Now(), time.Hour)
	p, err := ParseRequest(r)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Verify(r, p, lookup, Options{}); err != nil {
		t.Fatalf("verify: %v", err)
	}
}

func TestUnknownKey(t *testing.T) {
	r := httptest.NewRequest("GET", "http://localhost:9000/", nil)
	Sign(r, "nope", "x", "us-east-1", time.Now(), "")
	p, _ := ParseRequest(r)
	if _, err := Verify(r, p, lookup, Options{}); err != ErrUnknownAccessKey {
		t.Fatalf("got %v", err)
	}
}

func TestEncodePath(t *testing.T) {
	if got := EncodePath("/a b/é+~"); got != "/a%20b/%C3%A9%2B~" {
		t.Fatalf("got %q", got)
	}
}

// Build a signed aws-chunked body the way the SDKs do.
func buildSignedChunks(t *testing.T, key []byte, seed string, date time.Time, scope string, chunks [][]byte, trailer map[string]string) []byte {
	var out bytes.Buffer
	prev := seed
	sign := func(data []byte) string {
		h := sha256.Sum256(data)
		sts := Algorithm + "-PAYLOAD\n" + date.Format(timeFormat) + "\n" + scope + "\n" + prev + "\n" + EmptySHA256 + "\n" + hex.EncodeToString(h[:])
		return hex.EncodeToString(hmacSHA256(key, []byte(sts)))
	}
	for _, c := range chunks {
		sig := sign(c)
		out.WriteString(strings.ToLower(hex.EncodeToString([]byte{})))
		out.WriteString(strconvHex(len(c)) + ";chunk-signature=" + sig + "\r\n")
		out.Write(c)
		out.WriteString("\r\n")
		prev = sig
	}
	sig := sign(nil)
	out.WriteString("0;chunk-signature=" + sig + "\r\n")
	prev = sig
	if trailer != nil {
		var canon strings.Builder
		for k, v := range trailer {
			out.WriteString(k + ":" + v + "\r\n")
			canon.WriteString(k + ":" + v + "\n")
		}
		sts := Algorithm + "-TRAILER\n" + date.Format(timeFormat) + "\n" + scope + "\n" + prev + "\n" + hexSHA256([]byte(canon.String()))
		out.WriteString("x-amz-trailer-signature:" + hex.EncodeToString(hmacSHA256(key, []byte(sts))) + "\r\n")
	}
	out.WriteString("\r\n")
	return out.Bytes()
}

func strconvHex(n int) string {
	return strings.TrimLeft(hex.EncodeToString([]byte{byte(n >> 8), byte(n)}), "0")
}

func TestChunkedSigned(t *testing.T) {
	date := time.Date(2024, 1, 2, 3, 4, 5, 0, time.UTC)
	scope := "20240102/us-east-1/s3/aws4_request"
	key := SigningKey(testSecret, "20240102", "us-east-1", "s3")
	seed := "abc123"
	chunks := [][]byte{bytes.Repeat([]byte("a"), 300), []byte("tail")}
	body := buildSignedChunks(t, key, seed, date, scope, chunks, nil)
	cr, err := NewChunkedReader(bytes.NewReader(body), StreamingSigned, seed, key, date, scope, "")
	if err != nil {
		t.Fatal(err)
	}
	got, err := io.ReadAll(cr)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != strings.Repeat("a", 300)+"tail" {
		t.Fatalf("decoded %d bytes", len(got))
	}
	// Corrupt a byte.
	bad := bytes.Replace(body, []byte("aaaa"), []byte("aaab"), 1)
	cr, _ = NewChunkedReader(bytes.NewReader(bad), StreamingSigned, seed, key, date, scope, "")
	if _, err := io.ReadAll(cr); err != ErrChunkSignature {
		t.Fatalf("expected chunk signature error, got %v", err)
	}
}

func TestChunkedSignedTrailer(t *testing.T) {
	date := time.Date(2024, 1, 2, 3, 4, 5, 0, time.UTC)
	scope := "20240102/us-east-1/s3/aws4_request"
	key := SigningKey(testSecret, "20240102", "us-east-1", "s3")
	data := []byte("hello trailer")
	crc := crc32.ChecksumIEEE(data)
	sum := base64.StdEncoding.EncodeToString([]byte{byte(crc >> 24), byte(crc >> 16), byte(crc >> 8), byte(crc)})
	body := buildSignedChunks(t, key, "seed", date, scope, [][]byte{data}, map[string]string{"x-amz-checksum-crc32": sum})
	cr, err := NewChunkedReader(bytes.NewReader(body), StreamingSignedTrailer, "seed", key, date, scope, "x-amz-checksum-crc32")
	if err != nil {
		t.Fatal(err)
	}
	got, err := io.ReadAll(cr)
	if err != nil || string(got) != string(data) {
		t.Fatalf("read: %v %q", err, got)
	}
	if cr.Trailer().Get("x-amz-checksum-crc32") != sum {
		t.Fatalf("trailer: %v", cr.Trailer())
	}
}

func TestChunkedUnsignedTrailer(t *testing.T) {
	body := "5\r\nhello\r\n6\r\n world\r\n0\r\nx-amz-checksum-sha256:abc=\r\n\r\n"
	cr, err := NewChunkedReader(strings.NewReader(body), StreamingUnsignedTrailer, "", nil, time.Time{}, "", "x-amz-checksum-sha256")
	if err != nil {
		t.Fatal(err)
	}
	got, err := io.ReadAll(cr)
	if err != nil || string(got) != "hello world" {
		t.Fatalf("read: %v %q", err, got)
	}
	if cr.Trailer().Get("x-amz-checksum-sha256") != "abc=" {
		t.Fatal("trailer missing")
	}
	// Missing announced trailer.
	cr, _ = NewChunkedReader(strings.NewReader("0\r\n\r\n"), StreamingUnsignedTrailer, "", nil, time.Time{}, "", "x-amz-checksum-sha256")
	if _, err := io.ReadAll(cr); err != ErrTrailerMissing {
		t.Fatalf("expected trailer missing, got %v", err)
	}
}

var _ = http.MethodGet
