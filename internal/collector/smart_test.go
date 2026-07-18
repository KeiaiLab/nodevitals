package collector

import (
	"context"
	"errors"
	"testing"

	"github.com/KeiaiLab/nodevitals/internal/model"
)

func f64ptr(v float64) *float64 { return &v }
func u64ptr(v uint64) *uint64   { return &v }

func TestSmartSATAMapping(t *testing.T) {
	probe := func(ctx context.Context) ([]smartDevice, error) {
		return []smartDevice{{
			Name:         "sda",
			Transport:    "sata",
			Temperature:  f64ptr(36.0),
			PowerOnHours: u64ptr(1000),
			ATAAttrs: map[uint8]uint64{
				5:   0,
				187: 0,
				188: 2,
				197: 5,
				198: 0,
			},
		}}, nil
	}
	c := NewSmart("test-node", probe)
	got, err := c.Collect(context.Background())
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}
	if len(got) != 7 {
		t.Fatalf("want 7 samples (temp+poh+5 attrs), got %d: %+v", len(got), got)
	}

	byMetric := map[string]float64{}
	for _, s := range got {
		byMetric[s.Metric] = s.Value
		if s.Tier != "smart" {
			t.Fatalf("sample %s: Tier = %q, want %q", s.Metric, s.Tier, "smart")
		}
		if s.Device != "sda" {
			t.Fatalf("sample %s: Device = %q, want %q", s.Metric, s.Device, "sda")
		}
		if s.Kind != model.KindGauge {
			t.Fatalf("sample %s: Kind = %q, want gauge (zero value)", s.Metric, s.Kind)
		}
		if s.Node != "test-node" {
			t.Fatalf("sample %s: Node = %q, want %q", s.Metric, s.Node, "test-node")
		}
	}

	want := map[string]float64{
		"smart_temperature_celsius":    36,
		"smart_power_on_hours":         1000,
		"smart_reallocated_sectors":    0,
		"smart_reported_uncorrectable": 0,
		"smart_command_timeout":        2,
		"smart_pending_sectors":        5,
		"smart_offline_uncorrectable":  0,
	}
	for metric, wantVal := range want {
		gotVal, ok := byMetric[metric]
		if !ok {
			t.Fatalf("missing sample for metric %s", metric)
		}
		if gotVal != wantVal {
			t.Fatalf("%s = %v, want %v", metric, gotVal, wantVal)
		}
	}
}

func TestSmartCollectWrapsProbeError(t *testing.T) {
	probeErr := errors.New("ioctl: permission denied")
	probe := func(ctx context.Context) ([]smartDevice, error) { return nil, probeErr }
	c := NewSmart("n", probe)

	_, err := c.Collect(context.Background())
	if err == nil {
		t.Fatal("Collect: want error, got nil")
	}
	if !errors.Is(err, probeErr) {
		t.Fatalf("Collect error = %v, want wrapped %v", err, probeErr)
	}
}

func TestSmartZeroDevicesYieldsZeroSamplesNoError(t *testing.T) {
	probe := func(ctx context.Context) ([]smartDevice, error) { return nil, nil }
	c := NewSmart("n", probe)

	got, err := c.Collect(context.Background())
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("want 0 samples, got %d: %+v", len(got), got)
	}
}
