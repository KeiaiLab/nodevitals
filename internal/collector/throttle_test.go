package collector

import "testing"

func TestThrottleDecodeSingleBit(t *testing.T) {
	got := decodeThrottle(0x1)
	if len(got) != 1 || got[0] != "gpu_idle" {
		t.Fatalf("decodeThrottle(0x1) = %v, want [gpu_idle]", got)
	}
}

// TestThrottleDecodeCombinedThermalBits pins the exact ascending-bit output
// order the design demands (0x20 before 0x40) — a map-backed implementation
// would make this flaky.
func TestThrottleDecodeCombinedThermalBits(t *testing.T) {
	got := decodeThrottle(0x20 | 0x40)
	want := []string{"sw_thermal_slowdown", "hw_thermal_slowdown"}
	if len(got) != len(want) {
		t.Fatalf("decodeThrottle(0x60) = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("decodeThrottle(0x60)[%d] = %q, want %q (ascending-bit order)", i, got[i], want[i])
		}
	}
}

func TestThrottleDecodeZeroMaskIsEmpty(t *testing.T) {
	got := decodeThrottle(0)
	if len(got) != 0 {
		t.Fatalf("decodeThrottle(0) = %v, want empty", got)
	}
}

// TestThrottleDecodeAllBitsAscendingOrder covers every documented reason bit
// at once, pinning both the label text and the fixed ascending order.
func TestThrottleDecodeAllBitsAscendingOrder(t *testing.T) {
	all := uint64(0x1 | 0x2 | 0x4 | 0x8 | 0x10 | 0x20 | 0x40 | 0x80 | 0x100)
	want := []string{
		"gpu_idle",
		"app_clocks_setting",
		"sw_power_cap",
		"hw_slowdown",
		"sync_boost",
		"sw_thermal_slowdown",
		"hw_thermal_slowdown",
		"hw_power_brake_slowdown",
		"display_clock_setting",
	}
	got := decodeThrottle(all)
	if len(got) != len(want) {
		t.Fatalf("decodeThrottle(all) = %v (len %d), want %v (len %d)", got, len(got), want, len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("decodeThrottle(all)[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}

func TestThrottleDecodeUnknownBitsIgnored(t *testing.T) {
	got := decodeThrottle(0x1 | 0x200) // 0x200 is not a documented reason bit
	if len(got) != 1 || got[0] != "gpu_idle" {
		t.Fatalf("decodeThrottle(0x1|0x200) = %v, want [gpu_idle] (unknown bits ignored, no panic)", got)
	}
}
