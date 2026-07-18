package collector

import (
	"context"
	"fmt"
	"time"

	"github.com/prometheus/procfs"
	"github.com/KeiaiLab/nodevitals/internal/model"
)

type netCollector struct {
	node     string
	procRoot string
}

// NewNet reports per-interface network counters from /proc/net/dev (loopback skipped).
func NewNet(node, procRoot string) Collector { return &netCollector{node: node, procRoot: procRoot} }

func (c *netCollector) Name() string { return "net" }

func (c *netCollector) Collect(ctx context.Context) ([]model.Sample, error) {
	fs, err := procfs.NewFS(c.procRoot)
	if err != nil {
		return nil, fmt.Errorf("open procfs %q: %w", c.procRoot, err)
	}
	nd, err := fs.NetDev()
	if err != nil {
		return nil, fmt.Errorf("read net/dev: %w", err)
	}
	now := time.Now().UTC()
	var out []model.Sample
	for iface, line := range nd {
		if iface == "lo" {
			continue
		}
		mk := func(metric string, v uint64) model.Sample {
			return model.Sample{Node: c.node, Tier: "core", Device: iface, Metric: metric, Value: float64(v), Timestamp: now}
		}
		out = append(out,
			mk("net_rx_bytes", line.RxBytes), mk("net_tx_bytes", line.TxBytes),
			mk("net_rx_errors", line.RxErrors), mk("net_tx_errors", line.TxErrors))
	}
	return out, nil
}
