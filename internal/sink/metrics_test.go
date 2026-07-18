package sink

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

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
