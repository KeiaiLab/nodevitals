// Package agent wires collectors, the event engine, and sinks into a run loop.
package agent

import (
	"context"
	"log/slog"
	"math/rand"
	"sync"
	"time"

	"github.com/KeiaiLab/nodevitals/internal/collector"
	"github.com/KeiaiLab/nodevitals/internal/config"
	"github.com/KeiaiLab/nodevitals/internal/event"
	"github.com/KeiaiLab/nodevitals/internal/history"
	"github.com/KeiaiLab/nodevitals/internal/model"
	"github.com/KeiaiLab/nodevitals/internal/queue"
	"github.com/KeiaiLab/nodevitals/internal/sink"
)

type Agent struct {
	cfg      config.Config
	reg      *collector.Registry
	eng      *event.Engine
	webhooks []sink.Sink
	metrics  *sink.Metrics
	history  *history.Store // nil when history.enabled is false
	backoff  queue.Backoff

	mu   sync.RWMutex
	snap []model.Sample
}

func New(cfg config.Config, reg *collector.Registry, eng *event.Engine, webhooks []sink.Sink, metrics *sink.Metrics, hist *history.Store) *Agent {
	return &Agent{
		cfg: cfg, reg: reg, eng: eng, webhooks: webhooks, metrics: metrics, history: hist,
		backoff: queue.Backoff{Base: 500 * time.Millisecond, Max: 30 * time.Second},
	}
}

// Tick runs one collect→evaluate→deliver cycle.
func (a *Agent) Tick(ctx context.Context) {
	samples := a.reg.CollectAll(ctx)

	a.mu.Lock()
	a.snap = samples
	a.mu.Unlock()

	if a.metrics != nil {
		a.metrics.Update(samples)
	}
	if a.history != nil {
		if err := a.history.Ingest(samples, time.Now()); err != nil {
			slog.Error("history ingest", "err", err)
		}
	}

	events := a.eng.Evaluate(samples)
	if len(events) == 0 {
		return
	}
	if a.metrics != nil {
		a.metrics.RecordEvents(events)
	}
	for _, s := range a.webhooks {
		if err := queue.DeliverWithRetry(ctx, s, events, 5, a.backoff, time.Sleep, rand.Float64); err != nil {
			slog.Error("event delivery failed", "sink", s.Name(), "err", err)
			if a.metrics != nil {
				a.metrics.RecordDropped(s.Name(), len(events))
			}
		}
	}
}

// Snapshot implements httpapi.SnapshotSource.
func (a *Agent) Snapshot() []model.Sample {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return a.snap
}

// QueryHistory implements httpapi.HistorySource, delegating to the
// downsampled store when history is enabled and reporting
// history.ErrDisabled otherwise (httpapi maps that to 404, not 500).
func (a *Agent) QueryHistory(metric string, since time.Time, filter map[string]string) ([]history.SeriesHistory, error) {
	if a.history == nil {
		return nil, history.ErrDisabled
	}
	return a.history.Query(metric, since, filter)
}

// Run ticks on the configured interval until ctx is cancelled. It also
// starts one drain goroutine per registered EventSource collector (e.g. the
// gpu tier's XID stream): each goroutine delivers every event from its
// source straight to the webhook sinks, bypassing the threshold engine
// entirely (unlike Tick, which routes samples through a.eng first).
//
// Lifecycle: a drain goroutine exits when its source's Events() channel
// closes (the collector that owns the channel closes it — see gpu.go's
// NewGPUCollector) or when ctx is cancelled, whichever comes first. No
// WaitGroup or lifecycle manager is needed: Run does not wait for the drain
// goroutines before returning — by the time ctx is cancelled the process is
// shutting down, so there is nothing dangerous left for them to outlive.
func (a *Agent) Run(ctx context.Context) {
	for _, es := range a.reg.EventSources() {
		go a.drainEventSource(ctx, es)
	}

	t := time.NewTicker(a.cfg.Interval())
	defer t.Stop()
	a.Tick(ctx) // immediate first tick
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			a.Tick(ctx)
		}
	}
}

// drainEventSource delivers every event from es straight to all webhook
// sinks, bypassing the threshold engine (see Run's doc comment for the
// termination lifecycle).
func (a *Agent) drainEventSource(ctx context.Context, es collector.EventSource) {
	ch := es.Events()
	for {
		select {
		case <-ctx.Done():
			return
		case ev, ok := <-ch:
			if !ok {
				return
			}
			if a.metrics != nil {
				a.metrics.RecordEvents([]model.Event{ev})
			}
			for _, s := range a.webhooks {
				if err := queue.DeliverWithRetry(ctx, s, []model.Event{ev}, 5, a.backoff, time.Sleep, rand.Float64); err != nil {
					slog.Error("event delivery failed", "sink", s.Name(), "err", err)
					if a.metrics != nil {
						a.metrics.RecordDropped(s.Name(), 1)
					}
				}
			}
		}
	}
}
