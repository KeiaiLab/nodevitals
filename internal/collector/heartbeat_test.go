package collector

import (
	"context"
	"testing"
)

func TestHeartbeatCollector(t *testing.T) {
	c := NewHeartbeat("node-1", "0.8.5")
	if c.Name() != "heartbeat" {
		t.Fatalf("unexpected name: %s", c.Name())
	}

	samples, err := c.Collect(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(samples) != 2 {
		t.Fatalf("expected 2 samples, got %d", len(samples))
	}

	upFound := false
	buildInfoFound := false
	for _, s := range samples {
		if s.Metric == "nodevitals_up" && s.Value == 1.0 {
			upFound = true
		}
		if s.Metric == "nodevitals_build_info" && s.Labels["version"] == "0.8.5" {
			buildInfoFound = true
		}
	}

	if !upFound {
		t.Errorf("nodevitals_up missing or invalid")
	}
	if !buildInfoFound {
		t.Errorf("nodevitals_build_info missing or invalid")
	}
}
