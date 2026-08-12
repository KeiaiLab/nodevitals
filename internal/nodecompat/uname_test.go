package nodecompat

import (
	"testing"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
)

func TestUnameCollector(t *testing.T) {
	exp := exporterWith(newUname())
	reg := prometheus.NewRegistry()
	reg.MustRegister(exp)

	n, err := testutil.GatherAndCount(reg, "node_uname_info")
	if err != nil {
		t.Fatalf("failed to gather node_uname_info: %v", err)
	}
	if n != 1 {
		t.Fatalf("expected 1 node_uname_info metric, got %d", n)
	}
}
