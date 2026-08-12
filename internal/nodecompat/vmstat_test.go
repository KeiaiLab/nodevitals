package nodecompat

import (
	"strings"
	"testing"

	"github.com/prometheus/client_golang/prometheus/testutil"
)

func TestVMStatCollector(t *testing.T) {
	procRoot := t.TempDir()
	content := `nr_free_pages 10000
oom_kill 1
pgfault 54321
pgmajfault 12
`
	writeProcFile(t, procRoot, "vmstat", content)

	exp := exporterWith(newVMStat(procRoot))
	expected := `
		# HELP node_vmstat_oom_kill /proc/vmstat information field oom_kill.
		# TYPE node_vmstat_oom_kill untyped
		node_vmstat_oom_kill 1
		# HELP node_vmstat_pgfault /proc/vmstat information field pgfault.
		# TYPE node_vmstat_pgfault untyped
		node_vmstat_pgfault 54321
		# HELP node_vmstat_pgmajfault /proc/vmstat information field pgmajfault.
		# TYPE node_vmstat_pgmajfault untyped
		node_vmstat_pgmajfault 12
	`

	if err := testutil.CollectAndCompare(exp, strings.NewReader(expected)); err != nil {
		t.Fatalf("unexpected metrics: %v", err)
	}
}
