package collector

import (
	"encoding/binary"
	"math"
)

// decodeFieldValue interprets an NVML FieldValue's 8-byte value union by its
// ValueType discriminant. Untagged pure Go (the raw bytes carry no cgo types)
// so the decode is unit-testable in the default CGO_ENABLED=0 test run; the
// gpu-tagged reader is the only caller. NVML is little-endian x86_64 ABI —
// nodevitals only ships linux/amd64 images (repo-wide single-arch policy).
//
// Case values mirror nvml.VALUE_TYPE_* (const.go of go-nvml): 0 double,
// 1 unsigned int, 2 unsigned long, 3 unsigned long long, 4 signed long long,
// 5 signed int, 6 unsigned short.
func decodeFieldValue(valueType uint32, raw [8]byte) float64 {
	switch valueType {
	case 0:
		return math.Float64frombits(binary.LittleEndian.Uint64(raw[:]))
	case 1:
		return float64(binary.LittleEndian.Uint32(raw[:4]))
	case 2, 3:
		return float64(binary.LittleEndian.Uint64(raw[:]))
	case 4:
		return float64(int64(binary.LittleEndian.Uint64(raw[:])))
	case 5:
		return float64(int32(binary.LittleEndian.Uint32(raw[:4])))
	case 6:
		return float64(binary.LittleEndian.Uint16(raw[:2]))
	default:
		return 0
	}
}
