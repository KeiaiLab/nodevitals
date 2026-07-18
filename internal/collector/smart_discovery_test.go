package collector

import "testing"

func TestIsWholeDevice(t *testing.T) {
	cases := []struct {
		name string
		want bool
	}{
		{"sda", true},
		{"sdb", true},
		{"sda1", false},
		{"nvme0n1", true},
		{"nvme0n1p1", false},
		{"nvme10n1", true},
		{"loop0", false},
		{"dm-0", false},
		{"sr0", false},
		{"", false},
	}
	for _, tc := range cases {
		if got := isWholeDevice(tc.name); got != tc.want {
			t.Errorf("isWholeDevice(%q) = %v, want %v", tc.name, got, tc.want)
		}
	}
}
