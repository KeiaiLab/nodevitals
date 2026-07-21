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
	if c.DevRoot != "/dev" {
		t.Fatalf("devRoot default = %q, want /dev", c.DevRoot)
	}
	if c.Interval() != 15*time.Second {
		t.Fatalf("interval default = %v, want 15s", c.Interval())
	}
}

func TestLoadExpandsWebhookSecretFromEnv(t *testing.T) {
	t.Setenv("NV_TEST_WEBHOOK_SECRET", "whsec_from_k8s_secret")
	dir := t.TempDir()
	p := filepath.Join(dir, "c.yaml")
	os.WriteFile(p, []byte(`
node: n
tier: core
sinks:
  webhook:
    - url: https://backend.example/hook
      secret: ${NV_TEST_WEBHOOK_SECRET}
    - url: https://other.example/hook
      secret: literal-unchanged
    - url: https://third.example/hook
      secret: whsec_a$b${NOT_EXPANDED}c
`), 0o644)
	c, err := Load(p)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	// A bare ${ENV} placeholder is resolved from the environment (Secret-injected
	// via secretKeyRef), so the signing key never lives in the ConfigMap plaintext.
	if got := c.Sinks.Webhook[0].Secret; got != "whsec_from_k8s_secret" {
		t.Fatalf("webhook[0].secret = %q, want expanded env value", got)
	}
	// A literal secret (no ${...}) passes through unchanged (backward compat).
	if got := c.Sinks.Webhook[1].Secret; got != "literal-unchanged" {
		t.Fatalf("webhook[1].secret = %q, want literal-unchanged", got)
	}
	// A literal secret containing '$' / '${...}' is NOT a bare reference, so it
	// is left verbatim — never mangled by env expansion (real keys stay intact).
	if got := c.Sinks.Webhook[2].Secret; got != "whsec_a$b${NOT_EXPANDED}c" {
		t.Fatalf("webhook[2].secret = %q, want the literal left unchanged", got)
	}
}

func TestLoadFailsClosedOnEmptyWebhookSecretRef(t *testing.T) {
	// ${VAR} placeholder whose env var is unset → Load must error, not silently
	// sign with an empty (publicly reproducible) HMAC key.
	dir := t.TempDir()
	p := filepath.Join(dir, "c.yaml")
	os.WriteFile(p, []byte(`
node: n
tier: core
sinks:
  webhook:
    - url: https://backend.example/hook
      secret: ${NV_DEFINITELY_UNSET_SECRET_XYZ}
`), 0o644)
	if _, err := Load(p); err == nil {
		t.Fatal("Load should fail closed when a ${VAR} webhook secret resolves empty, got nil error")
	}
}

func TestResolvedTiersFallsBackToLegacyScalarThenCore(t *testing.T) {
	// A config that predates the list form must keep running exactly one tier,
	// and an empty config must not silently run nothing.
	for _, tc := range []struct {
		name string
		cfg  Config
		want []string
	}{
		{"legacy scalar", Config{Tier: "smart"}, []string{"smart"}},
		{"empty", Config{}, []string{"core"}},
		{"list wins over scalar", Config{Tier: "core", Tiers: []string{"gpu"}}, []string{"gpu"}},
		{"list order preserved", Config{Tiers: []string{"gpu", "core", "smart"}}, []string{"gpu", "core", "smart"}},
		{"duplicates collapse", Config{Tiers: []string{"core", "core", "smart"}}, []string{"core", "smart"}},
		{"blank entries dropped", Config{Tiers: []string{"", "smart", ""}}, []string{"smart"}},
		{"all blank falls back", Config{Tiers: []string{"", ""}}, []string{"core"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := tc.cfg.ResolvedTiers()
			if len(got) != len(tc.want) {
				t.Fatalf("ResolvedTiers() = %v, want %v", got, tc.want)
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Fatalf("ResolvedTiers() = %v, want %v", got, tc.want)
				}
			}
		})
	}
}

func TestLoadParsesTiersList(t *testing.T) {
	// The single-pod layout is only reachable if the list survives YAML load.
	dir := t.TempDir()
	p := filepath.Join(dir, "c.yaml")
	if err := os.WriteFile(p, []byte("tiers: [core, smart, gpu]\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(p)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got := cfg.ResolvedTiers(); len(got) != 3 || got[0] != "core" || got[2] != "gpu" {
		t.Fatalf("ResolvedTiers() = %v, want [core smart gpu]", got)
	}
}
