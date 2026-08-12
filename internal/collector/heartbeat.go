package collector

import (
	"context"
	"runtime"
	"time"

	"github.com/KeiaiLab/nodevitals/internal/model"
)

type heartbeatCollector struct {
	node    string
	version string
}

// NewHeartbeat returns a collector that emits nodevitals_up and nodevitals_build_info.
func NewHeartbeat(node, version string) Collector {
	if version == "" {
		version = "0.8.5"
	}
	return &heartbeatCollector{node: node, version: version}
}

func (c *heartbeatCollector) Name() string { return "heartbeat" }

func (c *heartbeatCollector) Collect(ctx context.Context) ([]model.Sample, error) {
	now := time.Now().UTC()
	return []model.Sample{
		{
			Node:      c.node,
			Tier:      "core",
			Device:    "agent",
			Metric:    "nodevitals_up",
			Kind:      model.KindGauge,
			Value:     1.0,
			Timestamp: now,
		},
		{
			Node:      c.node,
			Tier:      "core",
			Device:    "agent",
			Metric:    "nodevitals_build_info",
			Kind:      model.KindGauge,
			Value:     1.0,
			Labels:    map[string]string{"version": c.version, "goversion": runtime.Version()},
			Timestamp: now,
		},
	}, nil
}
