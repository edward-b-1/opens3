package checksum

import (
	"bytes"
	"encoding/hex"
	"testing"
)

func sum(t *testing.T, alg string, data []byte) []byte {
	h, err := New(alg)
	if err != nil {
		t.Fatal(err)
	}
	h.Write(data)
	return h.Sum(nil)
}

func TestKnownVectors(t *testing.T) {
	data := []byte("123456789")
	cases := map[string]string{
		CRC32:     "cbf43926",
		CRC32C:    "e3069283",
		SHA1:      "f7c3bc1d808e04732adf679965ccc34ca7ae3441",
		SHA256:    "15e2b0d3c33891ebb0f1ef609ec419420c20e320ce94c65fbc8c3312448eb225",
		CRC64NVME: "ae8b14860a799888",
	}
	for alg, want := range cases {
		if got := hex.EncodeToString(sum(t, alg, data)); got != want {
			t.Errorf("%s: got %s want %s", alg, got, want)
		}
	}
}

func TestCombine(t *testing.T) {
	a := bytes.Repeat([]byte("abc"), 1000)
	b := bytes.Repeat([]byte("xyz"), 777)
	c := []byte("tail")
	for _, alg := range []string{CRC32, CRC32C, CRC64NVME} {
		whole := sum(t, alg, append(append(append([]byte{}, a...), b...), c...))
		parts := [][]byte{sum(t, alg, a), sum(t, alg, b), sum(t, alg, c)}
		got, err := CombineCRC(alg, parts, []int64{int64(len(a)), int64(len(b)), int64(len(c))})
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(got, whole) {
			t.Errorf("%s: combine %x want %x", alg, got, whole)
		}
	}
	comp, _ := CompositeOf(SHA256, [][]byte{sum(t, SHA256, a), sum(t, SHA256, b)})
	if len(comp) != 32 {
		t.Fatal("composite length")
	}
}

func TestDecode(t *testing.T) {
	if _, err := Decode(CRC32, "AAAA"); err != nil { // 3 bytes → wrong
		// "AAAA" decodes to 3 bytes; must fail
	} else {
		t.Fatal("expected length error")
	}
	if _, err := Decode(CRC32, Encode([]byte{1, 2, 3, 4})); err != nil {
		t.Fatal(err)
	}
}
