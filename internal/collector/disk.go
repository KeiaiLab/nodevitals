package collector

import (
	"context"
	"fmt"
	"time"

	"github.com/KeiaiLab/nodevitals/internal/model"
	"github.com/prometheus/procfs/blockdevice"
)

type diskCollector struct {
	node     string
	procRoot string
	sysRoot  string
}

// NewDisk reports per-disk IO counters from /proc/diskstats.
func NewDisk(node, procRoot, sysRoot string) Collector {
	return &diskCollector{node: node, procRoot: procRoot, sysRoot: sysRoot}
}

func (c *diskCollector) Name() string { return "disk" }

func (c *diskCollector) Collect(ctx context.Context) ([]model.Sample, error) {
	fs, err := blockdevice.NewFS(c.procRoot, c.sysRoot)
	if err != nil {
		return nil, fmt.Errorf("open blockdevice fs: %w", err)
	}
	stats, err := fs.ProcDiskstats()
	if err != nil {
		return nil, fmt.Errorf("read diskstats: %w", err)
	}
	now := time.Now().UTC()
	var out []model.Sample
	for _, d := range stats {
		mk := func(metric string, v float64) model.Sample {
			return model.Sample{Node: c.node, Tier: "core", Device: d.DeviceName, Metric: metric, Value: v, Timestamp: now}
		}
		out = append(out,
			mk("disk_read_bytes", float64(d.ReadSectors)*512),
			mk("disk_write_bytes", float64(d.WriteSectors)*512),
			mk("disk_read_ios", float64(d.ReadIOs)),
			mk("disk_write_ios", float64(d.WriteIOs)))
	}
	return out, nil
}
