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
	driver  string // NVML driver version, read once at init

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

	// Subscribe to XID critical-error events only. A dedicated single/double-bit
	// ECC event subscription was evaluated and dropped: watchXid consumes only
	// XID events, so ECC events would be received and immediately discarded (pure
	// wakeup overhead, and SBE has a dedicated storm bit), while OR-ing the ECC
	// bits into an all-or-nothing RegisterEvents risks NOT_SUPPORTED on
	// ECC-eventless GPUs — which would drop the XID subscription too. The ECC
	// signal is already carried by the polled aggregate gpu_ecc_*_total counters
	// and by XID classification (48/92/94/95).
	for _, dev := range devices {
		if ret := dev.RegisterEvents(nvml.EventTypeXidCriticalError, set); ret != nvml.SUCCESS {
			uuid, _ := dev.GetUUID()
			slog.Warn("nvml register xid events", "device", uuid, "err", ret.Error())
		}
	}

	// Best-effort: an empty version leaves the DCGM_FI_DRIVER_VERSION label
	// empty (dropped at ingestion), never blocks the reader.
	driver, _ := nvml.SystemGetDriverVersion()

	r := &nvmlReader{
		devices: devices,
		driver:  driver,
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
			d.MemCopyUtil = float64(util.Memory)
		}
		// _v2 adds Free and Reserved (the DCGM FB_FREE/FB_RESERVED fields);
		// pre-R515 drivers only answer the v1 call, so fall back rather than
		// losing used/total on them.
		if mem, ret := dev.GetMemoryInfo_v2(); ret == nvml.SUCCESS {
			d.MemUsedBytes = float64(mem.Used)
			d.MemTotalBytes = float64(mem.Total)
			d.MemFreeBytes = float64(mem.Free)
			d.MemReservedBytes = float64(mem.Reserved)
		} else if mem, ret := dev.GetMemoryInfo(); ret == nvml.SUCCESS {
			d.MemUsedBytes = float64(mem.Used)
			d.MemTotalBytes = float64(mem.Total)
			d.MemFreeBytes = float64(mem.Free)
		}
		if temp, ret := dev.GetTemperature(nvml.TEMPERATURE_GPU); ret == nvml.SUCCESS {
			d.TempC = float64(temp)
		}
		// Memory (HBM) temperature only exists as an NVML field value, not a
		// TemperatureSensors_t member. Consumer GPUs answer NOT_SUPPORTED in
		// NvmlReturn — leaving 0, which is exactly what dcgm-exporter reports
		// for them (fleet-verified).
		fv := []nvml.FieldValue{{FieldId: nvml.FI_DEV_MEMORY_TEMP}}
		if ret := dev.GetFieldValues(fv); ret == nvml.SUCCESS && nvml.Return(fv[0].NvmlReturn) == nvml.SUCCESS {
			d.MemTempC = decodeFieldValue(fv[0].ValueType, fv[0].Value)
		}
		if mw, ret := dev.GetPowerUsage(); ret == nvml.SUCCESS {
			d.PowerW = float64(mw) / 1000.0
		}
		if mj, ret := dev.GetTotalEnergyConsumption(); ret == nvml.SUCCESS {
			d.EnergyMilliJoules = float64(mj)
		}
		if enc, _, ret := dev.GetEncoderUtilization(); ret == nvml.SUCCESS {
			d.EncUtil = float64(enc)
		}
		if dec, _, ret := dev.GetDecoderUtilization(); ret == nvml.SUCCESS {
			d.DecUtil = float64(dec)
		}
		if clk, ret := dev.GetClockInfo(nvml.CLOCK_SM); ret == nvml.SUCCESS {
			d.SMClockMHz = float64(clk)
		}
		if clk, ret := dev.GetClockInfo(nvml.CLOCK_MEM); ret == nvml.SUCCESS {
			d.MemClockMHz = float64(clk)
		}
		if replays, ret := dev.GetPcieReplayCounter(); ret == nvml.SUCCESS {
			d.PcieReplayTotal = float64(replays)
		}
		if corr, unc, _, failed, ret := dev.GetRemappedRows(); ret == nvml.SUCCESS {
			d.RemappedCorr = float64(corr)
			d.RemappedUnc = float64(unc)
			d.RemapFailed = failed
		}
		if reasons, ret := dev.GetCurrentClocksEventReasons(); ret == nvml.SUCCESS {
			d.ThrottleReasons = reasons
		}
		// BusId is a fixed-width, NUL-terminated C char array ([32]int8); the
		// int8→string conversion lives untagged in pci.go (unit-tested under
		// CGO_ENABLED=0), only the slice happens at this cgo boundary.
		if pci, ret := dev.GetPciInfo(); ret == nvml.SUCCESS {
			d.PCIBusID = int8ToString(pci.BusId[:])
		}
		// AGGREGATE (lifetime), not VOLATILE: the gpu_ecc_*_total metrics are
		// KindCounters and must be monotonic. VOLATILE resets on driver reload/
		// reboot, which would drop the counter to 0 and fire a spurious EXIT —
		// clearing a real hardware-fault alert. Aggregate persists.
		if ecc, ret := dev.GetTotalEccErrors(nvml.MEMORY_ERROR_TYPE_UNCORRECTED, nvml.AGGREGATE_ECC); ret == nvml.SUCCESS {
			d.EccUncorrected = float64(ecc)
		}
		if ecc, ret := dev.GetTotalEccErrors(nvml.MEMORY_ERROR_TYPE_CORRECTED, nvml.AGGREGATE_ECC); ret == nvml.SUCCESS {
			d.EccCorrected = float64(ecc)
		}

		out = append(out, d)
	}
	return out, nil
}

func (r *nvmlReader) XidEvents() <-chan xidRaw { return r.xidCh }

func (r *nvmlReader) DriverVersion() string { return r.driver }

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
