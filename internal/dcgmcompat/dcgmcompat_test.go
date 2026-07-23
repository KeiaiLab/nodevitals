package dcgmcompat

import (
	"strings"
	"testing"

	"github.com/prometheus/client_golang/prometheus/testutil"
)

// fixture mirrors a live RTX 4070 scrape (e104, dcgm-exporter 4.x) so the
// golden below IS the compatibility contract: names, HELP text, types, units.
var fixture = Device{
	Index: 0, UUID: "GPU-02e7ad33", Model: "NVIDIA GeForce RTX 4070", PCIBusID: "00000000:08:00.0",
	UtilGPU: 55, MemCopyUtil: 12, EncUtil: 3, DecUtil: 4,
	FBUsedBytes: 296 * mib, FBFreeBytes: 11719 * mib, FBRsvdBytes: 258 * mib,
	TempC: 48, MemTempC: 0,
	PowerW:            8.61,
	EnergyMilliJoules: 32972735665,
	SMClockMHz:        210, MemClockMHz: 405,
	PcieReplayTotal: 200,
	RemappedCorr:    1, RemappedUnc: 2, RemapFailed: true,
}

// golden is the full expected exposition for fixture. HELP/TYPE lines are
// byte-for-byte from a live dcgm-exporter scrape; FB_* prove the bytes→MiB
// conversion, ENERGY stays in mJ, ROW_REMAP_FAILURE proves bool→1.
const golden = `# HELP DCGM_FI_DEV_SM_CLOCK SM clock frequency (in MHz).
# TYPE DCGM_FI_DEV_SM_CLOCK gauge
DCGM_FI_DEV_SM_CLOCK{DCGM_FI_DRIVER_VERSION="580.159.03",Hostname="e104",UUID="GPU-02e7ad33",device="nvidia0",gpu="0",modelName="NVIDIA GeForce RTX 4070",pci_bus_id="00000000:08:00.0"} 210
# HELP DCGM_FI_DEV_MEM_CLOCK Memory clock frequency (in MHz).
# TYPE DCGM_FI_DEV_MEM_CLOCK gauge
DCGM_FI_DEV_MEM_CLOCK{DCGM_FI_DRIVER_VERSION="580.159.03",Hostname="e104",UUID="GPU-02e7ad33",device="nvidia0",gpu="0",modelName="NVIDIA GeForce RTX 4070",pci_bus_id="00000000:08:00.0"} 405
# HELP DCGM_FI_DEV_MEMORY_TEMP Memory temperature (in C).
# TYPE DCGM_FI_DEV_MEMORY_TEMP gauge
DCGM_FI_DEV_MEMORY_TEMP{DCGM_FI_DRIVER_VERSION="580.159.03",Hostname="e104",UUID="GPU-02e7ad33",device="nvidia0",gpu="0",modelName="NVIDIA GeForce RTX 4070",pci_bus_id="00000000:08:00.0"} 0
# HELP DCGM_FI_DEV_GPU_TEMP GPU temperature (in C).
# TYPE DCGM_FI_DEV_GPU_TEMP gauge
DCGM_FI_DEV_GPU_TEMP{DCGM_FI_DRIVER_VERSION="580.159.03",Hostname="e104",UUID="GPU-02e7ad33",device="nvidia0",gpu="0",modelName="NVIDIA GeForce RTX 4070",pci_bus_id="00000000:08:00.0"} 48
# HELP DCGM_FI_DEV_POWER_USAGE Power draw (in W).
# TYPE DCGM_FI_DEV_POWER_USAGE gauge
DCGM_FI_DEV_POWER_USAGE{DCGM_FI_DRIVER_VERSION="580.159.03",Hostname="e104",UUID="GPU-02e7ad33",device="nvidia0",gpu="0",modelName="NVIDIA GeForce RTX 4070",pci_bus_id="00000000:08:00.0"} 8.61
# HELP DCGM_FI_DEV_TOTAL_ENERGY_CONSUMPTION Total energy consumption since boot (in mJ).
# TYPE DCGM_FI_DEV_TOTAL_ENERGY_CONSUMPTION counter
DCGM_FI_DEV_TOTAL_ENERGY_CONSUMPTION{DCGM_FI_DRIVER_VERSION="580.159.03",Hostname="e104",UUID="GPU-02e7ad33",device="nvidia0",gpu="0",modelName="NVIDIA GeForce RTX 4070",pci_bus_id="00000000:08:00.0"} 32972735665
# HELP DCGM_FI_DEV_PCIE_REPLAY_COUNTER Total number of PCIe retries.
# TYPE DCGM_FI_DEV_PCIE_REPLAY_COUNTER counter
DCGM_FI_DEV_PCIE_REPLAY_COUNTER{DCGM_FI_DRIVER_VERSION="580.159.03",Hostname="e104",UUID="GPU-02e7ad33",device="nvidia0",gpu="0",modelName="NVIDIA GeForce RTX 4070",pci_bus_id="00000000:08:00.0"} 200
# HELP DCGM_FI_DEV_GPU_UTIL GPU utilization (in %).
# TYPE DCGM_FI_DEV_GPU_UTIL gauge
DCGM_FI_DEV_GPU_UTIL{DCGM_FI_DRIVER_VERSION="580.159.03",Hostname="e104",UUID="GPU-02e7ad33",device="nvidia0",gpu="0",modelName="NVIDIA GeForce RTX 4070",pci_bus_id="00000000:08:00.0"} 55
# HELP DCGM_FI_DEV_MEM_COPY_UTIL Memory utilization (in %).
# TYPE DCGM_FI_DEV_MEM_COPY_UTIL gauge
DCGM_FI_DEV_MEM_COPY_UTIL{DCGM_FI_DRIVER_VERSION="580.159.03",Hostname="e104",UUID="GPU-02e7ad33",device="nvidia0",gpu="0",modelName="NVIDIA GeForce RTX 4070",pci_bus_id="00000000:08:00.0"} 12
# HELP DCGM_FI_DEV_ENC_UTIL Encoder utilization (in %).
# TYPE DCGM_FI_DEV_ENC_UTIL gauge
DCGM_FI_DEV_ENC_UTIL{DCGM_FI_DRIVER_VERSION="580.159.03",Hostname="e104",UUID="GPU-02e7ad33",device="nvidia0",gpu="0",modelName="NVIDIA GeForce RTX 4070",pci_bus_id="00000000:08:00.0"} 3
# HELP DCGM_FI_DEV_DEC_UTIL Decoder utilization (in %).
# TYPE DCGM_FI_DEV_DEC_UTIL gauge
DCGM_FI_DEV_DEC_UTIL{DCGM_FI_DRIVER_VERSION="580.159.03",Hostname="e104",UUID="GPU-02e7ad33",device="nvidia0",gpu="0",modelName="NVIDIA GeForce RTX 4070",pci_bus_id="00000000:08:00.0"} 4
# HELP DCGM_FI_DEV_FB_FREE Framebuffer memory free (in MiB).
# TYPE DCGM_FI_DEV_FB_FREE gauge
DCGM_FI_DEV_FB_FREE{DCGM_FI_DRIVER_VERSION="580.159.03",Hostname="e104",UUID="GPU-02e7ad33",device="nvidia0",gpu="0",modelName="NVIDIA GeForce RTX 4070",pci_bus_id="00000000:08:00.0"} 11719
# HELP DCGM_FI_DEV_FB_USED Framebuffer memory used (in MiB).
# TYPE DCGM_FI_DEV_FB_USED gauge
DCGM_FI_DEV_FB_USED{DCGM_FI_DRIVER_VERSION="580.159.03",Hostname="e104",UUID="GPU-02e7ad33",device="nvidia0",gpu="0",modelName="NVIDIA GeForce RTX 4070",pci_bus_id="00000000:08:00.0"} 296
# HELP DCGM_FI_DEV_FB_RESERVED Framebuffer memory reserved (in MiB).
# TYPE DCGM_FI_DEV_FB_RESERVED gauge
DCGM_FI_DEV_FB_RESERVED{DCGM_FI_DRIVER_VERSION="580.159.03",Hostname="e104",UUID="GPU-02e7ad33",device="nvidia0",gpu="0",modelName="NVIDIA GeForce RTX 4070",pci_bus_id="00000000:08:00.0"} 258
# HELP DCGM_FI_DEV_UNCORRECTABLE_REMAPPED_ROWS Number of remapped rows for uncorrectable errors
# TYPE DCGM_FI_DEV_UNCORRECTABLE_REMAPPED_ROWS counter
DCGM_FI_DEV_UNCORRECTABLE_REMAPPED_ROWS{DCGM_FI_DRIVER_VERSION="580.159.03",Hostname="e104",UUID="GPU-02e7ad33",device="nvidia0",gpu="0",modelName="NVIDIA GeForce RTX 4070",pci_bus_id="00000000:08:00.0"} 2
# HELP DCGM_FI_DEV_CORRECTABLE_REMAPPED_ROWS Number of remapped rows for correctable errors
# TYPE DCGM_FI_DEV_CORRECTABLE_REMAPPED_ROWS counter
DCGM_FI_DEV_CORRECTABLE_REMAPPED_ROWS{DCGM_FI_DRIVER_VERSION="580.159.03",Hostname="e104",UUID="GPU-02e7ad33",device="nvidia0",gpu="0",modelName="NVIDIA GeForce RTX 4070",pci_bus_id="00000000:08:00.0"} 1
# HELP DCGM_FI_DEV_ROW_REMAP_FAILURE Whether remapping of rows has failed
# TYPE DCGM_FI_DEV_ROW_REMAP_FAILURE gauge
DCGM_FI_DEV_ROW_REMAP_FAILURE{DCGM_FI_DRIVER_VERSION="580.159.03",Hostname="e104",UUID="GPU-02e7ad33",device="nvidia0",gpu="0",modelName="NVIDIA GeForce RTX 4070",pci_bus_id="00000000:08:00.0"} 1
# HELP DCGM_FI_DEV_VGPU_LICENSE_STATUS vGPU License status
# TYPE DCGM_FI_DEV_VGPU_LICENSE_STATUS gauge
DCGM_FI_DEV_VGPU_LICENSE_STATUS{DCGM_FI_DRIVER_VERSION="580.159.03",Hostname="e104",UUID="GPU-02e7ad33",device="nvidia0",gpu="0",modelName="NVIDIA GeForce RTX 4070",pci_bus_id="00000000:08:00.0"} 0
`

func TestExporterMatchesLiveDCGMContract(t *testing.T) {
	e := New("e104")
	e.Update("580.159.03", []Device{fixture})
	if err := testutil.CollectAndCompare(e, strings.NewReader(golden)); err != nil {
		t.Fatal(err)
	}
}

func TestExporterEmptyUntilFirstUpdate(t *testing.T) {
	// A GPU-less or NVML-dead node must serve zero DCGM_FI_* series — exactly
	// like the dcgm-exporter DaemonSet, which never schedules there at all.
	if n := testutil.CollectAndCount(New("e1")); n != 0 {
		t.Fatalf("empty exporter must emit no series, got %d", n)
	}
}

func TestExporterOneSeriesPerDevicePerFamily(t *testing.T) {
	e := New("e104")
	second := fixture
	second.Index, second.UUID = 1, "GPU-second"
	e.Update("580.159.03", []Device{fixture, second})
	if n := testutil.CollectAndCount(e); n != 2*len(metricDefs) {
		t.Fatalf("want %d series (2 devices x %d families), got %d", 2*len(metricDefs), len(metricDefs), n)
	}
}
