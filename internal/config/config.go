// Package config loads the nodevitals agent configuration.
package config

import (
	"fmt"
	"os"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// Rule defines a threshold-based state-transition condition.
type Rule struct {
	Metric    string  `yaml:"metric"`
	Device    string  `yaml:"device"`
	Condition string  `yaml:"condition"`
	Severity  string  `yaml:"severity"`
	Threshold float64 `yaml:"threshold"`
	EnterFor  int     `yaml:"enterFor"`
	ExitFor   int     `yaml:"exitFor"`
}

// WebhookConfig is one customer backend webhook endpoint.
type WebhookConfig struct {
	URL    string `yaml:"url"`
	Secret string `yaml:"secret"`
}

// MetricsConfig configures the Prometheus /metrics endpoint.
type MetricsConfig struct {
	Enabled    bool   `yaml:"enabled"`
	ListenAddr string `yaml:"listenAddr"`
}

// SinksConfig groups the configured delivery sinks.
type SinksConfig struct {
	Webhook []WebhookConfig `yaml:"webhook"`
	Metrics MetricsConfig   `yaml:"metrics"`
}

// Config is the nodevitals agent configuration.
type Config struct {
	Node            string      `yaml:"node"`
	Tier            string      `yaml:"tier"`
	IntervalSeconds int         `yaml:"intervalSeconds"`
	ProcRoot        string      `yaml:"procRoot"`
	SysRoot         string      `yaml:"sysRoot"`
	DevRoot         string      `yaml:"devRoot"`
	Rules           []Rule      `yaml:"rules"`
	Sinks           SinksConfig `yaml:"sinks"`
}

// Interval returns the collection interval, defaulting to 15s.
func (c Config) Interval() time.Duration {
	if c.IntervalSeconds <= 0 {
		return 15 * time.Second
	}
	return time.Duration(c.IntervalSeconds) * time.Second
}

// Load reads and parses a YAML config file, applying defaults.
func Load(path string) (Config, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return Config{}, fmt.Errorf("read config: %w", err)
	}
	var c Config
	if err := yaml.Unmarshal(b, &c); err != nil {
		return Config{}, fmt.Errorf("parse config: %w", err)
	}
	if c.ProcRoot == "" {
		c.ProcRoot = "/proc"
	}
	if c.SysRoot == "" {
		c.SysRoot = "/sys"
	}
	if c.DevRoot == "" {
		c.DevRoot = "/dev"
	}
	// Resolve a ${ENV} reference in webhook signing secrets so the key can be
	// injected from a Kubernetes Secret via an env var (secretKeyRef) rather
	// than living in the ConfigMap as plaintext. Only a value that is EXACTLY a
	// single ${VAR} reference is resolved; any other literal — including a real
	// secret that happens to contain '$' — is left untouched, never mangled.
	//
	// Fail CLOSED: if a ${VAR} reference resolves to an empty/unset env var,
	// refuse to start. Signing with an empty key produces a publicly
	// reproducible HMAC (no authenticity), and the ConfigMap placeholder hides
	// the misconfiguration — so this must be a hard error, not a silent
	// unsigned webhook. A deliberately unsigned webhook uses a literal empty
	// secret (no ${...}), which is left as-is.
	for i := range c.Sinks.Webhook {
		if name, ok := envRef(c.Sinks.Webhook[i].Secret); ok {
			v := os.Getenv(name)
			if v == "" {
				return Config{}, fmt.Errorf("webhook[%d] secret references ${%s}, but that env var is empty or unset (refusing to sign with an empty key)", i, name)
			}
			c.Sinks.Webhook[i].Secret = v
		}
	}
	return c, nil
}

// envRef reports whether s is exactly a single "${VAR}" reference and, if so,
// returns VAR. Any literal value (including one containing a '$') returns
// ok=false so it is passed through unchanged.
func envRef(s string) (string, bool) {
	if len(s) < 4 || !strings.HasPrefix(s, "${") || !strings.HasSuffix(s, "}") {
		return "", false
	}
	name := s[2 : len(s)-1]
	if name == "" || strings.ContainsAny(name, "${} ") {
		return "", false
	}
	return name, true
}
