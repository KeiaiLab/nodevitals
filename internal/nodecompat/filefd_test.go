package nodecompat

import (
	"strings"
	"testing"

	"github.com/prometheus/client_golang/prometheus/testutil"
)

func TestFileFDCollector(t *testing.T) {
	procRoot := t.TempDir()
	writeProcFile(t, procRoot, "sys/fs/file-nr", "1234\t0\t9223372036854775807\n")

	exp := exporterWith(newFileFD(procRoot))
	expected := `
		# HELP node_filefd_allocated File descriptor statistics: allocated.
		# TYPE node_filefd_allocated gauge
		node_filefd_allocated 1234
		# HELP node_filefd_maximum File descriptor statistics: maximum.
		# TYPE node_filefd_maximum gauge
		node_filefd_maximum 9.223372036854776e+18
	`

	if err := testutil.CollectAndCompare(exp, strings.NewReader(expected)); err != nil {
		t.Fatalf("unexpected metrics: %v", err)
	}
}
