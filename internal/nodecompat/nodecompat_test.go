package nodecompat

import (
	"errors"
	"log/slog"
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

// embeddedHealthStub reproduces the two descriptors internal/nodeexporter's
// embedded collector set declares for its own per-collector health signal —
// node_scrape_collector_success and node_scrape_collector_duration_seconds,
// each with empty constLabels and one "collector" variable label (see
// NodeCollector.Describe/execute in the vendored
// github.com/prometheus/node_exporter/collector/collector.go). It stands in
// for internal/nodeexporter.New in these tests: that constructor parses
// global kingpin flags through a sync.Once and cannot be built fresh per
// test, but a registry collision is decided by Desc identity alone
// (fqName+constLabels — see TestScrapeHealthDescriptorCollidesWithEmbeddedNodeExporter),
// so this stand-in reproduces the exact e21 failure without it.
type embeddedHealthStub struct{}

var (
	embeddedScrapeDurationDesc = prometheus.NewDesc(
		"node_scrape_collector_duration_seconds",
		"node_exporter: Duration of a collector scrape.",
		[]string{"collector"}, nil)
	embeddedScrapeSuccessDesc = prometheus.NewDesc(
		"node_scrape_collector_success",
		"node_exporter: Whether a collector succeeded.",
		[]string{"collector"}, nil)
	// embeddedOtherDesc stands in for the ~40 other descriptors
	// (node_cpu_seconds_total, node_memory_*, ...) a live NodeCollector also
	// declares alongside the two health ones. Without this, the stub's
	// Describe would yield ONLY the two shared health descriptors, and
	// prometheus.Registry's collectorID (an XOR over every unique desc ID a
	// Collector declares — see Registry.Register) would then coincidentally
	// equal the health-only Exporter's own collectorID; the registry would
	// mistake that for RE-registering the same Collector and return
	// AlreadyRegisteredError instead of the real bug's "descriptor already
	// exists" error. A live NodeCollector never collapses to only-the-two
	// descriptors, so this extra one keeps the stub's collectorID honest.
	embeddedOtherDesc = prometheus.NewDesc(
		"node_stub_other_metric",
		"stand-in for the ~40 unrelated descriptors a live NodeCollector also declares.",
		nil, nil)
)

func (embeddedHealthStub) Describe(ch chan<- *prometheus.Desc) {
	ch <- embeddedScrapeDurationDesc
	ch <- embeddedScrapeSuccessDesc
	ch <- embeddedOtherDesc
}

func (embeddedHealthStub) Collect(ch chan<- prometheus.Metric) {
	for _, name := range []string{"cpu", "diskstats"} {
		ch <- prometheus.MustNewConstMetric(embeddedScrapeDurationDesc, prometheus.GaugeValue, 0.0001, name)
		ch <- prometheus.MustNewConstMetric(embeddedScrapeSuccessDesc, prometheus.GaugeValue, 1, name)
	}
	ch <- prometheus.MustNewConstMetric(embeddedOtherDesc, prometheus.GaugeValue, 1)
}

// TestScrapeHealthDescriptorCollidesWithEmbeddedNodeExporter pins the P0
// found live on e21 (nodeExporter.enabled=true + nativeCollectors=true made
// the agent os.Exit(1) at startup) and the reason review missed it:
// prometheus.Registry's collision check is Desc.id, a hash of
// fqName+constLabels ONLY (see Desc.id's doc comment and Registry.Register in
// client_golang/prometheus/registry.go) — the "collector" variable label's
// eventual *values* are irrelevant to it, so the fact that nodecompat and the
// embedded set always emit disjoint {collector="..."} values (the six native
// groups are --no-collector'd on the embedded side) does not avoid the
// collision. Two Collectors simply must not both declare a descriptor with
// this fqName+constLabels while both are registered in the same registry —
// no matter what values either one's variable labels take.
func TestScrapeHealthDescriptorCollidesWithEmbeddedNodeExporter(t *testing.T) {
	newNative := func(emit bool) *Exporter {
		return &Exporter{log: slog.Default(), emitScrapeHealth: emit, subs: []subCollector{&fakeSub{name: "loadavg"}}}
	}

	t.Run("embedded registered and health OFF registers cleanly", func(t *testing.T) {
		reg := prometheus.NewRegistry()
		if err := reg.Register(embeddedHealthStub{}); err != nil {
			t.Fatalf("register embedded stub: %v", err)
		}
		if err := reg.Register(newNative(false)); err != nil {
			t.Fatalf("emitScrapeHealth=false alongside the embedded set must not collide, got: %v", err)
		}
	})

	t.Run("embedded registered and health ON collides", func(t *testing.T) {
		reg := prometheus.NewRegistry()
		if err := reg.Register(embeddedHealthStub{}); err != nil {
			t.Fatalf("register embedded stub: %v", err)
		}
		err := reg.Register(newNative(true))
		if err == nil {
			t.Fatal("want a duplicate-descriptor error registering emitScrapeHealth=true alongside the embedded stub, got nil — this is the exact e21 CrashLoop this test pins closed")
		}
		if !strings.Contains(err.Error(), "already exists with the same fully-qualified name and const label values") {
			t.Fatalf("got error %q, want the fqName+constLabels collision message", err)
		}
	})

	t.Run("no embedded collector and health ON registers and the series appear", func(t *testing.T) {
		reg := prometheus.NewRegistry()
		nc := newNative(true)
		if err := reg.Register(nc); err != nil {
			t.Fatalf("emitScrapeHealth=true with no embedded collector must register cleanly, got: %v", err)
		}
		if got := testutil.CollectAndCount(nc, "node_scrape_collector_success"); got != 1 {
			t.Fatalf("node_scrape_collector_success series = %d, want 1", got)
		}
		if got := testutil.CollectAndCount(nc, "node_scrape_collector_duration_seconds"); got != 1 {
			t.Fatalf("node_scrape_collector_duration_seconds series = %d, want 1", got)
		}
	})
}
