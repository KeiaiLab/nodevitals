// Package collector reads hardware state. Each collector covers one domain and
// performs read-only access; all filesystem roots are injected for testability.
package collector

import (
	"context"

	"github.com/nodevitals/nodevitals/internal/model"
)

// Collector samples one hardware domain.
type Collector interface {
	Name() string
	Collect(ctx context.Context) ([]model.Sample, error)
}

// Registry holds the active collectors for a tier.
type Registry struct {
	collectors []Collector
}

func (r *Registry) Add(c Collector) { r.collectors = append(r.collectors, c) }

// CollectAll runs every collector; a failing collector is skipped (its error is
// dropped here — callers relying on liveness use agent-level self-metrics).
func (r *Registry) CollectAll(ctx context.Context) []model.Sample {
	var out []model.Sample
	for _, c := range r.collectors {
		s, err := c.Collect(ctx)
		if err != nil {
			continue
		}
		out = append(out, s...)
	}
	return out
}
