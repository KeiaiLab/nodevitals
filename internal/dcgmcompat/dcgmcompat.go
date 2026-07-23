// Package dcgmcompat serves a dcgm-exporter-compatible DCGM_FI_* metric
// surface from nodevitals' own NVML snapshot, so one DaemonSet can replace a
// separate dcgm-exporter one — the GPU sibling of internal/nodeexporter.
//
// Unlike node_exporter, dcgm-exporter cannot be embedded as a library: it
// hard-requires the DCGM host engine (libdcgm), a second GPU management
// daemon nodevitals exists to avoid. So this package re-emits the exact
// exposition contract instead — metric names, HELP text, value types, units
// (framebuffer in MiB, energy in mJ) and identity labels are matched
// byte-for-byte against a live dcgm-exporter 4.x so existing dashboards and
// rules keep working when dcgm-exporter is retired.
//
// Deliberately absent: the container/namespace/pod attribution labels
// dcgm-exporter adds from the kubelet pod-resources socket. An unallocated
// GPU's empty-valued labels are dropped at ingestion anyway, so for those
// series the output is identical; carrying real attribution needs a
// pod-resources client and is a separate increment behind its own flag.
package dcgmcompat

import (
	"strconv"
	"sync"

	"github.com/prometheus/client_golang/prometheus"
)

// Device is one GPU's polled snapshot, in nodevitals-native units (bytes —
// NOT pre-converted; conversion to DCGM units happens at render).
type Device struct {
	Index                                  int
	UUID, Model, PCIBusID                  string
	UtilGPU, MemCopyUtil, EncUtil, DecUtil float64
	FBUsedBytes, FBFreeBytes, FBRsvdBytes  float64
	TempC, MemTempC                        float64
	PowerW                                 float64
	EnergyMilliJoules                      float64
	SMClockMHz, MemClockMHz                float64
	PcieReplayTotal                        float64
	RemappedCorr, RemappedUnc              float64
	RemapFailed                            bool
}

// metricDef is one DCGM_FI_* family: name and help are byte-for-byte from a
// live dcgm-exporter scrape (see dcgmcompat_test.go), value pulls the field
// out of a Device in DCGM units.
type metricDef struct {
	name, help string
	kind       prometheus.ValueType
	value      func(Device) float64
}

const mib = 1 << 20

var metricDefs = []metricDef{
	{"DCGM_FI_DEV_SM_CLOCK", "SM clock frequency (in MHz).", prometheus.GaugeValue, func(d Device) float64 { return d.SMClockMHz }},
	{"DCGM_FI_DEV_MEM_CLOCK", "Memory clock frequency (in MHz).", prometheus.GaugeValue, func(d Device) float64 { return d.MemClockMHz }},
	{"DCGM_FI_DEV_MEMORY_TEMP", "Memory temperature (in C).", prometheus.GaugeValue, func(d Device) float64 { return d.MemTempC }},
	{"DCGM_FI_DEV_GPU_TEMP", "GPU temperature (in C).", prometheus.GaugeValue, func(d Device) float64 { return d.TempC }},
	{"DCGM_FI_DEV_POWER_USAGE", "Power draw (in W).", prometheus.GaugeValue, func(d Device) float64 { return d.PowerW }},
	{"DCGM_FI_DEV_TOTAL_ENERGY_CONSUMPTION", "Total energy consumption since boot (in mJ).", prometheus.CounterValue, func(d Device) float64 { return d.EnergyMilliJoules }},
	{"DCGM_FI_DEV_PCIE_REPLAY_COUNTER", "Total number of PCIe retries.", prometheus.CounterValue, func(d Device) float64 { return d.PcieReplayTotal }},
	{"DCGM_FI_DEV_GPU_UTIL", "GPU utilization (in %).", prometheus.GaugeValue, func(d Device) float64 { return d.UtilGPU }},
	{"DCGM_FI_DEV_MEM_COPY_UTIL", "Memory utilization (in %).", prometheus.GaugeValue, func(d Device) float64 { return d.MemCopyUtil }},
	{"DCGM_FI_DEV_ENC_UTIL", "Encoder utilization (in %).", prometheus.GaugeValue, func(d Device) float64 { return d.EncUtil }},
	{"DCGM_FI_DEV_DEC_UTIL", "Decoder utilization (in %).", prometheus.GaugeValue, func(d Device) float64 { return d.DecUtil }},
	{"DCGM_FI_DEV_FB_FREE", "Framebuffer memory free (in MiB).", prometheus.GaugeValue, func(d Device) float64 { return d.FBFreeBytes / mib }},
	{"DCGM_FI_DEV_FB_USED", "Framebuffer memory used (in MiB).", prometheus.GaugeValue, func(d Device) float64 { return d.FBUsedBytes / mib }},
	{"DCGM_FI_DEV_FB_RESERVED", "Framebuffer memory reserved (in MiB).", prometheus.GaugeValue, func(d Device) float64 { return d.FBRsvdBytes / mib }},
	{"DCGM_FI_DEV_UNCORRECTABLE_REMAPPED_ROWS", "Number of remapped rows for uncorrectable errors", prometheus.CounterValue, func(d Device) float64 { return d.RemappedUnc }},
	{"DCGM_FI_DEV_CORRECTABLE_REMAPPED_ROWS", "Number of remapped rows for correctable errors", prometheus.CounterValue, func(d Device) float64 { return d.RemappedCorr }},
	{"DCGM_FI_DEV_ROW_REMAP_FAILURE", "Whether remapping of rows has failed", prometheus.GaugeValue, func(d Device) float64 {
		if d.RemapFailed {
			return 1
		}
		return 0
	}},
	// Constant 0 on non-vGPU deployments — dcgm-exporter emits the same there,
	// and nodevitals targets bare-metal/passthrough nodes, never licensed vGPU.
	{"DCGM_FI_DEV_VGPU_LICENSE_STATUS", "vGPU License status", prometheus.GaugeValue, func(Device) float64 { return 0 }},
}

// labelNames is the dcgm-exporter identity labelset. Hostname carries the
// node name (matching live dcgm-exporter under gpu-operator, which resolves
// it to the node), DCGM_FI_DRIVER_VERSION the NVML driver version.
var labelNames = []string{"gpu", "UUID", "pci_bus_id", "device", "modelName", "Hostname", "DCGM_FI_DRIVER_VERSION"}

// Exporter implements prometheus.Collector over the latest Update snapshot.
type Exporter struct {
	hostname string

	mu            sync.RWMutex
	driverVersion string
	devices       []Device
}

func New(hostname string) *Exporter {
	return &Exporter{hostname: hostname}
}

// Update atomically replaces the exposed snapshot. driverVersion rides along
// because both come from the same NVML session owned by the caller.
func (e *Exporter) Update(driverVersion string, devices []Device) {
	e.mu.Lock()
	e.driverVersion = driverVersion
	e.devices = devices
	e.mu.Unlock()
}

func (e *Exporter) Describe(ch chan<- *prometheus.Desc) {
	prometheus.DescribeByCollect(e, ch)
}

func (e *Exporter) Collect(ch chan<- prometheus.Metric) {
	e.mu.RLock()
	defer e.mu.RUnlock()
	for _, def := range metricDefs {
		desc := prometheus.NewDesc(def.name, def.help, labelNames, nil)
		for _, d := range e.devices {
			ch <- prometheus.MustNewConstMetric(desc, def.kind, def.value(d),
				strconv.Itoa(d.Index), d.UUID, d.PCIBusID, "nvidia"+strconv.Itoa(d.Index), d.Model, e.hostname, e.driverVersion)
		}
	}
}
