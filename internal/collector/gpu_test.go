package collector

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/KeiaiLab/nodevitals/internal/model"
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
		Index: 0, UtilGPU: 55, MemUsedBytes: 1e9, MemTotalBytes: 8e9,
		TempC: 70, PowerW: 250, ThrottleReasons: 0x40, EccUncorrected: 3,
	}})
	c := NewGPUCollector("test-node", r)

	got, err := c.Collect(context.Background())
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}
	if len(got) != 7 {
		t.Fatalf("want 7 samples, got %d: %+v", len(got), got)
	}

	// deterministic order — table order from the design contract.
	wantOrder := []string{
		"gpu_utilization_pct",
		"gpu_mem_used_bytes",
		"gpu_mem_total_bytes",
		"gpu_temperature_celsius",
		"gpu_power_watts",
		"gpu_throttle_reasons",
		"gpu_ecc_uncorrected_total",
	}
	byMetric := map[string]model.Sample{}
	for i, s := range got {
		if s.Metric != wantOrder[i] {
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
}

func TestGPUCollectZeroDevicesYieldsZeroSamplesNoError(t *testing.T) {
	r := newFakeReader(nil)
	c := NewGPUCollector("test-node", r)

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
	c := NewGPUCollector("test-node", r)

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
	c := NewGPUCollector("test-node", r)

	got, err := c.Collect(context.Background())
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}
	if len(got) != 14 {
		t.Fatalf("want 14 samples (7 metrics × 2 devices), got %d", len(got))
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

func TestGPUEventsClassifiesXidToModelEvent(t *testing.T) {
	r := newFakeReader(nil)
	c := NewGPUCollector("test-node", r)
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
