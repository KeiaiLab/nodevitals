package history

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/KeiaiLab/nodevitals/internal/model"
)

func mustOpen(t *testing.T, flushInterval, retention time.Duration, metrics ...string) *Store {
	t.Helper()
	s, err := Open(t.TempDir(), metrics, flushInterval, retention)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func gpuSample(node, device string, util float64, ts time.Time) model.Sample {
	return model.Sample{
		Node: node, Tier: "gpu", Device: device, Metric: "gpu_utilization_pct", Value: util, Timestamp: ts,
		Labels: map[string]string{"gpu_uuid": "GPU-" + device, "gpu_model": "RTX 4070"},
	}
}

func TestIngestFlushesAverageAtWallClockBoundary(t *testing.T) {
	s := mustOpen(t, 5*time.Minute, 24*time.Hour, "gpu_utilization_pct")
	base := time.Date(2026, 1, 1, 10, 0, 0, 0, time.UTC) // exactly on a 5m boundary

	// Three ticks inside the same window: avg must be (10+20+30)/3=20, not
	// the last value seen.
	for i, v := range []float64{10, 20, 30} {
		now := base.Add(time.Duration(i) * time.Minute)
		if err := s.Ingest([]model.Sample{gpuSample("e104", "gpu0", v, now)}, now); err != nil {
			t.Fatalf("Ingest: %v", err)
		}
	}
	// Nothing flushed yet — window hasn't closed.
	res, err := s.Query("gpu_utilization_pct", time.Time{}, nil)
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	if len(res) != 0 {
		t.Fatalf("expected no flushed points before window close, got %+v", res)
	}

	// Crossing the boundary flushes exactly one averaged point, timestamped
	// at the boundary itself (not at `now`).
	closeTime := base.Add(5 * time.Minute)
	if err := s.Ingest(nil, closeTime); err != nil {
		t.Fatalf("Ingest: %v", err)
	}
	res, err = s.Query("gpu_utilization_pct", time.Time{}, nil)
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	if len(res) != 1 {
		t.Fatalf("want 1 series, got %d: %+v", len(res), res)
	}
	pts := res[0].Points
	if len(pts) != 1 {
		t.Fatalf("want 1 point, got %d: %+v", len(pts), pts)
	}
	if pts[0].Value != 20 {
		t.Fatalf("avg = %v, want 20", pts[0].Value)
	}
	// The point is timestamped at the boundary the window closed AT
	// (closeTime = base+5m), not the window's start (base) — matching how
	// Prometheus recording rules stamp results at evaluation time.
	if !pts[0].Timestamp.Equal(closeTime) {
		t.Fatalf("point timestamp = %v, want window-close boundary %v", pts[0].Timestamp, closeTime)
	}
}

func TestIngestSkipsNonAllowlistedMetrics(t *testing.T) {
	s := mustOpen(t, time.Minute, time.Hour, "gpu_utilization_pct")
	now := time.Date(2026, 1, 1, 0, 1, 0, 0, time.UTC)
	other := model.Sample{Node: "e104", Tier: "gpu", Device: "gpu0", Metric: "gpu_power_watts", Value: 250, Timestamp: now}
	if err := s.Ingest([]model.Sample{other}, now); err != nil {
		t.Fatalf("Ingest: %v", err)
	}
	res, err := s.Query("gpu_power_watts", time.Time{}, nil)
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	if len(res) != 0 {
		t.Fatalf("non-allowlisted metric must never be retained, got %+v", res)
	}
}

func TestQueryFiltersByNodeAndDevice(t *testing.T) {
	s := mustOpen(t, time.Minute, time.Hour, "gpu_utilization_pct")
	now := time.Date(2026, 1, 1, 0, 1, 0, 0, time.UTC)
	samples := []model.Sample{
		gpuSample("e104", "gpu0", 10, now),
		gpuSample("e104", "gpu1", 20, now),
		gpuSample("e105", "gpu0", 30, now),
	}
	if err := s.Ingest(samples, now); err != nil {
		t.Fatalf("Ingest: %v", err)
	}
	if err := s.Ingest(nil, now.Add(time.Minute)); err != nil { // cross the flush boundary
		t.Fatalf("Ingest (flush): %v", err)
	}

	res, err := s.Query("gpu_utilization_pct", time.Time{}, map[string]string{"node": "e104", "device": "gpu0"})
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	if len(res) != 1 || res[0].Node != "e104" || res[0].Device != "gpu0" {
		t.Fatalf("filter node=e104,device=gpu0 = %+v, want exactly the e104/gpu0 series", res)
	}
	if res[0].Points[0].Value != 10 {
		t.Fatalf("filtered series value = %v, want 10", res[0].Points[0].Value)
	}

	all, err := s.Query("gpu_utilization_pct", time.Time{}, nil)
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	if len(all) != 3 {
		t.Fatalf("no filter must return all 3 series, got %d", len(all))
	}
}

func TestPruneDeletesPointsOlderThanRetention(t *testing.T) {
	s := mustOpen(t, time.Hour, 24*time.Hour, "gpu_utilization_pct")
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

	// Flush one point per hour for 30 hours — retention is 24h, so only the
	// last 24 (roughly) must survive after the final flush's prune pass.
	for i := 0; i < 30; i++ {
		now := base.Add(time.Duration(i) * time.Hour)
		if err := s.Ingest([]model.Sample{gpuSample("e104", "gpu0", float64(i), now)}, now); err != nil {
			t.Fatalf("Ingest at hour %d: %v", i, err)
		}
	}
	res, err := s.Query("gpu_utilization_pct", time.Time{}, nil)
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	if len(res) != 1 {
		t.Fatalf("want 1 series, got %d", len(res))
	}
	pts := res[0].Points
	if len(pts) == 0 || len(pts) > 25 {
		t.Fatalf("want roughly <=25 surviving points after 24h retention prune, got %d", len(pts))
	}
	oldest := pts[0].Timestamp
	cutoff := pts[len(pts)-1].Timestamp.Add(-24 * time.Hour)
	if oldest.Before(cutoff) {
		t.Fatalf("surviving point %v is older than the 24h retention window (cutoff %v)", oldest, cutoff)
	}
}

func TestSeriesKeyRoundTrip(t *testing.T) {
	s := model.Sample{
		Metric: "gpu_utilization_pct", Node: "e104", Tier: "gpu", Device: "gpu0",
		Labels: map[string]string{"gpu_uuid": "GPU-abc", "pci_bus_id": "00000000:08:00.0"},
	}
	metric, node, tier, device, labels := parseSeriesKey(seriesKeyFor(s))
	if metric != s.Metric || node != s.Node || tier != s.Tier || device != s.Device {
		t.Fatalf("round trip identity = %q/%q/%q/%q, want %q/%q/%q/%q", metric, node, tier, device, s.Metric, s.Node, s.Tier, s.Device)
	}
	if labels["gpu_uuid"] != "GPU-abc" || labels["pci_bus_id"] != "00000000:08:00.0" {
		t.Fatalf("round trip labels = %+v, want original %+v", labels, s.Labels)
	}
}

func TestOpenCreatesDataDirAndReopens(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "nested", "history")
	s, err := Open(dir, []string{"gpu_utilization_pct"}, time.Minute, time.Hour)
	if err != nil {
		t.Fatalf("Open (create): %v", err)
	}
	now := time.Date(2026, 1, 1, 0, 1, 0, 0, time.UTC)
	if err := s.Ingest([]model.Sample{gpuSample("e104", "gpu0", 42, now)}, now); err != nil {
		t.Fatalf("Ingest: %v", err)
	}
	if err := s.Ingest(nil, now.Add(time.Minute)); err != nil { // cross the flush boundary
		t.Fatalf("Ingest (flush): %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	// Reopening the same dataDir must see the previously flushed point.
	s2, err := Open(dir, []string{"gpu_utilization_pct"}, time.Minute, time.Hour)
	if err != nil {
		t.Fatalf("Open (reopen): %v", err)
	}
	defer s2.Close()
	res, err := s2.Query("gpu_utilization_pct", time.Time{}, nil)
	if err != nil {
		t.Fatalf("Query after reopen: %v", err)
	}
	if len(res) != 1 || len(res[0].Points) != 1 || res[0].Points[0].Value != 42 {
		t.Fatalf("data did not survive Close+reopen: %+v", res)
	}
}

// TestRetentionLargerThanUnixEpochDoesNotDeleteEverything is a regression
// test for the ultracode adversarial-review finding: a retention window
// large enough to make (flush timestamp - retention) predate 1970 used to
// wrap the prune cutoff to ~2^64 via the int64->uint64 cast, so every real
// key compared "less than" it and the prune loop deleted every point in
// every bucket on every flush — including the point flushLocked itself had
// just written in the same transaction. A 100-year retention (safely under
// time.Duration's ~292y ceiling, so this isn't confounded by Duration
// overflow) reliably predates the epoch from any 2020s+ "now".
func TestRetentionLargerThanUnixEpochDoesNotDeleteEverything(t *testing.T) {
	s := mustOpen(t, time.Minute, 100*365*24*time.Hour, "gpu_utilization_pct")
	base := time.Date(2026, 1, 1, 0, 1, 0, 0, time.UTC)
	if err := s.Ingest([]model.Sample{gpuSample("e104", "gpu0", 55, base)}, base); err != nil {
		t.Fatalf("Ingest: %v", err)
	}
	if err := s.Ingest(nil, base.Add(time.Minute)); err != nil { // cross the flush boundary
		t.Fatalf("Ingest (flush): %v", err)
	}
	res, err := s.Query("gpu_utilization_pct", time.Time{}, nil)
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	if len(res) != 1 || len(res[0].Points) != 1 {
		t.Fatalf("a pre-epoch retention cutoff must not delete the point just written, got: %+v", res)
	}
	if res[0].Points[0].Value != 55 {
		t.Fatalf("point value = %v, want 55", res[0].Points[0].Value)
	}
}

// TestIngestCatchUpAfterLongGapFlushesOnceAtLatestBoundary is a regression
// test for the ultracode finding on multi-boundary catch-up: previously,
// crossing several stale flush boundaries in one Ingest call flushed a real
// point only at the OLDEST boundary (every later iteration found an already
// -emptied accumulator and silently wrote nothing). The fix flushes once,
// stamped at the LATEST boundary being caught up to.
func TestIngestCatchUpAfterLongGapFlushesOnceAtLatestBoundary(t *testing.T) {
	s := mustOpen(t, time.Minute, time.Hour, "gpu_utilization_pct")
	base := time.Date(2026, 1, 1, 0, 0, 30, 0, time.UTC) // 30s past a 1m boundary
	if err := s.Ingest([]model.Sample{gpuSample("e104", "gpu0", 10, base)}, base); err != nil {
		t.Fatalf("Ingest: %v", err)
	}
	// Jump 5 minutes ahead in a single call — 5 boundaries close at once,
	// simulating a process stall or a long scheduling gap.
	jump := base.Add(5 * time.Minute)
	if err := s.Ingest([]model.Sample{gpuSample("e104", "gpu0", 20, jump)}, jump); err != nil {
		t.Fatalf("Ingest (catch-up): %v", err)
	}
	res, err := s.Query("gpu_utilization_pct", time.Time{}, nil)
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	if len(res) != 1 {
		t.Fatalf("want 1 series, got %d", len(res))
	}
	pts := res[0].Points
	if len(pts) != 1 {
		t.Fatalf("catch-up across 5 boundaries must flush exactly once, got %d points: %+v", len(pts), pts)
	}
	wantTS := base.Truncate(time.Minute).Add(5 * time.Minute) // the LATEST closed boundary, not the first
	if !pts[0].Timestamp.Equal(wantTS) {
		t.Fatalf("catch-up point timestamp = %v, want the latest boundary %v (not the oldest)", pts[0].Timestamp, wantTS)
	}
	if pts[0].Value != 15 { // avg(10, 20) — both samples landed in the single flushed batch
		t.Fatalf("catch-up point value = %v, want 15 (avg of both samples)", pts[0].Value)
	}
}
