// Package checksum implements the S3 additional checksum algorithms
// (CRC32, CRC32C, SHA1, SHA256, CRC64NVME) including the composite
// (checksum-of-checksums) form used for multipart uploads.
package checksum

import (
	"crypto/sha1"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"hash"
	"hash/crc32"
	"hash/crc64"
	"strings"
)

// Algorithm names as used in x-amz-checksum-algorithm.
const (
	CRC32     = "CRC32"
	CRC32C    = "CRC32C"
	SHA1      = "SHA1"
	SHA256    = "SHA256"
	CRC64NVME = "CRC64NVME"
)

// Checksum types.
const (
	FullObject = "FULL_OBJECT"
	Composite  = "COMPOSITE"
)

// ErrUnknownAlgorithm is returned for unsupported algorithm names.
var ErrUnknownAlgorithm = errors.New("checksum: unknown algorithm")

// Reflected form of the NVMe CRC-64 polynomial 0xAD93D23594C93659.
const crc64NVMEPoly = 0x9A6C9329AC4BC9B5

var (
	crc32cTable    = crc32.MakeTable(crc32.Castagnoli)
	crc64NVMETable = crc64.MakeTable(crc64NVMEPoly)
)

// Normalize returns the canonical algorithm name or "" if unknown.
func Normalize(alg string) string {
	switch strings.ToUpper(alg) {
	case CRC32, CRC32C, SHA1, SHA256, CRC64NVME:
		return strings.ToUpper(alg)
	}
	return ""
}

// HeaderName returns the x-amz-checksum-* header for alg.
func HeaderName(alg string) string { return "x-amz-checksum-" + strings.ToLower(alg) }

// New returns a hash for the algorithm.
func New(alg string) (hash.Hash, error) {
	switch Normalize(alg) {
	case CRC32:
		return crc32.NewIEEE(), nil
	case CRC32C:
		return crc32.New(crc32cTable), nil
	case SHA1:
		return sha1.New(), nil
	case SHA256:
		return sha256.New(), nil
	case CRC64NVME:
		return crc64.New(crc64NVMETable), nil
	}
	return nil, ErrUnknownAlgorithm
}

// Encode base64-encodes a checksum sum.
func Encode(sum []byte) string { return base64.StdEncoding.EncodeToString(sum) }

// Decode base64-decodes a checksum header value and validates its length
// for the algorithm.
func Decode(alg, v string) ([]byte, error) {
	b, err := base64.StdEncoding.DecodeString(v)
	if err != nil {
		return nil, err
	}
	want := Size(alg)
	if want == 0 || len(b) != want {
		return nil, errors.New("checksum: wrong length")
	}
	return b, nil
}

// Size returns the digest length in bytes.
func Size(alg string) int {
	switch Normalize(alg) {
	case CRC32, CRC32C:
		return 4
	case SHA1:
		return 20
	case SHA256:
		return 32
	case CRC64NVME:
		return 8
	}
	return 0
}

// SupportsFullObjectMultipart reports whether alg can be linearly combined
// across parts (only the CRCs can).
func SupportsFullObjectMultipart(alg string) bool {
	switch Normalize(alg) {
	case CRC32, CRC32C, CRC64NVME:
		return true
	}
	return false
}

// CompositeOf computes the composite checksum: the checksum over the
// concatenated raw part checksums.
func CompositeOf(alg string, partSums [][]byte) ([]byte, error) {
	h, err := New(alg)
	if err != nil {
		return nil, err
	}
	for _, p := range partSums {
		h.Write(p)
	}
	return h.Sum(nil), nil
}

// CombineCRC combines part CRCs into the CRC of the concatenation
// (full-object checksum for multipart uploads). sizes are the part lengths.
func CombineCRC(alg string, partSums [][]byte, sizes []int64) ([]byte, error) {
	switch Normalize(alg) {
	case CRC32, CRC32C:
		tab := crc32.IEEETable
		if Normalize(alg) == CRC32C {
			tab = crc32cTable
		}
		var acc uint32
		for i, p := range partSums {
			c := binary.BigEndian.Uint32(p)
			if i == 0 {
				acc = c
				continue
			}
			acc = crc32Combine(tab, acc, c, sizes[i])
		}
		out := make([]byte, 4)
		binary.BigEndian.PutUint32(out, acc)
		return out, nil
	case CRC64NVME:
		var acc uint64
		for i, p := range partSums {
			c := binary.BigEndian.Uint64(p)
			if i == 0 {
				acc = c
				continue
			}
			acc = crc64Combine(acc, c, sizes[i])
		}
		out := make([]byte, 8)
		binary.BigEndian.PutUint64(out, acc)
		return out, nil
	}
	return nil, ErrUnknownAlgorithm
}

// crc32Combine is zlib's crc32_combine for a reflected polynomial table.
func crc32Combine(tab *crc32.Table, crc1, crc2 uint32, len2 int64) uint32 {
	if len2 <= 0 {
		return crc1
	}
	poly := reflectedPoly32(tab)
	var even, odd [32]uint32
	odd[0] = poly
	row := uint32(1)
	for n := 1; n < 32; n++ {
		odd[n] = row
		row <<= 1
	}
	gf2MatrixSquare32(even[:], odd[:])
	gf2MatrixSquare32(odd[:], even[:])
	for {
		gf2MatrixSquare32(even[:], odd[:])
		if len2&1 != 0 {
			crc1 = gf2MatrixTimes32(even[:], crc1)
		}
		len2 >>= 1
		if len2 == 0 {
			break
		}
		gf2MatrixSquare32(odd[:], even[:])
		if len2&1 != 0 {
			crc1 = gf2MatrixTimes32(odd[:], crc1)
		}
		len2 >>= 1
		if len2 == 0 {
			break
		}
	}
	return crc1 ^ crc2
}

func reflectedPoly32(tab *crc32.Table) uint32 {
	// tab[128] == poly for reflected tables (entry for input bit 0x80 → 1 after 7 shifts).
	// Derive the polynomial from tab[1]: tab[1] = poly reflected through 8 shifts.
	p := tab[1]
	for i := 0; i < 7; i++ {
		if p&1 != 0 {
			p = (p >> 1) ^ tab[1]
		} else {
			p >>= 1
		}
	}
	// The above is unreliable for arbitrary tables; use the known constants.
	switch tab[1] {
	case crc32.IEEETable[1]:
		return 0xEDB88320
	case crc32cTable[1]:
		return 0x82F63B78
	}
	return p
}

func gf2MatrixTimes32(mat []uint32, vec uint32) uint32 {
	var sum uint32
	for i := 0; vec != 0; i++ {
		if vec&1 != 0 {
			sum ^= mat[i]
		}
		vec >>= 1
	}
	return sum
}

func gf2MatrixSquare32(square, mat []uint32) {
	for n := 0; n < 32; n++ {
		square[n] = gf2MatrixTimes32(mat, mat[n])
	}
}

func crc64Combine(crc1, crc2 uint64, len2 int64) uint64 {
	if len2 <= 0 {
		return crc1
	}
	var even, odd [64]uint64
	odd[0] = crc64NVMEPoly
	row := uint64(1)
	for n := 1; n < 64; n++ {
		odd[n] = row
		row <<= 1
	}
	gf2MatrixSquare64(even[:], odd[:])
	gf2MatrixSquare64(odd[:], even[:])
	for {
		gf2MatrixSquare64(even[:], odd[:])
		if len2&1 != 0 {
			crc1 = gf2MatrixTimes64(even[:], crc1)
		}
		len2 >>= 1
		if len2 == 0 {
			break
		}
		gf2MatrixSquare64(odd[:], even[:])
		if len2&1 != 0 {
			crc1 = gf2MatrixTimes64(odd[:], crc1)
		}
		len2 >>= 1
		if len2 == 0 {
			break
		}
	}
	return crc1 ^ crc2
}

func gf2MatrixTimes64(mat []uint64, vec uint64) uint64 {
	var sum uint64
	for i := 0; vec != 0; i++ {
		if vec&1 != 0 {
			sum ^= mat[i]
		}
		vec >>= 1
	}
	return sum
}

func gf2MatrixSquare64(square, mat []uint64) {
	for n := 0; n < 64; n++ {
		square[n] = gf2MatrixTimes64(mat, mat[n])
	}
}
