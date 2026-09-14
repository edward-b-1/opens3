package object

import (
	"encoding/json"

	"github.com/edward-b-1/OpenS3/internal/meta"
)

func encodeObject(o *meta.Object) []byte {
	b, err := json.Marshal(o)
	if err != nil {
		panic(err)
	}
	return b
}
