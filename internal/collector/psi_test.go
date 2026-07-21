package collector

import (
	"context"
	"testing"

	"github.com/KeiaiLab/nodevitals/internal/model"
)

func TestPSIReadsAllResourcesFromFixture(t *testing.T) {
	got, err := NewPSI("n", "../../testdata/proc").Collect(context.Background())
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}

	// cpu/memory/io x some/full x (3 averages + 1 total), minus cpu's full
	// line which the kernel does not emit — the fixture carries one anyway to
	// prove we don't drop it if a future kernel adds it.
	byKey := map[string]model.Sample{}
	for _, s := range got {
		if s.Tier != "core" {
			t.Fatalf("psi samples belong to the core tier, got %q", s.Tier)
		}
		byKey[s.Device+"/"+s.Labels["share"]+"/"+s.Metric] = s
	}

	for _, res := range []string{"cpu", "memory", "io"} {
		if _, ok := byKey[res+"/some/psi_stall_ratio_avg10"]; !ok {
			t.Fatalf("missing some-pressure avg10 for %q: %+v", res, got)
		}
	}

	// Ratios pass through as percentages; the total converts microseconds to
	// seconds, which is where a unit slip would hide.
	if s := byKey["io/some/psi_stall_ratio_avg60"]; s.Value != 12.57 {
		t.Fatalf("io some avg60 = %v, want 12.57", s.Value)
	}
	if s := byKey["io/some/psi_stall_seconds_total"]; s.Value != 403268.895070 {
		t.Fatalf("io some total = %v seconds, want 403268.89507 (403268895070us)", s.Value)
	}
	if s := byKey["memory/full/psi_stall_ratio_avg10"]; s.Value != 0.10 {
		t.Fatalf("memory full avg10 = %v, want 0.10", s.Value)
	}

	// Averages are instantaneous, totals accumulate — mixing them up would
	// make rate() meaningless.
	if s := byKey["cpu/some/psi_stall_ratio_avg10"]; s.Kind != model.KindGauge {
		t.Fatalf("psi ratio must be a gauge, got %q", s.Kind)
	}
	if s := byKey["cpu/some/psi_stall_seconds_total"]; s.Kind != model.KindCounter {
		t.Fatalf("psi total must be a counter, got %q", s.Kind)
	}
}

func TestPSIMissingPressureDirIsAnErrorNotAPanic(t *testing.T) {
	// PSI needs CONFIG_PSI=y; on a kernel without it the directory is absent.
	// The collector must report that rather than emit silent zeros.
	got, err := NewPSI("n", "../../testdata/proc-noswap").Collect(context.Background())
	if err == nil {
		t.Fatalf("expected an error when /pressure is absent, got %d samples", len(got))
	}
	if got != nil {
		t.Fatalf("expected no samples when /pressure is absent, got %+v", got)
	}
}
