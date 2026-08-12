package main

import (
	"slices"
	"testing"

	"github.com/KeiaiLab/nodevitals/internal/config"
	"github.com/KeiaiLab/nodevitals/internal/nodecompat"
)

// With the native collectors on, every upstream collector they replace has to
// be switched off. Leaving one enabled makes both register the same metric
// names, and client_golang drops the collided family from the scrape while
// still answering 200 — the loss never surfaces as an error.
func TestNodeExporterFlagsDisableEveryNativelyServedCollector(t *testing.T) {
	flags := nodeExporterFlags(config.NodeExporterConfig{NativeCollectors: true})
	for _, name := range nodecompat.SupersededCollectors() {
		want := "--no-collector." + name
		if !slices.Contains(flags, want) {
			t.Errorf("missing %q: upstream %q stays enabled alongside its native replacement (got %v)",
				want, name, flags)
		}
	}
}

// Without the native collectors the upstream ones are the only source, so
// disabling them would delete the metrics outright rather than deduplicate.
func TestNodeExporterFlagsLeaveUpstreamAloneWhenNativeIsOff(t *testing.T) {
	flags := nodeExporterFlags(config.NodeExporterConfig{
		NativeCollectors: false,
		ExtraFlags:       []string{"--collector.systemd"},
	})
	for _, f := range flags {
		if f != "--collector.systemd" {
			t.Errorf("unexpected flag %q with nativeCollectors off; want only the operator's own flags", f)
		}
	}
}

func TestNodeExporterFlagsKeepOperatorSuppliedFlags(t *testing.T) {
	flags := nodeExporterFlags(config.NodeExporterConfig{
		NativeCollectors: true,
		ExtraFlags:       []string{"--collector.processes", "--collector.systemd"},
	})
	for _, want := range []string{"--collector.processes", "--collector.systemd"} {
		if !slices.Contains(flags, want) {
			t.Errorf("operator flag %q was dropped (got %v)", want, flags)
		}
	}
}

// append onto a caller-owned slice can write through to its backing array when
// there is spare capacity. The config is read once and reused, so a mutation
// here would leak into anything else reading ExtraFlags.
func TestNodeExporterFlagsDoNotMutateConfig(t *testing.T) {
	extra := make([]string, 1, 8) // 여유 cap — aliasing 이 드러나는 조건
	extra[0] = "--collector.systemd"
	cfg := config.NodeExporterConfig{NativeCollectors: true, ExtraFlags: extra}

	nodeExporterFlags(cfg)

	if got := cfg.ExtraFlags; len(got) != 1 || got[0] != "--collector.systemd" {
		t.Errorf("config.ExtraFlags was mutated: %v", got)
	}
	if got := extra[:cap(extra)]; got[1] != "" {
		t.Errorf("wrote past the caller's slice into its backing array: %v", got)
	}
}
