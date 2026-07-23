// Package httpapi serves the REST snapshot, /metrics, and health endpoints.
package httpapi

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/KeiaiLab/nodevitals/internal/history"
	"github.com/KeiaiLab/nodevitals/internal/model"
)

// SnapshotSource provides the current sample snapshot for GET /v1/state.
type SnapshotSource interface {
	Snapshot() []model.Sample
}

// HistorySource provides downsampled long-term history for
// GET /v1/history/{metric}. A nil-history-backed implementation returns
// history.ErrDisabled, which the handler maps to 404 rather than 500 — "not
// configured" isn't a server error.
type HistorySource interface {
	QueryHistory(metric string, since time.Time, filter map[string]string) ([]history.SeriesHistory, error)
}

func NewServer(src SnapshotSource, metricsHandler http.Handler, hist HistorySource) *http.ServeMux {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/state", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(src.Snapshot())
	})
	mux.HandleFunc("GET /v1/history/{metric}", func(w http.ResponseWriter, r *http.Request) {
		since, err := parseRange(r.URL.Query().Get("range"), time.Now())
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		filter := make(map[string]string)
		for _, k := range []string{"node", "device"} {
			if v := r.URL.Query().Get(k); v != "" {
				filter[k] = v
			}
		}
		res, err := hist.QueryHistory(r.PathValue("metric"), since, filter)
		switch {
		case errors.Is(err, history.ErrDisabled):
			http.Error(w, "history not enabled", http.StatusNotFound)
			return
		case err != nil:
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(res)
	})
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})
	mux.Handle("GET /metrics", metricsHandler)
	return mux
}

// parseRange turns a ?range= value into a "since" timestamp relative to now.
// Empty means unbounded (history.Store.Query treats a zero time.Time as "no
// lower bound"). Accepts a bare day count ("90d") on top of everything
// time.ParseDuration already understands ("6h", "45m") — day counts are how
// people actually think about hardware history, and ParseDuration has no
// day unit.
func parseRange(s string, now time.Time) (time.Time, error) {
	if s == "" {
		return time.Time{}, nil
	}
	if days, ok := strings.CutSuffix(s, "d"); ok {
		n, err := strconv.Atoi(days)
		if err != nil || n <= 0 {
			return time.Time{}, fmt.Errorf("invalid range %q", s)
		}
		return now.Add(-time.Duration(n) * 24 * time.Hour), nil
	}
	d, err := time.ParseDuration(s)
	if err != nil || d <= 0 {
		return time.Time{}, fmt.Errorf("invalid range %q", s)
	}
	return now.Add(-d), nil
}
