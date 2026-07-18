package agent

import (
	"context"
	"testing"

	"github.com/KeiaiLab/nodevitals/internal/collector"
	"github.com/KeiaiLab/nodevitals/internal/config"
	"github.com/KeiaiLab/nodevitals/internal/event"
	"github.com/KeiaiLab/nodevitals/internal/model"
	"github.com/KeiaiLab/nodevitals/internal/sink"
)

type captureSink struct{ events []model.Event }

func (c *captureSink) Name() string { return "capture" }
func (c *captureSink) EmitEvents(_ context.Context, ev []model.Event) error {
	c.events = append(c.events, ev...)
	return nil
}

type fixedCollector struct{ v float64 }

func (f fixedCollector) Name() string { return "fixed" }
func (f fixedCollector) Collect(context.Context) ([]model.Sample, error) {
	return []model.Sample{{Node: "n", Tier: "core", Device: "cpu", Metric: "load1", Value: f.v}}, nil
}

func TestTickCollectsUpdatesMetricsAndDeliversEvents(t *testing.T) {
	cfg := config.Config{
		Node: "n", Tier: "core",
		Rules: []config.Rule{{Metric: "load1", Device: "cpu", Condition: "load_high", Severity: "warning", Threshold: 4, EnterFor: 1, ExitFor: 1}},
	}
	var reg collector.Registry
	reg.Add(fixedCollector{v: 9}) // above threshold → ENTER on first tick (EnterFor=1)
	eng := event.NewEngine("n", cfg.Rules)
	cap := &captureSink{}
	metrics := sink.NewMetrics()

	a := New(cfg, &reg, eng, []sink.Sink{cap}, metrics)
	a.Tick(context.Background())

	if len(cap.events) != 1 || cap.events[0].Phase != model.PhaseEnter {
		t.Fatalf("want 1 ENTER event delivered, got %+v", cap.events)
	}
	if snap := a.Snapshot(); len(snap) != 1 || snap[0].Value != 9 {
		t.Fatalf("snapshot not updated: %+v", snap)
	}
}

func TestTickNoEventWhenBelowThreshold(t *testing.T) {
	cfg := config.Config{Node: "n", Tier: "core",
		Rules: []config.Rule{{Metric: "load1", Device: "cpu", Condition: "load_high", Threshold: 4, EnterFor: 1, ExitFor: 1}}}
	var reg collector.Registry
	reg.Add(fixedCollector{v: 1})
	cap := &captureSink{}
	a := New(cfg, &reg, event.NewEngine("n", cfg.Rules), []sink.Sink{cap}, sink.NewMetrics())
	a.Tick(context.Background())
	if len(cap.events) != 0 {
		t.Fatalf("want no events, got %+v", cap.events)
	}
}
