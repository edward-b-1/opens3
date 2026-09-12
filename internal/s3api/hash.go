package s3api

import (
	"crypto/md5"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
)

func sha256Hex(b []byte) string {
	s := sha256.Sum256(b)
	return hex.EncodeToString(s[:])
}

func md5Hex(b []byte) string {
	s := md5.Sum(b)
	return hex.EncodeToString(s[:])
}

func jsonUnmarshal(b []byte, v any) error { return json.Unmarshal(b, v) }
