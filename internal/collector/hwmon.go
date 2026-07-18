package collector

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/KeiaiLab/nodevitals/internal/model"
)

type hwmonCollector struct {
	node    string
	sysRoot string
}

// NewHwmon reports temperature/fan sensors from <sysRoot>/class/hwmon. A missing
// hwmon tree yields zero samples (not an error) — many nodes have no sensors.
func NewHwmon(node, sysRoot string) Collector { return &hwmonCollector{node: node, sysRoot: sysRoot} }

func (c *hwmonCollector) Name() string { return "hwmon" }

func readTrim(p string) (string, error) {
	b, err := os.ReadFile(p)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(b)), nil
}

func (c *hwmonCollector) Collect(ctx context.Context) ([]model.Sample, error) {
	base := filepath.Join(c.sysRoot, "class", "hwmon")
	chips, err := os.ReadDir(base)
	if err != nil {
		return nil, nil // no hwmon tree → no samples, not an error
	}
	now := time.Now().UTC()
	var out []model.Sample
	for _, chip := range chips {
		dir := filepath.Join(base, chip.Name())
		name, err := readTrim(filepath.Join(dir, "name"))
		if err != nil {
			name = chip.Name()
		}
		entries, err := os.ReadDir(dir)
		if err != nil {
			continue
		}
		for _, e := range entries {
			fn := e.Name()
			switch {
			case strings.HasPrefix(fn, "temp") && strings.HasSuffix(fn, "_input"):
				if v, err := readTrim(filepath.Join(dir, fn)); err == nil {
					if milli, err := strconv.ParseFloat(v, 64); err == nil {
						out = append(out, model.Sample{
							Node: c.node, Tier: "core",
							Device: name + "/" + strings.TrimSuffix(fn, "_input"),
							Metric: "temp_celsius", Value: milli / 1000.0, Timestamp: now,
						})
					}
				}
			case strings.HasPrefix(fn, "fan") && strings.HasSuffix(fn, "_input"):
				if v, err := readTrim(filepath.Join(dir, fn)); err == nil {
					if rpm, err := strconv.ParseFloat(v, 64); err == nil {
						out = append(out, model.Sample{
							Node: c.node, Tier: "core",
							Device: name + "/" + strings.TrimSuffix(fn, "_input"),
							Metric: "fan_rpm", Value: rpm, Timestamp: now,
						})
					}
				}
			}
		}
	}
	return out, nil
}
