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
	backoff  queue.Backoff

	mu   sync.RWMutex
	snap []model.Sample
}

func New(cfg config.Config, reg *collector.Registry, eng *event.Engine, webhooks []sink.Sink, metrics *sink.Metrics) *Agent {
	return &Agent{
		cfg: cfg, reg: reg, eng: eng, webhooks: webhooks, metrics: metrics,
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

	events := a.eng.Evaluate(samples)
	if len(events) == 0 {
		return
	}
	for _, s := range a.webhooks {
		if err := queue.DeliverWithRetry(ctx, s, events, 5, a.backoff, time.Sleep, rand.Float64); err != nil {
			slog.Error("event delivery failed", "sink", s.Name(), "err", err)
		}
	}
}

// Snapshot implements httpapi.SnapshotSource.
func (a *Agent) Snapshot() []model.Sample {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return a.snap
}

// Run ticks on the configured interval until ctx is cancelled.
func (a *Agent) Run(ctx context.Context) {
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
