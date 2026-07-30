package collector

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/prometheus/client_golang/prometheus/testutil"

	"github.com/KeiaiLab/nodevitals/internal/model"
	"github.com/KeiaiLab/nodevitals/internal/smartctlcompat"
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
	c := NewSmart("test-node", probe, nil)
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

func TestSmartNVMeMapping(t *testing.T) {
	probe := func(ctx context.Context) ([]smartDevice, error) {
		return []smartDevice{{
			Name:         "nvme0n1",
			Transport:    "nvme",
			Temperature:  f64ptr(41.0),
			PowerOnHours: u64ptr(500),
			NVMe: &nvmeHealth{
				PercentageUsed:  3,
				AvailableSpare:  100,
				SpareThreshold:  10,
				MediaErrors:     0,
				CriticalWarning: 0,
			},
		}}, nil
	}
	c := NewSmart("test-node", probe, nil)
	got, err := c.Collect(context.Background())
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}
	if len(got) != 7 {
		t.Fatalf("want 7 samples (temp+poh+5 nvme), got %d: %+v", len(got), got)
	}

	byMetric := map[string]float64{}
	for _, s := range got {
		byMetric[s.Metric] = s.Value
		if s.Tier != "smart" {
			t.Fatalf("sample %s: Tier = %q, want %q", s.Metric, s.Tier, "smart")
		}
		if s.Device != "nvme0n1" {
			t.Fatalf("sample %s: Device = %q, want %q", s.Metric, s.Device, "nvme0n1")
		}
		if s.Kind != model.KindGauge {
			t.Fatalf("sample %s: Kind = %q, want gauge (zero value)", s.Metric, s.Kind)
		}
		if s.Node != "test-node" {
			t.Fatalf("sample %s: Node = %q, want %q", s.Metric, s.Node, "test-node")
		}
	}

	want := map[string]float64{
		"smart_temperature_celsius":      41,
		"smart_power_on_hours":           500,
		"nvme_percentage_used":           3,
		"nvme_available_spare":           100,
		"nvme_available_spare_threshold": 10,
		"nvme_media_errors":              0,
		"nvme_critical_warning":          0,
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

func TestSmartMixedSATANVMeMapping(t *testing.T) {
	probe := func(ctx context.Context) ([]smartDevice, error) {
		return []smartDevice{
			{
				Name:         "sda",
				Transport:    "sata",
				Temperature:  f64ptr(30.0),
				PowerOnHours: u64ptr(2000),
				ATAAttrs: map[uint8]uint64{
					5:   1,
					187: 2,
					188: 3,
					197: 4,
					198: 5,
				},
			},
			{
				Name:         "nvme0n1",
				Transport:    "nvme",
				Temperature:  f64ptr(41.0),
				PowerOnHours: u64ptr(500),
				NVMe: &nvmeHealth{
					PercentageUsed:  3,
					AvailableSpare:  100,
					SpareThreshold:  10,
					MediaErrors:     0,
					CriticalWarning: 0,
				},
			},
		}, nil
	}
	c := NewSmart("test-node", probe, nil)
	got, err := c.Collect(context.Background())
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}
	if len(got) != 14 {
		t.Fatalf("want 14 samples (7 sda + 7 nvme0n1), got %d: %+v", len(got), got)
	}

	byDevice := map[string]map[string]float64{}
	for _, s := range got {
		if s.Tier != "smart" {
			t.Fatalf("sample %s/%s: Tier = %q, want %q", s.Device, s.Metric, s.Tier, "smart")
		}
		if s.Kind != model.KindGauge {
			t.Fatalf("sample %s/%s: Kind = %q, want gauge (zero value)", s.Device, s.Metric, s.Kind)
		}
		if byDevice[s.Device] == nil {
			byDevice[s.Device] = map[string]float64{}
		}
		byDevice[s.Device][s.Metric] = s.Value
	}

	wantSDA := map[string]float64{
		"smart_temperature_celsius":    30,
		"smart_power_on_hours":         2000,
		"smart_reallocated_sectors":    1,
		"smart_reported_uncorrectable": 2,
		"smart_command_timeout":        3,
		"smart_pending_sectors":        4,
		"smart_offline_uncorrectable":  5,
	}
	wantNVMe := map[string]float64{
		"smart_temperature_celsius":      41,
		"smart_power_on_hours":           500,
		"nvme_percentage_used":           3,
		"nvme_available_spare":           100,
		"nvme_available_spare_threshold": 10,
		"nvme_media_errors":              0,
		"nvme_critical_warning":          0,
	}

	if len(byDevice["sda"]) != len(wantSDA) {
		t.Fatalf("sda: got %d distinct metrics, want %d: %+v", len(byDevice["sda"]), len(wantSDA), byDevice["sda"])
	}
	for metric, wantVal := range wantSDA {
		gotVal, ok := byDevice["sda"][metric]
		if !ok {
			t.Fatalf("sda: missing sample for metric %s", metric)
		}
		if gotVal != wantVal {
			t.Fatalf("sda: %s = %v, want %v", metric, gotVal, wantVal)
		}
	}

	if len(byDevice["nvme0n1"]) != len(wantNVMe) {
		t.Fatalf("nvme0n1: got %d distinct metrics, want %d: %+v", len(byDevice["nvme0n1"]), len(wantNVMe), byDevice["nvme0n1"])
	}
	for metric, wantVal := range wantNVMe {
		gotVal, ok := byDevice["nvme0n1"][metric]
		if !ok {
			t.Fatalf("nvme0n1: missing sample for metric %s", metric)
		}
		if gotVal != wantVal {
			t.Fatalf("nvme0n1: %s = %v, want %v", metric, gotVal, wantVal)
		}
	}
}

func TestSmartCollectWrapsProbeError(t *testing.T) {
	probeErr := errors.New("ioctl: permission denied")
	probe := func(ctx context.Context) ([]smartDevice, error) { return nil, probeErr }
	c := NewSmart("n", probe, nil)

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
	c := NewSmart("n", probe, nil)

	got, err := c.Collect(context.Background())
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("want 0 samples, got %d: %+v", len(got), got)
	}
}

func TestSmartCollectFeedsSmartctlCompatExporter(t *testing.T) {
	// One Collect must land the same probe sweep on both surfaces: native
	// samples (power-on in hours, namespace device name) and the smartctl
	// compat exporter (seconds and controller name at render time).
	probe := func(ctx context.Context) ([]smartDevice, error) {
		return []smartDevice{{
			Name:         "nvme0n1",
			Transport:    "nvme",
			Temperature:  f64ptr(47),
			PowerOnHours: u64ptr(24871),
			NVMe:         &nvmeHealth{PercentageUsed: 7, AvailableSpare: 100, SpareThreshold: 5},
		}}, nil
	}
	sc := smartctlcompat.New()
	c := NewSmart("test-node", probe, sc)

	got, err := c.Collect(context.Background())
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}

	// Native power-on stays in hours, on the namespace device name.
	var seen bool
	for _, s := range got {
		if s.Metric != "smart_power_on_hours" {
			continue
		}
		seen = true
		if s.Value != 24871 {
			t.Fatalf("smart_power_on_hours = %v, want 24871", s.Value)
		}
		if s.Device != "nvme0n1" {
			t.Fatalf("native device = %q, want nvme0n1", s.Device)
		}
	}
	if !seen {
		t.Fatal("native smart_power_on_hours sample missing")
	}

	// The exporter renders the same sweep in smartctl's units and naming:
	// 24871h * 3600 == 8.95356e+07 s, device="nvme0" not "nvme0n1".
	exp := `# HELP smartctl_device_percentage_used Device write percentage used
# TYPE smartctl_device_percentage_used counter
smartctl_device_percentage_used{device="nvme0"} 7
# HELP smartctl_device_power_on_seconds Device power on seconds
# TYPE smartctl_device_power_on_seconds counter
smartctl_device_power_on_seconds{device="nvme0"} 8.95356e+07
`
	if err := testutil.CollectAndCompare(sc, strings.NewReader(exp),
		"smartctl_device_percentage_used", "smartctl_device_power_on_seconds"); err != nil {
		t.Fatal(err)
	}
}

// A nil exporter is the compat-off path and must stay a no-op, not a panic.
func TestSmartCollectWithoutCompatExporter(t *testing.T) {
	probe := func(ctx context.Context) ([]smartDevice, error) {
		return []smartDevice{{Name: "sda", Transport: "sata", Temperature: f64ptr(36)}}, nil
	}
	c := NewSmart("n", probe, nil)

	if _, err := c.Collect(context.Background()); err != nil {
		t.Fatalf("Collect with nil compat exporter: %v", err)
	}
}
