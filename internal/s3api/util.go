package s3api

import (
	"encoding/base64"
	"sort"
)

func base64URL(b []byte) string                { return base64.URLEncoding.EncodeToString(b) }
func base64URLDecode(s string) ([]byte, error) { return base64.URLEncoding.DecodeString(s) }
func sortStrings(l []string)                   { sort.Strings(l) }
