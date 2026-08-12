package nodecompat

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/prometheus/client_golang/prometheus"
)

var (
	procsRunningDesc = prometheus.NewDesc(
		"node_procs_running",
		"Number of processes in runnable state.",
		nil, nil,
	)
	procsBlockedDesc = prometheus.NewDesc(
		"node_procs_blocked",
		"Number of processes blocked waiting for I/O to complete.",
		nil, nil,
	)
)

type procsCollector struct {
	procRoot string
}

func newProcs(procRoot string) subCollector {
	return &procsCollector{procRoot: procRoot}
}

func (c *procsCollector) Name() string { return "procs" }

// node_procs_running / node_procs_blocked belong to upstream's "stat"
// collector, not to a "procs" one — that collector does not exist.
func (c *procsCollector) Supersedes() string { return "stat" }

func (c *procsCollector) Collect(ch chan<- prometheus.Metric) error {
	path := filepath.Join(c.procRoot, "stat")
	file, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("open proc stat: %w", err)
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := scanner.Text()
		if strings.HasPrefix(line, "procs_running ") {
			fields := strings.Fields(line)
			if len(fields) >= 2 {
				if val, err := strconv.ParseFloat(fields[1], 64); err == nil {
					ch <- prometheus.MustNewConstMetric(procsRunningDesc, prometheus.GaugeValue, val)
				}
			}
		} else if strings.HasPrefix(line, "procs_blocked ") {
			fields := strings.Fields(line)
			if len(fields) >= 2 {
				if val, err := strconv.ParseFloat(fields[1], 64); err == nil {
					ch <- prometheus.MustNewConstMetric(procsBlockedDesc, prometheus.GaugeValue, val)
				}
			}
		}
	}

	return scanner.Err()
}
