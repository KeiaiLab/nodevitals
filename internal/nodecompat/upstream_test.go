package nodecompat

import (
	"reflect"
	"testing"
)

// A correctly paired deployment: the chart already disabled the six native
// names on the embedded side, so nothing enabled upstream should collide.
func TestConflictingUpstreamCollectorsNoOverlap(t *testing.T) {
	enabled := []string{"cpu", "meminfo", "diskstats", "netdev", "stat"}
	if got := ConflictingUpstreamCollectors(enabled); len(got) != 0 {
		t.Fatalf("ConflictingUpstreamCollectors(%v) = %v, want none", enabled, got)
	}
}

// A hand-written ConfigMap that turned nativeCollectors on without the
// matching --no-collector.* flags — the failure mode main.go's startup check
// exists to catch before it becomes a rejected /metrics scrape.
func TestConflictingUpstreamCollectorsDetectsOverlap(t *testing.T) {
	enabled := []string{"cpu", "loadavg", "os", "stat"}
	want := []string{"loadavg", "os"}
	if got := ConflictingUpstreamCollectors(enabled); !reflect.DeepEqual(got, want) {
		t.Fatalf("ConflictingUpstreamCollectors(%v) = %v, want %v", enabled, got, want)
	}
}

// stat being enabled must never itself count as a conflict: no native group
// in Phase 1 maps to it (see UpstreamCollectors' doc comment for why).
func TestConflictingUpstreamCollectorsStatAloneIsNotAConflict(t *testing.T) {
	enabled := []string{"stat"}
	if got := ConflictingUpstreamCollectors(enabled); len(got) != 0 {
		t.Fatalf("ConflictingUpstreamCollectors(%v) = %v, want none (stat has no native twin)", enabled, got)
	}
}
