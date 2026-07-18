package collector

import (
	"context"
	"fmt"
	"time"

	"github.com/prometheus/procfs"

	"github.com/KeiaiLab/nodevitals/internal/model"
)

type cpuCollector struct {
	node     string
	procRoot string
	prev     map[string]procfs.CPUStat
}

// NewCPU reports per-CPU and aggregate utilization percentage from /proc/stat
// deltas. The first Collect establishes a baseline and emits no samples.
func NewCPU(node, procRoot string) Collector {
	return &cpuCollector{node: node, procRoot: procRoot, prev: map[string]procfs.CPUStat{}}
}

func (c *cpuCollector) Name() string { return "cpu" }

// busyIdle splits a CPUStat snapshot into busy and idle jiffy totals.
func busyIdle(s procfs.CPUStat) (busy, idle float64) {
	idle = s.Idle + s.Iowait
	busy = s.User + s.Nice + s.System + s.IRQ + s.SoftIRQ + s.Steal
	return busy, idle
}

func (c *cpuCollector) Collect(ctx context.Context) ([]model.Sample, error) {
	fs, err := procfs.NewFS(c.procRoot)
	if err != nil {
		return nil, fmt.Errorf("open procfs %q: %w", c.procRoot, err)
	}
	stat, err := fs.Stat()
	if err != nil {
		return nil, fmt.Errorf("read stat: %w", err)
	}

	cur := map[string]procfs.CPUStat{"cpu": stat.CPUTotal}
	for id, cs := range stat.CPU {
		cur[fmt.Sprintf("cpu%d", id)] = cs
	}

	now := time.Now().UTC()
	var out []model.Sample
	for dev, s := range cur {
		p, ok := c.prev[dev]
		if !ok {
			continue
		}
		bNow, iNow := busyIdle(s)
		bPrev, iPrev := busyIdle(p)
		db, di := bNow-bPrev, iNow-iPrev
		if db < 0 || di < 0 { // counter reset on either axis — skip this tick
			continue
		}
		denom := db + di
		if denom <= 0 {
			continue
		}
		out = append(out, model.Sample{
			Node: c.node, Tier: "core", Device: dev, Metric: "cpu_util_pct",
			Value: 100 * db / denom, Timestamp: now,
		})
	}
	c.prev = cur
	return out, nil
}
