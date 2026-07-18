// Package event turns a stream of samples into hardware state-transition events.
// It is deterministic and hardware-free: give it samples, get events. Timestamps
// are derived from the samples themselves, never from the wall clock.
package event

import (
	"fmt"
	"time"

	"github.com/KeiaiLab/nodevitals/internal/config"
	"github.com/KeiaiLab/nodevitals/internal/model"
)

// deviceState is the mutable hysteresis state for one concrete device matched
// by a rule. An exact-device rule (Device != "") ever populates exactly one
// entry (keyed by that device). A wildcard rule (Device == "") populates one
// independent entry per distinct Sample.Device it has seen, so e.g. sda
// entering ALERT never affects sdb's hysteresis.
type deviceState struct {
	active    bool
	breachRun int
	clearRun  int
	seq       uint64
	startedAt time.Time
}

type ruleState struct {
	rule     config.Rule
	enterFor int
	exitFor  int
	devices  map[string]*deviceState
}

// Engine evaluates rules against samples, holding per-rule (and, for
// device-wildcard rules, per-device) hysteresis state. Evaluate is
// deterministic: the same samples always yield the same events in a stable
// order (samples outer, rules in construction order inner).
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
			devices:  map[string]*deviceState{},
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
			if st.rule.Metric != s.Metric {
				continue
			}
			if st.rule.Device != "" && st.rule.Device != s.Device {
				continue // "" = wildcard (matches any device); non-empty = exact
			}
			ds := st.devices[s.Device]
			if ds == nil {
				ds = &deviceState{}
				st.devices[s.Device] = ds
			}
			if s.Value > st.rule.Threshold {
				ds.breachRun++
				ds.clearRun = 0
			} else {
				ds.clearRun++
				ds.breachRun = 0
			}

			switch {
			case !ds.active && ds.breachRun >= st.enterFor:
				ds.active = true
				ds.seq++
				ds.startedAt = s.Timestamp
				out = append(out, model.Event{
					Node: e.node, Tier: s.Tier, Device: s.Device,
					Condition: st.rule.Condition, Phase: model.PhaseEnter,
					Severity: st.rule.Severity, Seq: ds.seq, StartedAt: s.Timestamp,
					Detail: map[string]any{"value": s.Value, "threshold": st.rule.Threshold},
				})
			case ds.active && ds.clearRun >= st.exitFor:
				ds.active = false
				ds.seq++
				out = append(out, model.Event{
					Node: e.node, Tier: s.Tier, Device: s.Device,
					Condition: st.rule.Condition, Phase: model.PhaseExit,
					Severity: st.rule.Severity, Seq: ds.seq,
					StartedAt: ds.startedAt, EndedAt: s.Timestamp,
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
