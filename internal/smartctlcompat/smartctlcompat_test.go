package smartctlcompat

import (
	"strings"
	"testing"

	"github.com/prometheus/client_golang/prometheus/testutil"
)

func f64(v float64) *float64 { return &v }
func u64(v uint64) *uint64   { return &v }
func b(v bool) *bool         { return &v }

// prefailOnlineEventKeep is the ATA flag word a live scrape renders as
// "PO--CK" / "prefailure,updated_online,event_count,auto_keep": bits 0, 1, 4
// and 5 set.
const prefailOnlineEventKeep = 1<<0 | 1<<1 | 1<<4 | 1<<5

// fixture mirrors a live smartctl_exporter 0.14.0 scrape on node e21 — a SATA
// disk that has actually accumulated 40 reallocated sectors and 9 error-log
// entries, plus an NVMe drive. Every value was read off that scrape, so the
// golden below IS the compatibility contract: names, HELP, types, units,
// labels, and which transport contributes to which family.
var fixture = []Device{
	{
		Name:      "sdb",
		Transport: "sata",
		// 50617h * 3600 == 1.822212e+08 s, proving the hours→seconds conversion.
		Temperature:   f64(37),
		PowerOnHours:  u64(50617),
		PowerCycles:   u64(100),
		Healthy:       b(true),
		ErrorLogCount: u64(9),
		Attrs: []Attr{{
			ID: 5, Name: "Reallocated_Sector_Ct", Flags: prefailOnlineEventKeep,
			Raw: 40, Current: 100, Worst: 100, Threshold: 1, HasThreshold: true,
		}},
		Info: &Info{
			Model: "TOSHIBA MG07ACA1", Serial: "80H0A1LTFVGG", Firmware: "0104",
			LogicalBlockSize: 512, PhysicalBlockSize: 4096,
			CapacityBlocks: 27344764928, CapacityBytes: 14000519643136,
			RotationRate:          7200,
			InterfaceSpeedCurrent: 6e9, InterfaceSpeedMax: 6e9,
		},
	},
	{
		// Namespace name in, controller name out: live smartctl labels this
		// device="nvme0".
		Name:         "nvme0n1",
		Transport:    "nvme",
		Temperature:  f64(47),
		PowerOnHours: u64(24871),
		PowerCycles:  u64(643),
		Healthy:      b(true),
		NVMe: &NVMe{
			PercentageUsed: 7, AvailableSpare: 100, SpareThreshold: 5,
			MediaErrors: 0, CriticalWarning: 0,
			BytesRead: 1.781710592e+13, BytesWritten: 8.82367616e+13,
			NumErrLogEntries: 41345,
		},
		Info: &Info{
			Model: "CT2000P2SSD8", Serial: "2037E4AF6FED", Firmware: "P2CR031",
			// NVMe reports no physical sector size and the live scrape shows 0.
			LogicalBlockSize: 512, PhysicalBlockSize: 0,
			CapacityBlocks: 3907029168, CapacityBytes: 2000398934016,
			NVMeCapacityBytes: 2000398934016,
		},
	},
}

// golden is the full expected exposition for fixture — all 22 families. It
// differs from the live smartctl_exporter scrape only in the ways the package
// comment documents: the three process-metadata families are absent, and the
// info metric carries just the labels nodevitals can answer.
const golden = `# HELP smartctl_device Device info
# TYPE smartctl_device gauge
smartctl_device{device="nvme0",firmware_version="P2CR031",interface="nvme",model_name="CT2000P2SSD8",protocol="NVMe",serial_number="2037E4AF6FED"} 1
smartctl_device{device="sdb",firmware_version="0104",interface="sat",model_name="TOSHIBA MG07ACA1",protocol="ATA",serial_number="80H0A1LTFVGG"} 1
# HELP smartctl_device_attribute Device attributes
# TYPE smartctl_device_attribute gauge
smartctl_device_attribute{attribute_flags_long="prefailure,updated_online,event_count,auto_keep",attribute_flags_short="PO--CK",attribute_id="5",attribute_name="Reallocated_Sector_Ct",attribute_value_type="raw",device="sdb"} 40
smartctl_device_attribute{attribute_flags_long="prefailure,updated_online,event_count,auto_keep",attribute_flags_short="PO--CK",attribute_id="5",attribute_name="Reallocated_Sector_Ct",attribute_value_type="thresh",device="sdb"} 1
smartctl_device_attribute{attribute_flags_long="prefailure,updated_online,event_count,auto_keep",attribute_flags_short="PO--CK",attribute_id="5",attribute_name="Reallocated_Sector_Ct",attribute_value_type="value",device="sdb"} 100
smartctl_device_attribute{attribute_flags_long="prefailure,updated_online,event_count,auto_keep",attribute_flags_short="PO--CK",attribute_id="5",attribute_name="Reallocated_Sector_Ct",attribute_value_type="worst",device="sdb"} 100
# HELP smartctl_device_available_spare Normalized percentage (0 to 100%) of the remaining spare capacity available
# TYPE smartctl_device_available_spare counter
smartctl_device_available_spare{device="nvme0"} 100
# HELP smartctl_device_available_spare_threshold When the Available Spare falls below the threshold indicated in this field, an asynchronous event completion may occur. The value is indicated as a normalized percentage (0 to 100%)
# TYPE smartctl_device_available_spare_threshold counter
smartctl_device_available_spare_threshold{device="nvme0"} 5
# HELP smartctl_device_block_size Device block size
# TYPE smartctl_device_block_size gauge
smartctl_device_block_size{blocks_type="logical",device="nvme0"} 512
smartctl_device_block_size{blocks_type="logical",device="sdb"} 512
smartctl_device_block_size{blocks_type="physical",device="nvme0"} 0
smartctl_device_block_size{blocks_type="physical",device="sdb"} 4096
# HELP smartctl_device_bytes_read
# TYPE smartctl_device_bytes_read counter
smartctl_device_bytes_read{device="nvme0"} 1.781710592e+13
# HELP smartctl_device_bytes_written
# TYPE smartctl_device_bytes_written counter
smartctl_device_bytes_written{device="nvme0"} 8.82367616e+13
# HELP smartctl_device_capacity_blocks Device capacity in blocks
# TYPE smartctl_device_capacity_blocks gauge
smartctl_device_capacity_blocks{device="nvme0"} 3.907029168e+09
smartctl_device_capacity_blocks{device="sdb"} 2.7344764928e+10
# HELP smartctl_device_capacity_bytes Device capacity in bytes
# TYPE smartctl_device_capacity_bytes gauge
smartctl_device_capacity_bytes{device="nvme0"} 2.000398934016e+12
smartctl_device_capacity_bytes{device="sdb"} 1.4000519643136e+13
# HELP smartctl_device_critical_warning This field indicates critical warnings for the state of the controller
# TYPE smartctl_device_critical_warning counter
smartctl_device_critical_warning{device="nvme0"} 0
# HELP smartctl_device_error_log_count Device SMART error log count
# TYPE smartctl_device_error_log_count gauge
smartctl_device_error_log_count{device="sdb",error_log_type="summary"} 9
# HELP smartctl_device_interface_speed Device interface speed, bits per second
# TYPE smartctl_device_interface_speed gauge
smartctl_device_interface_speed{device="sdb",speed_type="current"} 6e+09
smartctl_device_interface_speed{device="sdb",speed_type="max"} 6e+09
# HELP smartctl_device_media_errors Contains the number of occurrences where the controller detected an unrecovered data integrity error. Errors such as uncorrectable ECC, CRC checksum failure, or LBA tag mismatch are included in this field
# TYPE smartctl_device_media_errors counter
smartctl_device_media_errors{device="nvme0"} 0
# HELP smartctl_device_num_err_log_entries Contains the number of Error Information log entries over the life of the controller
# TYPE smartctl_device_num_err_log_entries counter
smartctl_device_num_err_log_entries{device="nvme0"} 41345
# HELP smartctl_device_nvme_capacity_bytes NVMe device total capacity bytes
# TYPE smartctl_device_nvme_capacity_bytes gauge
smartctl_device_nvme_capacity_bytes{device="nvme0"} 2.000398934016e+12
# HELP smartctl_device_percentage_used Device write percentage used
# TYPE smartctl_device_percentage_used counter
smartctl_device_percentage_used{device="nvme0"} 7
# HELP smartctl_device_power_cycle_count Device power cycle count
# TYPE smartctl_device_power_cycle_count counter
smartctl_device_power_cycle_count{device="nvme0"} 643
smartctl_device_power_cycle_count{device="sdb"} 100
# HELP smartctl_device_power_on_seconds Device power on seconds
# TYPE smartctl_device_power_on_seconds counter
smartctl_device_power_on_seconds{device="nvme0"} 8.95356e+07
smartctl_device_power_on_seconds{device="sdb"} 1.822212e+08
# HELP smartctl_device_rotation_rate Device rotation rate
# TYPE smartctl_device_rotation_rate gauge
smartctl_device_rotation_rate{device="sdb"} 7200
# HELP smartctl_device_smart_status General smart status
# TYPE smartctl_device_smart_status gauge
smartctl_device_smart_status{device="nvme0"} 1
smartctl_device_smart_status{device="sdb"} 1
# HELP smartctl_device_temperature Device temperature celsius
# TYPE smartctl_device_temperature gauge
smartctl_device_temperature{device="nvme0",temperature_type="current"} 47
smartctl_device_temperature{device="sdb",temperature_type="current"} 37
# HELP smartctl_devices Number of devices configured or dynamically discovered
# TYPE smartctl_devices gauge
smartctl_devices 2
`

func TestCollect_MatchesLiveSmartctlExporter(t *testing.T) {
	e := New()
	e.Update(fixture)

	if err := testutil.CollectAndCompare(e, strings.NewReader(golden)); err != nil {
		t.Errorf("exposition drifted from the smartctl_exporter contract:\n%v", err)
	}
}

// An empty snapshot must serve only the device count (at zero), the way
// smartctl_exporter on a node with no supported device does — not a set of
// zero-valued health series that would read as "healthy disk" on a dashboard.
func TestCollect_EmptySnapshotServesOnlyDeviceCount(t *testing.T) {
	e := New()

	if got := testutil.CollectAndCount(e); got != 1 {
		t.Errorf("empty snapshot served %d series, want 1 (smartctl_devices)", got)
	}
	expected := `# HELP smartctl_devices Number of devices configured or dynamically discovered
# TYPE smartctl_devices gauge
smartctl_devices 0
`
	if err := testutil.CollectAndCompare(e, strings.NewReader(expected), "smartctl_devices"); err != nil {
		t.Error(err)
	}
}

// A SATA device carries no NVMe health log, so the NVMe-only families must be
// absent for it rather than present at zero — a zeroed available_spare would
// look like a drive with no spare capacity left. The reverse holds for the
// SATA-only families on an NVMe drive: smartctl_exporter emits neither, so
// neither may appear here even though the data would be knowable.
func TestCollect_TransportScopedFamilies(t *testing.T) {
	nvmeOnly := []string{
		"smartctl_device_percentage_used",
		"smartctl_device_available_spare",
		"smartctl_device_available_spare_threshold",
		"smartctl_device_media_errors",
		"smartctl_device_critical_warning",
		"smartctl_device_num_err_log_entries",
		"smartctl_device_bytes_read",
		"smartctl_device_bytes_written",
		"smartctl_device_nvme_capacity_bytes",
	}
	sataOnly := []string{
		"smartctl_device_rotation_rate",
		"smartctl_device_interface_speed",
		"smartctl_device_error_log_count",
	}

	sata := New()
	sata.Update([]Device{{Name: "sda", Transport: "sata", Temperature: f64(42),
		Info: &Info{LogicalBlockSize: 512, RotationRate: 7200, InterfaceSpeedCurrent: 6e9}}})
	for _, family := range nvmeOnly {
		if got := testutil.CollectAndCount(sata, family); got != 0 {
			t.Errorf("%s served %d series for a SATA device, want 0", family, got)
		}
	}

	nvme := New()
	nvme.Update([]Device{{Name: "nvme0n1", Transport: "nvme", Temperature: f64(47),
		NVMe: &NVMe{AvailableSpare: 100}, Info: &Info{LogicalBlockSize: 512}}})
	for _, family := range sataOnly {
		if got := testutil.CollectAndCount(nvme, family); got != 0 {
			t.Errorf("%s served %d series for an NVMe device, want 0", family, got)
		}
	}
}

// A device that reported no temperature, power-on hours or identity must skip
// those series entirely; nil is missing data, not zero.
func TestCollect_MissingFieldsAreSkippedNotZeroed(t *testing.T) {
	e := New()
	e.Update([]Device{{Name: "sdc", Transport: "sata"}})

	for _, family := range []string{
		"smartctl_device_temperature",
		"smartctl_device_power_on_seconds",
		"smartctl_device_power_cycle_count",
		"smartctl_device_smart_status",
		"smartctl_device",
		"smartctl_device_capacity_bytes",
		"smartctl_device_block_size",
	} {
		if got := testutil.CollectAndCount(e, family); got != 0 {
			t.Errorf("%s served %d series with no source data, want 0", family, got)
		}
	}
}

// An attribute whose thresholds page could not be read still yields raw, value
// and worst — but no "thresh" series, since a fabricated 0 threshold reads as
// "this attribute can never fail".
func TestCollect_AttributeWithoutThresholdOmitsThreshSeries(t *testing.T) {
	e := New()
	e.Update([]Device{{
		Name: "sdd", Transport: "sata",
		Attrs: []Attr{{ID: 5, Name: "Reallocated_Sector_Ct", Raw: 1, Current: 99, Worst: 98}},
	}})

	if got := testutil.CollectAndCount(e, "smartctl_device_attribute"); got != 3 {
		t.Errorf("attribute served %d series without thresholds, want 3 (raw/value/worst)", got)
	}
}

// A failing drive must report smart_status 0, not the absence of the series —
// an alert rule on smartctl_device_smart_status == 0 has to fire.
func TestCollect_FailedHealthReportsZero(t *testing.T) {
	e := New()
	e.Update([]Device{{Name: "sde", Transport: "sata", Healthy: b(false)}})

	expected := `# HELP smartctl_device_smart_status General smart status
# TYPE smartctl_device_smart_status gauge
smartctl_device_smart_status{device="sde"} 0
`
	if err := testutil.CollectAndCompare(e, strings.NewReader(expected),
		"smartctl_device_smart_status"); err != nil {
		t.Error(err)
	}
}

func TestRenderFlags(t *testing.T) {
	// Every case is a flag word observed on a live disk, with the exact
	// spellings smartctl_exporter rendered for it.
	tests := []struct {
		flags     uint16
		wantShort string
		wantLong  string
	}{
		{1<<0 | 1<<1 | 1<<4 | 1<<5, "PO--CK", "prefailure,updated_online,event_count,auto_keep"},
		{1<<1 | 1<<4 | 1<<5, "-O--CK", "updated_online,event_count,auto_keep"},
		{1<<1 | 1<<5, "-O---K", "updated_online,auto_keep"},
		{1 << 3, "---R--", "error_rate"},
		{1<<4 | 1<<5, "----CK", "event_count,auto_keep"},
		{1 << 4, "----C-", "event_count"},
		{1 << 1, "-O----", "updated_online"},
		{0, "------", ""},
	}
	for _, tt := range tests {
		short, long := renderFlags(tt.flags)
		if short != tt.wantShort || long != tt.wantLong {
			t.Errorf("renderFlags(%#b) = (%q, %q), want (%q, %q)",
				tt.flags, short, long, tt.wantShort, tt.wantLong)
		}
	}
}

func TestSmartctlName(t *testing.T) {
	tests := []struct {
		in, want string
	}{
		{"nvme0n1", "nvme0"},   // namespace → controller, as smartctl labels it
		{"nvme10n1", "nvme10"}, // multi-digit controller index
		{"nvme0n2", "nvme0"},   // second namespace on the same controller
		{"sda", "sda"},         // SATA passes through
		{"sdaa", "sdaa"},
		{"nvme0", "nvme0"}, // already controller-form, unchanged
	}
	for _, tt := range tests {
		if got := smartctlName(tt.in); got != tt.want {
			t.Errorf("smartctlName(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

func TestSmartctlInterfaceAndProtocol(t *testing.T) {
	// smartctl reaches a SATA disk through the SCSI/ATA translation layer, so
	// it names the interface "sat" while the protocol stays "ATA".
	if got := smartctlInterface("sata"); got != "sat" {
		t.Errorf("interface(sata) = %q, want sat", got)
	}
	if got := smartctlProtocol("sata"); got != "ATA" {
		t.Errorf("protocol(sata) = %q, want ATA", got)
	}
	if got := smartctlInterface("nvme"); got != "nvme" {
		t.Errorf("interface(nvme) = %q, want nvme", got)
	}
	if got := smartctlProtocol("nvme"); got != "NVMe" {
		t.Errorf("protocol(nvme) = %q, want NVMe", got)
	}
}
