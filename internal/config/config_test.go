package config

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestLoadParsesYAML(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "c.yaml")
	os.WriteFile(p, []byte(`
node: test-node
tier: core
intervalSeconds: 5
procRoot: /custom/proc
rules:
  - metric: load1
    device: cpu
    condition: load_high
    severity: warning
    threshold: 4.0
    enterFor: 2
    exitFor: 2
sinks:
  webhook:
    - url: https://backend.example/hook
      secret: shh
  metrics:
    enabled: true
    listenAddr: ":9847"
`), 0o644)

	c, err := Load(p)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if c.Node != "test-node" || c.Tier != "core" {
		t.Fatalf("bad node/tier: %+v", c)
	}
	if c.Interval() != 5*time.Second {
		t.Fatalf("interval = %v, want 5s", c.Interval())
	}
	if len(c.Rules) != 1 || c.Rules[0].Threshold != 4.0 {
		t.Fatalf("bad rules: %+v", c.Rules)
	}
	if len(c.Sinks.Webhook) != 1 || c.Sinks.Webhook[0].URL != "https://backend.example/hook" {
		t.Fatalf("bad webhook: %+v", c.Sinks.Webhook)
	}
	if !c.Sinks.Metrics.Enabled {
		t.Fatal("metrics should be enabled")
	}
	if c.ProcRoot != "/custom/proc" {
		t.Fatalf("procRoot = %q, want /custom/proc", c.ProcRoot)
	}
	r := c.Rules[0]
	if r.Device != "cpu" || r.Condition != "load_high" || r.Severity != "warning" || r.EnterFor != 2 || r.ExitFor != 2 {
		t.Fatalf("rule fields not fully parsed: %+v", r)
	}
	if c.Sinks.Webhook[0].Secret != "shh" {
		t.Fatalf("webhook secret = %q, want shh", c.Sinks.Webhook[0].Secret)
	}
	if c.Sinks.Metrics.ListenAddr != ":9847" {
		t.Fatalf("metrics listenAddr = %q, want :9847", c.Sinks.Metrics.ListenAddr)
	}
}

func TestLoadDefaultsProcRootAndInterval(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "c.yaml")
	os.WriteFile(p, []byte("node: n\ntier: core\n"), 0o644)
	c, err := Load(p)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if c.ProcRoot != "/proc" {
		t.Fatalf("procRoot default = %q, want /proc", c.ProcRoot)
	}
	if c.SysRoot != "/sys" {
		t.Fatalf("sysRoot default = %q, want /sys", c.SysRoot)
	}
	if c.Interval() != 15*time.Second {
		t.Fatalf("interval default = %v, want 15s", c.Interval())
	}
}
