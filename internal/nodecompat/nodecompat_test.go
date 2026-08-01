package nodecompat

import (
	"errors"
	"strings"
	"testing"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
)

// fakeSub is a subCollector whose Collect outcome is scripted by the test, so
// Exporter.Collect's health-signal emission can be exercised without a real
// /proc reader.
type fakeSub struct {
	name string
	err  error
}

func (f *fakeSub) Name() string { return f.name }

func (f *fakeSub) Collect(chan<- prometheus.Metric) error { return f.err }

// TestCollectEmitsScrapeHealthSignal pins the health-signal pair
// --no-collector.* takes away from node_exporter's own copy when a native
// group replaces it: a healthy sub-collector reports success=1, a failing one
// success=0, both labeled by Name() — the same shape node_exporter's own
// execute() produces per collector (collector/collector.go).
func TestCollectEmitsScrapeHealthSignal(t *testing.T) {
	e := exporterWith(
		&fakeSub{name: "healthy"},
		&fakeSub{name: "broken", err: errors.New("boom")},
	)

	// Duration is real elapsed time and therefore not byte-comparable against a
	// golden string; restrict the comparison to node_scrape_collector_success
	// and check duration's presence (not its value) separately.
	golden := `# HELP node_scrape_collector_success node_exporter: Whether a collector succeeded.
# TYPE node_scrape_collector_success gauge
node_scrape_collector_success{collector="broken"} 0
node_scrape_collector_success{collector="healthy"} 1
`
	if err := testutil.CollectAndCompare(e, strings.NewReader(golden), "node_scrape_collector_success"); err != nil {
		t.Errorf("scrape success signal drifted:\n%v", err)
	}

	if got := testutil.CollectAndCount(e, "node_scrape_collector_duration_seconds"); got != 2 {
		t.Fatalf("node_scrape_collector_duration_seconds series = %d, want 2 (one per sub-collector)", got)
	}
}
