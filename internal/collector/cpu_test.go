package collector

import (
	"context"
	"testing"

	"github.com/prometheus/procfs"
)

func TestCPUFirstTickIsBaselineNoSamples(t *testing.T) {
	c := NewCPU("n", "../../testdata/proc")
	got, err := c.Collect(context.Background())
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("first tick must be baseline (no delta yet), got %d samples", len(got))
	}
}

func TestCPUUtilComputedFromDelta(t *testing.T) {
	c := &cpuCollector{node: "n", procRoot: "../../testdata/proc", prev: map[string]procfs.CPUStat{}}
	if _, err := c.Collect(context.Background()); err != nil { // baseline from testdata/proc/stat
		t.Fatalf("baseline: %v", err)
	}
	c.procRoot = "../../testdata/proc2" // later reading
	got, err := c.Collect(context.Background())
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}
	var total *float64
	for i := range got {
		if got[i].Device == "cpu" && got[i].Metric == "cpu_util_pct" {
			total = &got[i].Value
		}
	}
	if total == nil {
		t.Fatalf("no aggregate cpu_util_pct sample: %+v", got)
	}
	if *total < 49.0 || *total > 51.0 {
		t.Fatalf("cpu_util_pct = %.2f, want ~50.0", *total)
	}
}

func TestCPUSkipsOnCounterReset(t *testing.T) {
	c := &cpuCollector{node: "n", procRoot: "../../testdata/proc2", prev: map[string]procfs.CPUStat{}}
	if _, err := c.Collect(context.Background()); err != nil { // baseline from busier proc2
		t.Fatalf("baseline: %v", err)
	}
	c.procRoot = "../../testdata/proc" // counters go BACKWARD (reset) → must skip, no samples
	got, err := c.Collect(context.Background())
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("counter reset must emit no samples, got %+v", got)
	}
}
