// Package collector reads hardware state. Each collector covers one domain and
// performs read-only access; all filesystem roots are injected for testability.
package collector

import (
	"context"

	"github.com/KeiaiLab/nodevitals/internal/model"
)

// Collector samples one hardware domain.
type Collector interface {
	Name() string
	Collect(ctx context.Context) ([]model.Sample, error)
}

// EventSource is an optional capability a Collector may also implement: a
// stream of hardware events produced asynchronously, outside the polled
// Collect path. The agent discovers implementers via a runtime type
// assertion (see Registry.EventSources) and drains them straight to the
// webhook sinks, bypassing the threshold engine. The gpu tier's XID stream is
// the first implementer.
type EventSource interface {
	Events() <-chan model.Event
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

// EventSources returns the registered collectors that also implement
// EventSource, discovered by type assertion. The agent drains each to the
// webhook sinks.
func (r *Registry) EventSources() []EventSource {
	var out []EventSource
	for _, c := range r.collectors {
		if es, ok := c.(EventSource); ok {
			out = append(out, es)
		}
	}
	return out
}
