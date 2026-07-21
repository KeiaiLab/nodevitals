package collector

import (
	"context"
	"fmt"
	"time"

	"github.com/prometheus/procfs"

	"github.com/KeiaiLab/nodevitals/internal/model"
)

// psiResources are the kernel's pressure classes, in a fixed order so sample
// output stays deterministic.
var psiResources = []string{"cpu", "memory", "io"}

type psi struct {
	node     string
	procRoot string
}

// NewPSI reads Pressure Stall Information from <procRoot>/pressure/{cpu,memory,io}.
//
// Utilisation says how busy a resource is; pressure says how much work is
// *waiting* on it, which is what actually correlates with a node feeling slow.
// A node at 100% CPU with no pressure is saturated but healthy; the same node
// with rising cpu pressure is starving its tasks. The files are world-readable,
// so this stays in the unprivileged core tier.
func NewPSI(node, procRoot string) Collector {
	return &psi{node: node, procRoot: procRoot}
}

func (p *psi) Name() string { return "psi" }

func (p *psi) Collect(ctx context.Context) ([]model.Sample, error) {
	fs, err := procfs.NewFS(p.procRoot)
	if err != nil {
		return nil, fmt.Errorf("open procfs %q: %w", p.procRoot, err)
	}

	now := time.Now().UTC()
	var out []model.Sample
	var firstErr error
	for _, res := range psiResources {
		st, err := fs.PSIStatsForResource(res)
		if err != nil {
			// PSI needs CONFIG_PSI=y and a 4.20+ kernel, and "cpu" has no full
			// line at all. Record the first failure but keep going, so one
			// missing class can't hide the others (Registry surfaces the error
			// while still taking the samples we did get).
			if firstErr == nil {
				firstErr = fmt.Errorf("read pressure/%s: %w", res, err)
			}
			continue
		}
		out = append(out, psiSamples(p.node, res, "some", st.Some, now)...)
		out = append(out, psiSamples(p.node, res, "full", st.Full, now)...)
	}
	if out == nil && firstErr != nil {
		return nil, firstErr
	}
	return out, firstErr
}

// psiSamples flattens one PSI line. share is "some" (at least one task
// stalled) or "full" (every non-idle task stalled) — cpu has no full line, so
// a nil line is normal and yields nothing.
func psiSamples(node, resource, share string, l *procfs.PSILine, now time.Time) []model.Sample {
	if l == nil {
		return nil
	}
	lbl := map[string]string{"share": share}
	return []model.Sample{
		{Node: node, Tier: "core", Device: resource, Metric: "psi_stall_ratio_avg10", Kind: model.KindGauge, Value: l.Avg10, Labels: lbl, Timestamp: now},
		{Node: node, Tier: "core", Device: resource, Metric: "psi_stall_ratio_avg60", Kind: model.KindGauge, Value: l.Avg60, Labels: lbl, Timestamp: now},
		{Node: node, Tier: "core", Device: resource, Metric: "psi_stall_ratio_avg300", Kind: model.KindGauge, Value: l.Avg300, Labels: lbl, Timestamp: now},
		// Total is a monotonic microsecond counter; seconds keep it in the
		// same unit family as the rest of the surface.
		{Node: node, Tier: "core", Device: resource, Metric: "psi_stall_seconds_total", Kind: model.KindCounter, Value: float64(l.Total) / 1e6, Labels: lbl, Timestamp: now},
	}
}
