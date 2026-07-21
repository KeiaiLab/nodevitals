package collector

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/KeiaiLab/nodevitals/internal/model"
)

func TestPowerReadsRAPLZoneFromFixture(t *testing.T) {
	got, err := NewPower("n", "../../testdata/sys").Collect(context.Background())
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("want 1 RAPL zone sample, got %+v", got)
	}
	s := got[0]
	if s.Metric != "power_energy_joules_total" || s.Kind != model.KindCounter {
		t.Fatalf("want a joules counter, got %q/%q", s.Metric, s.Kind)
	}
	// 65532610987 uj -> 65532.610987 J. A microjoule/joule slip is exactly the
	// kind of thing that silently makes a watts dashboard wrong by 1e6.
	if s.Value != 65532.610987 {
		t.Fatalf("energy = %v J, want 65532.610987", s.Value)
	}
	if s.Labels["zone"] != "package-0" {
		t.Fatalf("zone label = %q, want package-0", s.Labels["zone"])
	}
}

func TestPowerWithoutPowercapReturnsNothingNotAnError(t *testing.T) {
	// AMD without amd_energy, most VMs, and arm64 have no powercap subsystem.
	// A missing optional subsystem must not fail a collection round that also
	// carries CPU, memory and disk samples.
	got, err := NewPower("n", t.TempDir()).Collect(context.Background())
	if err != nil {
		t.Fatalf("missing powercap must not error, got %v", err)
	}
	if got != nil {
		t.Fatalf("want no samples, got %+v", got)
	}
}

func TestPowerUnreadableEnergyIsSkippedNotFailed(t *testing.T) {
	// Since CVE-2020-8694 energy_uj is 0400 root-only. Running unprivileged is
	// the default, so this path must degrade quietly (one warn, logged
	// elsewhere) instead of turning the collector permanently red.
	if os.Geteuid() == 0 {
		t.Skip("running as root — the permission path is unreachable")
	}
	root := t.TempDir()
	zone := filepath.Join(root, "class", "powercap", "intel-rapl:0")
	if err := os.MkdirAll(zone, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(zone, "energy_uj"), []byte("123\n"), 0o000); err != nil {
		t.Fatal(err)
	}
	got, err := NewPower("n", root).Collect(context.Background())
	if err != nil {
		t.Fatalf("permission denial must not error, got %v", err)
	}
	if got != nil {
		t.Fatalf("want no samples when energy_uj is unreadable, got %+v", got)
	}
}
