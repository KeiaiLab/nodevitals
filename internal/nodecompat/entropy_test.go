package nodecompat

import (
	"strings"
	"testing"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
)

func TestEntropy(t *testing.T) {
	procRoot := t.TempDir()
	// Live values (e21): a modern kernel keeps a 256-bit pool, saturated.
	writeProcFile(t, procRoot, "sys/kernel/random/entropy_avail", "256\n")
	writeProcFile(t, procRoot, "sys/kernel/random/poolsize", "256\n")

	golden := `# HELP node_entropy_available_bits Bits of available entropy.
# TYPE node_entropy_available_bits gauge
node_entropy_available_bits 256
# HELP node_entropy_pool_size_bits Bits of entropy pool.
# TYPE node_entropy_pool_size_bits gauge
node_entropy_pool_size_bits 256
`
	if err := testutil.CollectAndCompare(exporterWith(newEntropy(procRoot)), strings.NewReader(golden)); err != nil {
		t.Errorf("entropy exposition drifted:\n%v", err)
	}
}

func TestEntropyMissingFileErrors(t *testing.T) {
	if err := newEntropy(t.TempDir()).Collect(make(chan prometheus.Metric, 8)); err == nil {
		t.Fatal("want error for missing entropy files, got nil")
	}
}

// A missing second file must emit nothing at all: both entropy metrics should
// move together, and a metric already written to the channel cannot be retracted.
func TestEntropyUnparseableFieldEmitsNothing(t *testing.T) {
	procRoot := t.TempDir()
	writeProcFile(t, procRoot, "sys/kernel/random/entropy_avail", "256\n")
	writeProcFile(t, procRoot, "sys/kernel/random/poolsize", "notanumber\n")

	ch := make(chan prometheus.Metric, 8)
	if err := newEntropy(procRoot).Collect(ch); err == nil {
		t.Fatal("want error for an unparseable poolsize field, got nil")
	}
	if len(ch) != 0 {
		t.Fatalf("emitted %d metrics before failing, want 0", len(ch))
	}
}
