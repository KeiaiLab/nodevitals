package collector

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/KeiaiLab/nodevitals/internal/model"
)

// writeAERDevice lays down one fake PCI device with the AER files given.
func writeAERDevice(t *testing.T, sysRoot, addr string, files map[string]string) {
	t.Helper()
	dir := filepath.Join(sysRoot, "bus", "pci", "devices", addr)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", dir, err)
	}
	for name, body := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}
}

func TestPCIeAERMapping(t *testing.T) {
	sysRoot := t.TempDir()
	// Byte-for-byte the shape a live kernel emits (e21, 0000:00:01.1).
	writeAERDevice(t, sysRoot, "0000:00:01.1", map[string]string{
		"aer_dev_correctable": "RxErr 0\nBadTLP 3\nBadDLLP 0\nRollover 0\n",
		"aer_dev_nonfatal":    "Undefined 0\nDLP 1\nSDES 0\n",
		"aer_dev_fatal":       "Undefined 0\nDLP 0\n",
	})

	c := NewPCIeAER("test-node", sysRoot)
	got, err := c.Collect(context.Background())
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}
	if len(got) != 9 {
		t.Fatalf("want 9 samples (4 correctable + 3 nonfatal + 2 fatal), got %d", len(got))
	}

	type key struct{ metric, errType string }
	byKey := map[key]float64{}
	for _, s := range got {
		if s.Tier != "core" {
			t.Fatalf("%s: Tier = %q, want core", s.Metric, s.Tier)
		}
		if s.Device != "0000:00:01.1" {
			t.Fatalf("%s: Device = %q, want the PCI address", s.Metric, s.Device)
		}
		if s.Kind != model.KindCounter {
			t.Fatalf("%s: Kind = %q, want counter", s.Metric, s.Kind)
		}
		byKey[key{s.Metric, s.Labels["error_type"]}] = s.Value
	}

	for k, want := range map[key]float64{
		{"pcie_aer_correctable_total", "BadTLP"}: 3,
		// A zero must be PRESENT, not omitted: absence would be ambiguous
		// between "no errors" and "not collected".
		{"pcie_aer_correctable_total", "RxErr"}: 0,
		{"pcie_aer_nonfatal_total", "DLP"}:      1,
		{"pcie_aer_fatal_total", "DLP"}:         0,
	} {
		got, ok := byKey[k]
		if !ok {
			t.Fatalf("missing sample %s{error_type=%q}", k.metric, k.errType)
		}
		if got != want {
			t.Fatalf("%s{error_type=%q} = %v, want %v", k.metric, k.errType, got, want)
		}
	}
}

// Most PCI functions expose no AER files at all; that is normal topology, not
// a collection failure.
func TestPCIeAERSkipsDevicesWithoutAER(t *testing.T) {
	sysRoot := t.TempDir()
	writeAERDevice(t, sysRoot, "0000:00:02.0", map[string]string{"vendor": "0x8086\n"})
	writeAERDevice(t, sysRoot, "0000:00:01.1", map[string]string{
		"aer_dev_correctable": "RxErr 2\n",
	})

	got, err := NewPCIeAER("n", sysRoot).Collect(context.Background())
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("want 1 sample (only the AER-capable device), got %d: %+v", len(got), got)
	}
	if got[0].Device != "0000:00:01.1" || got[0].Value != 2 {
		t.Fatalf("unexpected sample: %+v", got[0])
	}
}

// A node with no PCI sysfs at all (VM, arm64 SoC) yields nothing and no error,
// matching how the power collector treats a missing powercap tree.
func TestPCIeAERMissingSysfsIsNotAnError(t *testing.T) {
	got, err := NewPCIeAER("n", t.TempDir()).Collect(context.Background())
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("want 0 samples, got %d", len(got))
	}
}

// Malformed lines are skipped rather than failing the device: a kernel that
// adds a header or a blank line must not cost us the counters below it.
func TestPCIeAERSkipsMalformedLines(t *testing.T) {
	sysRoot := t.TempDir()
	writeAERDevice(t, sysRoot, "0000:00:01.1", map[string]string{
		"aer_dev_correctable": "\nTOTAL_ERR_COR\nRxErr 4\nBadTLP notanumber\nBadDLLP 1\n",
	})

	got, err := NewPCIeAER("n", sysRoot).Collect(context.Background())
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("want 2 parseable samples, got %d: %+v", len(got), got)
	}
}
