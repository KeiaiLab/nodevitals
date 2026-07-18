// Package httpapi serves the REST snapshot, /metrics, and health endpoints.
package httpapi

import (
	"encoding/json"
	"net/http"

	"github.com/KeiaiLab/nodevitals/internal/model"
)

// SnapshotSource provides the current sample snapshot for GET /v1/state.
type SnapshotSource interface {
	Snapshot() []model.Sample
}

func NewServer(src SnapshotSource, metricsHandler http.Handler) *http.ServeMux {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/state", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(src.Snapshot())
	})
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})
	mux.Handle("GET /metrics", metricsHandler)
	return mux
}
