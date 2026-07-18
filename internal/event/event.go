// Package event turns a stream of samples into hardware state-transition events.
// It is deterministic and hardware-free: give it samples, get events. Timestamps
// are derived from the samples themselves, never from the wall clock.
package event

import (
	"fmt"
	"time"

	"github.com/nodevitals/nodevitals/internal/config"
	"github.com/nodevitals/nodevitals/internal/model"
)

type ruleState struct {
	rule      config.Rule
	enterFor  int
	exitFor   int
	active    bool
	breachRun int
	clearRun  int
	seq       uint64
	startedAt time.Time
}

// Engine evaluates rules against samples, holding per-rule hysteresis state.
// Evaluate is deterministic: the same samples always yield the same events in a
// stable order (samples outer, rules in construction order inner).
type Engine struct {
	node  string
	rules []*ruleState
}

// NewEngine builds an engine with one independent state slot per rule, in the
// order given. EnterFor/ExitFor below 1 are clamped to 1.
func NewEngine(node string, rules []config.Rule) *Engine {
	states := make([]*ruleState, 0, len(rules))
	for _, r := range rules {
		states = append(states, &ruleState{
			rule:     r,
			enterFor: max1(r.EnterFor),
			exitFor:  max1(r.ExitFor),
		})
	}
	return &Engine{node: node, rules: states}
}

// Evaluate returns any state-transition events triggered by these samples.
// Event timestamps are taken from the triggering sample's Timestamp, so the
// function is pure with respect to its inputs.
func (e *Engine) Evaluate(samples []model.Sample) []model.Event {
	var out []model.Event
	for _, s := range samples {
		for _, st := range e.rules {
			if st.rule.Metric != s.Metric || st.rule.Device != s.Device {
				continue
			}
			if s.Value > st.rule.Threshold {
				st.breachRun++
				st.clearRun = 0
			} else {
				st.clearRun++
				st.breachRun = 0
			}

			switch {
			case !st.active && st.breachRun >= st.enterFor:
				st.active = true
				st.seq++
				st.startedAt = s.Timestamp
				out = append(out, model.Event{
					Node: e.node, Tier: s.Tier, Device: s.Device,
					Condition: st.rule.Condition, Phase: model.PhaseEnter,
					Severity: st.rule.Severity, Seq: st.seq, StartedAt: s.Timestamp,
					Detail: map[string]any{"value": s.Value, "threshold": st.rule.Threshold},
				})
			case st.active && st.clearRun >= st.exitFor:
				st.active = false
				st.seq++
				out = append(out, model.Event{
					Node: e.node, Tier: s.Tier, Device: s.Device,
					Condition: st.rule.Condition, Phase: model.PhaseExit,
					Severity: st.rule.Severity, Seq: st.seq,
					StartedAt: st.startedAt, EndedAt: s.Timestamp,
					Detail: map[string]any{"value": s.Value, "threshold": st.rule.Threshold},
				})
			}
		}
	}
	for i := range out {
		out[i].ID = fmt.Sprintf("%s-%s-%d", out[i].Fingerprint(), out[i].Phase, out[i].Seq)
	}
	return out
}

func max1(n int) int {
	if n < 1 {
		return 1
	}
	return n
}
