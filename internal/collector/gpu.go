package collector

import (
	"context"
	"fmt"
	"sync/atomic"
	"time"

	"github.com/KeiaiLab/nodevitals/internal/dcgmcompat"
	"github.com/KeiaiLab/nodevitals/internal/model"
)

// gpuDevice is a neutral snapshot of one GPU's polled telemetry — keeps
// go-nvml's cgo-bound types out of the collector surface, mirroring how
// smartDevice keeps anatol/smart.go's ioctl-bound types out (see smart.go).
type gpuDevice struct {
	Index                                               int
	UUID, Name, PCIBusID                                string
	UtilGPU, MemUsedBytes, MemTotalBytes, TempC, PowerW float64
	ThrottleReasons                                     uint64
	EccUncorrected, EccCorrected                        float64
	// The fields below complete the dcgm-exporter surface (dcgmcompat): an
	// unsupported NVML call leaves its field at 0, which is also what
	// dcgm-exporter reports for unsupported fields — parity by default.
	MemFreeBytes, MemReservedBytes float64
	MemCopyUtil, EncUtil, DecUtil  float64
	SMClockMHz, MemClockMHz        float64
	MemTempC                       float64
	PcieReplayTotal                float64
	EnergyMilliJoules              float64
	RemappedCorr, RemappedUnc      float64
	RemapFailed                    bool
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
	DriverVersion() string                         // NVML driver version, "" if unknown
	Close() error
}

// gpuCollector reports polled GPU telemetry (Collect) and streams classified
// XID hardware events (Events) via an injected gpuReader — go-nvml never
// appears here, only the neutral gpuDevice/xidRaw types above.
type gpuCollector struct {
	node   string
	reader gpuReader
	dcgm   *dcgmcompat.Exporter // nil = compat surface off
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
//
// A non-nil dcgm receives the same polled snapshot on every Collect, keeping
// the DCGM_FI_* compat surface in lockstep with the native samples — one NVML
// poll feeds both, never two competing NVML sessions.
func NewGPUCollector(node string, r gpuReader, dcgm *dcgmcompat.Exporter) Collector {
	c := &gpuCollector{node: node, reader: r, dcgm: dcgm, events: make(chan model.Event)}
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
		// idLabels is the shared G1 identity attached to every fixed sample this
		// device emits, so a /metrics reading is attributable to one physical
		// GPU (uuid globally unique; pci_bus_id node-local but stable per slot).
		// The throttle loop below must NOT reuse this map — see the fresh-map note.
		idLabels := map[string]string{"gpu_uuid": d.UUID, "gpu_model": d.Name, "pci_bus_id": d.PCIBusID}
		mk := func(metric, kind string, v float64) model.Sample {
			return model.Sample{Node: c.node, Tier: "gpu", Device: device, Metric: metric, Kind: kind, Value: v, Labels: idLabels, Timestamp: now}
		}
		out = append(out,
			mk("gpu_utilization_pct", model.KindGauge, d.UtilGPU),
			mk("gpu_mem_used_bytes", model.KindGauge, d.MemUsedBytes),
			mk("gpu_mem_total_bytes", model.KindGauge, d.MemTotalBytes),
			mk("gpu_temperature_celsius", model.KindGauge, d.TempC),
			mk("gpu_power_watts", model.KindGauge, d.PowerW),
			mk("gpu_throttle_reasons", model.KindGauge, float64(d.ThrottleReasons)),
			mk("gpu_ecc_uncorrected_total", model.KindCounter, d.EccUncorrected),
			mk("gpu_ecc_corrected_total", model.KindCounter, d.EccCorrected),
			// Native families for every field the DCGM compat surface carries, so
			// consumers can finish on nodevitals_hw_* without the compat shim ever
			// becoming the richer surface. Native units stay SI-ish (bytes, joules);
			// only dcgmcompat renders DCGM's MiB/mJ.
			mk("gpu_mem_free_bytes", model.KindGauge, d.MemFreeBytes),
			mk("gpu_mem_reserved_bytes", model.KindGauge, d.MemReservedBytes),
			mk("gpu_mem_copy_util_pct", model.KindGauge, d.MemCopyUtil),
			mk("gpu_encoder_util_pct", model.KindGauge, d.EncUtil),
			mk("gpu_decoder_util_pct", model.KindGauge, d.DecUtil),
			mk("gpu_sm_clock_mhz", model.KindGauge, d.SMClockMHz),
			mk("gpu_mem_clock_mhz", model.KindGauge, d.MemClockMHz),
			mk("gpu_memory_temperature_celsius", model.KindGauge, d.MemTempC),
			mk("gpu_pcie_replay_total", model.KindCounter, d.PcieReplayTotal),
			mk("gpu_energy_joules_total", model.KindCounter, d.EnergyMilliJoules/1000),
			mk("gpu_remapped_rows_corrected_total", model.KindCounter, d.RemappedCorr),
			mk("gpu_remapped_rows_uncorrected_total", model.KindCounter, d.RemappedUnc),
			mk("gpu_row_remap_failed", model.KindGauge, boolToFloat(d.RemapFailed)),
		)
		// G4: emit one gpu_throttle_active gauge per *performance-limiting* reason
		// bit, alongside the raw mask sample above. Benign clock reasons (idle,
		// app-set clocks, sync-boost, display) are skipped so the metric means what
		// its name says — NVML sets gpu_idle on every idle GPU, which would else
		// make gpu_throttle_active==1 a permanent false positive. Each reason gets
		// a FRESH label map (identity + reason) — aliasing idLabels would leak a
		// reason key onto the fixed samples and break Prometheus family consistency.
		for _, reason := range decodeThrottle(d.ThrottleReasons) {
			if benignThrottleReasons[reason] {
				continue
			}
			lbls := map[string]string{"gpu_uuid": d.UUID, "gpu_model": d.Name, "pci_bus_id": d.PCIBusID, "reason": reason}
			out = append(out, model.Sample{Node: c.node, Tier: "gpu", Device: device, Metric: "gpu_throttle_active", Kind: model.KindGauge, Value: 1, Labels: lbls, Timestamp: now})
		}
	}
	if c.dcgm != nil {
		c.dcgm.Update(c.reader.DriverVersion(), toDCGMDevices(devices))
	}
	return out, nil
}

func boolToFloat(b bool) float64 {
	if b {
		return 1
	}
	return 0
}

// toDCGMDevices converts the reader snapshot into dcgmcompat's neutral type —
// a straight field copy; unit conversion is dcgmcompat's job.
func toDCGMDevices(devices []gpuDevice) []dcgmcompat.Device {
	out := make([]dcgmcompat.Device, 0, len(devices))
	for _, d := range devices {
		out = append(out, dcgmcompat.Device{
			Index: d.Index, UUID: d.UUID, Model: d.Name, PCIBusID: d.PCIBusID,
			UtilGPU: d.UtilGPU, MemCopyUtil: d.MemCopyUtil, EncUtil: d.EncUtil, DecUtil: d.DecUtil,
			FBUsedBytes: d.MemUsedBytes, FBFreeBytes: d.MemFreeBytes, FBRsvdBytes: d.MemReservedBytes,
			TempC: d.TempC, MemTempC: d.MemTempC,
			PowerW:            d.PowerW,
			EnergyMilliJoules: d.EnergyMilliJoules,
			SMClockMHz:        d.SMClockMHz, MemClockMHz: d.MemClockMHz,
			PcieReplayTotal: d.PcieReplayTotal,
			RemappedCorr:    d.RemappedCorr, RemappedUnc: d.RemappedUnc, RemapFailed: d.RemapFailed,
		})
	}
	return out
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
