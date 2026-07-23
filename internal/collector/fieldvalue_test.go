package collector

import (
	"encoding/binary"
	"math"
	"testing"
)

func TestDecodeFieldValue(t *testing.T) {
	le64 := func(v uint64) (b [8]byte) { binary.LittleEndian.PutUint64(b[:], v); return }

	cases := []struct {
		name      string
		valueType uint32
		raw       [8]byte
		want      float64
	}{
		{"double", 0, le64(math.Float64bits(72.5)), 72.5},
		{"unsigned int", 1, le64(83), 83},
		{"unsigned long", 2, le64(1 << 40), 1 << 40},
		{"unsigned long long", 3, le64(32972735665), 32972735665},
		{"signed long long negative", 4, le64(uint64(math.MaxUint64)), -1}, // -1 two's complement
		{"signed int negative", 5, le64(uint64(math.MaxUint32)), -1},      // low 4 bytes = -1
		{"unsigned short", 6, le64(65535), 65535},
		{"unknown type", 7, le64(42), 0},
	}
	for _, tc := range cases {
		if got := decodeFieldValue(tc.valueType, tc.raw); got != tc.want {
			t.Errorf("%s: decodeFieldValue(%d) = %v, want %v", tc.name, tc.valueType, got, tc.want)
		}
	}
}
