# Native node_* Collectors — Phase 1 Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Replace the embedded upstream node_exporter with nodevitals' own collectors, starting with the eight simplest `/proc`-backed metric groups, so the `node_*` surface is produced by nodevitals code rather than by a vendored library.

**Architecture:** A new `internal/nodecompat` package implements `prometheus.Collector` directly and registers on the same `/metrics` endpoint, exactly as `internal/dcgmcompat` and `internal/smartctlcompat` already do. Inside it, one small file per metric group implements a private `subCollector` interface that reads a `/proc` file under an injected root and emits `prometheus.MustNewConstMetric` with descriptors copied byte-for-byte from node_exporter 1.12.1. As each group lands, the corresponding upstream collector is disabled via `--no-collector.<name>` in the chart's `extraFlags`, because two collectors emitting the same metric name make the Prometheus registry reject the whole scrape.

**Tech Stack:** Go 1.26 · `github.com/prometheus/client_golang` (already a direct dependency) · `prometheus/client_golang/prometheus/testutil` for golden-exposition tests · Helm chart in `deploy/chart`

## Global Constraints

- **Metric contract is byte-for-byte.** Name, HELP text, TYPE, label names and label order must match node_exporter 1.12.1 exactly. The values quoted in each task were read off a live scrape on node `e21`; they are the contract, not a suggestion.
- **Never emit a name the embedded collector still emits.** `prometheus.Registry` rejects two descriptors with the same fully-qualified name and different label sets, and the failure takes down the entire `/metrics` response, not just the offending metric. Every task that adds a group also adds its `--no-collector.<name>` flag in Task 6.
- **All filesystem roots are injected.** Collectors take `procRoot`/`sysRoot` parameters and never hardcode `/proc` or `/sys`. In the DaemonSet these are `/host/proc` and `/host/sys`; in tests they are `t.TempDir()`. This is the existing convention in `internal/collector` — follow it.
- **Code comments in English.** This repository's Go comments are English throughout (`internal/collector/*.go`, `internal/dcgmcompat/dcgmcompat.go`). Chart `values.yaml` comments are Korean. Match the file you are editing.
- **Go version:** `go 1.26` (see `go.mod`). Container images are `linux/amd64` only.
- **No new third-party dependencies.** `client_golang` is already present; nothing else may be added.
- **A failing sub-collector must not fail the scrape.** One unreadable `/proc` file returns an error that is logged once and skipped, the way `internal/collector.Registry.CollectAll` already treats per-collector failures.

---

## File Structure

**New package `internal/nodecompat/`** — one file per metric group so each stays small enough to read in one sitting, matching how `internal/collector` splits `cpu.go` / `mem.go` / `net.go`:

| File | Responsibility |
|---|---|
| `nodecompat.go` | `Exporter` (implements `prometheus.Collector`), the private `subCollector` interface, `New()` wiring, once-only failure logging |
| `loadavg.go` | `/proc/loadavg` → `node_load1` / `node_load5` / `node_load15` |
| `filefd.go` | `/proc/sys/fs/file-nr` → `node_filefd_allocated` / `node_filefd_maximum` |
| `entropy.go` | `/proc/sys/kernel/random/{entropy_avail,poolsize}` → `node_entropy_available_bits` / `node_entropy_pool_size_bits` |
| `procs.go` | `/proc/stat` → `node_procs_running` / `node_procs_blocked` |
| `vmstat.go` | `/proc/vmstat` (allowlist) → `node_vmstat_*` 7 metrics |
| `uname.go` | `uname(2)` syscall → `node_uname_info` |
| `osrelease.go` | `/etc/os-release` under rootfs → `node_os_info` / `node_os_version` |

**Modified:**

| File | Change |
|---|---|
| `cmd/nodevitals/main.go` | Register the `nodecompat.Exporter` alongside the existing `nodeexporter` collector |
| `internal/config/config.go` | Add `NativeCollectors` toggle under `NodeExporterConfig` |
| `deploy/chart/values.yaml` | Add `nativeCollectors` flag + the `--no-collector.*` entries |
| `deploy/chart/templates/configmap-*.yaml` | Pass the new flag through |
| `deploy/chart/Chart.yaml` | Version bump |

**Tests:** one `_test.go` beside each source file, all using `testutil.CollectAndCompare` against a golden exposition string.

---

## Task 1: Package skeleton + loadavg

**Files:**
- Create: `internal/nodecompat/nodecompat.go`
- Create: `internal/nodecompat/loadavg.go`
- Test: `internal/nodecompat/loadavg_test.go`

**Interfaces:**
- Consumes: nothing (first task)
- Produces:
  - `func New(procRoot, rootFS string, log *slog.Logger) *Exporter` — the package entry point; `rootFS` is the host root mount (`/host/root` in the DaemonSet) and is used from Task 5 onward.
  - `type Exporter struct` implementing `Describe(chan<- *prometheus.Desc)` and `Collect(chan<- prometheus.Metric)`.
  - private `type subCollector interface { Name() string; Collect(ch chan<- prometheus.Metric) error }` — every later task adds one implementation and appends it to the slice built in `New`.
  - private `func newLoadAvg(procRoot string) subCollector`

- [ ] **Step 1: Write the failing test**

Create `internal/nodecompat/loadavg_test.go`:

```go
package nodecompat

import (
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/prometheus/client_golang/prometheus/testutil"
)

// writeProcFile lays down one file under a fake procRoot.
func writeProcFile(t *testing.T, procRoot, name, body string) {
	t.Helper()
	path := filepath.Join(procRoot, name)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir for %s: %v", name, err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("write %s: %v", name, err)
	}
}

// exporterWith builds an Exporter carrying exactly one sub-collector, so a
// golden comparison covers that group and nothing else.
func exporterWith(subs ...subCollector) *Exporter {
	return &Exporter{log: slog.Default(), subs: subs}
}

func TestLoadAvg(t *testing.T) {
	procRoot := t.TempDir()
	// Byte-for-byte the shape a live kernel emits (e21).
	writeProcFile(t, procRoot, "loadavg", "13.62 7.62 5.52 8/5206 1591949\n")

	golden := `# HELP node_load1 1m load average.
# TYPE node_load1 gauge
node_load1 13.62
# HELP node_load15 15m load average.
# TYPE node_load15 gauge
node_load15 5.52
# HELP node_load5 5m load average.
# TYPE node_load5 gauge
node_load5 7.62
`
	if err := testutil.CollectAndCompare(exporterWith(newLoadAvg(procRoot)), strings.NewReader(golden)); err != nil {
		t.Errorf("loadavg exposition drifted from node_exporter contract:\n%v", err)
	}
}

// A missing /proc/loadavg must surface as an error from the sub-collector, not
// a panic and not silence — the Exporter decides what to do with it.
func TestLoadAvgMissingFileErrors(t *testing.T) {
	if err := newLoadAvg(t.TempDir()).Collect(make(chan prometheus.Metric, 8)); err == nil {
		t.Fatal("want error for missing /proc/loadavg, got nil")
	}
}

// A truncated line must error rather than emit a partial set: three metrics
// that are supposed to move together should never disagree.
func TestLoadAvgShortLineErrors(t *testing.T) {
	procRoot := t.TempDir()
	writeProcFile(t, procRoot, "loadavg", "1.00 2.00\n")

	if err := newLoadAvg(procRoot).Collect(make(chan prometheus.Metric, 8)); err == nil {
		t.Fatal("want error for a 2-field loadavg, got nil")
	}
}
```

Add the missing import for `prometheus` used by the two error tests — the file's import block becomes:

```go
import (
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
)
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/nodecompat/... -run TestLoadAvg -v`
Expected: FAIL — the package does not exist yet (`no Go files in .../internal/nodecompat`).

- [ ] **Step 3: Write the package skeleton**

Create `internal/nodecompat/nodecompat.go`:

```go
// Package nodecompat serves the upstream node_exporter metric surface from
// nodevitals' own readers, so the node_* families come from this codebase
// rather than from a vendored collector set — the /proc sibling of
// internal/dcgmcompat and internal/smartctlcompat.
//
// Each metric group is one subCollector reading one file under an injected
// root. Descriptors are copied byte-for-byte from a live node_exporter 1.12.1
// scrape: name, HELP, type, and label names are the compatibility contract,
// and the golden tests beside each file are what holds them to it.
//
// A group that lands here MUST be disabled on the embedded side with
// --no-collector.<name>. Two collectors emitting one metric name make the
// registry reject the whole scrape, so the failure is total rather than
// partial.
package nodecompat

import (
	"log/slog"
	"sync"

	"github.com/prometheus/client_golang/prometheus"
)

// subCollector is one /proc- or /sys-backed metric group. Collect writes its
// metrics to ch and returns an error only for conditions the operator can act
// on — a missing file, an unparseable line. "This machine has no such
// hardware" is not an error; it is an empty result.
type subCollector interface {
	Name() string
	Collect(ch chan<- prometheus.Metric) error
}

// Exporter implements prometheus.Collector over the registered sub-collectors.
type Exporter struct {
	log  *slog.Logger
	subs []subCollector

	// warned keeps a broken sub-collector to a single log line rather than one
	// per scrape. An unreadable /proc file is a deployment fact — it does not
	// change until the pod is restarted with different mounts.
	warned sync.Map
}

// New wires the sub-collectors this package serves. procRoot is the host /proc
// mount and rootFS the host root mount; both are injected so tests can point
// at a fixture directory.
func New(procRoot, rootFS string, log *slog.Logger) *Exporter {
	return &Exporter{
		log: log,
		subs: []subCollector{
			newLoadAvg(procRoot),
		},
	}
}

func (e *Exporter) Describe(ch chan<- *prometheus.Desc) {
	prometheus.DescribeByCollect(e, ch)
}

// Collect runs every sub-collector. One failure is logged once and skipped so
// a single unreadable file cannot cost the operator every other family —
// the same per-collector isolation internal/collector.Registry.CollectAll uses.
func (e *Exporter) Collect(ch chan<- prometheus.Metric) {
	for _, s := range e.subs {
		if err := s.Collect(ch); err != nil {
			if _, seen := e.warned.LoadOrStore(s.Name(), true); !seen {
				e.log.Warn("nodecompat collector failed", "collector", s.Name(), "err", err)
			}
		}
	}
}
```

- [ ] **Step 4: Write the loadavg sub-collector**

Create `internal/nodecompat/loadavg.go`:

```go
package nodecompat

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/prometheus/client_golang/prometheus"
)

// loadAvgDescs are node_exporter's three load descriptors, in the order the
// fields appear in /proc/loadavg.
var loadAvgDescs = [3]*prometheus.Desc{
	prometheus.NewDesc("node_load1", "1m load average.", nil, nil),
	prometheus.NewDesc("node_load5", "5m load average.", nil, nil),
	prometheus.NewDesc("node_load15", "15m load average.", nil, nil),
}

type loadAvg struct{ procRoot string }

func newLoadAvg(procRoot string) subCollector { return &loadAvg{procRoot: procRoot} }

func (l *loadAvg) Name() string { return "loadavg" }

// Collect parses the first three fields of /proc/loadavg. The remaining two
// (runnable/total processes, last PID) belong to other collectors.
func (l *loadAvg) Collect(ch chan<- prometheus.Metric) error {
	b, err := os.ReadFile(filepath.Join(l.procRoot, "loadavg"))
	if err != nil {
		return err
	}
	fields := strings.Fields(string(b))
	if len(fields) < len(loadAvgDescs) {
		// Emit nothing rather than a partial set: three averages that are
		// supposed to move together must never disagree on a dashboard.
		return fmt.Errorf("loadavg: want at least %d fields, got %d", len(loadAvgDescs), len(fields))
	}
	for i, d := range loadAvgDescs {
		v, err := strconv.ParseFloat(fields[i], 64)
		if err != nil {
			return fmt.Errorf("loadavg field %d (%q): %w", i, fields[i], err)
		}
		ch <- prometheus.MustNewConstMetric(d, prometheus.GaugeValue, v)
	}
	return nil
}
```

- [ ] **Step 5: Run tests to verify they pass**

Run: `go test ./internal/nodecompat/... -v`
Expected: PASS for `TestLoadAvg`, `TestLoadAvgMissingFileErrors`, `TestLoadAvgShortLineErrors`.

- [ ] **Step 6: Commit**

```bash
gofmt -w internal/nodecompat
git add internal/nodecompat
git commit -m "feat(nodecompat): 패키지 골격 + loadavg 자체 수집

node_exporter 임베드를 걷어내는 첫 증분. Exporter 가 prometheus.Collector 를
직접 구현하고, subCollector 하나가 /proc 파일 하나를 읽는다. 디스크립터는
라이브 node_exporter 1.12.1 스크레이프에서 그대로 옮겼고 골든 테스트가 그
계약을 붙든다.

검증: go test ./internal/nodecompat/... — 3 케이스 PASS
(골든 대조 / 파일 부재 에러 / 필드 부족 에러)"
```

---

## Task 2: filefd + entropy

**Files:**
- Create: `internal/nodecompat/filefd.go`
- Create: `internal/nodecompat/entropy.go`
- Modify: `internal/nodecompat/nodecompat.go` (append to the `subs` slice in `New`)
- Test: `internal/nodecompat/filefd_test.go`, `internal/nodecompat/entropy_test.go`

**Interfaces:**
- Consumes: `subCollector`, `Exporter`, `writeProcFile`, `exporterWith` from Task 1
- Produces: `func newFileFD(procRoot string) subCollector`, `func newEntropy(procRoot string) subCollector`

- [ ] **Step 1: Write the failing tests**

Create `internal/nodecompat/filefd_test.go`:

```go
package nodecompat

import (
	"strings"
	"testing"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
)

func TestFileFD(t *testing.T) {
	procRoot := t.TempDir()
	// Live shape (e21): "allocated<TAB>unused<TAB>maximum". The middle field has
	// been hardwired to 0 since Linux 2.6 and node_exporter ignores it too.
	writeProcFile(t, procRoot, "sys/fs/file-nr", "9679\t0\t2097152\n")

	golden := `# HELP node_filefd_allocated File descriptor statistics: allocated.
# TYPE node_filefd_allocated gauge
node_filefd_allocated 9679
# HELP node_filefd_maximum File descriptor statistics: maximum.
# TYPE node_filefd_maximum gauge
node_filefd_maximum 2.097152e+06
`
	if err := testutil.CollectAndCompare(exporterWith(newFileFD(procRoot)), strings.NewReader(golden)); err != nil {
		t.Errorf("filefd exposition drifted:\n%v", err)
	}
}

func TestFileFDMalformedErrors(t *testing.T) {
	procRoot := t.TempDir()
	writeProcFile(t, procRoot, "sys/fs/file-nr", "9679\n")

	if err := newFileFD(procRoot).Collect(make(chan prometheus.Metric, 8)); err == nil {
		t.Fatal("want error for a 1-field file-nr, got nil")
	}
}
```

Create `internal/nodecompat/entropy_test.go`:

```go
package nodecompat

import (
	"strings"
	"testing"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
)

func TestEntropy(t *testing.T) {
	procRoot := t.TempDir()
	// Live values (e21): a modern kernel keeps a 256-bit pool, saturated.
	writeProcFile(t, procRoot, "sys/kernel/random/entropy_avail", "256\n")
	writeProcFile(t, procRoot, "sys/kernel/random/poolsize", "256\n")

	golden := `# HELP node_entropy_available_bits Bits of available entropy.
# TYPE node_entropy_available_bits gauge
node_entropy_available_bits 256
# HELP node_entropy_pool_size_bits Bits of entropy pool.
# TYPE node_entropy_pool_size_bits gauge
node_entropy_pool_size_bits 256
`
	if err := testutil.CollectAndCompare(exporterWith(newEntropy(procRoot)), strings.NewReader(golden)); err != nil {
		t.Errorf("entropy exposition drifted:\n%v", err)
	}
}

func TestEntropyMissingFileErrors(t *testing.T) {
	if err := newEntropy(t.TempDir()).Collect(make(chan prometheus.Metric, 8)); err == nil {
		t.Fatal("want error for missing entropy files, got nil")
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/nodecompat/... -run 'TestFileFD|TestEntropy' -v`
Expected: FAIL — `undefined: newFileFD`, `undefined: newEntropy`.

- [ ] **Step 3: Write both sub-collectors**

Create `internal/nodecompat/filefd.go`:

```go
package nodecompat

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/prometheus/client_golang/prometheus"
)

var (
	fileFDAllocatedDesc = prometheus.NewDesc(
		"node_filefd_allocated", "File descriptor statistics: allocated.", nil, nil)
	fileFDMaximumDesc = prometheus.NewDesc(
		"node_filefd_maximum", "File descriptor statistics: maximum.", nil, nil)
)

type fileFD struct{ procRoot string }

func newFileFD(procRoot string) subCollector { return &fileFD{procRoot: procRoot} }

func (f *fileFD) Name() string { return "filefd" }

// Collect reads /proc/sys/fs/file-nr, whose three fields are allocated, unused
// and maximum. The middle field has been hardwired to 0 since Linux 2.6, so
// only the outer two carry information.
func (f *fileFD) Collect(ch chan<- prometheus.Metric) error {
	b, err := os.ReadFile(filepath.Join(f.procRoot, "sys", "fs", "file-nr"))
	if err != nil {
		return err
	}
	fields := strings.Fields(string(b))
	if len(fields) != 3 {
		return fmt.Errorf("file-nr: want 3 fields, got %d", len(fields))
	}
	for _, m := range []struct {
		desc  *prometheus.Desc
		field string
	}{
		{fileFDAllocatedDesc, fields[0]},
		{fileFDMaximumDesc, fields[2]},
	} {
		v, err := strconv.ParseFloat(m.field, 64)
		if err != nil {
			return fmt.Errorf("file-nr field %q: %w", m.field, err)
		}
		ch <- prometheus.MustNewConstMetric(m.desc, prometheus.GaugeValue, v)
	}
	return nil
}
```

Create `internal/nodecompat/entropy.go`:

```go
package nodecompat

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/prometheus/client_golang/prometheus"
)

var (
	entropyAvailDesc = prometheus.NewDesc(
		"node_entropy_available_bits", "Bits of available entropy.", nil, nil)
	entropyPoolDesc = prometheus.NewDesc(
		"node_entropy_pool_size_bits", "Bits of entropy pool.", nil, nil)
)

type entropy struct{ procRoot string }

func newEntropy(procRoot string) subCollector { return &entropy{procRoot: procRoot} }

func (e *entropy) Name() string { return "entropy" }

func (e *entropy) Collect(ch chan<- prometheus.Metric) error {
	for _, m := range []struct {
		desc *prometheus.Desc
		file string
	}{
		{entropyAvailDesc, "entropy_avail"},
		{entropyPoolDesc, "poolsize"},
	} {
		v, err := readUintFile(filepath.Join(e.procRoot, "sys", "kernel", "random", m.file))
		if err != nil {
			return err
		}
		ch <- prometheus.MustNewConstMetric(m.desc, prometheus.GaugeValue, v)
	}
	return nil
}

// readUintFile reads a sysctl-style file holding a single number. Only entropy
// uses it today; it is kept package-level because the /sys-backed groups in
// later phases (rapl, cooling, hwmon) read the same one-value-per-file shape.
func readUintFile(path string) (float64, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return 0, err
	}
	v, err := strconv.ParseFloat(strings.TrimSpace(string(b)), 64)
	if err != nil {
		return 0, fmt.Errorf("%s: %w", path, err)
	}
	return v, nil
}
```

- [ ] **Step 4: Register both in New**

In `internal/nodecompat/nodecompat.go`, replace the `subs` literal inside `New`:

```go
		subs: []subCollector{
			newLoadAvg(procRoot),
			newFileFD(procRoot),
			newEntropy(procRoot),
		},
```

- [ ] **Step 5: Run tests to verify they pass**

Run: `go test ./internal/nodecompat/... -v`
Expected: PASS for all five tests (three from Task 1, two groups added here).

- [ ] **Step 6: Commit**

```bash
gofmt -w internal/nodecompat
git add internal/nodecompat
git commit -m "feat(nodecompat): filefd + entropy 자체 수집

file-nr 의 중간 필드(unused)는 Linux 2.6 이후 항상 0 이라 바깥 두 개만 쓴다.
readUintFile 헬퍼는 값 하나짜리 sysctl 파일을 읽는 후속 컬렉터들이 공유한다.

검증: go test ./internal/nodecompat/... — 5 케이스 PASS"
```

---

## Task 3: procs (/proc/stat)

**Files:**
- Create: `internal/nodecompat/procs.go`
- Modify: `internal/nodecompat/nodecompat.go` (append to `subs`)
- Test: `internal/nodecompat/procs_test.go`

**Interfaces:**
- Consumes: `subCollector`, `exporterWith`, `writeProcFile` from Task 1
- Produces: `func newProcs(procRoot string) subCollector`

- [ ] **Step 1: Write the failing test**

Create `internal/nodecompat/procs_test.go`:

```go
package nodecompat

import (
	"strings"
	"testing"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
)

func TestProcs(t *testing.T) {
	procRoot := t.TempDir()
	// /proc/stat carries far more than these two lines; the collector must pick
	// them out and ignore everything else.
	writeProcFile(t, procRoot, "stat", `cpu  1 2 3 4 5 6 7 8 9 10
cpu0 1 2 3 4 5 6 7 8 9 10
intr 12345
ctxt 987654
btime 1700000000
processes 4242
procs_running 6
procs_blocked 31
softirq 1 2 3
`)

	golden := `# HELP node_procs_blocked Number of processes blocked waiting for I/O to complete.
# TYPE node_procs_blocked gauge
node_procs_blocked 31
# HELP node_procs_running Number of processes in runnable state.
# TYPE node_procs_running gauge
node_procs_running 6
`
	if err := testutil.CollectAndCompare(exporterWith(newProcs(procRoot)), strings.NewReader(golden)); err != nil {
		t.Errorf("procs exposition drifted:\n%v", err)
	}
}

// A /proc/stat without the procs_ lines (very old kernels, some emulators)
// must error rather than silently report zero running processes.
func TestProcsMissingLinesErrors(t *testing.T) {
	procRoot := t.TempDir()
	writeProcFile(t, procRoot, "stat", "cpu  1 2 3 4\nintr 5\n")

	if err := newProcs(procRoot).Collect(make(chan prometheus.Metric, 8)); err == nil {
		t.Fatal("want error when procs_running/procs_blocked are absent, got nil")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/nodecompat/... -run TestProcs -v`
Expected: FAIL — `undefined: newProcs`.

- [ ] **Step 3: Write the sub-collector**

Create `internal/nodecompat/procs.go`:

```go
package nodecompat

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/prometheus/client_golang/prometheus"
)

var (
	procsRunningDesc = prometheus.NewDesc(
		"node_procs_running", "Number of processes in runnable state.", nil, nil)
	procsBlockedDesc = prometheus.NewDesc(
		"node_procs_blocked", "Number of processes blocked waiting for I/O to complete.", nil, nil)
)

type procs struct{ procRoot string }

func newProcs(procRoot string) subCollector { return &procs{procRoot: procRoot} }

func (p *procs) Name() string { return "processes" }

// Collect scans /proc/stat for its two scheduler summary lines. Everything
// else in that file (per-CPU jiffies, interrupts, context switches) belongs to
// other collectors, so it is skipped here rather than parsed and discarded.
func (p *procs) Collect(ch chan<- prometheus.Metric) error {
	f, err := os.Open(filepath.Join(p.procRoot, "stat"))
	if err != nil {
		return err
	}
	defer f.Close()

	want := map[string]*prometheus.Desc{
		"procs_running": procsRunningDesc,
		"procs_blocked": procsBlockedDesc,
	}
	found := 0
	s := bufio.NewScanner(f)
	for s.Scan() {
		fields := strings.Fields(s.Text())
		if len(fields) != 2 {
			continue
		}
		desc, ok := want[fields[0]]
		if !ok {
			continue
		}
		v, err := strconv.ParseFloat(fields[1], 64)
		if err != nil {
			return fmt.Errorf("/proc/stat %s=%q: %w", fields[0], fields[1], err)
		}
		ch <- prometheus.MustNewConstMetric(desc, prometheus.GaugeValue, v)
		found++
	}
	if err := s.Err(); err != nil {
		return err
	}
	if found != len(want) {
		// Reporting a missing line as 0 would read as "nothing is running",
		// which is a stronger claim than "the kernel did not tell us".
		return fmt.Errorf("/proc/stat: found %d of %d procs_ lines", found, len(want))
	}
	return nil
}
```

- [ ] **Step 4: Register in New**

In `internal/nodecompat/nodecompat.go`, add to the `subs` literal after `newEntropy(procRoot)`:

```go
			newProcs(procRoot),
```

- [ ] **Step 5: Run tests to verify they pass**

Run: `go test ./internal/nodecompat/... -v`
Expected: PASS for all seven tests.

- [ ] **Step 6: Commit**

```bash
gofmt -w internal/nodecompat
git add internal/nodecompat
git commit -m "feat(nodecompat): procs 자체 수집 (/proc/stat)

procs_running/procs_blocked 두 줄만 뽑고 나머지(퍼-CPU jiffies·인터럽트·
컨텍스트 스위치)는 다른 컬렉터 몫이라 건너뛴다. 줄이 없으면 0 을 내지 않고
에러 — '아무것도 안 돈다' 는 '커널이 안 알려줬다' 보다 강한 주장이다.

검증: go test ./internal/nodecompat/... — 7 케이스 PASS"
```

---

## Task 4: vmstat

**Files:**
- Create: `internal/nodecompat/vmstat.go`
- Modify: `internal/nodecompat/nodecompat.go` (append to `subs`)
- Test: `internal/nodecompat/vmstat_test.go`

**Interfaces:**
- Consumes: `subCollector`, `exporterWith`, `writeProcFile` from Task 1
- Produces: `func newVMStat(procRoot string) subCollector`

**Contract note:** node_exporter exposes only the fields matching its default
allowlist regex `^(oom_kill|pgpg|pswp|pg.*fault).*`, and types them `untyped`
(not `counter`), because `/proc/vmstat` mixes counters and gauges under one
format. The seven names below are what a live scrape on e21 produced.

- [ ] **Step 1: Write the failing test**

Create `internal/nodecompat/vmstat_test.go`:

```go
package nodecompat

import (
	"strings"
	"testing"

	"github.com/prometheus/client_golang/prometheus/testutil"
)

func TestVMStat(t *testing.T) {
	procRoot := t.TempDir()
	// A real /proc/vmstat has ~180 lines; only the allowlisted ones surface.
	writeProcFile(t, procRoot, "vmstat", `nr_free_pages 1234567
nr_zone_inactive_anon 42
pgpgin 1592701043
pgpgout 669113661
pswpin 0
pswpout 0
pgfault 5587768098
pgmajfault 93845
pgscan_kswapd 17
oom_kill 0
`)

	golden := `# HELP node_vmstat_oom_kill /proc/vmstat information field oom_kill.
# TYPE node_vmstat_oom_kill untyped
node_vmstat_oom_kill 0
# HELP node_vmstat_pgfault /proc/vmstat information field pgfault.
# TYPE node_vmstat_pgfault untyped
node_vmstat_pgfault 5.587768098e+09
# HELP node_vmstat_pgmajfault /proc/vmstat information field pgmajfault.
# TYPE node_vmstat_pgmajfault untyped
node_vmstat_pgmajfault 93845
# HELP node_vmstat_pgpgin /proc/vmstat information field pgpgin.
# TYPE node_vmstat_pgpgin untyped
node_vmstat_pgpgin 1.592701043e+09
# HELP node_vmstat_pgpgout /proc/vmstat information field pgpgout.
# TYPE node_vmstat_pgpgout untyped
node_vmstat_pgpgout 6.69113661e+08
# HELP node_vmstat_pswpin /proc/vmstat information field pswpin.
# TYPE node_vmstat_pswpin untyped
node_vmstat_pswpin 0
# HELP node_vmstat_pswpout /proc/vmstat information field pswpout.
# TYPE node_vmstat_pswpout untyped
node_vmstat_pswpout 0
`
	if err := testutil.CollectAndCompare(exporterWith(newVMStat(procRoot)), strings.NewReader(golden)); err != nil {
		t.Errorf("vmstat exposition drifted:\n%v", err)
	}
}

// nr_free_pages and pgscan_kswapd are real fields that the default allowlist
// deliberately excludes; emitting them would be a contract break in the
// "serving more than the original" direction.
func TestVMStatExcludesNonAllowlisted(t *testing.T) {
	procRoot := t.TempDir()
	writeProcFile(t, procRoot, "vmstat", "nr_free_pages 1\npgscan_kswapd 2\npgfault 3\n")

	if got := testutil.CollectAndCount(exporterWith(newVMStat(procRoot))); got != 1 {
		t.Fatalf("served %d series, want 1 (pgfault only)", got)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/nodecompat/... -run TestVMStat -v`
Expected: FAIL — `undefined: newVMStat`.

- [ ] **Step 3: Write the sub-collector**

Create `internal/nodecompat/vmstat.go`:

```go
package nodecompat

import (
	"bufio"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"github.com/prometheus/client_golang/prometheus"
)

// vmStatAllowlist is node_exporter's default --collector.vmstat.fields value.
// /proc/vmstat carries roughly 180 fields whose meaning and cardinality shift
// between kernel releases, so upstream ships a conservative subset and so do
// we — serving the extra fields would be a contract break in the "more than
// the original" direction, which breaks dashboards just as surely as "less".
var vmStatAllowlist = regexp.MustCompile(`^(oom_kill|pgpg|pswp|pg.*fault).*`)

type vmStat struct{ procRoot string }

func newVMStat(procRoot string) subCollector { return &vmStat{procRoot: procRoot} }

func (v *vmStat) Name() string { return "vmstat" }

// Collect emits every allowlisted field as an untyped metric. Untyped is
// upstream's choice and it is the honest one: /proc/vmstat mixes monotonic
// counters (pgfault) with gauges (nr_free_pages) in one flat format, and
// nothing in the file says which is which.
func (v *vmStat) Collect(ch chan<- prometheus.Metric) error {
	f, err := os.Open(filepath.Join(v.procRoot, "vmstat"))
	if err != nil {
		return err
	}
	defer f.Close()

	s := bufio.NewScanner(f)
	for s.Scan() {
		fields := strings.Fields(s.Text())
		if len(fields) != 2 || !vmStatAllowlist.MatchString(fields[0]) {
			continue
		}
		val, err := strconv.ParseFloat(fields[1], 64)
		if err != nil {
			// One unparseable field must not cost the rest of the file.
			continue
		}
		ch <- prometheus.MustNewConstMetric(
			prometheus.NewDesc(
				"node_vmstat_"+fields[0],
				"/proc/vmstat information field "+fields[0]+".",
				nil, nil),
			prometheus.UntypedValue, val)
	}
	return s.Err()
}
```

- [ ] **Step 4: Register in New**

In `internal/nodecompat/nodecompat.go`, add to the `subs` literal after `newProcs(procRoot)`:

```go
			newVMStat(procRoot),
```

- [ ] **Step 5: Run tests to verify they pass**

Run: `go test ./internal/nodecompat/... -v`
Expected: PASS for all nine tests.

- [ ] **Step 6: Commit**

```bash
gofmt -w internal/nodecompat
git add internal/nodecompat
git commit -m "feat(nodecompat): vmstat 자체 수집 (기본 allowlist 준수)

upstream 기본 정규식 ^(oom_kill|pgpg|pswp|pg.*fault).* 을 그대로 쓴다.
/proc/vmstat 은 ~180 필드가 커널 릴리스마다 바뀌고 카운터와 게이지가 한
포맷에 섞여 있어 upstream 이 보수적 부분집합을 고른 것이고, 더 내는 것도
덜 내는 것만큼 계약 위반이다. 타입 untyped 도 그 이유로 upstream 을 따른다.

검증: go test ./internal/nodecompat/... — 9 케이스 PASS
(allowlist 제외 필드 미노출 검증 포함)"
```

---

## Task 5: uname + os-release

**Files:**
- Create: `internal/nodecompat/uname.go`
- Create: `internal/nodecompat/osrelease.go`
- Modify: `internal/nodecompat/nodecompat.go` (append to `subs`, wire `rootFS`)
- Test: `internal/nodecompat/uname_test.go`, `internal/nodecompat/osrelease_test.go`

**Interfaces:**
- Consumes: `subCollector`, `exporterWith` from Task 1; `New`'s `rootFS` parameter declared in Task 1
- Produces:
  - `func newUname(u unameFunc) subCollector` and `type unameFunc func() (unameInfo, error)`, plus `func realUname() (unameInfo, error)` — the syscall is behind a seam because `uname(2)` cannot be pointed at a fixture directory.
  - `type unameInfo struct { Sysname, Nodename, Release, Version, Machine, Domainname string }`
  - `func newOSRelease(rootFS string) subCollector`

- [ ] **Step 1: Write the failing tests**

Create `internal/nodecompat/uname_test.go`:

```go
package nodecompat

import (
	"errors"
	"strings"
	"testing"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
)

func TestUnameInfo(t *testing.T) {
	// Live values from e21.
	fake := func() (unameInfo, error) {
		return unameInfo{
			Sysname:    "Linux",
			Nodename:   "e21",
			Release:    "6.17.0-22-generic",
			Version:    "#22~24.04.1-Ubuntu SMP PREEMPT_DYNAMIC Thu Mar 26 15:25:54 UTC 2",
			Machine:    "x86_64",
			Domainname: "(none)",
		}, nil
	}

	golden := `# HELP node_uname_info Labeled system information as provided by the uname system call.
# TYPE node_uname_info gauge
node_uname_info{domainname="(none)",machine="x86_64",nodename="e21",release="6.17.0-22-generic",sysname="Linux",version="#22~24.04.1-Ubuntu SMP PREEMPT_DYNAMIC Thu Mar 26 15:25:54 UTC 2"} 1
`
	if err := testutil.CollectAndCompare(exporterWith(newUname(fake)), strings.NewReader(golden)); err != nil {
		t.Errorf("uname exposition drifted:\n%v", err)
	}
}

func TestUnameSyscallFailurePropagates(t *testing.T) {
	fail := func() (unameInfo, error) { return unameInfo{}, errors.New("EFAULT") }

	if err := newUname(fail).Collect(make(chan prometheus.Metric, 8)); err == nil {
		t.Fatal("want error when the syscall fails, got nil")
	}
}
```

Create `internal/nodecompat/osrelease_test.go`:

```go
package nodecompat

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/prometheus/client_golang/prometheus/testutil"
)

func writeOSRelease(t *testing.T, rootFS, body string) {
	t.Helper()
	dir := filepath.Join(rootFS, "etc")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir etc: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "os-release"), []byte(body), 0o644); err != nil {
		t.Fatalf("write os-release: %v", err)
	}
}

func TestOSRelease(t *testing.T) {
	rootFS := t.TempDir()
	// Live /etc/os-release from e21, quoting and all.
	writeOSRelease(t, rootFS, `PRETTY_NAME="Ubuntu 24.04.4 LTS"
NAME="Ubuntu"
VERSION_ID="24.04"
VERSION="24.04.4 LTS (Noble Numbat)"
VERSION_CODENAME=noble
ID=ubuntu
ID_LIKE=debian
HOME_URL="https://www.ubuntu.com/"
`)

	golden := `# HELP node_os_info A metric with a constant '1' value labeled by build_id, id, id_like, image_id, image_version, name, pretty_name, variant, variant_id, version, version_codename, version_id.
# TYPE node_os_info gauge
node_os_info{build_id="",id="ubuntu",id_like="debian",image_id="",image_version="",name="Ubuntu",pretty_name="Ubuntu 24.04.4 LTS",variant="",variant_id="",version="24.04.4 LTS (Noble Numbat)",version_codename="noble",version_id="24.04"} 1
# HELP node_os_version Metric containing the major.minor part of the OS version.
# TYPE node_os_version gauge
node_os_version{id="ubuntu",id_like="debian",name="Ubuntu"} 24.04
`
	if err := testutil.CollectAndCompare(exporterWith(newOSRelease(rootFS)), strings.NewReader(golden)); err != nil {
		t.Errorf("os-release exposition drifted:\n%v", err)
	}
}

// A VERSION_ID the kernel vendor wrote as a single number ("42") still has to
// produce a parseable node_os_version.
func TestOSReleaseSingleComponentVersion(t *testing.T) {
	rootFS := t.TempDir()
	writeOSRelease(t, rootFS, "NAME=\"Fedora\"\nID=fedora\nVERSION_ID=42\n")

	golden := `# HELP node_os_version Metric containing the major.minor part of the OS version.
# TYPE node_os_version gauge
node_os_version{id="fedora",id_like="",name="Fedora"} 42
`
	if err := testutil.CollectAndCompare(exporterWith(newOSRelease(rootFS)),
		strings.NewReader(golden), "node_os_version"); err != nil {
		t.Errorf("single-component version:\n%v", err)
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/nodecompat/... -run 'TestUname|TestOSRelease' -v`
Expected: FAIL — `undefined: newUname`, `undefined: unameInfo`, `undefined: newOSRelease`.

- [ ] **Step 3: Write the uname sub-collector**

Create `internal/nodecompat/uname.go`:

```go
package nodecompat

import (
	"github.com/prometheus/client_golang/prometheus"
	"golang.org/x/sys/unix"
)

var unameDesc = prometheus.NewDesc(
	"node_uname_info",
	"Labeled system information as provided by the uname system call.",
	[]string{"sysname", "release", "version", "machine", "nodename", "domainname"},
	nil)

// unameInfo is the subset of struct utsname the metric carries.
type unameInfo struct {
	Sysname, Nodename, Release, Version, Machine, Domainname string
}

// unameFunc is the seam over uname(2). Unlike every other source in this
// package the syscall cannot be pointed at a fixture directory, so tests
// inject a fake instead of a root path.
type unameFunc func() (unameInfo, error)

type uname struct{ get unameFunc }

func newUname(get unameFunc) subCollector { return &uname{get: get} }

func (u *uname) Name() string { return "uname" }

func (u *uname) Collect(ch chan<- prometheus.Metric) error {
	info, err := u.get()
	if err != nil {
		return err
	}
	ch <- prometheus.MustNewConstMetric(unameDesc, prometheus.GaugeValue, 1,
		info.Sysname, info.Release, info.Version, info.Machine, info.Nodename, info.Domainname)
	return nil
}

// realUname calls uname(2). The fields are NUL-padded fixed arrays, so each is
// trimmed at the first NUL rather than converted whole.
func realUname() (unameInfo, error) {
	var u unix.Utsname
	if err := unix.Uname(&u); err != nil {
		return unameInfo{}, err
	}
	return unameInfo{
		Sysname:    nulString(u.Sysname[:]),
		Nodename:   nulString(u.Nodename[:]),
		Release:    nulString(u.Release[:]),
		Version:    nulString(u.Version[:]),
		Machine:    nulString(u.Machine[:]),
		Domainname: nulString(u.Domainname[:]),
	}, nil
}

func nulString(b []byte) string {
	for i, c := range b {
		if c == 0 {
			return string(b[:i])
		}
	}
	return string(b)
}
```

`golang.org/x/sys` is already an indirect dependency (via `prometheus/procfs`);
promote it to direct:

```bash
go get golang.org/x/sys/unix
go mod tidy
```

- [ ] **Step 4: Write the os-release sub-collector**

Create `internal/nodecompat/osrelease.go`:

```go
package nodecompat

import (
	"bufio"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/prometheus/client_golang/prometheus"
)

// osInfoLabels is the label set node_exporter emits, in the order its HELP
// text lists them. Fields absent from a given distribution's os-release come
// out empty, which is what upstream does too — an empty label is dropped at
// ingestion, so the stored series is identical.
var osInfoLabels = []string{
	"build_id", "id", "id_like", "image_id", "image_version", "name",
	"pretty_name", "variant", "variant_id", "version", "version_codename", "version_id",
}

var (
	osInfoDesc = prometheus.NewDesc(
		"node_os_info",
		"A metric with a constant '1' value labeled by build_id, id, id_like, image_id, image_version, name, pretty_name, variant, variant_id, version, version_codename, version_id.",
		osInfoLabels, nil)
	osVersionDesc = prometheus.NewDesc(
		"node_os_version",
		"Metric containing the major.minor part of the OS version.",
		[]string{"id", "id_like", "name"}, nil)
)

type osRelease struct{ rootFS string }

func newOSRelease(rootFS string) subCollector { return &osRelease{rootFS: rootFS} }

func (o *osRelease) Name() string { return "os" }

func (o *osRelease) Collect(ch chan<- prometheus.Metric) error {
	kv, err := parseOSRelease(filepath.Join(o.rootFS, "etc", "os-release"))
	if err != nil {
		return err
	}

	values := make([]string, len(osInfoLabels))
	for i, l := range osInfoLabels {
		values[i] = kv[strings.ToUpper(l)]
	}
	ch <- prometheus.MustNewConstMetric(osInfoDesc, prometheus.GaugeValue, 1, values...)

	// node_os_version carries only major.minor as a number, so a VERSION_ID the
	// vendor wrote as "24.04.4" or "42" must both parse.
	if v, ok := majorMinor(kv["VERSION_ID"]); ok {
		ch <- prometheus.MustNewConstMetric(osVersionDesc, prometheus.GaugeValue, v,
			kv["ID"], kv["ID_LIKE"], kv["NAME"])
	}
	return nil
}

// parseOSRelease reads the shell-fragment format: KEY=value or KEY="value",
// blank lines and # comments skipped.
func parseOSRelease(path string) (map[string]string, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	kv := map[string]string{}
	s := bufio.NewScanner(f)
	for s.Scan() {
		line := strings.TrimSpace(s.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		k, v, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		kv[strings.TrimSpace(k)] = strings.Trim(strings.TrimSpace(v), `"'`)
	}
	return kv, s.Err()
}

// majorMinor turns "24.04.4" into 24.04 and "42" into 42. ok is false when the
// field is absent or unparseable, and the caller then omits the metric rather
// than reporting version 0.
func majorMinor(version string) (float64, bool) {
	if version == "" {
		return 0, false
	}
	parts := strings.SplitN(version, ".", 3)
	joined := parts[0]
	if len(parts) > 1 {
		joined += "." + parts[1]
	}
	v, err := strconv.ParseFloat(joined, 64)
	if err != nil {
		return 0, false
	}
	return v, true
}
```

- [ ] **Step 5: Register both in New**

In `internal/nodecompat/nodecompat.go`, add to the `subs` literal after `newVMStat(procRoot)`:

```go
			newUname(realUname),
			newOSRelease(rootFS),
```

- [ ] **Step 6: Run tests to verify they pass**

Run: `go test ./internal/nodecompat/... -v`
Expected: PASS for all thirteen tests.

- [ ] **Step 7: Commit**

```bash
gofmt -w internal/nodecompat
git add internal/nodecompat go.mod go.sum
git commit -m "feat(nodecompat): uname + os-release 자체 수집

uname(2) 는 fixture 디렉터리로 가리킬 수 없는 유일한 원천이라 unameFunc
seam 뒤에 두고 테스트는 fake 를 주입한다(smartProbe·gpuReader 와 같은 패턴).
os-release 는 배포판마다 없는 필드가 있어 빈 라벨로 나가며, 빈 라벨은 수집 시
버려지므로 저장 시리즈는 upstream 과 동일하다.

검증: go test ./internal/nodecompat/... — 13 케이스 PASS
(단일 컴포넌트 VERSION_ID 파싱 포함)"
```

---

## Task 6: Wire into the agent, disable the embedded twins, verify live

**Files:**
- Modify: `cmd/nodevitals/main.go`
- Modify: `internal/config/config.go`
- Modify: `deploy/chart/values.yaml`
- Modify: `deploy/chart/templates/configmap-single.yaml`
- Modify: `deploy/chart/templates/configmap.yaml`
- Modify: `deploy/chart/Chart.yaml`
- Test: `internal/config/config_test.go`

**Interfaces:**
- Consumes: `nodecompat.New(procRoot, rootFS string, log *slog.Logger) *Exporter` from Task 1
- Produces: `config.NodeExporterConfig.NativeCollectors bool` (yaml `nativeCollectors`)

**Contract note:** the six groups landing here map to these upstream collector
names, and every one must be disabled on the embedded side in the same change:
`loadavg`, `filefd`, `entropy`, `processes`, `vmstat`, `uname`, `os`.

- [ ] **Step 1: Write the failing config test**

Append to `internal/config/config_test.go`:

```go
func TestLoadNativeCollectors(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "c.yaml")
	os.WriteFile(path, []byte("node: e21\nnodeExporter:\n  enabled: true\n  nativeCollectors: true\n"), 0o644)
	c, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !c.NodeExporter.NativeCollectors {
		t.Fatal("nodeExporter.nativeCollectors: true must parse")
	}
	// Absent block stays off — the native surface is opt-in until every group
	// has a live parity check.
	os.WriteFile(path, []byte("node: e21\n"), 0o644)
	c, err = Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if c.NodeExporter.NativeCollectors {
		t.Fatal("nativeCollectors must default to disabled")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/config/... -run TestLoadNativeCollectors -v`
Expected: FAIL — `c.NodeExporter.NativeCollectors undefined`.

- [ ] **Step 3: Add the config field**

In `internal/config/config.go`, inside `type NodeExporterConfig struct`, after `ExtraFlags`:

```go
	// NativeCollectors serves the node_* groups nodevitals implements itself
	// (internal/nodecompat) instead of the embedded upstream ones. Every group
	// it serves MUST also be disabled through ExtraFlags with
	// --no-collector.<name>: two collectors emitting one metric name make the
	// registry reject the entire scrape, not just the duplicate.
	NativeCollectors bool `yaml:"nativeCollectors"`
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/config/... -run TestLoadNativeCollectors -v`
Expected: PASS.

- [ ] **Step 5: Register the exporter in main**

In `cmd/nodevitals/main.go`, add the import:

```go
	"github.com/KeiaiLab/nodevitals/internal/nodecompat"
```

and immediately after the `if cfg.NodeExporter.Enabled { ... }` block that
registers the embedded collectors, add:

```go
	// Native node_* groups. Registered independently of the embedded set so a
	// deployment can run either, but never both for the same group — the
	// chart pairs this flag with the matching --no-collector.* entries.
	if cfg.NodeExporter.NativeCollectors {
		nc := nodecompat.New(cfg.ProcRoot, cfg.NodeExporter.RootFSPath, slog.Default())
		if err := metrics.Register(nc); err != nil {
			slog.Error("register native node collectors", "err", err)
			os.Exit(1)
		}
		slog.Info("native node_* collectors enabled")
	}
```

- [ ] **Step 6: Verify the build and full test suite**

Run: `GOOS=linux GOARCH=amd64 go build ./... && go test ./...`
Expected: build succeeds, all packages `ok`.

- [ ] **Step 7: Wire the chart**

In `deploy/chart/values.yaml`, under `nodeExporter:` after `mountSystemd`:

```yaml
    # nodevitals 자체 구현 node_* 그룹(internal/nodecompat)을 임베드 대신 쓴다.
    # 켜면 아래 extraFlags 의 --no-collector.* 7종이 반드시 함께 있어야 한다 —
    # 같은 메트릭 이름을 두 컬렉터가 내면 registry 가 스크레이프 전체를 거부하며,
    # 실패가 해당 메트릭 하나가 아니라 /metrics 응답 전부다.
    nativeCollectors: false
```

In the same file's `extraFlags` list, append (keeping the two existing entries):

```yaml
      - --no-collector.loadavg
      - --no-collector.filefd
      - --no-collector.entropy
      - --no-collector.processes
      - --no-collector.vmstat
      - --no-collector.uname
      - --no-collector.os
```

In both `deploy/chart/templates/configmap-single.yaml` and
`deploy/chart/templates/configmap.yaml`, inside the `nodeExporter:` block that
already renders `rootfsPath`, add:

```yaml
{{- if .Values.nodeExporter.nativeCollectors }}
      nativeCollectors: true
{{- end }}
```

Bump `deploy/chart/Chart.yaml`:

```yaml
version: 0.9.0
appVersion: "0.9.0"
```

- [ ] **Step 8: Verify chart rendering**

```bash
helm lint deploy/chart
helm template t deploy/chart --set singlePod=true --set nodeExporter.enabled=true \
  --set nodeExporter.nativeCollectors=true | grep -E "nativeCollectors|no-collector"
bash deploy/chart/tests/tier-runtime.sh
```

Expected: lint `0 chart(s) failed`; the template output shows
`nativeCollectors: true` plus all seven `--no-collector.*` flags; the runtime
test prints `PASS`.

- [ ] **Step 9: Commit**

```bash
git add cmd internal/config deploy/chart
git commit -m "feat(nodecompat): 에이전트 배선 + 임베드 7 컬렉터 비활성 + 차트 0.9.0

nativeCollectors 를 켜면 nodecompat 이 loadavg/filefd/entropy/processes/
vmstat/uname/os 를 내고, 같은 이름을 내던 임베드 컬렉터 7종은
--no-collector.* 로 끈다. 둘을 동시에 켜면 registry 가 스크레이프 전체를
거부하므로 이 짝은 선택이 아니라 필수다.

검증: go build(linux) + go test ./... 전 패키지 ok /
helm lint 0 failed / helm template 에서 flag 7종 + nativeCollectors 렌더 /
tier-runtime.sh PASS"
```

- [ ] **Step 10: Live parity check against upstream**

This is the step that actually proves the rewrite. Deploy the chart, then run
upstream node_exporter beside the agent on the same node and compare.

```bash
# On a cluster node (e.g. e21), with the new image rolled out:
curl -s http://127.0.0.1:9847/metrics > /tmp/NV.txt

# Upstream, same host, same collectors enabled:
curl -sSL -o /tmp/ne.tar.gz \
  https://github.com/prometheus/node_exporter/releases/download/v1.12.1/node_exporter-1.12.1.linux-amd64.tar.gz
mkdir -p /tmp/ne && tar xzf /tmp/ne.tar.gz -C /tmp/ne --strip-components=1
sudo /tmp/ne/node_exporter --web.listen-address=127.0.0.1:9101 \
  --collector.processes >/tmp/ne.log 2>&1 &
sleep 15
curl -s http://127.0.0.1:9101/metrics > /tmp/NE.txt
sudo pkill -f "ne/node_exporter"

# Compare only the seven migrated groups.
for g in load filefd entropy procs vmstat uname os; do
  a=$(grep -cE "^node_${g}" /tmp/NE.txt)
  b=$(grep -cE "^node_${g}" /tmp/NV.txt)
  printf "%-10s upstream=%-4s nodevitals=%s\n" "$g" "$a" "$b"
done

# HELP/TYPE lines must be identical for every migrated family.
for g in load filefd entropy procs vmstat uname os; do
  diff <(grep -E "^# (HELP|TYPE) node_${g}" /tmp/NE.txt | sort) \
       <(grep -E "^# (HELP|TYPE) node_${g}" /tmp/NV.txt | sort) \
    && echo "OK   node_${g}" || echo "DIFF node_${g}"
done

rm -rf /tmp/ne /tmp/ne.tar.gz /tmp/ne.log /tmp/NE.txt /tmp/NV.txt
```

Expected: matching series counts per group (values may differ by a scrape's
worth of drift on counters like `pgfault`), and `OK` for all seven HELP/TYPE
comparisons. A `DIFF` here means the descriptor text drifted and must be fixed
before the next phase.

---

## Roadmap — remaining phases

Phase 1 covers ~700 of the 7,021 `node_*` series. The rest, in the order their
size and independence suggest, each becoming its own plan:

| Phase | Groups | Series | Notes |
|---|---|---|---|
| 2 | filesystem, disk | 2,061 | `/proc/self/mountinfo` + `/proc/diskstats`; needs mount-point filtering rules |
| 3 | network, netstat, sockstat, softnet, nf, udp | ~1,900 | `/proc/net/*`; hostNetwork already in place |
| 4 | hwmon, cooling, rapl, nvme, infiniband, edac | ~500 | `/sys/class/*`; 57 families in hwmon alone, mostly label-driven |
| 5 | systemd | 1,591 | dbus client over `/run/systemd/private` — the only group needing a protocol implementation |
| 6 | cpu, schedstat, ipvs, bonding, pressure, timex, time, textfile | ~800 | cpu is 576 series of per-core jiffies; textfile is a file-format reader |

**Note (post-review, 2026-07-31):** `node_procs_running`/`node_procs_blocked` are not in
Phase 1 and not yet in any row above. They belong to node_exporter's `stat` collector
(`collector/stat_linux.go`, default-enabled), not a `processes` collector — `processes` is
default-disabled and owns entirely different names (`node_processes_threads`/`_state`/
`_pids`/`_max_processes`). Phase 1 originally shipped a `procs` group disabled via
`--no-collector.processes`, which left `stat` running and emitting the same two names — a
live registry conflict, so that group was dropped rather than shipped broken. The two
metrics are deferred to whichever future phase takes on `stat`'s other five families
(`node_intr_total`, `node_context_switches_total`, `node_forks_total`,
`node_boot_time_seconds`, `node_softirqs_total`); `stat` needs its own row when that phase
is scoped.

When the last phase lands, remove `internal/nodeexporter`, drop
`github.com/prometheus/node_exporter` from `go.mod`, and delete the
`--no-collector.*` list along with the `nativeCollectors` toggle.
