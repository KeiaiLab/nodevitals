package smartctlcompat

import (
	"strings"
	"testing"

	"github.com/prometheus/client_golang/prometheus/testutil"
)

func f64(v float64) *float64 { return &v }
func u64(v uint64) *uint64   { return &v }

// fixture mirrors a live smartctl_exporter 0.14.0 scrape on node e21: a SATA
// TOSHIBA MG07ACA1 (sdb) that has actually accumulated 40 reallocated sectors,
// and an NVMe namespace. Every value below was read off that scrape, so the
// golden IS the compatibility contract — names, HELP, types, units, labels.
var fixture = []Device{
	{
		Name: "sdb",
		// live: smartctl_device_temperature{device="sdb",...} 37
		Temperature: f64(37),
		// live: smartctl_device_power_on_seconds{device="sdb"} 1.822212e+08
		// 50617h * 3600 == 182221200, proving the hours→seconds conversion.
		PowerOnHours: u64(50617),
		// live: attribute_id="5" raw == 40, 197 and 198 == 0. 187/188 absent —
		// this firmware does not report them, and neither does the surface.
		ATAAttrs: map[uint8]uint64{5: 40, 197: 0, 198: 0},
	},
	{
		// Namespace name in, controller name out: live smartctl labels this
		// device="nvme0".
		Name:         "nvme0n1",
		Temperature:  f64(47),
		PowerOnHours: u64(24871), // 8.95356e+07 s
		NVMe: &NVMe{
			PercentageUsed:  7,
			AvailableSpare:  100,
			SpareThreshold:  5,
			MediaErrors:     0,
			CriticalWarning: 0,
		},
	},
}

// golden is the full expected exposition for fixture. It differs from the live
// smartctl_exporter scrape in exactly the ways documented in the package
// comment — no attribute_flags_* labels, raw-only attribute_value_type, and no
// identity/capacity families — and in no other way.
const golden = `# HELP smartctl_device_attribute Device attributes
# TYPE smartctl_device_attribute gauge
smartctl_device_attribute{attribute_id="197",attribute_name="Current_Pending_Sector",attribute_value_type="raw",device="sdb"} 0
smartctl_device_attribute{attribute_id="198",attribute_name="Offline_Uncorrectable",attribute_value_type="raw",device="sdb"} 0
smartctl_device_attribute{attribute_id="5",attribute_name="Reallocated_Sector_Ct",attribute_value_type="raw",device="sdb"} 40
# HELP smartctl_device_available_spare Normalized percentage (0 to 100%) of the remaining spare capacity available
# TYPE smartctl_device_available_spare counter
smartctl_device_available_spare{device="nvme0"} 100
# HELP smartctl_device_available_spare_threshold When the Available Spare falls below the threshold indicated in this field, an asynchronous event completion may occur. The value is indicated as a normalized percentage (0 to 100%)
# TYPE smartctl_device_available_spare_threshold counter
smartctl_device_available_spare_threshold{device="nvme0"} 5
# HELP smartctl_device_critical_warning This field indicates critical warnings for the state of the controller
# TYPE smartctl_device_critical_warning counter
smartctl_device_critical_warning{device="nvme0"} 0
# HELP smartctl_device_media_errors Contains the number of occurrences where the controller detected an unrecovered data integrity error. Errors such as uncorrectable ECC, CRC checksum failure, or LBA tag mismatch are included in this field
# TYPE smartctl_device_media_errors counter
smartctl_device_media_errors{device="nvme0"} 0
# HELP smartctl_device_percentage_used Device write percentage used
# TYPE smartctl_device_percentage_used counter
smartctl_device_percentage_used{device="nvme0"} 7
# HELP smartctl_device_power_on_seconds Device power on seconds
# TYPE smartctl_device_power_on_seconds counter
smartctl_device_power_on_seconds{device="nvme0"} 8.95356e+07
smartctl_device_power_on_seconds{device="sdb"} 1.822212e+08
# HELP smartctl_device_temperature Device temperature celsius
# TYPE smartctl_device_temperature gauge
smartctl_device_temperature{device="nvme0",temperature_type="current"} 47
smartctl_device_temperature{device="sdb",temperature_type="current"} 37
`

func TestCollect_MatchesLiveSmartctlExporter(t *testing.T) {
	e := New()
	e.Update(fixture)

	if err := testutil.CollectAndCompare(e, strings.NewReader(golden)); err != nil {
		t.Errorf("exposition drifted from the smartctl_exporter contract:\n%v", err)
	}
}

// An empty snapshot must serve nothing at all, the way smartctl_exporter on a
// node with no supported device does — not a set of zero-valued series that
// would read as "healthy disk" on a dashboard.
func TestCollect_EmptySnapshotServesNothing(t *testing.T) {
	e := New()

	if got := testutil.CollectAndCount(e); got != 0 {
		t.Errorf("empty snapshot served %d series, want 0", got)
	}
}

// A SATA device carries no NVMe health log, so the NVMe families must be
// absent for it rather than present at zero — a zeroed available_spare would
// look like a drive with no spare capacity left.
func TestCollect_SATADeviceOmitsNVMeFamilies(t *testing.T) {
	e := New()
	e.Update([]Device{{Name: "sda", Temperature: f64(42)}})

	for _, family := range []string{
		"smartctl_device_percentage_used",
		"smartctl_device_available_spare",
		"smartctl_device_available_spare_threshold",
		"smartctl_device_media_errors",
		"smartctl_device_critical_warning",
	} {
		if got := testutil.CollectAndCount(e, family); got != 0 {
			t.Errorf("%s served %d series for a SATA device, want 0", family, got)
		}
	}
}

// A device that reported no temperature or power-on hours must skip those
// series entirely; nil is missing data, not zero.
func TestCollect_MissingFieldsAreSkippedNotZeroed(t *testing.T) {
	e := New()
	e.Update([]Device{{Name: "sdc"}})

	for _, family := range []string{
		"smartctl_device_temperature",
		"smartctl_device_power_on_seconds",
	} {
		if got := testutil.CollectAndCount(e, family); got != 0 {
			t.Errorf("%s served %d series with no source data, want 0", family, got)
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
