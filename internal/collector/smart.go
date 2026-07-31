package collector

import (
	"context"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/KeiaiLab/nodevitals/internal/model"
	"github.com/KeiaiLab/nodevitals/internal/smartctlcompat"
)

// smartDevice is a neutral parse result for one storage device's SMART
// state — keeps anatol/smart.go's ioctl-bound types out of the collector
// surface so the mapping logic below stays testable without real hardware.
type smartDevice struct {
	Name         string           // "sda", "nvme0n1"
	Transport    string           // "sata" | "nvme"
	Temperature  *float64         // °C (nil if unavailable)
	PowerOnHours *uint64          // nil if unavailable
	ATAAttrs     map[uint8]uint64 // SATA raw attribute values; only 5,187,188,197,198 populated
	NVMe         *nvmeHealth      // nvme-only fields (Task 2 mapping)
	// PowerCycles is the lifetime power-cycle count, nil if unavailable. Read
	// from the same generic-attribute pass that yields Temperature.
	PowerCycles *uint64
	// Attrs carries the FULL SATA attribute table (not just the five in
	// sataAttrMetrics), each with its normalised value/worst/threshold and
	// flag word. The native surface still emits only the frozen five; this
	// table exists so the smartctl compat surface can reproduce
	// smartctl_device_attribute in full. Nil on NVMe.
	Attrs []smartAttr
	// Info is the device's IDENTIFY-derived identity and geometry, nil when
	// the IDENTIFY command failed.
	Info *smartInfo
	// Healthy mirrors SMART overall-health: true = passed. Nil when it could
	// not be determined.
	Healthy *bool
	// ErrorLogCount is the SATA SMART summary error-log entry count, nil on
	// NVMe or when the log could not be read.
	ErrorLogCount *uint64
}

// smartAttr is one SATA SMART attribute with every field smartctl reports.
type smartAttr struct {
	ID                        uint8
	Name                      string
	Flags                     uint16
	Raw                       uint64
	Current, Worst, Threshold uint8
	HasThreshold              bool
}

// smartInfo is the IDENTIFY-derived device identity and geometry. Fields the
// transport does not define stay zero and are skipped by their consumers —
// rotation rate is SATA-only, NVMeCapacityBytes NVMe-only.
type smartInfo struct {
	Model, Serial, Firmware string
	// LogicalBlockSize and PhysicalBlockSize are bytes. CapacityBlocks counts
	// logical blocks; CapacityBytes is their product.
	LogicalBlockSize, PhysicalBlockSize uint64
	CapacityBlocks, CapacityBytes       uint64
	// RotationRate is RPM; 0 means non-rotating or unreported, and the
	// consumer omits the series rather than claiming 0 RPM.
	RotationRate uint64
	// InterfaceSpeedCurrent / Max are bits per second, 0 = unknown.
	InterfaceSpeedCurrent, InterfaceSpeedMax float64
	// NVMeCapacityBytes is the controller's total NVM capacity (TNVMCAP),
	// 0 when unreported.
	NVMeCapacityBytes uint64
}

// nvmeHealth carries NVMe-specific health counters. Defined here for Task 2,
// which adds the smartDevice.NVMe → Sample mapping.
type nvmeHealth struct {
	PercentageUsed, AvailableSpare, SpareThreshold float64
	MediaErrors, CriticalWarning                   float64
	// BytesRead / BytesWritten are host I/O totals in bytes, already widened
	// from the health log's 512-byte-unit-of-1000 counters. SATA has no
	// equivalent the same way (its Total_LBAs_* attributes are vendor-scaled),
	// which is why smartctl_exporter emits bytes only for NVMe.
	BytesRead, BytesWritten float64
	// NumErrLogEntries is the controller's lifetime error-information log
	// entry count.
	NumErrLogEntries float64
}

// smartProbe returns the current SMART state for every discovered storage
// device. anatol/smart.go is ioctl-bound and unmockable, so production code
// wraps it behind this seam (Task 3); tests inject a fake.
type smartProbe func(ctx context.Context) ([]smartDevice, error)

// wholeSATADevice matches whole SATA/SCSI disk device names (sda, sdb, ...,
// sdaa) but not their partitions (sda1). wholeNVMeDevice matches whole NVMe
// namespace devices (nvme0n1, nvme10n1) but not their partitions
// (nvme0n1p1). Compiled once — used by isWholeDevice.
var (
	wholeSATADevice = regexp.MustCompile(`^sd[a-z]+$`)
	wholeNVMeDevice = regexp.MustCompile(`^nvme\d+n\d+$`)
)

// isWholeDevice reports whether name (a basename under devRoot, e.g. "sda" or
// "nvme0n1") identifies a whole block device that the production probe
// (smart_probe_linux.go) should glob in — as opposed to a partition, loop
// device, device-mapper node, or optical drive, none of which carry their own
// SMART/NVMe health data. OS-independent so it's unit-testable without a
// disk.
func isWholeDevice(name string) bool {
	return wholeSATADevice.MatchString(name) || wholeNVMeDevice.MatchString(name)
}

// ataString decodes one of IDENTIFY's fixed-width text fields (model, serial,
// firmware).
//
// These fields are defined as sequences of 16-bit words with the FIRST
// character in the high byte, so over a little-endian ioctl every byte pair
// arrives transposed: a drive reporting "WDC  WUH721414ALE600" reads back as
// "DW CW HU274141LA6E00" until the pairs are swapped. The spec pads with
// spaces (20h), which the trim removes. Lives here rather than in the
// linux-only probe so it stays unit-testable without a disk, the same reason
// isWholeDevice does.
func ataString(b []byte) string {
	s := make([]byte, len(b))
	copy(s, b)
	for i := 0; i+1 < len(s); i += 2 {
		s[i], s[i+1] = s[i+1], s[i]
	}
	return strings.TrimSpace(string(s))
}

type smartCollector struct {
	node  string
	probe smartProbe
	sc    *smartctlcompat.Exporter // nil = compat surface off
}

// NewSmart reports disk SMART health (SATA attributes, NVMe health log) via
// an injected probe.
//
// A non-nil sc receives the same probed snapshot on every Collect, keeping the
// smartctl_* compat surface in lockstep with the native samples — one ioctl
// sweep feeds both, never two competing SMART probes. This mirrors how
// NewGPUCollector feeds dcgmcompat from a single NVML poll.
func NewSmart(node string, probe smartProbe, sc *smartctlcompat.Exporter) Collector {
	return &smartCollector{node: node, probe: probe, sc: sc}
}

func (c *smartCollector) Name() string { return "smart" }

// sataAttrMetrics fixes ATAAttrs emission order — map iteration is
// nondeterministic and would make sample order flaky.
var sataAttrMetrics = []struct {
	id     uint8
	metric string
}{
	{5, "smart_reallocated_sectors"},
	{187, "smart_reported_uncorrectable"},
	{188, "smart_command_timeout"},
	{197, "smart_pending_sectors"},
	{198, "smart_offline_uncorrectable"},
}

func (c *smartCollector) Collect(ctx context.Context) ([]model.Sample, error) {
	devices, err := c.probe(ctx)
	if err != nil {
		return nil, fmt.Errorf("smart probe: %w", err)
	}

	now := time.Now().UTC()
	var out []model.Sample
	for _, d := range devices {
		mk := func(metric string, v float64) model.Sample {
			return model.Sample{Node: c.node, Tier: "smart", Device: d.Name, Metric: metric, Value: v, Timestamp: now}
		}
		if d.Temperature != nil {
			out = append(out, mk("smart_temperature_celsius", *d.Temperature))
		}
		if d.PowerOnHours != nil {
			out = append(out, mk("smart_power_on_hours", float64(*d.PowerOnHours)))
		}
		for _, a := range sataAttrMetrics {
			if v, ok := d.ATAAttrs[a.id]; ok {
				out = append(out, mk(a.metric, float64(v)))
			}
		}
		if d.NVMe != nil {
			out = append(out, mk("nvme_percentage_used", d.NVMe.PercentageUsed))
			out = append(out, mk("nvme_available_spare", d.NVMe.AvailableSpare))
			out = append(out, mk("nvme_available_spare_threshold", d.NVMe.SpareThreshold))
			out = append(out, mk("nvme_media_errors", d.NVMe.MediaErrors))
			out = append(out, mk("nvme_critical_warning", d.NVMe.CriticalWarning))
			out = append(out, mk("nvme_bytes_read_total", d.NVMe.BytesRead))
			out = append(out, mk("nvme_bytes_written_total", d.NVMe.BytesWritten))
			out = append(out, mk("nvme_error_log_entries", d.NVMe.NumErrLogEntries))
		}
		// Native families for every field the smartctl compat surface carries,
		// so consumers can finish on nodevitals_hw_* without the compat shim
		// ever becoming the richer surface — the rule gpu.go follows for DCGM.
		if d.PowerCycles != nil {
			out = append(out, mk("smart_power_cycles", float64(*d.PowerCycles)))
		}
		if d.Healthy != nil {
			out = append(out, mk("smart_health_passed", boolToFloat(*d.Healthy)))
		}
		if d.ErrorLogCount != nil {
			out = append(out, mk("smart_error_log_count", float64(*d.ErrorLogCount)))
		}
		if d.Info != nil {
			if d.Info.CapacityBytes > 0 {
				out = append(out, mk("smart_capacity_bytes", float64(d.Info.CapacityBytes)))
			}
			if d.Info.LogicalBlockSize > 0 {
				out = append(out, mk("smart_logical_block_size_bytes", float64(d.Info.LogicalBlockSize)))
			}
			// 0 RPM means non-rotating or unreported, not "a stopped disk".
			if d.Info.RotationRate > 0 {
				out = append(out, mk("smart_rotation_rate_rpm", float64(d.Info.RotationRate)))
			}
			if d.Info.InterfaceSpeedCurrent > 0 {
				out = append(out, mk("smart_interface_speed_bits_per_second", d.Info.InterfaceSpeedCurrent))
			}
		}
	}
	if c.sc != nil {
		c.sc.Update(toSmartctlDevices(devices))
	}
	return out, nil
}

// toSmartctlDevices projects the probed snapshot onto the compat package's
// own Device type. The probe hands back freshly built values on every call and
// the collector keeps no reference to them afterwards, so handing the pointers
// and the attribute map straight over is safe — the exporter becomes their
// only owner the moment Update returns.
func toSmartctlDevices(devices []smartDevice) []smartctlcompat.Device {
	out := make([]smartctlcompat.Device, 0, len(devices))
	for _, d := range devices {
		sd := smartctlcompat.Device{
			Name:          d.Name,
			Transport:     d.Transport,
			Temperature:   d.Temperature,
			PowerOnHours:  d.PowerOnHours,
			PowerCycles:   d.PowerCycles,
			Healthy:       d.Healthy,
			ErrorLogCount: d.ErrorLogCount,
		}
		for _, a := range d.Attrs {
			sd.Attrs = append(sd.Attrs, smartctlcompat.Attr{
				ID:           a.ID,
				Name:         a.Name,
				Flags:        a.Flags,
				Raw:          a.Raw,
				Current:      a.Current,
				Worst:        a.Worst,
				Threshold:    a.Threshold,
				HasThreshold: a.HasThreshold,
			})
		}
		if d.Info != nil {
			sd.Info = &smartctlcompat.Info{
				Model:                 d.Info.Model,
				Serial:                d.Info.Serial,
				Firmware:              d.Info.Firmware,
				LogicalBlockSize:      d.Info.LogicalBlockSize,
				PhysicalBlockSize:     d.Info.PhysicalBlockSize,
				CapacityBlocks:        d.Info.CapacityBlocks,
				CapacityBytes:         d.Info.CapacityBytes,
				RotationRate:          d.Info.RotationRate,
				InterfaceSpeedCurrent: d.Info.InterfaceSpeedCurrent,
				InterfaceSpeedMax:     d.Info.InterfaceSpeedMax,
				NVMeCapacityBytes:     d.Info.NVMeCapacityBytes,
			}
		}
		if d.NVMe != nil {
			sd.NVMe = &smartctlcompat.NVMe{
				PercentageUsed:   d.NVMe.PercentageUsed,
				AvailableSpare:   d.NVMe.AvailableSpare,
				SpareThreshold:   d.NVMe.SpareThreshold,
				MediaErrors:      d.NVMe.MediaErrors,
				CriticalWarning:  d.NVMe.CriticalWarning,
				BytesRead:        d.NVMe.BytesRead,
				BytesWritten:     d.NVMe.BytesWritten,
				NumErrLogEntries: d.NVMe.NumErrLogEntries,
			}
		}
		out = append(out, sd)
	}
	return out
}
