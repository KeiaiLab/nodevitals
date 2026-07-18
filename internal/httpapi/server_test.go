package httpapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/nodevitals/nodevitals/internal/model"
)

type stubSrc struct{ s []model.Sample }

func (s stubSrc) Snapshot() []model.Sample { return s.s }

func TestStateEndpointReturnsSnapshot(t *testing.T) {
	src := stubSrc{s: []model.Sample{{Node: "n", Metric: "load1", Value: 2}}}
	mux := NewServer(src, http.NotFoundHandler())
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
	mux := NewServer(stubSrc{}, http.NotFoundHandler())
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
