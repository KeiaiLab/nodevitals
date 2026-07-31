package collector

import (
	"bufio"
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/KeiaiLab/nodevitals/internal/model"
)

// pcieAER reads the PCI Express Advanced Error Reporting counters the kernel
// exposes per device under <sysRoot>/bus/pci/devices/<addr>/aer_dev_*.
//
// AER is the bus-level counterpart to EDAC: EDAC counts memory errors the
// controller corrected, AER counts link errors the PCIe fabric corrected or
// failed to. A GPU that starts throwing correctable BadTLP under load, or an
// NVMe behind a marginal riser, shows up here long before it shows up as a
// device fault.
//
// Every discovered device reports its counters even when they are zero. The
// shell collector this replaces emitted a series only when a count was
// non-zero, which makes "no series" ambiguous between *no errors* and *not
// collected* — precisely the distinction an operator needs during an incident.
type pcieAER struct {
	node    string
	sysRoot string
}

// NewPCIeAER reports PCIe AER error counters from
// <sysRoot>/bus/pci/devices/*/aer_dev_{correctable,nonfatal,fatal}.
func NewPCIeAER(node, sysRoot string) Collector {
	return &pcieAER{node: node, sysRoot: sysRoot}
}

func (p *pcieAER) Name() string { return "pcie_aer" }

// aerFiles maps each AER sysfs file to the metric that carries it. The kernel
// splits uncorrectable errors into non-fatal and fatal; both are kept distinct
// here because a fatal error means the link was reset, while a non-fatal one
// means the transaction was dropped but the link survived.
var aerFiles = []struct {
	file   string
	metric string
}{
	{"aer_dev_correctable", "pcie_aer_correctable_total"},
	{"aer_dev_nonfatal", "pcie_aer_nonfatal_total"},
	{"aer_dev_fatal", "pcie_aer_fatal_total"},
}

func (p *pcieAER) Collect(ctx context.Context) ([]model.Sample, error) {
	root := filepath.Join(p.sysRoot, "bus", "pci", "devices")
	entries, err := os.ReadDir(root)
	if err != nil {
		// No PCI subsystem visible (a VM without the sysfs mount, arm64 SoC):
		// not an error worth surfacing every interval, matching how the power
		// collector treats a missing powercap tree.
		return nil, nil
	}

	now := time.Now().UTC()
	var out []model.Sample
	for _, e := range entries {
		addr := e.Name()
		for _, af := range aerFiles {
			counts, ok := readAERFile(filepath.Join(root, addr, af.file))
			if !ok {
				// AER is optional per device — a root port exposes it, a leaf
				// function often does not. Absence is normal, not a failure.
				continue
			}
			for _, c := range counts {
				out = append(out, model.Sample{
					Node:      p.node,
					Tier:      "core",
					Device:    addr,
					Metric:    af.metric,
					Kind:      model.KindCounter,
					Value:     c.value,
					Labels:    map[string]string{"error_type": c.name},
					Timestamp: now,
				})
			}
		}
	}
	return out, nil
}

// aerCount is one "<ErrorName> <count>" line of an AER sysfs file.
type aerCount struct {
	name  string
	value float64
}

// readAERFile parses the kernel's AER counter format: one "Name Count" pair
// per line, e.g. "RxErr 0" / "BadTLP 3". ok is false when the file does not
// exist or cannot be read, which is the common case for devices without AER
// capability.
func readAERFile(path string) ([]aerCount, bool) {
	f, err := os.Open(path)
	if err != nil {
		return nil, false
	}
	defer f.Close()

	var out []aerCount
	s := bufio.NewScanner(f)
	for s.Scan() {
		fields := strings.Fields(s.Text())
		if len(fields) != 2 {
			continue
		}
		v, err := strconv.ParseFloat(fields[1], 64)
		if err != nil {
			continue
		}
		out = append(out, aerCount{name: fields[0], value: v})
	}
	if err := s.Err(); err != nil {
		return nil, false
	}
	return out, true
}
