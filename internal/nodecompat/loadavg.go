package nodecompat

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/prometheus/client_golang/prometheus"
)

// loadAvgDescs are node_exporter's three load descriptors, in the order the
// fields appear in /proc/loadavg.
var loadAvgDescs = [3]*prometheus.Desc{
	prometheus.NewDesc("node_load1", "1m load average.", nil, nil),
	prometheus.NewDesc("node_load5", "5m load average.", nil, nil),
	prometheus.NewDesc("node_load15", "15m load average.", nil, nil),
}

type loadAvg struct{ procRoot string }

func newLoadAvg(procRoot string) subCollector { return &loadAvg{procRoot: procRoot} }

func (l *loadAvg) Name() string { return "loadavg" }

// Collect parses the first three fields of /proc/loadavg. The remaining two
// (runnable/total processes, last PID) belong to other collectors.
func (l *loadAvg) Collect(ch chan<- prometheus.Metric) error {
	b, err := os.ReadFile(filepath.Join(l.procRoot, "loadavg"))
	if err != nil {
		return err
	}
	fields := strings.Fields(string(b))
	if len(fields) < len(loadAvgDescs) {
		// Emit nothing rather than a partial set: three averages that are
		// supposed to move together must never disagree on a dashboard.
		return fmt.Errorf("loadavg: want at least %d fields, got %d", len(loadAvgDescs), len(fields))
	}
	// Parse all three values before emitting any, so a bad field doesn't
	// leave the channel with a partial set.
	var values [3]float64
	for i := range values {
		v, err := strconv.ParseFloat(fields[i], 64)
		if err != nil {
			return fmt.Errorf("loadavg field %d (%q): %w", i, fields[i], err)
		}
		values[i] = v
	}
	// All parsed successfully; now emit atomically.
	for i, d := range loadAvgDescs {
		ch <- prometheus.MustNewConstMetric(d, prometheus.GaugeValue, values[i])
	}
	return nil
}
