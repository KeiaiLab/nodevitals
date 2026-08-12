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

var vmstatAllowlist = map[string]string{
	"oom_kill":   "oom_kill",
	"pgfault":    "pgfault",
	"pgmajfault": "pgmajfault",
	"pgpgin":     "pgpgin",
	"pgpgout":    "pgpgout",
	"pswpin":     "pswpin",
	"pswpout":    "pswpout",
}

type vmstatCollector struct {
	procRoot string
}

func newVMStat(procRoot string) subCollector {
	return &vmstatCollector{procRoot: procRoot}
}

func (c *vmstatCollector) Name() string { return "vmstat" }

func (c *vmstatCollector) Supersedes() string { return "vmstat" }

func (c *vmstatCollector) Collect(ch chan<- prometheus.Metric) error {
	path := filepath.Join(c.procRoot, "vmstat")
	file, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("open vmstat: %w", err)
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) < 2 {
			continue
		}
		key := fields[0]
		if _, ok := vmstatAllowlist[key]; ok {
			val, err := strconv.ParseFloat(fields[1], 64)
			if err != nil {
				continue
			}
			desc := prometheus.NewDesc(
				"node_vmstat_"+key,
				"/proc/vmstat information field "+key+".",
				nil, nil,
			)
			ch <- prometheus.MustNewConstMetric(desc, prometheus.UntypedValue, val)
		}
	}
	return scanner.Err()
}
