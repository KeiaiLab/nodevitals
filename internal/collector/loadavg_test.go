package collector

import (
	"context"
	"testing"
)

func TestLoadAvgReadsFixture(t *testing.T) {
	c := NewLoadAvg("test-node", "../../testdata/proc")
	samples, err := c.Collect(context.Background())
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}
	if len(samples) != 1 {
		t.Fatalf("want 1 sample, got %d", len(samples))
	}
	s := samples[0]
	if s.Metric != "load1" || s.Value != 0.52 {
		t.Fatalf("bad sample: %+v", s)
	}
	if s.Node != "test-node" || s.Tier != "core" || s.Device != "cpu" {
		t.Fatalf("bad identity: %+v", s)
	}
}

func TestLoadAvgMissingFileErrors(t *testing.T) {
	c := NewLoadAvg("n", "/nonexistent")
	if _, err := c.Collect(context.Background()); err == nil {
		t.Fatal("expected error for missing loadavg")
	}
}
