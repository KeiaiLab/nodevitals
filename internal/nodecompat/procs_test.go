package nodecompat

import (
	"strings"
	"testing"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
)

func TestProcs(t *testing.T) {
	procRoot := t.TempDir()
	// /proc/stat carries far more than these two lines; the collector must pick
	// them out and ignore everything else.
	writeProcFile(t, procRoot, "stat", `cpu  1 2 3 4 5 6 7 8 9 10
cpu0 1 2 3 4 5 6 7 8 9 10
intr 12345
ctxt 987654
btime 1700000000
processes 4242
procs_running 6
procs_blocked 31
softirq 1 2 3
`)

	golden := `# HELP node_procs_blocked Number of processes blocked waiting for I/O to complete.
# TYPE node_procs_blocked gauge
node_procs_blocked 31
# HELP node_procs_running Number of processes in runnable state.
# TYPE node_procs_running gauge
node_procs_running 6
`
	if err := testutil.CollectAndCompare(exporterWith(newProcs(procRoot)), strings.NewReader(golden)); err != nil {
		t.Errorf("procs exposition drifted:\n%v", err)
	}
}

// A /proc/stat without the procs_ lines (very old kernels, some emulators)
// must error rather than silently report zero running processes.
func TestProcsMissingLinesErrors(t *testing.T) {
	procRoot := t.TempDir()
	writeProcFile(t, procRoot, "stat", "cpu  1 2 3 4\nintr 5\n")

	if err := newProcs(procRoot).Collect(make(chan prometheus.Metric, 8)); err == nil {
		t.Fatal("want error when procs_running/procs_blocked are absent, got nil")
	}
}

// A /proc/stat containing procs_running but not procs_blocked must return an
// error AND emit nothing. The channel is buffered so we can check its length
// after Collect returns — a metric cannot be retracted once sent.
func TestProcsAtomicityOneMetricMissingEmitsNothing(t *testing.T) {
	procRoot := t.TempDir()
	writeProcFile(t, procRoot, "stat", `cpu  1 2 3 4
procs_running 6
intr 5
`)

	ch := make(chan prometheus.Metric, 8)
	if err := newProcs(procRoot).Collect(ch); err == nil {
		t.Fatal("want error when procs_blocked is absent, got nil")
	}
	if len(ch) != 0 {
		t.Fatalf("emitted %d metrics before failing, want 0", len(ch))
	}
}
