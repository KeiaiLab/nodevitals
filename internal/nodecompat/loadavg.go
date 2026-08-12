package nodecompat

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/prometheus/client_golang/prometheus"
)

var (
	load1Desc = prometheus.NewDesc(
		"node_load1",
		"1m load average.",
		nil, nil,
	)
	load5Desc = prometheus.NewDesc(
		"node_load5",
		"5m load average.",
		nil, nil,
	)
	load15Desc = prometheus.NewDesc(
		"node_load15",
		"15m load average.",
		nil, nil,
	)
)

type loadAvgCollector struct {
	procRoot string
}

func newLoadAvg(procRoot string) subCollector {
	return &loadAvgCollector{procRoot: procRoot}
}

func (c *loadAvgCollector) Name() string { return "loadavg" }

func (c *loadAvgCollector) Supersedes() string { return "loadavg" }

func (c *loadAvgCollector) Collect(ch chan<- prometheus.Metric) error {
	path := filepath.Join(c.procRoot, "loadavg")
	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read loadavg: %w", err)
	}

	parts := strings.Fields(string(data))
	if len(parts) < 3 {
		return fmt.Errorf("invalid loadavg format: %q", string(data))
	}

	l1, err := strconv.ParseFloat(parts[0], 64)
	if err != nil {
		return fmt.Errorf("parse load1: %w", err)
	}
	l5, err := strconv.ParseFloat(parts[1], 64)
	if err != nil {
		return fmt.Errorf("parse load5: %w", err)
	}
	l15, err := strconv.ParseFloat(parts[2], 64)
	if err != nil {
		return fmt.Errorf("parse load15: %w", err)
	}

	ch <- prometheus.MustNewConstMetric(load1Desc, prometheus.GaugeValue, l1)
	ch <- prometheus.MustNewConstMetric(load5Desc, prometheus.GaugeValue, l5)
	ch <- prometheus.MustNewConstMetric(load15Desc, prometheus.GaugeValue, l15)
	return nil
}
