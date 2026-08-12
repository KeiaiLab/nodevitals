package nodecompat

import (
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/prometheus/client_golang/prometheus/testutil"
)

func writeProcFile(t *testing.T, procRoot, name, body string) {
	t.Helper()
	path := filepath.Join(procRoot, name)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir for %s: %v", name, err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("write %s: %v", name, err)
	}
}

func exporterWith(subs ...subCollector) *Exporter {
	return &Exporter{log: slog.Default(), subs: subs}
}

func TestLoadAvgCollector(t *testing.T) {
	procRoot := t.TempDir()
	writeProcFile(t, procRoot, "loadavg", "0.15 0.25 0.35 2/180 12345\n")

	exp := exporterWith(newLoadAvg(procRoot))
	expected := `
		# HELP node_load1 1m load average.
		# TYPE node_load1 gauge
		node_load1 0.15
		# HELP node_load15 15m load average.
		# TYPE node_load15 gauge
		node_load15 0.35
		# HELP node_load5 5m load average.
		# TYPE node_load5 gauge
		node_load5 0.25
	`

	if err := testutil.CollectAndCompare(exp, strings.NewReader(expected)); err != nil {
		t.Fatalf("unexpected metrics: %v", err)
	}
}
