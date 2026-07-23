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
		// This endpoint has no auth (matching /v1/state and /metrics — nodevitals
		// has never put auth on anything, relying entirely on cluster network
		// boundaries), so unlike a range-parse error it can't reject a caller it
		// doesn't trust — the only lever left is bounding how much any one
		// request can force into memory. Without this, an ordinary un-ranged
		// query at the chart's own defaults (5y retention, 5m resolution) can
		// need on the order of 1M points — tens of MB JSON-encoded — against a
		// container memory limit measured in tens of MB. Reject loudly rather
		// than silently truncating: the caller narrows range/node/device and
		// gets exactly what it asked for, instead of a quietly incomplete answer.
		if total := totalPoints(res); total > maxHistoryResponsePoints {
			http.Error(w, fmt.Sprintf(
				"history query would return %d points (limit %d) — narrow with ?range=, ?node=, or ?device=",
				total, maxHistoryResponsePoints), http.StatusRequestEntityTooLarge)
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

// maxRangeDays bounds ?range=Nd — well beyond the chart's own 5y default
// retention (so any legitimately retained range is still reachable), but
// small enough that time.Duration(n)*24*time.Hour cannot silently overflow.
// int64 nanoseconds overflows past ~106,751 days; 36,500 (100y) leaves
// enormous headroom while still rejecting a mistyped/adversarial day count
// long before the multiplication gets anywhere near that ceiling.
const maxRangeDays = 36_500

// maxHistoryResponsePoints bounds how many points a single GET
// /v1/history/{metric} response may carry in total (summed across every
// returned series). Sized generously above real dashboard use (e.g. one
// series over a full year at 5m resolution is ~105k points, several series
// over 90 days is ~130k) while still firmly rejecting "everything, every
// device, full retention" in one unranged, unauthenticated request.
const maxHistoryResponsePoints = 200_000

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
		if err != nil || n <= 0 || n > maxRangeDays {
			return time.Time{}, fmt.Errorf("invalid range %q (want 1-%d days)", s, maxRangeDays)
		}
		return now.Add(-time.Duration(n) * 24 * time.Hour), nil
	}
	d, err := time.ParseDuration(s)
	if err != nil || d <= 0 || d > maxRangeDays*24*time.Hour {
		return time.Time{}, fmt.Errorf("invalid range %q", s)
	}
	return now.Add(-d), nil
}

func totalPoints(res []history.SeriesHistory) int {
	n := 0
	for _, s := range res {
		n += len(s.Points)
	}
	return n
}
