package integration

import (
	"crypto/md5"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"net/http"
	"time"

	"github.com/edward-b-1/opens3/internal/auth/sigv4"
)

func b64(b []byte) string { return base64.StdEncoding.EncodeToString(b) }

func b64md5(b []byte) string {
	s := md5.Sum(b)
	return base64.StdEncoding.EncodeToString(s[:])
}

func signRequest(r *http.Request, ak, sk string, body []byte) {
	sum := sha256.Sum256(body)
	sigv4.Sign(r, ak, sk, "us-east-1", time.Now(), hex.EncodeToString(sum[:]))
}
