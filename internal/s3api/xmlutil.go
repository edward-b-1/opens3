package s3api

import "encoding/xml"

func xmlUnmarshal(b []byte, v any) error { return xml.Unmarshal(b, v) }
