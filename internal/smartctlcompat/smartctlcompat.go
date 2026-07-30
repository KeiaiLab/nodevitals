// Package smartctlcompat serves a smartctl_exporter-compatible smartctl_*
// metric surface from nodevitals' own SMART snapshot, so one DaemonSet can
// replace a separate smartctl_exporter one — the storage sibling of
// internal/nodeexporter and internal/dcgmcompat.
//
// Unlike node_exporter, smartctl_exporter cannot be embedded as a library: it
// shells out to the smartmontools `smartctl` binary and parses its JSON, so
// embedding would drag a runtime binary dependency into an image that
// deliberately ships no shell. nodevitals reads the same SATA attributes and
// NVMe health log over ioctl (anatol/smart.go) instead, so this package
// re-emits the exposition contract from that native snapshot — metric names,
// HELP text, value types, units (power-on in seconds, not hours) and identity
// labels are matched against a live smartctl_exporter 0.14.0 scrape
// (see smartctlcompat_test.go).
//
// Deliberately absent, because nodevitals does not read the underlying data
// and inventing it would be worse than omitting it:
//
//   - attribute_flags_long / attribute_flags_short. The ioctl path returns raw
//     attribute values, not the flag byte. The flags are also per-firmware, not
//     per-attribute — on one live node id=5 reports "PO--CK" on a WDC disk
//     while id=187 reports "-O--CK" on a Seagate — so a constant table would be
//     wrong for some fleet. Empty-valued labels are dropped at ingestion
//     anyway, so omitting them yields the same stored series.
//   - attribute_value_type "value" / "worst" / "thresh". Only "raw" is
//     collected; the normalised triple has no source here.
//   - smartctl_device (info), _capacity_bytes, _block_size, _rotation_rate,
//     _interface_speed, _power_cycle_count, _bytes_read / _written,
//     _smart_status, _error_log_count, _num_err_log_entries. These come from
//     smartctl's JSON identity and log pages, which nodevitals does not parse.
//
// Every field this surface does carry also exists natively as
// nodevitals_hw_smart_* / nodevitals_hw_nvme_*, so consumers can finish the
// migration on the native names without the compat shim ever becoming the
// richer surface — the same rule internal/collector/gpu.go follows for DCGM.
package smartctlcompat

import (
	"regexp"
	"strconv"
	"sync"

	"github.com/prometheus/client_golang/prometheus"
)

// Device is one storage device's polled SMART snapshot in nodevitals-native
// units — power-on arrives in HOURS and is converted to smartctl's seconds at
// render, mirroring how dcgmcompat converts bytes to MiB.
type Device struct {
	// Name is the nodevitals block-device name ("sda", "nvme0n1"), not the
	// smartctl one; smartctlName below does that translation.
	Name string
	// Temperature is degrees celsius, nil when the device did not report one.
	Temperature *float64
	// PowerOnHours is nil when the device did not report it.
	PowerOnHours *uint64
	// ATAAttrs holds raw SATA attribute values keyed by attribute ID. Only the
	// IDs in ataAttrs are rendered.
	ATAAttrs map[uint8]uint64
	// NVMe is nil on non-NVMe devices.
	NVMe *NVMe
}

// NVMe carries the NVMe health-log fields the compat surface exposes.
type NVMe struct {
	PercentageUsed, AvailableSpare, SpareThreshold float64
	MediaErrors, CriticalWarning                   float64
}

// nvmeNamespace matches an NVMe namespace block name (nvme0n1, nvme10n1).
// smartctl addresses the CONTROLLER (nvme0), so the compat surface must too:
// a dashboard filtering device="nvme0" would otherwise match nothing.
var nvmeNamespace = regexp.MustCompile(`^(nvme\d+)n\d+$`)

// smartctlName renders a nodevitals block name the way smartctl_exporter
// labels it. SATA names pass through unchanged.
func smartctlName(name string) string {
	if m := nvmeNamespace.FindStringSubmatch(name); m != nil {
		return m[1]
	}
	return name
}

// ataAttrs maps the SMART attribute IDs nodevitals collects to smartmontools'
// canonical names. The slice fixes exposition order — ranging Device.ATAAttrs
// directly would be map-order nondeterministic and make output flaky.
var ataAttrs = []struct {
	id   uint8
	name string
}{
	{5, "Reallocated_Sector_Ct"},
	{187, "Reported_Uncorrect"},
	{188, "Command_Timeout"},
	{197, "Current_Pending_Sector"},
	{198, "Offline_Uncorrectable"},
}

// scalarDef is one smartctl_* family carrying a single value per device, with
// only a device label. name and help are byte-for-byte from a live
// smartctl_exporter 0.14.0 scrape. value reports ok=false when the device did
// not supply the field, and the series is then skipped entirely — matching
// smartctl_exporter, which emits nothing rather than a zero for a field the
// hardware never reported.
type scalarDef struct {
	name, help string
	kind       prometheus.ValueType
	value      func(Device) (float64, bool)
}

// nvmeField lifts an NVMe accessor into a scalarDef value func, skipping
// non-NVMe devices.
func nvmeField(get func(NVMe) float64) func(Device) (float64, bool) {
	return func(d Device) (float64, bool) {
		if d.NVMe == nil {
			return 0, false
		}
		return get(*d.NVMe), true
	}
}

const secondsPerHour = 3600

var scalarDefs = []scalarDef{
	{"smartctl_device_power_on_seconds", "Device power on seconds", prometheus.CounterValue, func(d Device) (float64, bool) {
		if d.PowerOnHours == nil {
			return 0, false
		}
		return float64(*d.PowerOnHours) * secondsPerHour, true
	}},
	{"smartctl_device_percentage_used", "Device write percentage used", prometheus.CounterValue,
		nvmeField(func(n NVMe) float64 { return n.PercentageUsed })},
	{"smartctl_device_available_spare", "Normalized percentage (0 to 100%) of the remaining spare capacity available", prometheus.CounterValue,
		nvmeField(func(n NVMe) float64 { return n.AvailableSpare })},
	{"smartctl_device_available_spare_threshold", "When the Available Spare falls below the threshold indicated in this field, an asynchronous event completion may occur. The value is indicated as a normalized percentage (0 to 100%)", prometheus.CounterValue,
		nvmeField(func(n NVMe) float64 { return n.SpareThreshold })},
	{"smartctl_device_media_errors", "Contains the number of occurrences where the controller detected an unrecovered data integrity error. Errors such as uncorrectable ECC, CRC checksum failure, or LBA tag mismatch are included in this field", prometheus.CounterValue,
		nvmeField(func(n NVMe) float64 { return n.MediaErrors })},
	{"smartctl_device_critical_warning", "This field indicates critical warnings for the state of the controller", prometheus.CounterValue,
		nvmeField(func(n NVMe) float64 { return n.CriticalWarning })},
}

// Exporter implements prometheus.Collector over the latest Update snapshot.
// The zero value is not usable — call New.
type Exporter struct {
	mu      sync.RWMutex
	devices []Device
}

// New returns an Exporter with an empty snapshot. Unlike dcgmcompat it needs
// no hostname: smartctl_exporter carries no node identity label, leaving the
// node dimension to the Prometheus target labels.
func New() *Exporter {
	return &Exporter{}
}

// Update atomically replaces the exposed snapshot. Called by the smart
// collector with the same polled devices it turns into native samples, so one
// ioctl sweep feeds both surfaces — never two competing SMART probes.
func (e *Exporter) Update(devices []Device) {
	e.mu.Lock()
	e.devices = devices
	e.mu.Unlock()
}

func (e *Exporter) Describe(ch chan<- *prometheus.Desc) {
	prometheus.DescribeByCollect(e, ch)
}

func (e *Exporter) Collect(ch chan<- prometheus.Metric) {
	e.mu.RLock()
	defer e.mu.RUnlock()

	for _, def := range scalarDefs {
		desc := prometheus.NewDesc(def.name, def.help, []string{"device"}, nil)
		for _, d := range e.devices {
			if v, ok := def.value(d); ok {
				ch <- prometheus.MustNewConstMetric(desc, def.kind, v, smartctlName(d.Name))
			}
		}
	}

	// temperature_type distinguishes current from the min/max lifetime values
	// smartctl also reports; nodevitals only reads the current one.
	tempDesc := prometheus.NewDesc("smartctl_device_temperature", "Device temperature celsius",
		[]string{"device", "temperature_type"}, nil)
	for _, d := range e.devices {
		if d.Temperature != nil {
			ch <- prometheus.MustNewConstMetric(tempDesc, prometheus.GaugeValue, *d.Temperature,
				smartctlName(d.Name), "current")
		}
	}

	attrDesc := prometheus.NewDesc("smartctl_device_attribute", "Device attributes",
		[]string{"device", "attribute_id", "attribute_name", "attribute_value_type"}, nil)
	for _, d := range e.devices {
		for _, a := range ataAttrs {
			v, ok := d.ATAAttrs[a.id]
			if !ok {
				continue
			}
			ch <- prometheus.MustNewConstMetric(attrDesc, prometheus.GaugeValue, float64(v),
				smartctlName(d.Name), strconv.Itoa(int(a.id)), a.name, "raw")
		}
	}
}
