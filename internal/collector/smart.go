package collector

import (
	"context"
	"fmt"
	"regexp"
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
}

// nvmeHealth carries NVMe-specific health counters. Defined here for Task 2,
// which adds the smartDevice.NVMe → Sample mapping.
type nvmeHealth struct {
	PercentageUsed, AvailableSpare, SpareThreshold float64
	MediaErrors, CriticalWarning                   float64
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
			Name:         d.Name,
			Temperature:  d.Temperature,
			PowerOnHours: d.PowerOnHours,
			ATAAttrs:     d.ATAAttrs,
		}
		if d.NVMe != nil {
			sd.NVMe = &smartctlcompat.NVMe{
				PercentageUsed:  d.NVMe.PercentageUsed,
				AvailableSpare:  d.NVMe.AvailableSpare,
				SpareThreshold:  d.NVMe.SpareThreshold,
				MediaErrors:     d.NVMe.MediaErrors,
				CriticalWarning: d.NVMe.CriticalWarning,
			}
		}
		out = append(out, sd)
	}
	return out
}
