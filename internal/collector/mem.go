package collector

import (
	"context"
	"fmt"
	"time"

	"github.com/KeiaiLab/nodevitals/internal/model"
	"github.com/prometheus/procfs"
)

type memCollector struct {
	node     string
	procRoot string
}

// NewMem reports memory and swap usage from /proc/meminfo.
func NewMem(node, procRoot string) Collector { return &memCollector{node: node, procRoot: procRoot} }

func (c *memCollector) Name() string { return "mem" }

func (c *memCollector) Collect(ctx context.Context) ([]model.Sample, error) {
	fs, err := procfs.NewFS(c.procRoot)
	if err != nil {
		return nil, fmt.Errorf("open procfs %q: %w", c.procRoot, err)
	}
	mi, err := fs.Meminfo()
	if err != nil {
		return nil, fmt.Errorf("read meminfo: %w", err)
	}
	now := time.Now().UTC()
	s := func(metric string, v float64) model.Sample {
		return model.Sample{Node: c.node, Tier: "core", Device: "mem", Metric: metric, Value: v, Timestamp: now}
	}
	var out []model.Sample
	// Meminfo *Bytes fields are already byte-normalized; nil-guard each.
	if mi.MemTotalBytes != nil {
		out = append(out, s("mem_total_bytes", float64(*mi.MemTotalBytes)))
	}
	if mi.MemAvailableBytes != nil {
		out = append(out, s("mem_available_bytes", float64(*mi.MemAvailableBytes)))
		if mi.MemTotalBytes != nil {
			out = append(out, s("mem_used_bytes", float64(*mi.MemTotalBytes-*mi.MemAvailableBytes)))
		}
	}
	if mi.SwapTotalBytes != nil && mi.SwapFreeBytes != nil {
		out = append(out, s("swap_used_bytes", float64(*mi.SwapTotalBytes-*mi.SwapFreeBytes)))
	}
	return out, nil
}
