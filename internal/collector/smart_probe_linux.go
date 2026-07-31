//go:build linux

package collector

import (
	"context"
	"path/filepath"
	"slices"
	"strings"

	smart "github.com/anatol/smart.go"
)

// NewDevProbe returns a smartProbe that discovers whole block devices under
// devRoot (e.g. "/dev") and reads their SMART/NVMe health via
// github.com/anatol/smart.go. A device that fails to open or read is
// skipped — not an error — so an insufficient-privilege device or a USB
// bridge that doesn't support passthrough can't fail the whole probe
// (mirrors Registry.CollectAll's per-collector skip idiom). The probe itself
// only errors on a devRoot glob failure.
func NewDevProbe(devRoot string) smartProbe {
	return func(ctx context.Context) ([]smartDevice, error) {
		paths, err := filepath.Glob(filepath.Join(devRoot, "*"))
		if err != nil {
			return nil, err
		}

		var out []smartDevice
		for _, path := range paths {
			name := filepath.Base(path)
			if !isWholeDevice(name) {
				continue
			}
			if d, ok := probeDevice(name, path); ok {
				out = append(out, d)
			}
		}
		return out, nil
	}
}

// probeDevice opens and reads one device's SMART state. ok is false if the
// device could not be opened, read, or is a transport smartDevice doesn't
// model (anything but SATA/NVMe) — the caller skips it.
//
// Identity and error-log reads are best-effort: a device that answers the
// health data but refuses IDENTIFY still yields a usable smartDevice with Info
// nil. Losing temperature and wear because a bridge won't pass IDENTIFY
// through would be a worse outcome than a missing model name.
func probeDevice(name, path string) (d smartDevice, ok bool) {
	dev, err := smart.Open(path)
	if err != nil {
		return smartDevice{}, false
	}
	defer dev.Close()

	ga, err := dev.ReadGenericAttributes()
	if err != nil {
		return smartDevice{}, false
	}
	temp := float64(ga.Temperature)
	poh := ga.PowerOnHours
	cycles := ga.PowerCycles
	d = smartDevice{Name: name, Temperature: &temp, PowerOnHours: &poh, PowerCycles: &cycles}

	switch typed := dev.(type) {
	case *smart.SataDevice:
		page, err := typed.ReadSMARTData()
		if err != nil {
			return smartDevice{}, false
		}
		d.Transport = "sata"
		d.ATAAttrs = ataAttrs(page)
		d.Attrs = ataFullAttrs(typed, page)
		d.Healthy = ataHealthy(d.Attrs)
		d.Info = ataInfo(typed)
		if summary, err := typed.ReadSMARTErrorLogSummary(); err == nil && summary != nil {
			count := uint64(summary.ErrorCount)
			d.ErrorLogCount = &count
		}
	case *smart.NVMeDevice:
		log, err := typed.ReadSMART()
		if err != nil {
			return smartDevice{}, false
		}
		d.Transport = "nvme"
		d.NVMe = &nvmeHealth{
			PercentageUsed:  float64(log.PercentUsed),
			AvailableSpare:  float64(log.AvailSpare),
			SpareThreshold:  float64(log.SpareThresh),
			MediaErrors:     uint128ToFloat64(log.MediaErrors),
			CriticalWarning: float64(log.CritWarning),
			// A data unit is 1000 blocks of 512 bytes per the NVMe spec, which
			// is the same conversion smartctl applies to these counters.
			BytesRead:        uint128ToFloat64(log.DataUnitsRead) * nvmeDataUnitBytes,
			BytesWritten:     uint128ToFloat64(log.DataUnitsWritten) * nvmeDataUnitBytes,
			NumErrLogEntries: uint128ToFloat64(log.NumErrLogEntries),
		}
		// Any critical-warning bit set means the controller is reporting a
		// failure condition, which is what SMART overall-health means here.
		healthy := log.CritWarning == 0
		d.Healthy = &healthy
		d.Info = nvmeInfo(typed)
	default:
		return smartDevice{}, false // SCSI or unrecognized transport — out of scope
	}

	return d, true
}

// nvmeDataUnitBytes is the NVMe health log's unit: 1000 blocks of 512 bytes.
const nvmeDataUnitBytes = 1000 * 512

// ataAttrs reads the raw values of the frozen SATA attribute set
// (sataAttrMetrics, smart.go) out of a SMART data page; ids absent from the
// device's attribute table are omitted, matching Collect's nil-means-absent
// handling.
func ataAttrs(page *smart.AtaSmartPage) map[uint8]uint64 {
	m := make(map[uint8]uint64, len(sataAttrMetrics))
	for _, a := range sataAttrMetrics {
		if attr, ok := page.Attrs[a.id]; ok {
			m[a.id] = attr.ValueRaw
		}
	}
	return m
}

// ataFullAttrs projects the device's WHOLE attribute table, thresholds folded
// in where the thresholds page could be read. The native surface still emits
// only the frozen five (ataAttrs above); this fuller table exists so the
// smartctl compat surface can reproduce smartctl_device_attribute, which
// covers every attribute the drive reports.
func ataFullAttrs(dev *smart.SataDevice, page *smart.AtaSmartPage) []smartAttr {
	var thresholds map[uint8]uint8
	if tp, err := dev.ReadSMARTThresholds(); err == nil && tp != nil {
		thresholds = tp.Thresholds
	}

	out := make([]smartAttr, 0, len(page.Attrs))
	for id, a := range page.Attrs {
		sa := smartAttr{
			ID:      id,
			Name:    a.Name,
			Flags:   a.Flags,
			Raw:     a.ValueRaw,
			Current: a.Current,
			Worst:   a.Worst,
		}
		if t, ok := thresholds[id]; ok {
			sa.Threshold = t
			sa.HasThreshold = true
		}
		out = append(out, sa)
	}
	// Map iteration is nondeterministic; sort so exposition order is stable.
	slices.SortFunc(out, func(a, b smartAttr) int { return int(a.ID) - int(b.ID) })
	return out
}

// ataHealthy derives SMART overall-health the way the ATA spec defines it: the
// drive is failing when a PRE-FAILURE attribute's normalised value has fallen
// to or below its threshold. Old-age attributes crossing their threshold mean
// wear, not impending failure, and are excluded. Returns nil when no
// pre-failure attribute carried a threshold — "no data" must not read as
// "passed".
func ataHealthy(attrs []smartAttr) *bool {
	const prefailureBit = 1 << 0
	saw := false
	for _, a := range attrs {
		if !a.HasThreshold || a.Flags&prefailureBit == 0 {
			continue
		}
		// Threshold 0 means "no failure threshold defined" per the spec, so
		// such an attribute can neither pass nor fail the check.
		if a.Threshold == 0 {
			continue
		}
		saw = true
		if a.Current <= a.Threshold {
			failed := false
			return &failed
		}
	}
	if !saw {
		return nil
	}
	passed := true
	return &passed
}

// ataInfo reads identity and geometry out of the IDENTIFY DEVICE response.
// Returns nil when IDENTIFY fails — the caller keeps the health readings.
func ataInfo(dev *smart.SataDevice) *smartInfo {
	id, err := dev.Identify()
	if err != nil || id == nil {
		return nil
	}

	logical, physical := ataBlockSizes(id)
	blocks := id.CapacityLba48
	if blocks == 0 {
		blocks = uint64(id.CapacityLba28)
	}
	current, max := ataInterfaceSpeed(id)

	return &smartInfo{
		Model:                 ataString(id.ModelNumberRaw[:]),
		Serial:                ataString(id.SerialNumberRaw[:]),
		Firmware:              ataString(id.FirmwareRevisionRaw[:]),
		LogicalBlockSize:      logical,
		PhysicalBlockSize:     physical,
		CapacityBlocks:        blocks,
		CapacityBytes:         blocks * logical,
		RotationRate:          ataRotationRate(id.RotationRate),
		InterfaceSpeedCurrent: current,
		InterfaceSpeedMax:     max,
	}
}

// ataRotationRate normalises IDENTIFY word 217: 1 means non-rotating (SSD) and
// 0 means unreported. Both map to 0 here so the consumer omits the series
// rather than claiming a 1 RPM disk.
func ataRotationRate(raw uint16) uint64 {
	if raw <= 1 {
		return 0
	}
	return uint64(raw)
}

// ataBlockSizes decodes IDENTIFY word 106. Bit 14 set with bit 15 clear marks
// the word valid; bit 12 says the logical sector is larger than 256 words
// (in which case words 117-118 hold its size), and bits 3:0 are log2 of the
// number of logical sectors per physical sector.
func ataBlockSizes(id *smart.AtaIdentifyDevice) (logical, physical uint64) {
	logical = 512
	w := id.LogicalPerPhisicalSectors
	if w&(1<<14) == 0 || w&(1<<15) != 0 {
		return logical, logical
	}
	if w&(1<<12) != 0 {
		if words := uint64(id.LogicalSectorSize[0]) | uint64(id.LogicalSectorSize[1])<<16; words > 0 {
			logical = words * 2 // the field counts 16-bit words
		}
	}
	return logical, logical << (w & 0xf)
}

// sataLinkSpeeds maps the SATA signal-speed generation code to bits per
// second, matching what smartctl reports as units_per_second * bits_per_unit.
var sataLinkSpeeds = [...]float64{0, 1.5e9, 3e9, 6e9}

// ataInterfaceSpeed decodes the negotiated speed from IDENTIFY word 77 (bits
// 3:1) and the highest supported speed from word 76's capability bits.
func ataInterfaceSpeed(id *smart.AtaIdentifyDevice) (current, max float64) {
	if gen := int((id.SATACapAddl >> 1) & 0x7); gen < len(sataLinkSpeeds) {
		current = sataLinkSpeeds[gen]
	}
	for gen := len(sataLinkSpeeds) - 1; gen >= 1; gen-- {
		if id.SATACap&(1<<uint(gen)) != 0 {
			max = sataLinkSpeeds[gen]
			break
		}
	}
	return current, max
}

// nvmeInfo reads identity and capacity from Identify Controller plus the first
// active namespace, whose LBA format gives the logical block size. Returns nil
// when IDENTIFY fails.
func nvmeInfo(dev *smart.NVMeDevice) *smartInfo {
	ctrl, namespaces, err := dev.Identify()
	if err != nil || ctrl == nil {
		return nil
	}

	info := &smartInfo{
		Model:            strings.TrimSpace(ctrl.ModelNumber()),
		Serial:           strings.TrimSpace(ctrl.SerialNumber()),
		Firmware:         strings.TrimSpace(ctrl.FirmwareRev()),
		LogicalBlockSize: 512,
		// PhysicalBlockSize stays 0: NVMe reports none, and the live
		// smartctl_exporter scrape shows 0 for these devices too.
		NVMeCapacityBytes: ctrl.Tnvmcap.Val[0],
	}
	for _, ns := range namespaces {
		if ns.Nsze == 0 {
			continue
		}
		// Flbas bits 3:0 index the LBA format table; that entry's Ds field is
		// log2 of the block size in bytes.
		if f := int(ns.Flbas & 0xf); ns.Lbaf[f].Ds > 0 {
			info.LogicalBlockSize = 1 << ns.Lbaf[f].Ds
		}
		info.CapacityBlocks = ns.Nsze
		info.CapacityBytes = ns.Nsze * info.LogicalBlockSize
		break
	}
	return info
}

// uint128ToFloat64 widens an anatol Uint128 (low, high uint64 pair) into the
// float64 the health fields expect. Val[1] (the high word) is non-zero only
// for lifetime counts beyond 2^64 — never observed in practice, but folded in
// rather than silently dropped.
func uint128ToFloat64(v smart.Uint128) float64 {
	const two64 = 18446744073709551616.0 // 2^64
	return float64(v.Val[0]) + float64(v.Val[1])*two64
}
