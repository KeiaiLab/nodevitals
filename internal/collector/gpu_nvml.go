//go:build gpu

package collector

import (
	"context"
	"fmt"
	"log/slog"
	"sync"

	"github.com/NVIDIA/go-nvml/pkg/nvml"
)

// nvmlReader is the gpu-tagged, cgo-backed gpuReader implementation. See
// gpu.go for why gpuReader exists as a seam: NVML has no pure-Go interface
// package, so this file (and go-nvml) only ever enters the build graph under
// `-tags gpu` with CGO_ENABLED=1 — the default CGO_ENABLED=0 build never
// compiles it (gpu_stub.go stands in instead).
type nvmlReader struct {
	devices []nvml.Device

	set   nvml.EventSet
	xidCh chan xidRaw

	// done tells watchXid to stop; stopped is closed by watchXid right
	// after it closes xidCh, so Close can block until it's safe to Free
	// the EventSet and Shutdown NVML (see Close/watchXid).
	done      chan struct{}
	stopped   chan struct{}
	closeOnce sync.Once
}

// NewNVMLReader initializes NVML, enumerates devices, creates the XID
// EventSet, and starts the XID watch goroutine. Device enumeration and XID
// registration are best-effort per device — a single device that fails to
// hand back a handle, or that doesn't support XID event registration (some
// GPUs/vGPU configurations don't), is logged and skipped rather than failing
// the whole reader. Init/DeviceGetCount/EventSetCreate failures are fundamental
// and fail the reader, unwinding any partial NVML initialization first.
func NewNVMLReader() (gpuReader, error) {
	if ret := nvml.Init(); ret != nvml.SUCCESS {
		return nil, fmt.Errorf("nvml init: %s", ret.Error())
	}

	count, ret := nvml.DeviceGetCount()
	if ret != nvml.SUCCESS {
		nvml.Shutdown()
		return nil, fmt.Errorf("nvml device count: %s", ret.Error())
	}

	devices := make([]nvml.Device, 0, count)
	for i := range count {
		dev, ret := nvml.DeviceGetHandleByIndex(i)
		if ret != nvml.SUCCESS {
			slog.Warn("nvml device handle", "index", i, "err", ret.Error())
			continue
		}
		devices = append(devices, dev)
	}

	set, ret := nvml.EventSetCreate()
	if ret != nvml.SUCCESS {
		nvml.Shutdown()
		return nil, fmt.Errorf("nvml event set create: %s", ret.Error())
	}

	for _, dev := range devices {
		if ret := dev.RegisterEvents(nvml.EventTypeXidCriticalError, set); ret != nvml.SUCCESS {
			uuid, _ := dev.GetUUID()
			slog.Warn("nvml register xid events", "device", uuid, "err", ret.Error())
		}
	}

	r := &nvmlReader{
		devices: devices,
		set:     set,
		xidCh:   make(chan xidRaw),
		done:    make(chan struct{}),
		stopped: make(chan struct{}),
	}
	go r.watchXid()
	return r, nil
}

// watchXid blocks on the EventSet and forwards XID numbers until told to
// stop. It owns xidCh's lifecycle: closing it (via the deferred close below)
// is what ends NewGPUCollector's `for range r.XidEvents()` drain goroutine.
// The two deferred closes run in reverse (LIFO) order — close(xidCh) first,
// then close(stopped) — so by the time Close observes <-r.stopped, xidCh has
// already closed and watchXid is guaranteed to have made its last call to
// set.Wait, making it safe to Free the set.
func (r *nvmlReader) watchXid() {
	defer close(r.stopped)
	defer close(r.xidCh)

	for {
		select {
		case <-r.done:
			return
		default:
		}

		data, ret := r.set.Wait(1000)
		if ret == nvml.ERROR_TIMEOUT {
			continue // no event this cycle — loop back to observe done within ~1s
		}
		if ret != nvml.SUCCESS {
			continue
		}

		idx, _ := data.Device.GetIndex()
		uuid, _ := data.Device.GetUUID()
		raw := xidRaw{DeviceIndex: idx, UUID: uuid, Xid: data.EventData}

		select {
		case r.xidCh <- raw:
		case <-r.done: // a blocked send can't outlive shutdown
			return
		}
	}
}

// Read polls every enumerated device for its current telemetry. Each metric
// is best-effort: an unsupported or failing call leaves that field at its
// zero value rather than dropping the device from the snapshot. Read returns
// a nil error unless something fundamental fails — there is currently
// nothing at that level, since per-device/per-metric failures are all
// absorbed above.
func (r *nvmlReader) Read(_ context.Context) ([]gpuDevice, error) {
	out := make([]gpuDevice, 0, len(r.devices))
	for _, dev := range r.devices {
		idx, ret := dev.GetIndex()
		if ret != nvml.SUCCESS {
			continue // can't even identify the device — skip it
		}
		uuid, _ := dev.GetUUID()
		name, _ := dev.GetName()
		d := gpuDevice{Index: idx, UUID: uuid, Name: name}

		if util, ret := dev.GetUtilizationRates(); ret == nvml.SUCCESS {
			d.UtilGPU = float64(util.Gpu)
		}
		if mem, ret := dev.GetMemoryInfo(); ret == nvml.SUCCESS {
			d.MemUsedBytes = float64(mem.Used)
			d.MemTotalBytes = float64(mem.Total)
		}
		if temp, ret := dev.GetTemperature(nvml.TEMPERATURE_GPU); ret == nvml.SUCCESS {
			d.TempC = float64(temp)
		}
		if mw, ret := dev.GetPowerUsage(); ret == nvml.SUCCESS {
			d.PowerW = float64(mw) / 1000.0
		}
		if reasons, ret := dev.GetCurrentClocksEventReasons(); ret == nvml.SUCCESS {
			d.ThrottleReasons = reasons
		}
		// AGGREGATE (lifetime), not VOLATILE: gpu_ecc_uncorrected_total is a
		// KindCounter and must be monotonic. VOLATILE resets on driver reload/
		// reboot, which would drop the counter to 0 and fire a spurious EXIT —
		// clearing a real hardware-fault alert. Aggregate persists.
		if ecc, ret := dev.GetTotalEccErrors(nvml.MEMORY_ERROR_TYPE_UNCORRECTED, nvml.AGGREGATE_ECC); ret == nvml.SUCCESS {
			d.EccUncorrected = float64(ecc)
		}

		out = append(out, d)
	}
	return out, nil
}

func (r *nvmlReader) XidEvents() <-chan xidRaw { return r.xidCh }

// Close stops watchXid, waits for it to fully return (guaranteeing it will
// never call set.Wait again), and only then frees the EventSet and shuts
// NVML down. sync.Once makes a double Close safe. The ordering is the crux
// of this reader's correctness: Free/Shutdown must never race a live
// set.Wait call, and watchXid must never send on xidCh after it's been
// asked to stop — both are enforced by done/stopped above.
func (r *nvmlReader) Close() error {
	r.closeOnce.Do(func() {
		close(r.done)
		<-r.stopped
		r.set.Free()
		nvml.Shutdown()
	})
	return nil
}
