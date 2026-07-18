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
