package nodecompat

import (
	"strings"
	"testing"

	"github.com/prometheus/client_golang/prometheus/testutil"
)

func TestEntropyCollector(t *testing.T) {
	procRoot := t.TempDir()
	writeProcFile(t, procRoot, "sys/kernel/random/entropy_avail", "256\n")
	writeProcFile(t, procRoot, "sys/kernel/random/poolsize", "4096\n")

	exp := exporterWith(newEntropy(procRoot))
	expected := `
		# HELP node_entropy_available_bits Bits of available entropy.
		# TYPE node_entropy_available_bits gauge
		node_entropy_available_bits 256
		# HELP node_entropy_pool_size_bits Bits of entropy pool size.
		# TYPE node_entropy_pool_size_bits gauge
		node_entropy_pool_size_bits 4096
	`

	if err := testutil.CollectAndCompare(exp, strings.NewReader(expected)); err != nil {
		t.Fatalf("unexpected metrics: %v", err)
	}
}
