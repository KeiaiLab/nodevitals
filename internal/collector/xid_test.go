package collector

import "testing"

// TestXidClassifyKnownSeverities pins every table entry from design doc §3
// to its severity bucket so a future edit can't silently reclassify one.
func TestXidClassifyKnownSeverities(t *testing.T) {
	cases := []struct {
		xid  uint64
		want string
	}{
		// info (benign)
		{13, "info"},
		{31, "info"},
		{43, "info"},
		// critical
		{48, "critical"},
		{64, "critical"},
		{79, "critical"},
		{95, "critical"},
		{119, "critical"},
		{120, "critical"},
		// warning
		{63, "warning"},
		{74, "warning"},
		{92, "warning"},
		{94, "warning"},
	}
	for _, c := range cases {
		got := classifyXid(c.xid)
		if got.Severity != c.want {
			t.Fatalf("classifyXid(%d).Severity = %q, want %q", c.xid, got.Severity, c.want)
		}
	}
}

func TestXidClassifyUnknownDefaultsToWarning(t *testing.T) {
	got := classifyXid(999)
	if got.Severity != "warning" {
		t.Fatalf("classifyXid(999).Severity = %q, want %q (conservative default for unregistered XID)", got.Severity, "warning")
	}
}

func TestXidClassifyConditionAlwaysGpuXidError(t *testing.T) {
	for _, xid := range []uint64{13, 31, 43, 48, 63, 64, 74, 79, 92, 94, 95, 119, 120, 999} {
		got := classifyXid(xid)
		if got.Condition != "gpu_xid_error" {
			t.Fatalf("classifyXid(%d).Condition = %q, want %q", xid, got.Condition, "gpu_xid_error")
		}
	}
}

func TestXidClassifyDescriptionNonEmpty(t *testing.T) {
	all := []uint64{13, 31, 43, 48, 63, 64, 74, 79, 92, 94, 95, 119, 120, 999}
	for _, xid := range all {
		got := classifyXid(xid)
		if got.Description == "" {
			t.Fatalf("classifyXid(%d).Description is empty, want a short human-readable string", xid)
		}
	}
}
