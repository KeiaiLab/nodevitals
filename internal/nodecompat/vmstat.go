package nodecompat

import (
	"bufio"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"github.com/prometheus/client_golang/prometheus"
)

// vmStatAllowlist is node_exporter's default --collector.vmstat.fields value.
// /proc/vmstat carries roughly 180 fields whose meaning and cardinality shift
// between kernel releases, so upstream ships a conservative subset and so do
// we — serving the extra fields would be a contract break in the "more than
// the original" direction, which breaks dashboards just as surely as "less".
var vmStatAllowlist = regexp.MustCompile(`^(oom_kill|pgpg|pswp|pg.*fault).*`)

type vmStat struct{ procRoot string }

func newVMStat(procRoot string) subCollector { return &vmStat{procRoot: procRoot} }

func (v *vmStat) Name() string { return "vmstat" }

// Collect emits every allowlisted field as an untyped metric. Untyped is
// upstream's choice and it is the honest one: /proc/vmstat mixes monotonic
// counters (pgfault) with gauges (nr_free_pages) in one flat format, and
// nothing in the file says which is which.
func (v *vmStat) Collect(ch chan<- prometheus.Metric) error {
	f, err := os.Open(filepath.Join(v.procRoot, "vmstat"))
	if err != nil {
		return err
	}
	defer f.Close()

	s := bufio.NewScanner(f)
	for s.Scan() {
		fields := strings.Fields(s.Text())
		if len(fields) != 2 || !vmStatAllowlist.MatchString(fields[0]) {
			continue
		}
		val, err := strconv.ParseFloat(fields[1], 64)
		if err != nil {
			// One unparseable field must not cost the rest of the file.
			continue
		}
		ch <- prometheus.MustNewConstMetric(
			prometheus.NewDesc(
				"node_vmstat_"+fields[0],
				"/proc/vmstat information field "+fields[0]+".",
				nil, nil),
			prometheus.UntypedValue, val)
	}
	return s.Err()
}
