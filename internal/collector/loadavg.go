package collector

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/KeiaiLab/nodevitals/internal/model"
)

type loadAvg struct {
	node     string
	procRoot string
}

// NewLoadAvg reads 1-minute load average from <procRoot>/loadavg.
func NewLoadAvg(node, procRoot string) Collector {
	return &loadAvg{node: node, procRoot: procRoot}
}

func (l *loadAvg) Name() string { return "loadavg" }

func (l *loadAvg) Collect(ctx context.Context) ([]model.Sample, error) {
	b, err := os.ReadFile(filepath.Join(l.procRoot, "loadavg"))
	if err != nil {
		return nil, fmt.Errorf("read loadavg: %w", err)
	}
	fields := strings.Fields(string(b))
	if len(fields) < 1 {
		return nil, fmt.Errorf("malformed loadavg: %q", string(b))
	}
	v, err := strconv.ParseFloat(fields[0], 64)
	if err != nil {
		return nil, fmt.Errorf("parse load1: %w", err)
	}
	return []model.Sample{{
		Node: l.node, Tier: "core", Device: "cpu", Metric: "load1",
		Value: v, Timestamp: time.Now().UTC(),
	}}, nil
}
