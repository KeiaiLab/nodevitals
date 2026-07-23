package httpapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/KeiaiLab/nodevitals/internal/history"
	"github.com/KeiaiLab/nodevitals/internal/model"
)

type stubSrc struct{ s []model.Sample }

func (s stubSrc) Snapshot() []model.Sample { return s.s }

type stubHist struct {
	res      []history.SeriesHistory
	err      error
	gotSince time.Time
	gotFilt  map[string]string
}

func (s *stubHist) QueryHistory(metric string, since time.Time, filter map[string]string) ([]history.SeriesHistory, error) {
	s.gotSince = since
	s.gotFilt = filter
	return s.res, s.err
}

func TestStateEndpointReturnsSnapshot(t *testing.T) {
	src := stubSrc{s: []model.Sample{{Node: "n", Metric: "load1", Value: 2}}}
	mux := NewServer(src, http.NotFoundHandler(), &stubHist{})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/v1/state")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); ct != "application/json" {
		t.Fatalf("Content-Type = %q, want application/json", ct)
	}
	var got []model.Sample
	json.NewDecoder(resp.Body).Decode(&got)
	if len(got) != 1 || got[0].Metric != "load1" {
		t.Fatalf("bad snapshot: %+v", got)
	}
}

func TestHealthzOK(t *testing.T) {
	mux := NewServer(stubSrc{}, http.NotFoundHandler(), &stubHist{})
	srv := httptest.NewServer(mux)
	defer srv.Close()
	resp, err := http.Get(srv.URL + "/healthz")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("healthz status = %d", resp.StatusCode)
	}
}

func TestHistoryEndpointReturnsQueryResult(t *testing.T) {
	h := &stubHist{res: []history.SeriesHistory{{
		Metric: "gpu_utilization_pct", Node: "e104", Device: "gpu0",
		Points: []history.Point{{Timestamp: time.Unix(1000, 0).UTC(), Value: 42}},
	}}}
	mux := NewServer(stubSrc{}, http.NotFoundHandler(), h)
	srv := httptest.NewServer(mux)
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/v1/history/gpu_utilization_pct?node=e104&device=gpu0&range=90d")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	var got []history.SeriesHistory
	if err := json.NewDecoder(resp.Body).Decode(&got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(got) != 1 || got[0].Points[0].Value != 42 {
		t.Fatalf("bad history response: %+v", got)
	}
	if h.gotFilt["node"] != "e104" || h.gotFilt["device"] != "gpu0" {
		t.Fatalf("query params not forwarded as filter: %+v", h.gotFilt)
	}
	if h.gotSince.IsZero() {
		t.Fatalf("range=90d must resolve to a non-zero since")
	}
}

func TestHistoryEndpointDisabledReturns404(t *testing.T) {
	h := &stubHist{err: history.ErrDisabled}
	mux := NewServer(stubSrc{}, http.NotFoundHandler(), h)
	srv := httptest.NewServer(mux)
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/v1/history/gpu_utilization_pct")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("status = %d, want 404 when history is disabled", resp.StatusCode)
	}
}

func TestHistoryEndpointInvalidRangeReturns400(t *testing.T) {
	mux := NewServer(stubSrc{}, http.NotFoundHandler(), &stubHist{})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/v1/history/gpu_utilization_pct?range=notaduration")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 for an invalid range", resp.StatusCode)
	}
}

func TestParseRange(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	cases := []struct {
		in      string
		wantErr bool
		want    time.Time
	}{
		{"", false, time.Time{}},
		{"90d", false, now.Add(-90 * 24 * time.Hour)},
		{"6h", false, now.Add(-6 * time.Hour)},
		{"0d", true, time.Time{}},
		{"-5d", true, time.Time{}},
		{"garbage", true, time.Time{}},
	}
	for _, c := range cases {
		got, err := parseRange(c.in, now)
		if (err != nil) != c.wantErr {
			t.Errorf("parseRange(%q) err = %v, wantErr %v", c.in, err, c.wantErr)
			continue
		}
		if !c.wantErr && !got.Equal(c.want) {
			t.Errorf("parseRange(%q) = %v, want %v", c.in, got, c.want)
		}
	}
}
