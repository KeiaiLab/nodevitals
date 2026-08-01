package nodecompat

import (
	"strings"
	"testing"

	"github.com/prometheus/client_golang/prometheus/testutil"
)

func TestVMStat(t *testing.T) {
	procRoot := t.TempDir()
	// A real /proc/vmstat has ~180 lines; only the allowlisted ones surface.
	writeProcFile(t, procRoot, "vmstat", `nr_free_pages 1234567
nr_zone_inactive_anon 42
pgpgin 1592701043
pgpgout 669113661
pswpin 0
pswpout 0
pgfault 5587768098
pgmajfault 93845
pgscan_kswapd 17
oom_kill 0
`)

	golden := `# HELP node_vmstat_oom_kill /proc/vmstat information field oom_kill.
# TYPE node_vmstat_oom_kill untyped
node_vmstat_oom_kill 0
# HELP node_vmstat_pgfault /proc/vmstat information field pgfault.
# TYPE node_vmstat_pgfault untyped
node_vmstat_pgfault 5.587768098e+09
# HELP node_vmstat_pgmajfault /proc/vmstat information field pgmajfault.
# TYPE node_vmstat_pgmajfault untyped
node_vmstat_pgmajfault 93845
# HELP node_vmstat_pgpgin /proc/vmstat information field pgpgin.
# TYPE node_vmstat_pgpgin untyped
node_vmstat_pgpgin 1.592701043e+09
# HELP node_vmstat_pgpgout /proc/vmstat information field pgpgout.
# TYPE node_vmstat_pgpgout untyped
node_vmstat_pgpgout 6.69113661e+08
# HELP node_vmstat_pswpin /proc/vmstat information field pswpin.
# TYPE node_vmstat_pswpin untyped
node_vmstat_pswpin 0
# HELP node_vmstat_pswpout /proc/vmstat information field pswpout.
# TYPE node_vmstat_pswpout untyped
node_vmstat_pswpout 0
`
	// Restricted to this group's own names — see loadavg_test.go for why.
	if err := testutil.CollectAndCompare(exporterWith(newVMStat(procRoot)), strings.NewReader(golden),
		"node_vmstat_oom_kill", "node_vmstat_pgfault", "node_vmstat_pgmajfault", "node_vmstat_pgpgin",
		"node_vmstat_pgpgout", "node_vmstat_pswpin", "node_vmstat_pswpout"); err != nil {
		t.Errorf("vmstat exposition drifted:\n%v", err)
	}
}

// nr_free_pages and pgscan_kswapd are real fields the default allowlist
// deliberately excludes. Asserting on the surviving series BY NAME (not merely
// counting one survivor) is what makes this test fail if the regex ever starts
// matching the wrong field.
func TestVMStatExcludesNonAllowlisted(t *testing.T) {
	procRoot := t.TempDir()
	writeProcFile(t, procRoot, "vmstat", "nr_free_pages 1\npgscan_kswapd 2\npgfault 3\n")

	golden := `# HELP node_vmstat_pgfault /proc/vmstat information field pgfault.
# TYPE node_vmstat_pgfault untyped
node_vmstat_pgfault 3
`
	// Naming the excluded fields here (not just the survivor) keeps this test's
	// power intact: GatherAndCompare's metricNames filters BOTH the gathered and
	// expected sets, so if the regex regressed and started emitting
	// node_vmstat_nr_free_pages/pgscan_kswapd, they would still surface in the
	// comparison and fail it — while the new node_scrape_collector_* health
	// signal, left unnamed, is filtered out of the comparison entirely.
	if err := testutil.CollectAndCompare(exporterWith(newVMStat(procRoot)), strings.NewReader(golden),
		"node_vmstat_pgfault", "node_vmstat_nr_free_pages", "node_vmstat_pgscan_kswapd"); err != nil {
		t.Errorf("allowlist exposed the wrong field set:\n%v", err)
	}
}
