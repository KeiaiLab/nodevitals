package nodecompat

import (
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
)

// writeProcFile lays down one file under a fake procRoot.
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

// exporterWith builds an Exporter carrying exactly one sub-collector, so a
// golden comparison covers that group and nothing else.
func exporterWith(subs ...subCollector) *Exporter {
	return &Exporter{log: slog.Default(), subs: subs}
}

func TestLoadAvg(t *testing.T) {
	procRoot := t.TempDir()
	// Byte-for-byte the shape a live kernel emits (e21).
	writeProcFile(t, procRoot, "loadavg", "13.62 7.62 5.52 8/5206 1591949\n")

	golden := `# HELP node_load1 1m load average.
# TYPE node_load1 gauge
node_load1 13.62
# HELP node_load15 15m load average.
# TYPE node_load15 gauge
node_load15 5.52
# HELP node_load5 5m load average.
# TYPE node_load5 gauge
node_load5 7.62
`
	// Restricted to this group's own names: Exporter.Collect also emits the
	// node_scrape_collector_success/duration health-signal pair now, and
	// duration's value is real elapsed time, not byte-comparable to a golden.
	if err := testutil.CollectAndCompare(exporterWith(newLoadAvg(procRoot)), strings.NewReader(golden),
		"node_load1", "node_load5", "node_load15"); err != nil {
		t.Errorf("loadavg exposition drifted from node_exporter contract:\n%v", err)
	}
}

// A missing /proc/loadavg must surface as an error from the sub-collector, not
// a panic and not silence — the Exporter decides what to do with it.
func TestLoadAvgMissingFileErrors(t *testing.T) {
	if err := newLoadAvg(t.TempDir()).Collect(make(chan prometheus.Metric, 8)); err == nil {
		t.Fatal("want error for missing /proc/loadavg, got nil")
	}
}

// A truncated line must error rather than emit a partial set: three metrics
// that are supposed to move together should never disagree.
func TestLoadAvgShortLineErrors(t *testing.T) {
	procRoot := t.TempDir()
	writeProcFile(t, procRoot, "loadavg", "1.00 2.00\n")

	if err := newLoadAvg(procRoot).Collect(make(chan prometheus.Metric, 8)); err == nil {
		t.Fatal("want error for a 2-field loadavg, got nil")
	}
}

// A line with the right field count but a bad value must emit nothing at all:
// a dashboard showing node_load1 without node_load5 is worse than showing
// neither, and a metric already written to the channel cannot be retracted.
func TestLoadAvgUnparseableFieldEmitsNothing(t *testing.T) {
	procRoot := t.TempDir()
	writeProcFile(t, procRoot, "loadavg", "13.62 notanumber 5.52 8/5206 1591949\n")

	ch := make(chan prometheus.Metric, 8)
	if err := newLoadAvg(procRoot).Collect(ch); err == nil {
		t.Fatal("want error for an unparseable loadavg field, got nil")
	}
	if len(ch) != 0 {
		t.Fatalf("emitted %d metrics before failing, want 0", len(ch))
	}
}
