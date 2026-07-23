package collector

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/KeiaiLab/nodevitals/internal/dcgmcompat"
	"github.com/KeiaiLab/nodevitals/internal/model"
	"github.com/prometheus/client_golang/prometheus/testutil"
)

// fakeReader is a hand-written gpuReader test double — no go-nvml, no
// hardware. Read returns a canned snapshot (or readErr, to exercise the
// error path); XidEvents returns a test-controlled channel the test pushes
// xidRaw values to (and closes).
type fakeReader struct {
	devices []gpuDevice
	xidCh   chan xidRaw
	readErr error
}

func newFakeReader(devices []gpuDevice) *fakeReader {
	return &fakeReader{devices: devices, xidCh: make(chan xidRaw)}
}

func (f *fakeReader) Read(ctx context.Context) ([]gpuDevice, error) { return f.devices, f.readErr }
func (f *fakeReader) XidEvents() <-chan xidRaw                      { return f.xidCh }
func (f *fakeReader) DriverVersion() string                         { return "580.0" }
func (f *fakeReader) Close() error                                  { return nil }

// recvEvent reads one event with a timeout instead of a sleep, so the test
// fails deterministically (rather than hanging) if the collector never sends.
func recvEvent(t *testing.T, ch <-chan model.Event) model.Event {
	t.Helper()
	select {
	case ev := <-ch:
		return ev
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for event")
		return model.Event{}
	}
}

func TestGPUCollectMapsDeviceToSamples(t *testing.T) {
	r := newFakeReader([]gpuDevice{{
		Index: 0, UUID: "GPU-abc", Name: "NVIDIA A100", PCIBusID: "00000000:65:00.0",
		UtilGPU: 55, MemUsedBytes: 1e9, MemTotalBytes: 8e9,
		TempC: 70, PowerW: 250, ThrottleReasons: 0x40, EccUncorrected: 3, EccCorrected: 5,
	}})
	c := NewGPUCollector("test-node", r, nil)

	got, err := c.Collect(context.Background())
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}
	// 21 fixed samples (8 original + 13 DCGM-parity fields) + 1
	// gpu_throttle_active (ThrottleReasons 0x40 = hw_thermal_slowdown) = 22.
	if len(got) != 22 {
		t.Fatalf("want 22 samples (21 fixed + 1 throttle_active), got %d: %+v", len(got), got)
	}

	// deterministic order — table order from the design contract. The 21 fixed
	// samples come first, in this order; the variable gpu_throttle_active
	// sample(s) follow, so only the fixed prefix is order-checked.
	wantOrder := []string{
		"gpu_utilization_pct",
		"gpu_mem_used_bytes",
		"gpu_mem_total_bytes",
		"gpu_temperature_celsius",
		"gpu_power_watts",
		"gpu_throttle_reasons",
		"gpu_ecc_uncorrected_total",
		"gpu_ecc_corrected_total",
		"gpu_mem_free_bytes",
		"gpu_mem_reserved_bytes",
		"gpu_mem_copy_util_pct",
		"gpu_encoder_util_pct",
		"gpu_decoder_util_pct",
		"gpu_sm_clock_mhz",
		"gpu_mem_clock_mhz",
		"gpu_memory_temperature_celsius",
		"gpu_pcie_replay_total",
		"gpu_energy_joules_total",
		"gpu_remapped_rows_corrected_total",
		"gpu_remapped_rows_uncorrected_total",
		"gpu_row_remap_failed",
	}
	byMetric := map[string]model.Sample{}
	for i, s := range got {
		if i < len(wantOrder) && s.Metric != wantOrder[i] {
			t.Fatalf("got[%d].Metric = %q, want %q (deterministic order)", i, s.Metric, wantOrder[i])
		}
		byMetric[s.Metric] = s
		if s.Node != "test-node" {
			t.Fatalf("sample %s: Node = %q, want %q", s.Metric, s.Node, "test-node")
		}
		if s.Tier != "gpu" {
			t.Fatalf("sample %s: Tier = %q, want %q", s.Metric, s.Tier, "gpu")
		}
		if s.Device != "gpu0" {
			t.Fatalf("sample %s: Device = %q, want %q", s.Metric, s.Device, "gpu0")
		}
	}

	wantGauges := map[string]float64{
		"gpu_utilization_pct":     55,
		"gpu_mem_used_bytes":      1e9,
		"gpu_mem_total_bytes":     8e9,
		"gpu_temperature_celsius": 70,
		"gpu_power_watts":         250,
		"gpu_throttle_reasons":    64, // 0x40
	}
	for metric, want := range wantGauges {
		s := byMetric[metric]
		if s.Value != want {
			t.Fatalf("%s = %v, want %v", metric, s.Value, want)
		}
		if s.Kind != model.KindGauge {
			t.Fatalf("%s: Kind = %q, want gauge (zero value)", metric, s.Kind)
		}
	}

	ecc := byMetric["gpu_ecc_uncorrected_total"]
	if ecc.Value != 3 {
		t.Fatalf("gpu_ecc_uncorrected_total = %v, want 3", ecc.Value)
	}
	if ecc.Kind != model.KindCounter {
		t.Fatalf("gpu_ecc_uncorrected_total: Kind = %q, want %q", ecc.Kind, model.KindCounter)
	}

	corrected := byMetric["gpu_ecc_corrected_total"]
	if corrected.Value != 5 {
		t.Fatalf("gpu_ecc_corrected_total = %v, want 5", corrected.Value)
	}
	if corrected.Kind != model.KindCounter {
		t.Fatalf("gpu_ecc_corrected_total: Kind = %q, want %q", corrected.Kind, model.KindCounter)
	}
}

func TestGPUCollectZeroDevicesYieldsZeroSamplesNoError(t *testing.T) {
	r := newFakeReader(nil)
	c := NewGPUCollector("test-node", r, nil)

	got, err := c.Collect(context.Background())
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("want 0 samples, got %d: %+v", len(got), got)
	}
}

// TestGPUCollectWrapsReaderError mirrors smart.go's TestSmartCollectWrapsProbeError:
// a reader failure must surface as a wrapped error that errors.Is can unwrap,
// so callers can distinguish a GPU-read failure from an empty node.
func TestGPUCollectWrapsReaderError(t *testing.T) {
	sentinel := errors.New("nvml init failed")
	r := &fakeReader{xidCh: make(chan xidRaw), readErr: sentinel}
	c := NewGPUCollector("test-node", r, nil)

	_, err := c.Collect(context.Background())
	if err == nil {
		t.Fatal("Collect should propagate the reader error, got nil")
	}
	if !errors.Is(err, sentinel) {
		t.Fatalf("Collect error = %v, want it to wrap %v (errors.Is)", err, sentinel)
	}
}

// TestGPUCollectMultipleDevices covers the primary real-world case — a
// multi-GPU node — asserting each device's samples carry its own gpu<Index>
// device label and value, not just a single-device happy path.
func TestGPUCollectMultipleDevices(t *testing.T) {
	r := newFakeReader([]gpuDevice{
		{Index: 0, UtilGPU: 10},
		{Index: 1, UtilGPU: 20},
	})
	c := NewGPUCollector("test-node", r, nil)

	got, err := c.Collect(context.Background())
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}
	// Both devices have ThrottleReasons 0 → no gpu_throttle_active samples, so
	// only the 21 fixed metrics per device: 21 × 2 = 42.
	if len(got) != 42 {
		t.Fatalf("want 42 samples (21 metrics × 2 devices), got %d", len(got))
	}

	util := map[string]float64{}
	for _, s := range got {
		if s.Metric == "gpu_utilization_pct" {
			util[s.Device] = s.Value
		}
	}
	if util["gpu0"] != 10 {
		t.Fatalf("gpu0 utilization = %v, want 10", util["gpu0"])
	}
	if util["gpu1"] != 20 {
		t.Fatalf("gpu1 utilization = %v, want 20", util["gpu1"])
	}
}

// TestGPUCollectEmitsIdentityLabels pins G1: every sample a device produces —
// the fixed metrics and any gpu_throttle_active — carries that device's
// gpu_uuid/gpu_model/pci_bus_id identity, so a /metrics reading is attributable
// to one physical GPU rather than just a per-node gpu<Index> position.
func TestGPUCollectEmitsIdentityLabels(t *testing.T) {
	r := newFakeReader([]gpuDevice{{
		Index: 0, UUID: "GPU-abc", Name: "NVIDIA A100", PCIBusID: "00000000:65:00.0",
		ThrottleReasons: 0x40, // → also exercises a gpu_throttle_active sample
	}})
	c := NewGPUCollector("test-node", r, nil)

	got, err := c.Collect(context.Background())
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}
	if len(got) == 0 {
		t.Fatal("expected samples, got none")
	}
	sawThrottle := false
	for _, s := range got {
		if s.Metric == "gpu_throttle_active" {
			sawThrottle = true
		}
		if s.Labels["gpu_uuid"] != "GPU-abc" {
			t.Fatalf("%s: Labels[gpu_uuid] = %q, want %q", s.Metric, s.Labels["gpu_uuid"], "GPU-abc")
		}
		if s.Labels["gpu_model"] != "NVIDIA A100" {
			t.Fatalf("%s: Labels[gpu_model] = %q, want %q", s.Metric, s.Labels["gpu_model"], "NVIDIA A100")
		}
		if s.Labels["pci_bus_id"] != "00000000:65:00.0" {
			t.Fatalf("%s: Labels[pci_bus_id] = %q, want %q", s.Metric, s.Labels["pci_bus_id"], "00000000:65:00.0")
		}
	}
	if !sawThrottle {
		t.Fatal("expected a gpu_throttle_active sample (0x40) to also carry identity labels")
	}
}

// TestGPUCollectEccCorrectedCounter pins G3: gpu_ecc_corrected_total mirrors
// the uncorrected counter — a monotonic KindCounter carrying the polled
// aggregate corrected-ECC count, emitted immediately after
// gpu_ecc_uncorrected_total in the fixed order.
func TestGPUCollectEccCorrectedCounter(t *testing.T) {
	r := newFakeReader([]gpuDevice{{Index: 0, EccCorrected: 5}})
	c := NewGPUCollector("test-node", r, nil)

	got, err := c.Collect(context.Background())
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}

	uncorrectedIdx, correctedIdx := -1, -1
	var corrected model.Sample
	for i, s := range got {
		switch s.Metric {
		case "gpu_ecc_uncorrected_total":
			uncorrectedIdx = i
		case "gpu_ecc_corrected_total":
			correctedIdx = i
			corrected = s
		}
	}
	if correctedIdx == -1 {
		t.Fatalf("gpu_ecc_corrected_total not emitted: %+v", got)
	}
	if corrected.Value != 5 {
		t.Fatalf("gpu_ecc_corrected_total = %v, want 5", corrected.Value)
	}
	if corrected.Kind != model.KindCounter {
		t.Fatalf("gpu_ecc_corrected_total: Kind = %q, want %q", corrected.Kind, model.KindCounter)
	}
	if correctedIdx != uncorrectedIdx+1 {
		t.Fatalf("gpu_ecc_corrected_total at index %d, want right after gpu_ecc_uncorrected_total (index %d)", correctedIdx, uncorrectedIdx)
	}
}

// TestGPUCollectThrottleActivePerReason pins G4: each set throttle-reason bit
// becomes one gpu_throttle_active gauge (value 1) labeled with that reason plus
// the device identity, in decodeThrottle's ascending-bit order, while the raw
// gpu_throttle_reasons mask sample stays present. A zero mask emits none.
func TestGPUCollectThrottleActivePerReason(t *testing.T) {
	r := newFakeReader([]gpuDevice{{
		Index: 0, UUID: "GPU-abc", Name: "NVIDIA A100", PCIBusID: "00000000:65:00.0",
		ThrottleReasons: 0x20 | 0x40,
	}})
	c := NewGPUCollector("test-node", r, nil)

	got, err := c.Collect(context.Background())
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}

	var reasons []string
	rawMaskPresent := false
	for _, s := range got {
		switch s.Metric {
		case "gpu_throttle_active":
			reasons = append(reasons, s.Labels["reason"])
			if s.Value != 1 {
				t.Fatalf("gpu_throttle_active value = %v, want 1", s.Value)
			}
			if s.Kind != model.KindGauge {
				t.Fatalf("gpu_throttle_active: Kind = %q, want gauge", s.Kind)
			}
			// identity labels ride alongside the reason label.
			if s.Labels["gpu_uuid"] != "GPU-abc" || s.Labels["pci_bus_id"] != "00000000:65:00.0" {
				t.Fatalf("gpu_throttle_active missing identity labels: %+v", s.Labels)
			}
		case "gpu_throttle_reasons":
			rawMaskPresent = true
			if s.Value != 96 { // 0x20 | 0x40
				t.Fatalf("raw gpu_throttle_reasons mask = %v, want 96", s.Value)
			}
		}
	}

	want := []string{"sw_thermal_slowdown", "hw_thermal_slowdown"} // ascending-bit order
	if len(reasons) != len(want) {
		t.Fatalf("gpu_throttle_active reasons = %v, want %v", reasons, want)
	}
	for i := range want {
		if reasons[i] != want[i] {
			t.Fatalf("gpu_throttle_active[%d] reason = %q, want %q (ascending-bit order)", i, reasons[i], want[i])
		}
	}
	if !rawMaskPresent {
		t.Fatal("raw gpu_throttle_reasons mask sample must remain present alongside decoded reasons")
	}

	// Second case: a zero mask emits zero gpu_throttle_active samples.
	r0 := newFakeReader([]gpuDevice{{Index: 0, ThrottleReasons: 0}})
	got0, err := NewGPUCollector("test-node", r0, nil).Collect(context.Background())
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}
	for _, s := range got0 {
		if s.Metric == "gpu_throttle_active" {
			t.Fatalf("zero throttle mask must emit no gpu_throttle_active, got %+v", s)
		}
	}

	// Third case: benign clock reasons (idle/app-clocks/sync-boost/display) are
	// filtered — gpu_throttle_active must fire only for performance-limiting bits,
	// else NVML's ever-set gpu_idle would make every idle GPU a false positive.
	rb := newFakeReader([]gpuDevice{{Index: 0, ThrottleReasons: 0x1 | 0x40}}) // gpu_idle(benign) + hw_thermal_slowdown(active)
	gotb, err := NewGPUCollector("test-node", rb, nil).Collect(context.Background())
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}
	var active []string
	for _, s := range gotb {
		if s.Metric == "gpu_throttle_active" {
			active = append(active, s.Labels["reason"])
		}
	}
	if len(active) != 1 || active[0] != "hw_thermal_slowdown" {
		t.Fatalf("gpu_throttle_active reasons = %v, want exactly [hw_thermal_slowdown] (benign gpu_idle filtered)", active)
	}
}

func TestGPUEventsClassifiesXidToModelEvent(t *testing.T) {
	r := newFakeReader(nil)
	c := NewGPUCollector("test-node", r, nil)
	// The agent reaches XID events by type-asserting the registered Collector
	// to EventSource — exercise that same path here, not a concrete shortcut.
	events := c.(EventSource).Events()

	r.xidCh <- xidRaw{DeviceIndex: 0, Xid: 79}
	ev := recvEvent(t, events)

	if ev.Node != "test-node" {
		t.Fatalf("Node = %q, want %q", ev.Node, "test-node")
	}
	if ev.Tier != "gpu" {
		t.Fatalf("Tier = %q, want %q", ev.Tier, "gpu")
	}
	if ev.Device != "gpu0" {
		t.Fatalf("Device = %q, want %q", ev.Device, "gpu0")
	}
	if ev.Condition != "gpu_xid_error" {
		t.Fatalf("Condition = %q, want %q", ev.Condition, "gpu_xid_error")
	}
	if ev.Phase != model.PhaseEnter {
		t.Fatalf("Phase = %q, want %q", ev.Phase, model.PhaseEnter)
	}
	if ev.Severity != "critical" {
		t.Fatalf("Severity (xid 79) = %q, want %q", ev.Severity, "critical")
	}
	wantClass := classifyXid(79)
	xid, ok := ev.Detail["xid"].(uint64)
	if !ok || xid != 79 {
		t.Fatalf("Detail[xid] = %#v, want uint64(79)", ev.Detail["xid"])
	}
	desc, ok := ev.Detail["description"].(string)
	if !ok || desc != wantClass.Description {
		t.Fatalf("Detail[description] = %#v, want %q", ev.Detail["description"], wantClass.Description)
	}
	// ID composition mirrors event.go: Fingerprint()+"-"+Phase+"-"+Seq. This
	// is the first event out of a fresh collector, so Seq == 1.
	wantID := ev.Fingerprint() + "-" + model.PhaseEnter + "-1"
	if ev.ID != wantID {
		t.Fatalf("ID = %q, want %q (Fingerprint()+phase+seq composition)", ev.ID, wantID)
	}

	r.xidCh <- xidRaw{DeviceIndex: 0, Xid: 13}
	ev2 := recvEvent(t, events)
	if ev2.Severity != "info" {
		t.Fatalf("Severity (xid 13) = %q, want %q", ev2.Severity, "info")
	}

	// Same (node,tier,device,condition) on both events → same fingerprint;
	// distinct seq → distinct ID (else dedup would drop the second XID).
	if ev.Fingerprint() != ev2.Fingerprint() {
		t.Fatalf("both XID events on gpu0 should share a fingerprint: %q vs %q", ev.Fingerprint(), ev2.Fingerprint())
	}
	if ev.ID == ev2.ID {
		t.Fatalf("sequential XID events must have distinct IDs, both %q", ev.ID)
	}

	// Closing the source must close Events() too, with no goroutine leak.
	close(r.xidCh)
	select {
	case _, open := <-events:
		if open {
			t.Fatal("events channel should be closed after source closes, got a value instead")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("events channel did not close within 2s after source closed (possible goroutine leak)")
	}
}

func TestGPUCollectFeedsDCGMCompatExporter(t *testing.T) {
	// One Collect must land the same snapshot on both surfaces: native samples
	// (bytes/joules) and the DCGM compat exporter (MiB/mJ at render time).
	r := newFakeReader([]gpuDevice{{
		Index: 0, UUID: "GPU-abc", Name: "RTX 4070", PCIBusID: "00000000:08:00.0",
		MemUsedBytes: 296 * 1 << 20, MemFreeBytes: 11719 * 1 << 20,
		EnergyMilliJoules: 5000, SMClockMHz: 210,
	}})
	dcgm := dcgmcompat.New("test-node")
	c := NewGPUCollector("test-node", r, dcgm)

	got, err := c.Collect(context.Background())
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}

	// Native energy is joules (mJ/1000).
	for _, s := range got {
		if s.Metric == "gpu_energy_joules_total" && s.Value != 5 {
			t.Fatalf("gpu_energy_joules_total = %v, want 5 (5000 mJ)", s.Value)
		}
	}

	// The exporter now renders the same device with DCGM units and the fake
	// reader's driver version on the label.
	exp := `# HELP DCGM_FI_DEV_FB_USED Framebuffer memory used (in MiB).
# TYPE DCGM_FI_DEV_FB_USED gauge
DCGM_FI_DEV_FB_USED{DCGM_FI_DRIVER_VERSION="580.0",Hostname="test-node",UUID="GPU-abc",device="nvidia0",gpu="0",modelName="RTX 4070",pci_bus_id="00000000:08:00.0"} 296
# HELP DCGM_FI_DEV_TOTAL_ENERGY_CONSUMPTION Total energy consumption since boot (in mJ).
# TYPE DCGM_FI_DEV_TOTAL_ENERGY_CONSUMPTION counter
DCGM_FI_DEV_TOTAL_ENERGY_CONSUMPTION{DCGM_FI_DRIVER_VERSION="580.0",Hostname="test-node",UUID="GPU-abc",device="nvidia0",gpu="0",modelName="RTX 4070",pci_bus_id="00000000:08:00.0"} 5000
`
	if err := testutil.CollectAndCompare(dcgm, strings.NewReader(exp),
		"DCGM_FI_DEV_FB_USED", "DCGM_FI_DEV_TOTAL_ENERGY_CONSUMPTION"); err != nil {
		t.Fatal(err)
	}
}
