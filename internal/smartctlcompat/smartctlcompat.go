// Package smartctlcompat serves a smartctl_exporter-compatible smartctl_*
// metric surface from nodevitals' own SMART snapshot, so one DaemonSet can
// replace a separate smartctl_exporter one — the storage sibling of
// internal/nodeexporter and internal/dcgmcompat.
//
// Unlike node_exporter, smartctl_exporter cannot be embedded as a library: it
// shells out to the smartmontools `smartctl` binary and parses its JSON, so
// embedding would drag a runtime binary dependency into an image that
// deliberately ships no shell. nodevitals reads the same IDENTIFY response,
// SATA attribute table and NVMe health log over ioctl (anatol/smart.go), so
// this package re-emits the exposition contract from that native snapshot —
// metric names, HELP text, value types, units (power-on in seconds not hours,
// NVMe host I/O in bytes not 512-byte units) and identity labels are matched
// against a live smartctl_exporter 0.14.0 scrape (see smartctlcompat_test.go).
//
// Coverage is 22 of the 25 families a live smartctl_exporter serves. The three
// left out are not hardware readings but facts about the smartctl_exporter
// process itself, which nodevitals would have to fabricate:
//
//   - smartctl_version — the version of the smartmontools binary. nodevitals
//     runs no such binary.
//   - smartctl_exporter_build_info — build metadata of smartctl_exporter.
//     Emitting it would assert that smartctl_exporter is running.
//   - smartctl_device_smartctl_exit_status — the exit code of the per-device
//     smartctl invocation, and not merely 0/1: a live SATA scrape returns 64
//     (device error log not empty). There is no smartctl process here whose
//     status could be reported, and a synthetic 0 would silently mask the
//     condition that a real 64 signals.
//
// Which families appear on which transport follows the live scrape rather than
// data availability: bytes_read/bytes_written and num_err_log_entries are NVMe
// only, while rotation_rate, interface_speed and error_log_count are SATA
// only, because that is what smartctl_exporter emits. Serving a family the
// original omits would break the contract just as surely as omitting one.
//
// Every field this surface carries also exists natively as
// nodevitals_hw_smart_* / nodevitals_hw_nvme_*, so consumers can finish the
// migration on the native names without the compat shim ever becoming the
// richer surface — the same rule internal/collector/gpu.go follows for DCGM.
package smartctlcompat

import (
	"regexp"
	"strconv"
	"strings"
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
	// Transport is "sata" or "nvme" and decides which families this device
	// contributes to.
	Transport string
	// Temperature is degrees celsius, nil when the device did not report one.
	Temperature *float64
	// PowerOnHours / PowerCycles are nil when unreported.
	PowerOnHours *uint64
	PowerCycles  *uint64
	// Healthy mirrors SMART overall-health; nil when undetermined.
	Healthy *bool
	// ErrorLogCount is the SATA summary error-log entry count, nil otherwise.
	ErrorLogCount *uint64
	// Attrs is the full SATA attribute table, empty on NVMe.
	Attrs []Attr
	// Info is IDENTIFY-derived identity and geometry, nil when unavailable.
	Info *Info
	// NVMe is nil on non-NVMe devices.
	NVMe *NVMe
}

// Attr is one SATA SMART attribute with every field smartctl reports.
type Attr struct {
	ID                        uint8
	Name                      string
	Flags                     uint16
	Raw                       uint64
	Current, Worst, Threshold uint8
	// HasThreshold is false when the thresholds page could not be read; the
	// "thresh" series is then skipped rather than reported as 0, which would
	// read as "any value fails".
	HasThreshold bool
}

// Info is the IDENTIFY-derived identity and geometry.
type Info struct {
	Model, Serial, Firmware             string
	LogicalBlockSize, PhysicalBlockSize uint64
	CapacityBlocks, CapacityBytes       uint64
	// RotationRate is RPM; 0 means non-rotating or unreported.
	RotationRate uint64
	// InterfaceSpeedCurrent / Max are bits per second, 0 = unknown.
	InterfaceSpeedCurrent, InterfaceSpeedMax float64
	// NVMeCapacityBytes is the controller's TNVMCAP, 0 when unreported.
	NVMeCapacityBytes uint64
}

// NVMe carries the NVMe health-log fields the compat surface exposes.
type NVMe struct {
	PercentageUsed, AvailableSpare, SpareThreshold float64
	MediaErrors, CriticalWarning                   float64
	BytesRead, BytesWritten                        float64
	NumErrLogEntries                               float64
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

// attrFlagBits maps ATA SMART attribute flag-word bits to the single-letter
// and long spellings smartctl renders them with. Index is the bit position,
// so the short form is always six characters with '-' for a clear bit —
// "PO--CK" for a pre-fail, online, event-counted, auto-keep attribute.
var attrFlagBits = [6]struct {
	ch   byte
	name string
}{
	{'P', "prefailure"},
	{'O', "updated_online"},
	{'S', "performance"},
	{'R', "error_rate"},
	{'C', "event_count"},
	{'K', "auto_keep"},
}

// renderFlags produces smartctl's two flag spellings from the raw ATA flag
// word.
func renderFlags(flags uint16) (short, long string) {
	b := make([]byte, len(attrFlagBits))
	names := make([]string, 0, len(attrFlagBits))
	for i, f := range attrFlagBits {
		if flags&(1<<uint(i)) != 0 {
			b[i] = f.ch
			names = append(names, f.name)
			continue
		}
		b[i] = '-'
	}
	return string(b), strings.Join(names, ",")
}

// smartctlInterface / smartctlProtocol render the transport the way smartctl
// names it: a SATA disk reached through the SCSI/ATA translation layer is
// interface "sat" carrying protocol "ATA".
func smartctlInterface(transport string) string {
	if transport == "nvme" {
		return "nvme"
	}
	return "sat"
}

func smartctlProtocol(transport string) string {
	if transport == "nvme" {
		return "NVMe"
	}
	return "ATA"
}

// scalarDef is one smartctl_* family carrying a single value per device with
// only a device label. name and help are byte-for-byte from a live
// smartctl_exporter 0.14.0 scrape. value reports ok=false when this device
// does not contribute to the family — either the hardware never reported the
// field, or the family is not emitted for this transport — and the series is
// skipped entirely rather than zeroed.
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

// infoField lifts an Info accessor, skipping devices without IDENTIFY data and
// zero values (an unreported geometry field, not a real zero).
func infoField(get func(Info) uint64) func(Device) (float64, bool) {
	return func(d Device) (float64, bool) {
		if d.Info == nil {
			return 0, false
		}
		v := get(*d.Info)
		if v == 0 {
			return 0, false
		}
		return float64(v), true
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
	{"smartctl_device_power_cycle_count", "Device power cycle count", prometheus.CounterValue, func(d Device) (float64, bool) {
		if d.PowerCycles == nil {
			return 0, false
		}
		return float64(*d.PowerCycles), true
	}},
	{"smartctl_device_smart_status", "General smart status", prometheus.GaugeValue, func(d Device) (float64, bool) {
		if d.Healthy == nil {
			return 0, false
		}
		if *d.Healthy {
			return 1, true
		}
		return 0, true
	}},
	{"smartctl_device_capacity_blocks", "Device capacity in blocks", prometheus.GaugeValue,
		infoField(func(i Info) uint64 { return i.CapacityBlocks })},
	{"smartctl_device_capacity_bytes", "Device capacity in bytes", prometheus.GaugeValue,
		infoField(func(i Info) uint64 { return i.CapacityBytes })},
	// SATA-only families below: smartctl_exporter emits rotation rate only for
	// ATA devices, so an NVMe drive contributes nothing here even though its
	// "non-rotating" state is knowable.
	{"smartctl_device_rotation_rate", "Device rotation rate", prometheus.GaugeValue,
		infoField(func(i Info) uint64 { return i.RotationRate })},
	{"smartctl_device_nvme_capacity_bytes", "NVMe device total capacity bytes", prometheus.GaugeValue,
		infoField(func(i Info) uint64 { return i.NVMeCapacityBytes })},
	// smartctl_device_error_log_count is NOT here: it carries an
	// error_log_type label, so collectErrorLogCount owns it. Declaring it in
	// both places would register two descriptors under one name with
	// different label sets, which the registry rejects outright.
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
	{"smartctl_device_num_err_log_entries", "Contains the number of Error Information log entries over the life of the controller", prometheus.CounterValue,
		nvmeField(func(n NVMe) float64 { return n.NumErrLogEntries })},
	// HELP is empty in the live scrape for both byte counters — reproduced
	// as-is rather than improved, because the exposition is the contract.
	{"smartctl_device_bytes_read", "", prometheus.CounterValue,
		nvmeField(func(n NVMe) float64 { return n.BytesRead })},
	{"smartctl_device_bytes_written", "", prometheus.CounterValue,
		nvmeField(func(n NVMe) float64 { return n.BytesWritten })},
}

// errorLogLabelValue is the only error-log kind nodevitals reads; smartctl
// labels the SATA summary log this way.
const errorLogSummary = "summary"

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
// collector with the same probed devices it turns into native samples, so one
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

	e.collectDeviceCount(ch)
	e.collectInfo(ch)
	e.collectTemperature(ch)
	e.collectBlockSize(ch)
	e.collectInterfaceSpeed(ch)
	e.collectErrorLogCount(ch)
	e.collectAttributes(ch)
}

// smartctl_devices counts what the probe found, the way smartctl_exporter
// counts what it was configured with or discovered.
func (e *Exporter) collectDeviceCount(ch chan<- prometheus.Metric) {
	desc := prometheus.NewDesc("smartctl_devices",
		"Number of devices configured or dynamically discovered", nil, nil)
	ch <- prometheus.MustNewConstMetric(desc, prometheus.GaugeValue, float64(len(e.devices)))
}

// smartctl_device is an info metric: the value is a constant 1 and the identity
// lives in the labels. Only the labels nodevitals can actually answer are
// carried — model_family, ata_version, sata_version, form_factor and the
// scsi_* set come from smartctl's drivedb and JSON identity pages, and empty
// labels are dropped at ingestion anyway, so omitting them yields the same
// stored series while claiming nothing untrue.
func (e *Exporter) collectInfo(ch chan<- prometheus.Metric) {
	desc := prometheus.NewDesc("smartctl_device", "Device info",
		[]string{"device", "model_name", "serial_number", "firmware_version", "interface", "protocol"}, nil)
	for _, d := range e.devices {
		if d.Info == nil {
			continue
		}
		ch <- prometheus.MustNewConstMetric(desc, prometheus.GaugeValue, 1,
			smartctlName(d.Name), d.Info.Model, d.Info.Serial, d.Info.Firmware,
			smartctlInterface(d.Transport), smartctlProtocol(d.Transport))
	}
}

// temperature_type distinguishes current from the min/max lifetime values
// smartctl also reports; nodevitals only reads the current one.
func (e *Exporter) collectTemperature(ch chan<- prometheus.Metric) {
	desc := prometheus.NewDesc("smartctl_device_temperature", "Device temperature celsius",
		[]string{"device", "temperature_type"}, nil)
	for _, d := range e.devices {
		if d.Temperature == nil {
			continue
		}
		ch <- prometheus.MustNewConstMetric(desc, prometheus.GaugeValue, *d.Temperature,
			smartctlName(d.Name), "current")
	}
}

// block_size carries both the logical and the physical sector size. NVMe
// reports no physical size and the live scrape shows 0 there, so the zero is
// reproduced rather than skipped — unlike a missing geometry field, this zero
// IS what smartctl_exporter emits.
func (e *Exporter) collectBlockSize(ch chan<- prometheus.Metric) {
	desc := prometheus.NewDesc("smartctl_device_block_size", "Device block size",
		[]string{"device", "blocks_type"}, nil)
	for _, d := range e.devices {
		if d.Info == nil || d.Info.LogicalBlockSize == 0 {
			continue
		}
		name := smartctlName(d.Name)
		ch <- prometheus.MustNewConstMetric(desc, prometheus.GaugeValue,
			float64(d.Info.LogicalBlockSize), name, "logical")
		ch <- prometheus.MustNewConstMetric(desc, prometheus.GaugeValue,
			float64(d.Info.PhysicalBlockSize), name, "physical")
	}
}

func (e *Exporter) collectInterfaceSpeed(ch chan<- prometheus.Metric) {
	desc := prometheus.NewDesc("smartctl_device_interface_speed",
		"Device interface speed, bits per second", []string{"device", "speed_type"}, nil)
	for _, d := range e.devices {
		if d.Info == nil {
			continue
		}
		name := smartctlName(d.Name)
		for _, s := range []struct {
			kind string
			v    float64
		}{{"current", d.Info.InterfaceSpeedCurrent}, {"max", d.Info.InterfaceSpeedMax}} {
			if s.v == 0 {
				continue
			}
			ch <- prometheus.MustNewConstMetric(desc, prometheus.GaugeValue, s.v, name, s.kind)
		}
	}
}

func (e *Exporter) collectErrorLogCount(ch chan<- prometheus.Metric) {
	desc := prometheus.NewDesc("smartctl_device_error_log_count", "Device SMART error log count",
		[]string{"device", "error_log_type"}, nil)
	for _, d := range e.devices {
		if d.ErrorLogCount == nil {
			continue
		}
		ch <- prometheus.MustNewConstMetric(desc, prometheus.GaugeValue, float64(*d.ErrorLogCount),
			smartctlName(d.Name), errorLogSummary)
	}
}

// collectAttributes reproduces smartctl's four views of every SATA attribute:
// the vendor raw value plus the normalised current/worst/threshold triple.
func (e *Exporter) collectAttributes(ch chan<- prometheus.Metric) {
	desc := prometheus.NewDesc("smartctl_device_attribute", "Device attributes",
		[]string{"device", "attribute_id", "attribute_name", "attribute_value_type",
			"attribute_flags_long", "attribute_flags_short"}, nil)
	for _, d := range e.devices {
		name := smartctlName(d.Name)
		for _, a := range d.Attrs {
			short, long := renderFlags(a.Flags)
			id := strconv.Itoa(int(a.ID))
			emit := func(valueType string, v float64) {
				ch <- prometheus.MustNewConstMetric(desc, prometheus.GaugeValue, v,
					name, id, a.Name, valueType, long, short)
			}
			emit("raw", float64(a.Raw))
			emit("value", float64(a.Current))
			emit("worst", float64(a.Worst))
			if a.HasThreshold {
				emit("thresh", float64(a.Threshold))
			}
		}
	}
}
