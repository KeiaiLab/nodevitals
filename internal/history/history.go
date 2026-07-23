// Package history persists downsampled long-term series to a local bbolt
// file, so a hardware trend (e.g. "GPU utilization over the last year")
// survives well past the Prometheus scrape retention window without a
// second collector or central store — each node keeps its own history,
// queryable from that node's own /v1/history endpoint.
//
// Store never sees raw scrape-interval samples on disk: Ingest accumulates
// sum+count per series in memory and only a rolling average lands on disk,
// once per flush interval. That keeps years of data at 5-minute resolution
// small enough for a DaemonSet's local hostPath (see design note in
// dataviz-free math: a few dozen series x 288 points/day is tens of
// thousands of points/year, not millions).
package history

import (
	"bytes"
	"encoding/binary"
	"errors"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/KeiaiLab/nodevitals/internal/model"
	"go.etcd.io/bbolt"
)

// ErrDisabled is returned by a nil-Store-backed caller (see agent.Agent) so
// httpapi can tell "history not configured" apart from a real query failure.
var ErrDisabled = errors.New("history: not enabled")

// Point is one downsampled (timestamp, average value) sample.
type Point struct {
	Timestamp time.Time `json:"timestamp"`
	Value     float64   `json:"value"`
}

// SeriesHistory is one series' identity plus its retained points, oldest
// first (bbolt cursor order == chronological, since keys are big-endian
// timestamps).
type SeriesHistory struct {
	Metric string            `json:"metric"`
	Node   string            `json:"node"`
	Tier   string            `json:"tier"`
	Device string            `json:"device"`
	Labels map[string]string `json:"labels,omitempty"`
	Points []Point           `json:"points"`
}

type accumulator struct {
	sum   float64
	count int
}

// Store accumulates samples in memory and flushes one averaged point per
// series to disk every flushInterval, pruning points older than retention
// in the same pass. All exported methods are safe for concurrent use.
type Store struct {
	db            *bbolt.DB
	metrics       map[string]bool
	flushInterval time.Duration
	retention     time.Duration

	mu   sync.Mutex
	acc  map[string]*accumulator
	next time.Time // next flush boundary; zero until the first Ingest
}

// Open creates dataDir if needed and opens (or creates) the bbolt file
// inside it. metrics is the allowlist of Sample.Metric names to retain —
// anything else is ignored by Ingest, keeping cardinality and disk use
// bounded to what the caller actually wants years of history for.
func Open(dataDir string, metrics []string, flushInterval, retention time.Duration) (*Store, error) {
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		return nil, err
	}
	db, err := bbolt.Open(filepath.Join(dataDir, "history.db"), 0o600, &bbolt.Options{Timeout: 5 * time.Second})
	if err != nil {
		return nil, err
	}
	allow := make(map[string]bool, len(metrics))
	for _, m := range metrics {
		allow[m] = true
	}
	return &Store{db: db, metrics: allow, flushInterval: flushInterval, retention: retention, acc: map[string]*accumulator{}}, nil
}

func (s *Store) Close() error { return s.db.Close() }

// Ingest folds allowlisted samples into the in-flight average for their
// series, then flushes+prunes every series bucket whose window has closed
// as of now. now is a parameter (not time.Now internally) so tests can drive
// flush boundaries deterministically, mirroring queue.DeliverWithRetry's
// injected clock.
func (s *Store) Ingest(samples []model.Sample, now time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.next.IsZero() {
		s.next = now.Truncate(s.flushInterval).Add(s.flushInterval)
	}
	for _, sample := range samples {
		if !s.metrics[sample.Metric] {
			continue
		}
		key := seriesKeyFor(sample)
		a := s.acc[key]
		if a == nil {
			a = &accumulator{}
			s.acc[key] = a
		}
		a.sum += sample.Value
		a.count++
	}

	// A gap longer than one flushInterval (process paused, node stalled) means
	// several boundaries close in this single call. Only the LAST one gets an
	// actual flush — it carries whatever the accumulator holds (every sample
	// collected since the previous flush, regardless of which skipped
	// boundary they'd nominally belong to) and is stamped at that most-recent
	// boundary: "the average as of now, catching up" rather than the oldest
	// one. Flushing per-boundary here would (a) only ever have real data on
	// the first iteration, since s.acc empties after every flush — silently
	// writing nothing for the rest — and (b) stamp that one real point at the
	// OLDEST stale boundary, understating how current the average is.
	last := time.Time{}
	for !s.next.After(now) {
		last = s.next
		s.next = s.next.Add(s.flushInterval)
	}
	if !last.IsZero() {
		if err := s.flushLocked(last); err != nil {
			return err
		}
	}
	return nil
}

// flushLocked writes one point at ts per series with count>0, resets every
// accumulator, and prunes points older than retention — all in one bbolt
// transaction. Called with s.mu held.
func (s *Store) flushLocked(ts time.Time) error {
	// cutoffTime can predate the Unix epoch when retention is configured
	// larger than the time since the epoch (e.g. a very large retentionDays
	// meaning "keep forever"). encodeTimestamp's uint64(t.Unix()) cast does
	// not clamp a negative Unix() to 0 — it wraps to a value near 2^64, which
	// would make EVERY real, valid key compare less than it, so the prune
	// loop below would delete every point in every bucket on every flush,
	// including the one flushLocked itself just wrote in this same
	// transaction. Skipping the prune pass for a pre-epoch cutoff is correct
	// for any retention value: nothing has actually aged out yet if the
	// cutoff isn't a valid point in time.
	cutoffTime := ts.Add(-s.retention)
	prune := !cutoffTime.Before(time.Unix(0, 0))
	var cutoff []byte
	if prune {
		cutoff = encodeTimestamp(cutoffTime)
	}
	err := s.db.Update(func(tx *bbolt.Tx) error {
		for key, a := range s.acc {
			if a.count == 0 {
				continue
			}
			b, err := tx.CreateBucketIfNotExists([]byte(key))
			if err != nil {
				return err
			}
			if err := b.Put(encodeTimestamp(ts), encodeValue(a.sum/float64(a.count))); err != nil {
				return err
			}
			if !prune {
				continue
			}
			c := b.Cursor()
			for k, _ := c.First(); k != nil && bytes.Compare(k, cutoff) < 0; k, _ = c.Next() {
				if err := c.Delete(); err != nil {
					return err
				}
			}
		}
		return nil
	})
	if err != nil {
		return err
	}
	s.acc = map[string]*accumulator{}
	return nil
}

// Query returns every retained series for metric whose Node/Device/Labels
// match filter (a value in filter must equal the series' Node, Device, or a
// same-named label; an empty filter matches everything), each with points
// from since onward.
func (s *Store) Query(metric string, since time.Time, filter map[string]string) ([]SeriesHistory, error) {
	prefix := metric + "\x00"
	// since.IsZero() means "no lower bound". Encoding it via encodeTimestamp
	// would wrap: the Go zero Time predates the Unix epoch, so its Unix() is
	// negative, and casting a negative int64 to uint64 doesn't clamp to 0 —
	// it wraps to a near-max value, which would make Seek skip every real
	// point instead of returning all of them.
	var sinceKey []byte
	if !since.IsZero() {
		sinceKey = encodeTimestamp(since)
	}
	var out []SeriesHistory
	err := s.db.View(func(tx *bbolt.Tx) error {
		return tx.ForEach(func(name []byte, b *bbolt.Bucket) error {
			key := string(name)
			if !strings.HasPrefix(key, prefix) {
				return nil
			}
			// canonicalMetric (parsed back out of the bucket key) rather than the
			// caller-supplied metric argument: prefix matching means a short or
			// malformed query string must never be echoed back as if it were the
			// actual stored series' name.
			canonicalMetric, node, tier, device, labels := parseSeriesKey(key)
			if !matchesFilter(node, device, labels, filter) {
				return nil
			}
			sh := SeriesHistory{Metric: canonicalMetric, Node: node, Tier: tier, Device: device, Labels: labels}
			c := b.Cursor()
			var k, v []byte
			if sinceKey != nil {
				k, v = c.Seek(sinceKey)
			} else {
				k, v = c.First()
			}
			for ; k != nil; k, v = c.Next() {
				sh.Points = append(sh.Points, Point{Timestamp: decodeTimestamp(k), Value: decodeValue(v)})
			}
			out = append(out, sh)
			return nil
		})
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

func matchesFilter(node, device string, labels map[string]string, filter map[string]string) bool {
	for k, v := range filter {
		switch k {
		case "node":
			if node != v {
				return false
			}
		case "device":
			if device != v {
				return false
			}
		default:
			if labels[k] != v {
				return false
			}
		}
	}
	return true
}

// seriesKeyFor builds a stable, human-diffable series identity string that
// doubles as the bbolt bucket name — Query reconstructs Node/Tier/Device/
// Labels straight from it (parseSeriesKey), so no separate metadata bucket
// is needed. Label keys are sorted for the same reason sink/metrics.go
// sorts them: Go map iteration order is randomized, and an unsorted key
// would fragment one logical series into multiple bucket names.
func seriesKeyFor(s model.Sample) string {
	keys := make([]string, 0, len(s.Labels))
	for k := range s.Labels {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	var b strings.Builder
	b.WriteString(s.Metric)
	b.WriteByte(0)
	b.WriteString(s.Node)
	b.WriteByte(0)
	b.WriteString(s.Tier)
	b.WriteByte(0)
	b.WriteString(s.Device)
	for _, k := range keys {
		b.WriteByte(0)
		b.WriteString(k)
		b.WriteByte('=')
		b.WriteString(s.Labels[k])
	}
	return b.String()
}

func parseSeriesKey(key string) (metric, node, tier, device string, labels map[string]string) {
	parts := strings.Split(key, "\x00")
	metric, node, tier, device = parts[0], parts[1], parts[2], parts[3]
	if len(parts) > 4 {
		labels = make(map[string]string, len(parts)-4)
		for _, p := range parts[4:] {
			if i := strings.IndexByte(p, '='); i >= 0 {
				labels[p[:i]] = p[i+1:]
			}
		}
	}
	return
}

func encodeTimestamp(t time.Time) []byte {
	b := make([]byte, 8)
	binary.BigEndian.PutUint64(b, uint64(t.Unix()))
	return b
}

func decodeTimestamp(b []byte) time.Time {
	return time.Unix(int64(binary.BigEndian.Uint64(b)), 0).UTC()
}

func encodeValue(v float64) []byte {
	b := make([]byte, 8)
	binary.BigEndian.PutUint64(b, math.Float64bits(v))
	return b
}

func decodeValue(b []byte) float64 {
	return math.Float64frombits(binary.BigEndian.Uint64(b))
}
