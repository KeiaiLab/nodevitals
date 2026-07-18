package collector

import (
	"context"
	"testing"
)

func TestHwmonReadsTempFixture(t *testing.T) {
	c := NewHwmon("n", "../../testdata/sys")
	got, err := c.Collect(context.Background())
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}
	var temp *float64
	for i := range got {
		if got[i].Metric == "temp_celsius" {
			temp = &got[i].Value
		}
	}
	if temp == nil {
		t.Fatalf("no temp_celsius sample: %+v", got)
	}
	if *temp != 45.0 {
		t.Fatalf("temp = %v, want 45.0", *temp)
	}
}

func TestHwmonMissingDirIsEmptyNotError(t *testing.T) {
	c := NewHwmon("n", "/nonexistent")
	got, err := c.Collect(context.Background())
	if err != nil {
		t.Fatalf("missing hwmon dir should be empty, not error: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("want 0 samples, got %d", len(got))
	}
}
