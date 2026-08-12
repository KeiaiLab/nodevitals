package nodecompat

import (
	"strings"
	"testing"

	"github.com/prometheus/client_golang/prometheus/testutil"
)

func TestProcsCollector(t *testing.T) {
	procRoot := t.TempDir()
	content := `cpu  123 456 789
procs_running 5
procs_blocked 2
`
	writeProcFile(t, procRoot, "stat", content)

	exp := exporterWith(newProcs(procRoot))
	expected := `
		# HELP node_procs_blocked Number of processes blocked waiting for I/O to complete.
		# TYPE node_procs_blocked gauge
		node_procs_blocked 2
		# HELP node_procs_running Number of processes in runnable state.
		# TYPE node_procs_running gauge
		node_procs_running 5
	`

	if err := testutil.CollectAndCompare(exp, strings.NewReader(expected)); err != nil {
		t.Fatalf("unexpected metrics: %v", err)
	}
}
