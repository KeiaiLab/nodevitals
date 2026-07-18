package collector

import (
	"context"
	"errors"
	"testing"

	"github.com/nodevitals/nodevitals/internal/model"
)

type stubCollector struct {
	name    string
	samples []model.Sample
	err     error
}

func (s stubCollector) Name() string { return s.name }
func (s stubCollector) Collect(context.Context) ([]model.Sample, error) {
	return s.samples, s.err
}

func TestRegistrySkipsFailingCollector(t *testing.T) {
	var r Registry
	r.Add(stubCollector{name: "ok", samples: []model.Sample{{Metric: "a"}}})
	r.Add(stubCollector{name: "bad", err: errors.New("boom")})
	r.Add(stubCollector{name: "ok2", samples: []model.Sample{{Metric: "b"}}})

	got := r.CollectAll(context.Background())
	if len(got) != 2 {
		t.Fatalf("want 2 samples (failing skipped), got %d", len(got))
	}
}
