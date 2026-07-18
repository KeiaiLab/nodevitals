package model

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestFingerprintStableAcrossVolatileFields(t *testing.T) {
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	a := Event{Node: "n1", Tier: "core", Device: "cpu", Condition: "load_high", Seq: 1, StartedAt: base}
	b := Event{Node: "n1", Tier: "core", Device: "cpu", Condition: "load_high", Seq: 99, StartedAt: base.Add(time.Hour), EndedAt: base.Add(2 * time.Hour)}
	if a.Fingerprint() != b.Fingerprint() {
		t.Fatalf("fingerprint must ignore volatile fields (seq, timestamps): %s != %s", a.Fingerprint(), b.Fingerprint())
	}
}

func TestFingerprintVariesByEachIdentityField(t *testing.T) {
	ref := Event{Node: "n1", Tier: "core", Device: "cpu", Condition: "load_high"}
	cases := []Event{
		{Node: "n2", Tier: "core", Device: "cpu", Condition: "load_high"},
		{Node: "n1", Tier: "gpu", Device: "cpu", Condition: "load_high"},
		{Node: "n1", Tier: "core", Device: "gpu0", Condition: "load_high"},
		{Node: "n1", Tier: "core", Device: "cpu", Condition: "temp_high"},
	}
	for _, c := range cases {
		if c.Fingerprint() == ref.Fingerprint() {
			t.Fatalf("fingerprint must differ when an identity field changes: %+v", c)
		}
	}
}

func TestEndedAtOmittedWhenZero(t *testing.T) {
	enter := Event{Node: "n", Condition: "load_high", Phase: PhaseEnter}
	b, err := json.Marshal(enter)
	if err != nil {
		t.Fatalf("marshal enter: %v", err)
	}
	if strings.Contains(string(b), "ended_at") {
		t.Fatalf("ENTER event must omit ended_at: %s", b)
	}
	exit := Event{Node: "n", Condition: "load_high", Phase: PhaseExit, EndedAt: time.Unix(1, 0).UTC()}
	b2, err := json.Marshal(exit)
	if err != nil {
		t.Fatalf("marshal exit: %v", err)
	}
	if !strings.Contains(string(b2), "ended_at") {
		t.Fatalf("EXIT event must include ended_at: %s", b2)
	}
}
