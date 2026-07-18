package collector

import (
	"context"
	"fmt"
	"sync/atomic"
	"time"

	"github.com/KeiaiLab/nodevitals/internal/model"
)

// gpuDevice is a neutral snapshot of one GPU's polled telemetry — keeps
// go-nvml's cgo-bound types out of the collector surface, mirroring how
// smartDevice keeps anatol/smart.go's ioctl-bound types out (see smart.go).
type gpuDevice struct {
	Index                                               int
	UUID, Name                                          string
	UtilGPU, MemUsedBytes, MemTotalBytes, TempC, PowerW float64
	ThrottleReasons                                     uint64
	EccUncorrected                                      float64
}

// xidRaw is one raw XID event as delivered by the NVML EventSet subscription
// goroutine (added in a later task, gpu-tagged/cgo). Classification of the
// Xid field happens in xid.go (untagged, pure Go).
type xidRaw struct {
	DeviceIndex int
	UUID        string
	Xid         uint64
}

// gpuReader is production code's seam onto go-nvml: NVML has no pure-Go
// interface package (even pkg/nvml/mock imports the cgo-bound pkg/nvml), so
// CGO_ENABLED=0 builds and tests can never import go-nvml. All GPU collector
// logic (a later task) is tested against a fake gpuReader instead — the same
// pattern smartProbe uses for anatol/smart.go. The gpu-tagged NVML
// implementation lives behind this interface in a later task.
type gpuReader interface {
	Read(ctx context.Context) ([]gpuDevice, error) // polled snapshot
	XidEvents() <-chan xidRaw                      // async XID feed
	Close() error
}

// gpuCollector reports polled GPU telemetry (Collect) and streams classified
// XID hardware events (Events) via an injected gpuReader — go-nvml never
// appears here, only the neutral gpuDevice/xidRaw types above.
type gpuCollector struct {
	node   string
	reader gpuReader
	events chan model.Event
	// seq is the per-collector monotonic event sequence. Only the XID drain
	// goroutine (started in NewGPUCollector) increments it, but it's atomic
	// so that invariant is enforced rather than assumed.
	seq atomic.Uint64
}

// NewGPUCollector wires a GPU collector against an injected gpuReader and
// immediately starts its XID drain goroutine: it ranges r.XidEvents(),
// classifies each raw XID via classifyXid, and forwards the resulting
// model.Event on the channel Events() returns. Both the goroutine and the
// Events() channel end when r.XidEvents() closes — the reader owns closing
// it, mirroring how smartProbe owns the fake in smart tests.
func NewGPUCollector(node string, r gpuReader) Collector {
	c := &gpuCollector{node: node, reader: r, events: make(chan model.Event)}
	go func() {
		defer close(c.events)
		for raw := range r.XidEvents() {
			c.events <- c.toEvent(raw)
		}
	}()
	return c
}

func (c *gpuCollector) Name() string { return "gpu" }

func (c *gpuCollector) Collect(ctx context.Context) ([]model.Sample, error) {
	devices, err := c.reader.Read(ctx)
	if err != nil {
		return nil, fmt.Errorf("gpu read: %w", err)
	}

	now := time.Now().UTC()
	var out []model.Sample
	for _, d := range devices {
		device := fmt.Sprintf("gpu%d", d.Index)
		mk := func(metric, kind string, v float64) model.Sample {
			return model.Sample{Node: c.node, Tier: "gpu", Device: device, Metric: metric, Kind: kind, Value: v, Timestamp: now}
		}
		out = append(out,
			mk("gpu_utilization_pct", model.KindGauge, d.UtilGPU),
			mk("gpu_mem_used_bytes", model.KindGauge, d.MemUsedBytes),
			mk("gpu_mem_total_bytes", model.KindGauge, d.MemTotalBytes),
			mk("gpu_temperature_celsius", model.KindGauge, d.TempC),
			mk("gpu_power_watts", model.KindGauge, d.PowerW),
			mk("gpu_throttle_reasons", model.KindGauge, float64(d.ThrottleReasons)),
			mk("gpu_ecc_uncorrected_total", model.KindCounter, d.EccUncorrected),
		)
	}
	return out, nil
}

// Events returns the channel of classified XID events wired at construction,
// satisfying collector.EventSource. It is safe to call more than once — every
// call returns the same channel — and the agent reaches it by type-asserting
// the registered Collector to EventSource.
func (c *gpuCollector) Events() <-chan model.Event { return c.events }

// toEvent transforms one raw XID into a model.Event shaped exactly like the
// engine's own construction (event.go): Fingerprint() is computed from
// Node/Tier/Device/Condition, and ID = Fingerprint()+"-"+Phase+"-"+Seq. XID
// events bypass the engine — classifyXid pre-classifies them, no threshold
// evaluation applies — so Phase is always ENTER; each XID is a momentary
// occurrence, not a hysteresis state that later exits.
func (c *gpuCollector) toEvent(raw xidRaw) model.Event {
	class := classifyXid(raw.Xid)
	ev := model.Event{
		Node:      c.node,
		Tier:      "gpu",
		Device:    fmt.Sprintf("gpu%d", raw.DeviceIndex),
		Condition: class.Condition,
		Phase:     model.PhaseEnter,
		Severity:  class.Severity,
		Seq:       c.seq.Add(1),
		StartedAt: time.Now().UTC(),
		Detail:    map[string]any{"xid": raw.Xid, "description": class.Description},
	}
	ev.ID = fmt.Sprintf("%s-%s-%d", ev.Fingerprint(), ev.Phase, ev.Seq)
	return ev
}
