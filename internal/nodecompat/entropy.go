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
	entropyAvailDesc = prometheus.NewDesc(
		"node_entropy_available_bits", "Bits of available entropy.", nil, nil)
	entropyPoolDesc = prometheus.NewDesc(
		"node_entropy_pool_size_bits", "Bits of entropy pool.", nil, nil)
)

type entropy struct{ procRoot string }

func newEntropy(procRoot string) subCollector { return &entropy{procRoot: procRoot} }

func (e *entropy) Name() string { return "entropy" }

// Collect reads entropy_avail and poolsize from /proc/sys/kernel/random/.
func (e *entropy) Collect(ch chan<- prometheus.Metric) error {
	// Parse all values before emitting any, so a bad field doesn't leave the
	// channel with a partial set.
	avail, err := readUintFile(filepath.Join(e.procRoot, "sys", "kernel", "random", "entropy_avail"))
	if err != nil {
		return err
	}

	poolSize, err := readUintFile(filepath.Join(e.procRoot, "sys", "kernel", "random", "poolsize"))
	if err != nil {
		return err
	}

	// All parsed successfully; now emit atomically.
	ch <- prometheus.MustNewConstMetric(entropyAvailDesc, prometheus.GaugeValue, avail)
	ch <- prometheus.MustNewConstMetric(entropyPoolDesc, prometheus.GaugeValue, poolSize)
	return nil
}

// readUintFile reads a sysctl-style file holding a single number. Only entropy
// uses it today; it is kept package-level because the /sys-backed groups in
// later phases (rapl, cooling, hwmon) read the same one-value-per-file shape.
func readUintFile(path string) (float64, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return 0, err
	}
	v, err := strconv.ParseFloat(strings.TrimSpace(string(b)), 64)
	if err != nil {
		return 0, fmt.Errorf("%s: %w", path, err)
	}
	return v, nil
}
