package s3api

import (
	"crypto/hmac"
	"crypto/sha1"
	"crypto/subtle"
	"encoding/base64"
	"net/http"
	"sort"
	"strings"
)

// Sub-resources included in the SigV2 canonical resource.
var v2SubResources = []string{"acl", "cors", "delete", "encryption", "legal-hold", "lifecycle", "location", "logging", "notification", "object-lock",
	"partNumber", "policy", "publicAccessBlock", "requestPayment", "response-cache-control", "response-content-disposition",
	"response-content-encoding", "response-content-language", "response-content-type", "response-expires", "restore", "retention",
	"tagging", "torrent", "uploadId", "uploads", "versionId", "versioning", "versions", "website"}

// verifyV2 implements AWS Signature Version 2 for header and query auth.
func verifyV2(r *http.Request, bucket, key, secret, signature string, presigned bool) bool {
	var b strings.Builder
	b.WriteString(r.Method + "\n")
	b.WriteString(r.Header.Get("Content-MD5") + "\n")
	b.WriteString(r.Header.Get("Content-Type") + "\n")
	if presigned {
		b.WriteString(r.URL.Query().Get("Expires") + "\n")
	} else if r.Header.Get("x-amz-date") != "" {
		b.WriteString("\n")
	} else {
		b.WriteString(r.Header.Get("Date") + "\n")
	}
	// Canonicalised x-amz- headers.
	var amz []string
	for k := range r.Header {
		lk := strings.ToLower(k)
		if strings.HasPrefix(lk, "x-amz-") {
			amz = append(amz, lk+":"+strings.Join(r.Header.Values(k), ",")+"\n")
		}
	}
	sort.Strings(amz)
	for _, h := range amz {
		b.WriteString(h)
	}
	// Canonical resource: /bucket/key?subresources (path-style form).
	res := "/"
	if bucket != "" {
		res += bucket
		if key != "" || strings.HasSuffix(r.URL.Path, "/") || r.URL.Path == "/"+bucket {
			res += "/" + key
		}
	}
	q := r.URL.Query()
	var subs []string
	for _, sr := range v2SubResources {
		if v, ok := q[sr]; ok {
			if len(v) > 0 && v[0] != "" {
				subs = append(subs, sr+"="+v[0])
			} else {
				subs = append(subs, sr)
			}
		}
	}
	sort.Strings(subs)
	if len(subs) > 0 {
		res += "?" + strings.Join(subs, "&")
	}
	b.WriteString(res)
	mac := hmac.New(sha1.New, []byte(secret))
	mac.Write([]byte(b.String()))
	want := base64.StdEncoding.EncodeToString(mac.Sum(nil))
	return subtle.ConstantTimeCompare([]byte(want), []byte(signature)) == 1
}
