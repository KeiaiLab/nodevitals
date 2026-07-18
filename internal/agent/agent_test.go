package agent

import (
	"context"
	"runtime"
	"testing"
	"time"

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

// fakeEventCollector is a Collector that also implements collector.EventSource,
// for exercising Agent.Run's drain path. Collect returns no samples — this
// fake exists only to feed the Events() channel under test control.
type fakeEventCollector struct{ ch chan model.Event }

func (f *fakeEventCollector) Name() string { return "fake-events" }
func (f *fakeEventCollector) Collect(context.Context) ([]model.Sample, error) {
	return nil, nil
}
func (f *fakeEventCollector) Events() <-chan model.Event { return f.ch }

// chanSink is a webhook capture sink backed by a channel instead of a plain
// slice. Run's drain goroutine calls EmitEvents concurrently with the test
// goroutine's assertions, so a plain slice would race; a channel lets the
// test synchronize with select-with-timeout instead of sleep, and is
// race-free by construction.
type chanSink struct{ ch chan model.Event }

func newChanSink() *chanSink { return &chanSink{ch: make(chan model.Event, 4)} }

func (c *chanSink) Name() string { return "chan-capture" }
func (c *chanSink) EmitEvents(_ context.Context, ev []model.Event) error {
	for _, e := range ev {
		c.ch <- e
	}
	return nil
}

func TestRunDrainsEventSourceCollectorsToWebhooksBypassingEngine(t *testing.T) {
	// No Rules: the threshold engine never fires on its own, isolating this
	// test to the drain path (EventSource → webhook, no engine involvement).
	cfg := config.Config{Node: "n", Tier: "gpu"}
	var reg collector.Registry
	// Buffered so the send below never blocks on a reader: synchronization
	// for the actual assertion happens on the receive side (select-with-
	// timeout on cap.ch), not here. An unbuffered channel would hang this
	// test indefinitely — instead of failing fast — if the drain regressed.
	fake := &fakeEventCollector{ch: make(chan model.Event, 1)}
	reg.Add(fake)
	eng := event.NewEngine("n", cfg.Rules)
	// Two sinks: the drain must fan every event out to ALL webhooks, not just
	// the first (mirrors Tick's delivery loop).
	sinks := []*chanSink{newChanSink(), newChanSink()}

	a := New(cfg, &reg, eng, []sink.Sink{sinks[0], sinks[1]}, sink.NewMetrics())

	baseGoroutines := runtime.NumGoroutine()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go a.Run(ctx)

	want := model.Event{
		Node: "n", Tier: "gpu", Device: "gpu0",
		Condition: "xid_fatal", Phase: model.PhaseEnter, Severity: model.SevCritical,
		Seq: 1, StartedAt: time.Now().UTC(),
	}
	fake.ch <- want

	// Every webhook must receive the drained event.
	for i, s := range sinks {
		select {
		case got := <-s.ch:
			if got.Condition != want.Condition || got.Device != want.Device || got.Phase != want.Phase {
				t.Fatalf("sink[%d]: drained event mismatch: got %+v, want %+v", i, got, want)
			}
		case <-time.After(2 * time.Second):
			t.Fatalf("sink[%d]: timed out waiting for drained event to reach webhook sink", i)
		}
	}

	// Both termination paths — ctx cancel and source channel close — must stop
	// the drain goroutine, not leak it. Cancel, close, then poll until the
	// goroutine count returns to the pre-Run baseline; a genuine leak keeps it
	// elevated until the 2s deadline and fails.
	cancel()
	close(fake.ch)
	deadline := time.After(2 * time.Second)
	for runtime.NumGoroutine() > baseGoroutines {
		select {
		case <-deadline:
			t.Fatalf("drain goroutine leaked: %d goroutines still running, want <= %d", runtime.NumGoroutine(), baseGoroutines)
		default:
			time.Sleep(5 * time.Millisecond)
		}
	}
}
