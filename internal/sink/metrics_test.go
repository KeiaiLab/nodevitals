package sink

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/KeiaiLab/nodevitals/internal/model"
)

func TestMetricsExposesLatestSample(t *testing.T) {
	m := NewMetrics()
	m.Update([]model.Sample{
		{Node: "n1", Tier: "core", Device: "cpu", Metric: "load1", Value: 1.5},
	})

	srv := httptest.NewServer(m.Handler())
	defer srv.Close()

	resp, err := http.Get(srv.URL)
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	body := string(raw)

	if !strings.Contains(body, `nodevitals_hw_load1`) {
		t.Fatalf("missing metric name in output:\n%s", body)
	}
	if !strings.Contains(body, `device="cpu"`) || !strings.Contains(body, `1.5`) {
		t.Fatalf("missing labels/value:\n%s", body)
	}
}

func TestMetricsExposesCounterWithTotalNameAndType(t *testing.T) {
	m := NewMetrics()
	m.Update([]model.Sample{
		{Node: "n1", Tier: "core", Device: "eth0", Metric: "net_rx_bytes_total", Kind: model.KindCounter, Value: 5000},
	})
	srv := httptest.NewServer(m.Handler())
	defer srv.Close()
	resp, err := http.Get(srv.URL)
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	body := string(raw)
	if !strings.Contains(body, "# TYPE nodevitals_hw_net_rx_bytes_total counter") {
		t.Fatalf("counter TYPE declaration missing:\n%s", body)
	}
	if !strings.Contains(body, `nodevitals_hw_net_rx_bytes_total{device="eth0"`) {
		t.Fatalf("counter sample line missing:\n%s", body)
	}
}

func TestMetricsUpdateReplacesSnapshot(t *testing.T) {
	m := NewMetrics()
	m.Update([]model.Sample{{Node: "n", Tier: "core", Device: "cpu", Metric: "load1", Value: 1}})
	m.Update([]model.Sample{{Node: "n", Tier: "core", Device: "cpu", Metric: "load1", Value: 9}})

	srv := httptest.NewServer(m.Handler())
	defer srv.Close()
	resp, err := http.Get(srv.URL)
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(raw), "9") {
		t.Fatalf("expected updated value 9:\n%s", string(raw))
	}
}

// TestMetricsPromotesSampleLabels is the load-bearing proof that G1 identity
// labels reach /metrics: the sink must promote Sample.Labels onto the const
// metric's variable labels. A 200 (not 500) also proves the promoted desc is
// self-consistent — this test fails before the label-promotion change.
func TestMetricsPromotesSampleLabels(t *testing.T) {
	m := NewMetrics()
	m.Update([]model.Sample{{
		Node: "n1", Tier: "gpu", Device: "gpu0", Metric: "gpu_utilization_pct", Value: 55,
		Labels: map[string]string{
			"gpu_uuid":   "GPU-abc",
			"gpu_model":  "NVIDIA A100",
			"pci_bus_id": "00000000:65:00.0",
		},
	}})

	srv := httptest.NewServer(m.Handler())
	defer srv.Close()
	resp, err := http.Get(srv.URL)
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	body := string(raw)

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200 (inconsistent descriptor would 500):\n%s", resp.StatusCode, body)
	}

	var line string
	for _, l := range strings.Split(body, "\n") {
		if strings.HasPrefix(l, "nodevitals_hw_gpu_utilization_pct{") {
			line = l
			break
		}
	}
	if line == "" {
		t.Fatalf("no nodevitals_hw_gpu_utilization_pct sample line:\n%s", body)
	}
	for _, want := range []string{
		`gpu_uuid="GPU-abc"`,
		`gpu_model="NVIDIA A100"`,
		`pci_bus_id="00000000:65:00.0"`,
	} {
		if !strings.Contains(line, want) {
			t.Fatalf("utilization line missing promoted label %s:\n%s", want, line)
		}
	}
}

// TestMetricsMixedLabeledAndUnlabeledStaysConsistent guards backward compat and
// determinism: a labeled gpu sample and a nil-Labels core sample in the same
// snapshot must both scrape 200, with the unlabeled one keeping the bare
// [node,tier,device] shape (no collision, no inconsistent-descriptor 500).
func TestMetricsMixedLabeledAndUnlabeledStaysConsistent(t *testing.T) {
	m := NewMetrics()
	m.Update([]model.Sample{
		{Node: "n1", Tier: "gpu", Device: "gpu0", Metric: "gpu_utilization_pct", Value: 55,
			Labels: map[string]string{"gpu_uuid": "GPU-abc"}},
		{Node: "n1", Tier: "core", Device: "cpu", Metric: "load1", Value: 1.5}, // nil Labels
	})

	srv := httptest.NewServer(m.Handler())
	defer srv.Close()
	resp, err := http.Get(srv.URL)
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	body := string(raw)

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200:\n%s", resp.StatusCode, body)
	}
	if !strings.Contains(body, "nodevitals_hw_gpu_utilization_pct") {
		t.Fatalf("labeled gpu metric missing:\n%s", body)
	}
	// unlabeled sample keeps the exact bare shape (labels sorted alphabetically).
	if !strings.Contains(body, `nodevitals_hw_load1{device="cpu",node="n1",tier="core"} 1.5`) {
		t.Fatalf("unlabeled metric should keep bare [node,tier,device] shape:\n%s", body)
	}
}

func TestMetricsRecordDroppedExposesCounter(t *testing.T) {
	m := NewMetrics()
	m.RecordDropped("webhook-a", 3)
	m.RecordDropped("webhook-a", 2) // accumulates → 5
	m.RecordDropped("webhook-b", 1)
	m.RecordDropped("webhook-b", 0) // no-op

	srv := httptest.NewServer(m.Handler())
	defer srv.Close()
	resp, err := http.Get(srv.URL)
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	body := string(raw)

	if !strings.Contains(body, "# TYPE nodevitals_delivery_dropped_total counter") {
		t.Fatalf("dropped counter TYPE missing:\n%s", body)
	}
	if !strings.Contains(body, `nodevitals_delivery_dropped_total{sink="webhook-a"} 5`) {
		t.Fatalf("want webhook-a dropped=5:\n%s", body)
	}
	if !strings.Contains(body, `nodevitals_delivery_dropped_total{sink="webhook-b"} 1`) {
		t.Fatalf("want webhook-b dropped=1:\n%s", body)
	}
}

// conflictingCollector exports a metric name with help text that disagrees
// with another collector's — the shape a user-written textfile .prom takes
// when it reuses a built-in collector's metric name.
type conflictingCollector struct{ help string }

// Describe sends nothing, which makes this an *unchecked* collector — the
// registry accepts it without comparing descriptors, so a name/help conflict
// only surfaces at scrape time. That is precisely how node_exporter's textfile
// collector behaves: the .prom file is read at collect time, so nothing can be
// validated up front.
func (c conflictingCollector) Describe(chan<- *prometheus.Desc) {}

func (c conflictingCollector) Collect(ch chan<- prometheus.Metric) {
	ch <- prometheus.MustNewConstMetric(
		prometheus.NewDesc("node_edac_correctable_errors_total", c.help, nil, nil),
		prometheus.GaugeValue, 0,
	)
}

func TestHandlerServesDespiteConflictingHelpText(t *testing.T) {
	// Two collectors, same metric name, different help. client_golang's default
	// error handling rejects the entire scrape with a 500 — which on a real
	// node meant every series disappeared because one ansible-written .prom
	// reused a built-in collector's name. The endpoint must keep serving the
	// metrics it can and report the conflict through the log instead.
	m := NewMetrics()
	// A known-good series that must survive the conflicting one.
	m.RecordDropped("webhook", 3)
	if err := m.Register(conflictingCollector{help: "Total correctable memory errors."}); err != nil {
		t.Fatalf("register first: %v", err)
	}
	if err := m.Register(conflictingCollector{help: "Metric read from /var/lib/node_exporter/textfile_collector/edac.prom"}); err != nil {
		t.Fatalf("register second: %v", err)
	}

	rec := httptest.NewRecorder()
	m.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/metrics", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("conflicting help text must not fail the scrape, got HTTP %d: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "nodevitals_delivery_dropped_total") {
		t.Fatalf("unrelated metrics must still be served, body:\n%s", rec.Body.String())
	}
}

// TestMetricsRecordEvents proves the event stream reaches /metrics: an async
// XID error carries its Xid code as the xid label, an engine (threshold)
// transition leaves it empty, and repeats accumulate as a counter.
func TestMetricsRecordEvents(t *testing.T) {
	m := NewMetrics()
	xid := model.Event{
		Node: "n1", Tier: "gpu", Device: "gpu0",
		Condition: "gpu_xid_error", Phase: model.PhaseEnter, Severity: "critical",
		Detail: map[string]any{"xid": uint64(79), "description": "GPU has fallen off the bus"},
	}
	engine := model.Event{
		Node: "n1", Tier: "core", Device: "cpu",
		Condition: "high_load", Phase: model.PhaseEnter, Severity: "warning",
	}
	m.RecordEvents([]model.Event{xid, engine})
	m.RecordEvents([]model.Event{xid})

	srv := httptest.NewServer(m.Handler())
	defer srv.Close()
	resp, err := http.Get(srv.URL)
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	body := string(raw)

	if !strings.Contains(body, "# TYPE nodevitals_events_total counter") {
		t.Fatalf("counter TYPE declaration missing:\n%s", body)
	}
	xidLine := `nodevitals_events_total{condition="gpu_xid_error",device="gpu0",node="n1",phase="ENTER",severity="critical",tier="gpu",xid="79"} 2`
	if !strings.Contains(body, xidLine) {
		t.Fatalf("XID event line missing or wrong count, want %q in:\n%s", xidLine, body)
	}
	if !strings.Contains(body, `nodevitals_events_total{condition="high_load",device="cpu",node="n1",phase="ENTER",severity="warning",tier="core",xid=""} 1`) {
		t.Fatalf("engine event line missing (empty xid label):\n%s", body)
	}
}
