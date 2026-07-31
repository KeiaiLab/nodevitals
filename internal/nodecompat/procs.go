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
		"node_procs_running", "Number of processes in runnable state.", nil, nil)
	procsBlockedDesc = prometheus.NewDesc(
		"node_procs_blocked", "Number of processes blocked waiting for I/O to complete.", nil, nil)
)

type procs struct{ procRoot string }

func newProcs(procRoot string) subCollector { return &procs{procRoot: procRoot} }

func (p *procs) Name() string { return "processes" }

// Collect scans /proc/stat for its two scheduler summary lines. Everything
// else in that file (per-CPU jiffies, interrupts, context switches) belongs to
// other collectors, so it is skipped here rather than parsed and discarded.
func (p *procs) Collect(ch chan<- prometheus.Metric) error {
	f, err := os.Open(filepath.Join(p.procRoot, "stat"))
	if err != nil {
		return err
	}
	defer f.Close()

	want := map[string]*prometheus.Desc{
		"procs_running": procsRunningDesc,
		"procs_blocked": procsBlockedDesc,
	}
	// Accumulate parsed values before emitting any. A metric on ch cannot be
	// retracted, so if both lines are not found, we must not send any metrics.
	values := make(map[string]float64)

	s := bufio.NewScanner(f)
	for s.Scan() {
		fields := strings.Fields(s.Text())
		if len(fields) != 2 {
			continue
		}
		_, ok := want[fields[0]]
		if !ok {
			continue
		}
		v, err := strconv.ParseFloat(fields[1], 64)
		if err != nil {
			return fmt.Errorf("/proc/stat %s=%q: %w", fields[0], fields[1], err)
		}
		values[fields[0]] = v
	}
	if err := s.Err(); err != nil {
		return err
	}

	// Verify both lines were found before emitting anything.
	if len(values) != len(want) {
		// Reporting a missing line as 0 would read as "nothing is running",
		// which is a stronger claim than "the kernel did not tell us".
		return fmt.Errorf("/proc/stat: found %d of %d procs_ lines", len(values), len(want))
	}

	// All parsed and found; now emit atomically.
	ch <- prometheus.MustNewConstMetric(procsRunningDesc, prometheus.GaugeValue, values["procs_running"])
	ch <- prometheus.MustNewConstMetric(procsBlockedDesc, prometheus.GaugeValue, values["procs_blocked"])
	return nil
}
