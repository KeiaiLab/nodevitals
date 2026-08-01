package nodecompat

import (
	"strings"
	"testing"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
)

func TestFileFD(t *testing.T) {
	procRoot := t.TempDir()
	// Live shape (e21): "allocated<TAB>unused<TAB>maximum". The middle field has
	// been hardwired to 0 since Linux 2.6 and node_exporter ignores it too.
	writeProcFile(t, procRoot, "sys/fs/file-nr", "9679\t0\t2097152\n")

	golden := `# HELP node_filefd_allocated File descriptor statistics: allocated.
# TYPE node_filefd_allocated gauge
node_filefd_allocated 9679
# HELP node_filefd_maximum File descriptor statistics: maximum.
# TYPE node_filefd_maximum gauge
node_filefd_maximum 2.097152e+06
`
	// Restricted to this group's own names — see loadavg_test.go for why.
	if err := testutil.CollectAndCompare(exporterWith(newFileFD(procRoot)), strings.NewReader(golden),
		"node_filefd_allocated", "node_filefd_maximum"); err != nil {
		t.Errorf("filefd exposition drifted:\n%v", err)
	}
}

func TestFileFDMalformedErrors(t *testing.T) {
	procRoot := t.TempDir()
	writeProcFile(t, procRoot, "sys/fs/file-nr", "9679\n")

	if err := newFileFD(procRoot).Collect(make(chan prometheus.Metric, 8)); err == nil {
		t.Fatal("want error for a 1-field file-nr, got nil")
	}
}

// A line with the right field count but a bad value must emit nothing at all:
// a dashboard showing node_filefd_allocated without node_filefd_maximum is worse
// than showing neither, and a metric already written to the channel cannot be retracted.
// The first field (allocated) parses successfully, but the third (maximum) fails —
// this is the shape that discriminates atomic from non-atomic implementations.
func TestFileFDUnparseableFieldEmitsNothing(t *testing.T) {
	procRoot := t.TempDir()
	writeProcFile(t, procRoot, "sys/fs/file-nr", "9679\t0\tnotanumber\n")

	ch := make(chan prometheus.Metric, 8)
	if err := newFileFD(procRoot).Collect(ch); err == nil {
		t.Fatal("want error for an unparseable file-nr field, got nil")
	}
	if len(ch) != 0 {
		t.Fatalf("emitted %d metrics before failing, want 0", len(ch))
	}
}
