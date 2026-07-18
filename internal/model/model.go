// Package model defines the core data types shared across nodevitals.
package model

import (
	"crypto/sha256"
	"encoding/hex"
	"time"
)

const (
	PhaseEnter = "ENTER"
	PhaseExit  = "EXIT"

	SevInfo     = "info"
	SevWarning  = "warning"
	SevCritical = "critical"
)

// Sample is one hardware measurement.
type Sample struct {
	Node      string            `json:"node"`
	Tier      string            `json:"tier"`
	Device    string            `json:"device"`
	Metric    string            `json:"metric"`
	Value     float64           `json:"value"`
	Labels    map[string]string `json:"labels,omitempty"`
	Timestamp time.Time         `json:"timestamp"`
}

// Event is a hardware state transition.
type Event struct {
	ID        string         `json:"id"`
	Node      string         `json:"node"`
	Tier      string         `json:"tier"`
	Device    string         `json:"device"`
	Condition string         `json:"condition"`
	Phase     string         `json:"phase"`
	Severity  string         `json:"severity"`
	Seq       uint64         `json:"seq"`
	StartedAt time.Time      `json:"started_at"`
	EndedAt   time.Time      `json:"ended_at,omitzero"`
	Detail    map[string]any `json:"detail,omitempty"`
}

// Fingerprint is a stable identity for an event's (node,tier,device,condition),
// ignoring volatile fields (seq, timestamps). Used for dedup and idempotency.
func (e Event) Fingerprint() string {
	h := sha256.Sum256([]byte(e.Node + "\x00" + e.Tier + "\x00" + e.Device + "\x00" + e.Condition))
	return hex.EncodeToString(h[:8])
}
