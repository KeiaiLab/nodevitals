package collector

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/KeiaiLab/nodevitals/internal/model"
)

type power struct {
	node    string
	sysRoot string

	// warnOnce keeps an unreadable energy counter to a single log line instead
	// of one per interval. The permission case is a deployment fact, not an
	// event — it does not change until the pod is restarted with more
	// privilege.
	warnOnce sync.Once
}

// NewPower reads Intel RAPL energy counters from
// <sysRoot>/class/powercap/intel-rapl:*/energy_uj.
//
// Since the CVE-2020-8694 mitigation the kernel ships energy_uj as 0400
// root-only, because a fine-grained energy readout is a side channel. So this
// collector only produces data where the agent already runs as root — the
// smart tier, or a singlePod layout that includes it. Everywhere else it logs
// once and returns nothing rather than failing the whole collection round:
// power draw is a nice-to-have next to disk and GPU health, and an operator
// who never asked for it should not see a permanently red collector.
func NewPower(node, sysRoot string) Collector {
	return &power{node: node, sysRoot: sysRoot}
}

func (p *power) Name() string { return "power" }

func (p *power) Collect(ctx context.Context) ([]model.Sample, error) {
	root := filepath.Join(p.sysRoot, "class", "powercap")
	entries, err := os.ReadDir(root)
	if err != nil {
		// No powercap subsystem at all (AMD without amd_energy, a VM, arm64):
		// not an error worth surfacing every interval.
		return nil, nil
	}

	now := time.Now().UTC()
	var out []model.Sample
	var denied bool
	for _, e := range entries {
		// Top-level packages are "intel-rapl:N"; "intel-rapl:N:M" are their
		// subzones (core/uncore/dram). Both are read — the zone name label
		// tells them apart.
		name := e.Name()
		if !strings.HasPrefix(name, "intel-rapl:") {
			continue
		}
		zone := filepath.Join(root, name)
		uj, err := readUint(filepath.Join(zone, "energy_uj"))
		if err != nil {
			if errors.Is(err, fs.ErrPermission) {
				denied = true
			}
			continue
		}
		lbl := map[string]string{}
		if zn, err := readTrimmed(filepath.Join(zone, "name")); err == nil {
			lbl["zone"] = zn
		}
		out = append(out, model.Sample{
			Node: p.node, Tier: "core", Device: name,
			// Joules as a monotonic counter — rate() over it is watts. The
			// counter wraps at max_energy_range_uj, which Prometheus-style
			// rate handling already treats as a reset.
			Metric: "power_energy_joules_total", Kind: model.KindCounter,
			Value: float64(uj) / 1e6, Labels: lbl, Timestamp: now,
		})
	}

	if out == nil && denied {
		p.warnOnce.Do(func() {
			slog.Warn("power: RAPL energy counters are root-only since CVE-2020-8694 — " +
				"skipping power metrics (run this tier as root to collect them)")
		})
	}
	return out, nil
}

func readUint(path string) (uint64, error) {
	s, err := readTrimmed(path)
	if err != nil {
		return 0, err
	}
	return strconv.ParseUint(s, 10, 64)
}

func readTrimmed(path string) (string, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("read %s: %w", path, err)
	}
	return strings.TrimSpace(string(b)), nil
}
