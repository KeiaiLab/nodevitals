package nodecompat

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/prometheus/client_golang/prometheus"
)

var (
	entropyAvailDesc = prometheus.NewDesc(
		"node_entropy_available_bits",
		"Bits of available entropy.",
		nil, nil,
	)
	entropyPoolSizeDesc = prometheus.NewDesc(
		"node_entropy_pool_size_bits",
		"Bits of entropy pool size.",
		nil, nil,
	)
)

type entropyCollector struct {
	procRoot string
}

func newEntropy(procRoot string) subCollector {
	return &entropyCollector{procRoot: procRoot}
}

func (c *entropyCollector) Name() string { return "entropy" }

func (c *entropyCollector) Collect(ch chan<- prometheus.Metric) error {
	availPath := filepath.Join(c.procRoot, "sys/kernel/random/entropy_avail")
	if data, err := os.ReadFile(availPath); err == nil {
		if val, err := strconv.ParseFloat(strings.TrimSpace(string(data)), 64); err == nil {
			ch <- prometheus.MustNewConstMetric(entropyAvailDesc, prometheus.GaugeValue, val)
		}
	}

	poolPath := filepath.Join(c.procRoot, "sys/kernel/random/poolsize")
	if data, err := os.ReadFile(poolPath); err == nil {
		if val, err := strconv.ParseFloat(strings.TrimSpace(string(data)), 64); err == nil {
			ch <- prometheus.MustNewConstMetric(entropyPoolSizeDesc, prometheus.GaugeValue, val)
		}
	}

	return nil
}
