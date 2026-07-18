// Package config loads the nodevitals agent configuration.
package config

import (
	"fmt"
	"os"
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
	return c, nil
}
