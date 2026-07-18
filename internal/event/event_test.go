package event

import (
	"testing"
	"time"

	"github.com/KeiaiLab/nodevitals/internal/config"
	"github.com/KeiaiLab/nodevitals/internal/model"
)

func rule() config.Rule {
	return config.Rule{
		Metric: "load1", Device: "cpu", Condition: "load_high",
		Severity: "warning", Threshold: 4.0, EnterFor: 2, ExitFor: 2,
	}
}

func sample(v float64) []model.Sample {
	return []model.Sample{{Node: "n", Tier: "core", Device: "cpu", Metric: "load1", Value: v, Timestamp: time.Now()}}
}

func TestEnterFiresAfterEnterForConsecutiveBreaches(t *testing.T) {
	e := NewEngine("n", []config.Rule{rule()})
	if ev := e.Evaluate(sample(5)); len(ev) != 0 {
		t.Fatalf("breach 1/2 must not fire, got %d", len(ev))
	}
	ev := e.Evaluate(sample(5))
	if len(ev) != 1 || ev[0].Phase != model.PhaseEnter {
		t.Fatalf("breach 2/2 must ENTER, got %+v", ev)
	}
	if ev[0].Condition != "load_high" || ev[0].Severity != "warning" || ev[0].Seq != 1 {
		t.Fatalf("bad enter event: %+v", ev[0])
	}
}

func TestNoDuplicateEnterWhileActive(t *testing.T) {
	e := NewEngine("n", []config.Rule{rule()})
	e.Evaluate(sample(5))
	e.Evaluate(sample(5)) // ENTER
	if ev := e.Evaluate(sample(6)); len(ev) != 0 {
		t.Fatalf("must not re-ENTER while active, got %+v", ev)
	}
}

func TestExitFiresAfterExitForConsecutiveClears(t *testing.T) {
	e := NewEngine("n", []config.Rule{rule()})
	e.Evaluate(sample(5))
	e.Evaluate(sample(5)) // ENTER (seq 1)
	if ev := e.Evaluate(sample(1)); len(ev) != 0 {
		t.Fatalf("clear 1/2 must not EXIT, got %+v", ev)
	}
	ev := e.Evaluate(sample(1))
	if len(ev) != 1 || ev[0].Phase != model.PhaseExit {
		t.Fatalf("clear 2/2 must EXIT, got %+v", ev)
	}
	if ev[0].Seq != 2 || ev[0].EndedAt.IsZero() {
		t.Fatalf("exit must carry seq=2 and EndedAt: %+v", ev[0])
	}
}

func TestHysteresisResetsClearCountOnRebreapch(t *testing.T) {
	e := NewEngine("n", []config.Rule{rule()})
	e.Evaluate(sample(5))
	e.Evaluate(sample(5)) // ENTER
	e.Evaluate(sample(1)) // clear 1/2
	e.Evaluate(sample(9)) // breach again → clear count resets
	if ev := e.Evaluate(sample(1)); len(ev) != 0 {
		t.Fatalf("clear count must have reset; single clear must not EXIT, got %+v", ev)
	}
}

func TestEnterForBelowOneClampsToOne(t *testing.T) {
	r := config.Rule{Metric: "load1", Device: "cpu", Condition: "load_high", Severity: "warning", Threshold: 4.0, EnterFor: 0, ExitFor: 0}
	e := NewEngine("n", []config.Rule{r})
	ev := e.Evaluate(sample(5))
	if len(ev) != 1 || ev[0].Phase != model.PhaseEnter {
		t.Fatalf("EnterFor=0 must clamp to 1 and ENTER on first breach, got %+v", ev)
	}
}

func TestMultipleRulesSameMetricDeviceFireInConstructionOrder(t *testing.T) {
	rules := []config.Rule{
		{Metric: "load1", Device: "cpu", Condition: "load_warn", Severity: "warning", Threshold: 4.0, EnterFor: 1, ExitFor: 1},
		{Metric: "load1", Device: "cpu", Condition: "load_crit", Severity: "critical", Threshold: 8.0, EnterFor: 1, ExitFor: 1},
	}
	e := NewEngine("n", rules)
	ev := e.Evaluate(sample(9)) // breaches both thresholds
	if len(ev) != 2 {
		t.Fatalf("both rules must fire, got %d: %+v", len(ev), ev)
	}
	if ev[0].Condition != "load_warn" || ev[1].Condition != "load_crit" {
		t.Fatalf("event order must follow rule construction order, got %s then %s", ev[0].Condition, ev[1].Condition)
	}
}

func TestSameConditionDifferentDeviceBothTracked(t *testing.T) {
	rules := []config.Rule{
		{Metric: "temp", Device: "sda", Condition: "disk_hot", Severity: "warning", Threshold: 50, EnterFor: 1, ExitFor: 1},
		{Metric: "temp", Device: "sdb", Condition: "disk_hot", Severity: "warning", Threshold: 50, EnterFor: 1, ExitFor: 1},
	}
	e := NewEngine("n", rules)
	ts := time.Unix(1, 0).UTC()
	sda := model.Sample{Node: "n", Tier: "smart", Device: "sda", Metric: "temp", Value: 60, Timestamp: ts}
	sdb := model.Sample{Node: "n", Tier: "smart", Device: "sdb", Metric: "temp", Value: 60, Timestamp: ts}
	ev := e.Evaluate([]model.Sample{sda, sdb})
	if len(ev) != 2 {
		t.Fatalf("same condition on two devices must both fire (no map collision), got %d: %+v", len(ev), ev)
	}
	if ev[0].Device != "sda" || ev[1].Device != "sdb" {
		t.Fatalf("both devices must be tracked independently, got %+v", ev)
	}
}

func TestEnterAndExitInOneEvaluateDeriveSampleTimestamps(t *testing.T) {
	r := config.Rule{Metric: "load1", Device: "cpu", Condition: "load_high", Severity: "warning", Threshold: 4.0, EnterFor: 1, ExitFor: 1}
	e := NewEngine("n", []config.Rule{r})
	t1 := time.Unix(100, 0).UTC()
	t2 := time.Unix(200, 0).UTC()
	breach := model.Sample{Node: "n", Tier: "core", Device: "cpu", Metric: "load1", Value: 9, Timestamp: t1}
	clear := model.Sample{Node: "n", Tier: "core", Device: "cpu", Metric: "load1", Value: 1, Timestamp: t2}
	ev := e.Evaluate([]model.Sample{breach, clear})
	if len(ev) != 2 || ev[0].Phase != model.PhaseEnter || ev[1].Phase != model.PhaseExit {
		t.Fatalf("one Evaluate spanning breach+clear must emit ENTER then EXIT, got %+v", ev)
	}
	if !ev[0].StartedAt.Equal(t1) {
		t.Fatalf("ENTER StartedAt must derive from breach sample timestamp %v, got %v", t1, ev[0].StartedAt)
	}
	if !ev[1].StartedAt.Equal(t1) || !ev[1].EndedAt.Equal(t2) {
		t.Fatalf("EXIT must carry StartedAt=%v EndedAt=%v, got StartedAt=%v EndedAt=%v", t1, t2, ev[1].StartedAt, ev[1].EndedAt)
	}
}

func TestEvaluateIsDeterministic(t *testing.T) {
	rules := []config.Rule{
		{Metric: "load1", Device: "cpu", Condition: "load_warn", Severity: "warning", Threshold: 4.0, EnterFor: 1, ExitFor: 1},
		{Metric: "load1", Device: "cpu", Condition: "load_crit", Severity: "critical", Threshold: 8.0, EnterFor: 1, ExitFor: 1},
	}
	s := model.Sample{Node: "n", Tier: "core", Device: "cpu", Metric: "load1", Value: 9, Timestamp: time.Unix(1, 0).UTC()}
	a := NewEngine("n", rules).Evaluate([]model.Sample{s})
	b := NewEngine("n", rules).Evaluate([]model.Sample{s})
	if len(a) != len(b) {
		t.Fatalf("nondeterministic length: %d vs %d", len(a), len(b))
	}
	for i := range a {
		if a[i].Condition != b[i].Condition || a[i].ID != b[i].ID {
			t.Fatalf("nondeterministic event at %d: %q vs %q", i, a[i].Condition, b[i].Condition)
		}
	}
}

func TestWildcardRuleTracksDevicesIndependently(t *testing.T) {
	r := config.Rule{
		Metric: "smart_pending_sectors", Device: "", Condition: "pending",
		Severity: "critical", Threshold: 0, EnterFor: 1, ExitFor: 1,
	}
	e := NewEngine("n", []config.Rule{r})
	t1 := time.Unix(1, 0).UTC()

	sda1 := model.Sample{Node: "n", Tier: "smart", Device: "sda", Metric: "smart_pending_sectors", Value: 5, Timestamp: t1}
	sdb1 := model.Sample{Node: "n", Tier: "smart", Device: "sdb", Metric: "smart_pending_sectors", Value: 0, Timestamp: t1}
	ev := e.Evaluate([]model.Sample{sda1, sdb1})
	if len(ev) != 1 {
		t.Fatalf("want exactly 1 ENTER (sda breach, sdb clear), got %d: %+v", len(ev), ev)
	}
	if ev[0].Phase != model.PhaseEnter || ev[0].Device != "sda" {
		t.Fatalf("want ENTER for device sda, got %+v", ev[0])
	}

	t2 := time.Unix(2, 0).UTC()
	sda2 := model.Sample{Node: "n", Tier: "smart", Device: "sda", Metric: "smart_pending_sectors", Value: 0, Timestamp: t2}
	sdb2 := model.Sample{Node: "n", Tier: "smart", Device: "sdb", Metric: "smart_pending_sectors", Value: 5, Timestamp: t2}
	ev2 := e.Evaluate([]model.Sample{sda2, sdb2})
	if len(ev2) != 2 {
		t.Fatalf("want EXIT(sda)+ENTER(sdb), got %d: %+v", len(ev2), ev2)
	}
	if ev2[0].Phase != model.PhaseExit || ev2[0].Device != "sda" {
		t.Fatalf("want EXIT for device sda first (sample order), got %+v", ev2[0])
	}
	if ev2[1].Phase != model.PhaseEnter || ev2[1].Device != "sdb" {
		t.Fatalf("want ENTER for device sdb second (sample order), got %+v", ev2[1])
	}
}

func TestExactDeviceRuleUnaffectedByWildcardChange(t *testing.T) {
	r := config.Rule{
		Metric: "load1", Device: "cpu", Condition: "load_high",
		Severity: "warning", Threshold: 4.0, EnterFor: 1, ExitFor: 1,
	}
	e := NewEngine("n", []config.Rule{r})
	ts := time.Unix(1, 0).UTC()

	cpu := model.Sample{Node: "n", Tier: "core", Device: "cpu", Metric: "load1", Value: 9, Timestamp: ts}
	ev := e.Evaluate([]model.Sample{cpu})
	if len(ev) != 1 || ev[0].Phase != model.PhaseEnter || ev[0].Device != "cpu" {
		t.Fatalf("exact-device rule must still ENTER for its device, got %+v", ev)
	}

	other := model.Sample{Node: "n", Tier: "core", Device: "cpu0", Metric: "load1", Value: 9, Timestamp: ts}
	ev2 := e.Evaluate([]model.Sample{other})
	if len(ev2) != 0 {
		t.Fatalf("exact-device rule (Device=cpu) must NOT match a different device cpu0, got %+v", ev2)
	}
}

func TestEnterAndExitHaveDistinctIDs(t *testing.T) {
	r := config.Rule{Metric: "load1", Device: "cpu", Condition: "load_high", Severity: "warning", Threshold: 4.0, EnterFor: 1, ExitFor: 1}
	e := NewEngine("n", []config.Rule{r})
	breach := model.Sample{Node: "n", Tier: "core", Device: "cpu", Metric: "load1", Value: 9, Timestamp: time.Unix(100, 0).UTC()}
	clear := model.Sample{Node: "n", Tier: "core", Device: "cpu", Metric: "load1", Value: 1, Timestamp: time.Unix(200, 0).UTC()}
	ev := e.Evaluate([]model.Sample{breach, clear})
	if len(ev) != 2 {
		t.Fatalf("want ENTER+EXIT, got %d", len(ev))
	}
	if ev[0].ID == ev[1].ID {
		t.Fatalf("ENTER and EXIT must have distinct IDs (else dedup drops EXIT): both %q", ev[0].ID)
	}
	if ev[0].Fingerprint() != ev[1].Fingerprint() {
		t.Fatalf("ENTER and EXIT should still share the condition fingerprint")
	}
}
