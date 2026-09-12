package object

import (
	"encoding/json"

	"gitlab.com/Birdsall/opens3/internal/meta"
)

func encodeObject(o *meta.Object) []byte {
	b, err := json.Marshal(o)
	if err != nil {
		panic(err)
	}
	return b
}
