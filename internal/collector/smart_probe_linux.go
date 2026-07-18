//go:build linux

package collector

import (
	"context"
	"path/filepath"

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
	d = smartDevice{Name: name, Temperature: &temp, PowerOnHours: &poh}

	switch typed := dev.(type) {
	case *smart.SataDevice:
		page, err := typed.ReadSMARTData()
		if err != nil {
			return smartDevice{}, false
		}
		d.Transport = "sata"
		d.ATAAttrs = ataAttrs(page)
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
		}
	default:
		return smartDevice{}, false // SCSI or unrecognized transport — out of scope
	}

	return d, true
}

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

// uint128ToFloat64 widens an anatol Uint128 (low, high uint64 pair) into the
// float64 nvmeHealth.MediaErrors expects. Val[1] (the high word) is non-zero
// only for lifetime error counts beyond 2^64 — never observed in practice,
// but folded in rather than silently dropped.
func uint128ToFloat64(v smart.Uint128) float64 {
	const two64 = 18446744073709551616.0 // 2^64
	return float64(v.Val[0]) + float64(v.Val[1])*two64
}
