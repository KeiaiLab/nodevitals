# nodevitals M2b — GPU Tier Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: superpowers:subagent-driven-development. Steps use checkbox syntax.

**Goal:** GPU tier — NVIDIA polled metrics + async XID events, via a `gpuReader` seam so all LOGIC is CGO_ENABLED=0 unit-tested; NVML binding isolated behind `//go:build gpu` (glibc `:v-gpu` image), compile-checked in Docker. Real-GPU smoke deferred (no GPU here).

**Architecture:** [M2b design](../specs/2026-07-18-nodevitals-m2b-gpu-design.md). Own `gpuReader` interface (untagged, plain structs) → NVML impl behind `//go:build gpu` + `//go:build !gpu` stub. GPU collector is both `Collector` (polled → samples) and `EventSource` (async XID → events, engine-bypassed to sinks). Agent gains an EventSource drain.

**Tech Stack:** Go 1.26 · `github.com/NVIDIA/go-nvml` (Apache-2.0, cgo/dlopen, gpu-tagged only) · Docker `golang:1.26-bookworm` for the gpu compile-check.

## Global Constraints

- Module `github.com/KeiaiLab/nodevitals`, go-directive `1.26`. **`github.com/NVIDIA/go-nvml` is added but MUST NOT enter the default build** — it appears only in `//go:build gpu` files. `go mod tidy` will list it in go.mod (that's fine — it's a direct dep of a tagged file); the default `CGO_ENABLED=0 go build ./...` must still succeed (proves go-nvml isn't in the default graph).
- **CGO_ENABLED=0 default build is sacred**: `go build ./...` and `go test ./...` on macOS (this machine) must stay green. NO go-nvml import in any untagged or `_test.go` file. The GPU collector logic is tested against a hand-written fake `gpuReader` — zero go-nvml.
- Metric names (frozen #1 contract): `nodevitals_hw_` prefix, native names. GPU gauges: `gpu_utilization_pct`, `gpu_mem_used_bytes`, `gpu_mem_total_bytes`, `gpu_temperature_celsius`, `gpu_power_watts`, `gpu_throttle_reasons`. Counter: `gpu_ecc_uncorrected_total` (Kind=KindCounter). Tier `"gpu"`, Device `gpu<Index>`.
- XID events bypass the engine (they're pre-classified) → delivered directly to webhook sinks via the agent EventSource drain.
- gpu-tagged code is compile-checked via `docker run golang:1.26-bookworm go build -tags gpu ./...` (NO CUDA/driver needed — runtime dlopen). Real NVML runtime = deferred GPU smoke.
- gofmt clean (`make fmt` gate). Conventional Commits.

## File Structure (additions)

```
internal/collector/
  gpu.go        gpu_test.go       # untagged: gpuReader iface, gpuDevice/xidRaw structs, GPU collector (Collect+Events), EventSource
  xid.go        xid_test.go       # untagged: XID→severity classification table
  throttle.go   throttle_test.go  # untagged: throttle bitmask decode
  gpu_nvml.go                     # //go:build gpu — NVML gpuReader impl + XID subscription goroutine
  gpu_stub.go                     # //go:build !gpu — no-op gpuReader (compiles without tag)
internal/collector/collector.go   # + EventSource iface + Registry.EventSources()
internal/agent/agent.go           # + drain EventSources → webhooks
cmd/nodevitals/main.go            # + case "gpu"
Dockerfile                        # + gpu variant stage (:v-gpu, cc-debian12)
Makefile                          # + build-gpu (docker compile-check)
deploy/chart/templates/daemonset-gpu.yaml  configmap-gpu.yaml
deploy/chart/values.yaml          # gpuRules + image.gpuTag
docs .../m2b-gpu-design.md        # (already committed)
```

**Order**: 1 seam+xid+throttle (untagged logic) → 2 GPU collector (fake-tested) → 3 agent EventSource drain → 4 NVML impl+stub+main wiring (docker compile-check) → 5 Dockerfile+Makefile → 6 chart+docs+GPU-smoke issue.

---

### Task 1: gpuReader seam + XID classification + throttle decode (untagged, pure logic)

**Files:** Create `internal/collector/gpu.go` (types only this task), `xid.go`, `xid_test.go`, `throttle.go`, `throttle_test.go`.

**Interfaces (Produces):**
```go
// gpu.go (untagged) — plain structs, NO go-nvml types
type gpuDevice struct {
	Index int; UUID, Name string
	UtilGPU, MemUsedBytes, MemTotalBytes, TempC, PowerW float64
	ThrottleReasons uint64
	EccUncorrected  float64
}
type xidRaw struct { DeviceIndex int; UUID string; Xid uint64 }
type gpuReader interface {
	Read(ctx context.Context) ([]gpuDevice, error)
	XidEvents() <-chan xidRaw
	Close() error
}
// xid.go
type xidClass struct { Severity, Condition, Description string }
func ClassifyXid(xid uint64) xidClass   // table lookup; unknown → warning default
// throttle.go
func DecodeThrottle(mask uint64) []string  // human labels for set bits
```

- [ ] **Step 1: xid.go table + test** — `xid_test.go`: assert `ClassifyXid(79).Severity=="critical"`, `ClassifyXid(13).Severity=="info"`, `ClassifyXid(63).Severity=="warning"`, `ClassifyXid(999).Severity=="warning"` (unknown default). RED → implement the `map[uint64]xidClass` per design §3 table (13/31/43=info; 48/64/79/95/119/120=critical; 63/74/92/94=warning; default=warning) + Condition `"gpu_xid_error"`, Description short strings → GREEN.
- [ ] **Step 2: throttle.go + test** — `throttle_test.go`: `DecodeThrottle(0x20|0x40)` contains "sw_thermal_slowdown"+"hw_thermal_slowdown"; `DecodeThrottle(0x1)` → ["gpu_idle"] (or empty of the benign-only). Implement bit→label map (idle 0x1, app_clocks 0x2, sw_power_cap 0x4, sw_thermal 0x20, hw_thermal 0x40, hw_power_brake 0x80, sync_boost 0x10, display 0x100), iterate a fixed bit order (deterministic).
- [ ] **Step 3: gpu.go types** — define the 3 structs + `gpuReader` interface (no impl yet). `go build ./...` compiles (unused interface OK in Go).
- [ ] **Step 4: verify + commit** — `go test ./internal/collector/ -run 'TestXid|TestThrottle' -v` PASS, `go build ./... && go vet ./...` clean, `gofmt -l` empty. `git commit -am "feat(collector): gpu seam types + XID classification + throttle decode"`

---

### Task 2: GPU collector (Collect metrics + Events XID) — fake-reader tested

**Files:** Modify `internal/collector/gpu.go` (add collector), create/extend `internal/collector/gpu_test.go`.

**Interfaces:**
- Consumes: `gpuReader`, `model.Sample/Event`, `ClassifyXid`, `DecodeThrottle`.
- Produces: `NewGPUCollector(node string, r gpuReader) *gpuCollector` where `*gpuCollector` implements `Collector` (`Name()`, `Collect(ctx)`) AND `EventSource` (`Events() <-chan model.Event`). Collect maps each gpuDevice → samples (gauges + `gpu_ecc_uncorrected_total` counter Kind=KindCounter). Events() returns a channel the collector feeds by transforming `r.XidEvents()` xidRaw → `model.Event` via ClassifyXid (Tier "gpu", Device "gpu<Index>", Condition "gpu_xid_error", Phase ENTER, per-collector seq, detail.xid/description, ID=Fingerprint()+phase+seq).

- [ ] **Step 1: fake reader in test** — `gpu_test.go` defines a `fakeReader` implementing gpuReader: `Read` returns a canned `[]gpuDevice`; `XidEvents` returns a channel the test can push `xidRaw` to; `Close` no-op. (No go-nvml.)
- [ ] **Step 2: Collect test (RED)** — fake returns one gpuDevice{Index:0, UtilGPU:55, MemUsedBytes:1e9, MemTotalBytes:8e9, TempC:70, PowerW:250, ThrottleReasons:0x40, EccUncorrected:3}. Assert samples include `gpu_utilization_pct==55` (Device "gpu0", Tier "gpu", gauge), `gpu_ecc_uncorrected_total==3` with `Kind==model.KindCounter`, `gpu_throttle_reasons==0x40`. Deterministic order.
- [ ] **Step 3: Events test (RED)** — start the collector's Events() drain; push `xidRaw{DeviceIndex:0, Xid:79}` to the fake's channel; assert one `model.Event` arrives with Condition "gpu_xid_error", Severity "critical", Device "gpu0", Phase ENTER, detail["xid"]==uint64(79). Push xid 13 → Severity "info". Use a select-with-timeout (NOT sleep) to read the event channel deterministically.
- [ ] **Step 4: implement** the collector (Collect + a goroutine-free Events that ranges the reader channel in a spawned goroutine started lazily, or Events() returns a channel wired at construction — pick the simpler: construct an out-channel in NewGPUCollector, spawn a goroutine that ranges r.XidEvents() → transforms → out; the goroutine ends when r.XidEvents() closes). Ensure Events() is safe to call once (return the same channel).
- [ ] **Step 5: verify + commit** — `go test ./internal/collector/ -run TestGPU -v` PASS (+ all collector tests), build/vet/gofmt clean. `git commit -am "feat(collector): GPU collector — polled metrics + async XID events via reader seam"`

---

### Task 3: agent EventSource drain + Registry.EventSources()

**Files:** Modify `internal/collector/collector.go` (+EventSource iface + Registry.EventSources()), `internal/agent/agent.go` (+drain), `internal/agent/agent_test.go` (+test).

**Interfaces:**
- Produces: `collector.EventSource interface { Events() <-chan model.Event }`; `func (r *Registry) EventSources() []EventSource` (type-asserts each collector). Agent: in `Run`, before the ticker loop, spawn one goroutine per `reg.EventSources()` that `for ev := range src.Events() { for each webhook: queue.DeliverWithRetry(ctx, sink, []Event{ev}, ...) }`.

- [ ] **Step 1: EventSource iface + Registry.EventSources() in collector.go** — plus a tiny test `collector_test.go` that a stub implementing EventSource is returned by EventSources() and a plain collector is not.
- [ ] **Step 2: agent drain test (RED)** — `agent_test.go`: a fake collector implementing both Collector (Collect returns nil) and EventSource (Events() returns a channel). Register it, build the agent with a captureSink webhook, run `a.Run(ctx)` in a goroutine, push a model.Event to the source channel, assert the captureSink receives it (select-with-timeout), then cancel ctx. (This tests the async delivery path.)
- [ ] **Step 3: implement** the drain in agent.Run (spawn goroutines for each EventSource before the tick loop; each delivers events to all webhooks via DeliverWithRetry; goroutines exit when the source channel closes or ctx cancels). Ensure no goroutine leak (the source closes its channel on Close; agent's Run returns on ctx.Done — document that event-source goroutines rely on the source closing the channel).
- [ ] **Step 4: verify + commit** — `go test ./internal/agent/ ./internal/collector/ -v` PASS, full `go test ./...` green, build/vet/gofmt clean. `git commit -am "feat(agent): drain EventSource collectors to webhooks (async events)"`

---

### Task 4: NVML impl (gpu-tagged) + stub + main.go wiring + Docker compile-check

**Files:** Create `internal/collector/gpu_nvml.go` (`//go:build gpu`), `internal/collector/gpu_stub.go` (`//go:build !gpu`). Modify `cmd/nodevitals/main.go` (+case "gpu"). Add `go get github.com/NVIDIA/go-nvml`.

**Interfaces:**
- Both files export `func NewNVMLReader() (gpuReader, error)` with the SAME signature (build-tag split). Stub returns a no-op reader (Read→nil,nil; XidEvents→a closed channel; Close→nil). Real one uses go-nvml.
- main.go `case "gpu"`: `r, err := collector.NewNVMLReader(); if err != nil { fatal }; gc := collector.NewGPUCollector(cfg.Node, r); reg.Add(gc)` (gc is both Collector and EventSource; the agent picks it up via EventSources()).

- [ ] **Step 1: stub** `gpu_stub.go` (`//go:build !gpu`) — `NewNVMLReader() (gpuReader, error)` returning a no-op reader struct (satisfies the interface). This keeps the default (untagged) build compiling with a real `case "gpu"` in main.go.
- [ ] **Step 2: main.go case "gpu"** — add the gpu case to the tier switch. `go build ./...` (default, uses stub) clean; `go test ./...` green.
- [ ] **Step 3: add go-nvml dep** — `go get github.com/NVIDIA/go-nvml`; `go mod tidy`. Confirm default `CGO_ENABLED=0 go build ./...` STILL succeeds (go-nvml not in default graph — it's only referenced by the not-yet-added gpu file). go-directive stays 1.26.
- [ ] **Step 4: implement `gpu_nvml.go`** (`//go:build gpu`) — VERIFY the go-nvml API first inside Docker: `docker run --rm --platform=linux/amd64 -v "$PWD":/src -w /src golang:1.26-bookworm sh -c 'go doc github.com/NVIDIA/go-nvml/pkg/nvml Device'` etc. Implement `nvmlReader`: `NewNVMLReader` → `nvml.Init()` (Return check), enumerate devices, start the XID subscription goroutine (EventSetCreate/RegisterEvents/Wait loop per design §3, feeding an internal xidRaw channel), keep device handles. `Read` → per device: GetUtilizationRates/GetMemoryInfo/GetTemperature/GetPowerUsage/GetCurrentClocksThrottleReasons/GetTotalEccErrors → gpuDevice. `Close` → stop goroutine (via an internal done channel) + close xid channel + `nvml.Shutdown()`. ADAPT method names/returns to the actual go-nvml API found via go doc — the goal is correct gpuDevice population, not a guessed API.
- [ ] **Step 5: Docker compile-check** — `docker run --rm --platform=linux/amd64 -v "$PWD":/src -w /src golang:1.26-bookworm sh -c 'CGO_ENABLED=1 go build -tags gpu ./...'` → clean (this is the ONLY way to verify the gpu build on macOS; no CUDA needed). Also confirm `go build ./...` (default, macOS) still clean.
- [ ] **Step 6: verify + commit** — default build+test+vet green on macOS; Docker `-tags gpu` build clean; gofmt clean. Commit `feat(collector): NVML gpuReader (gpu-tagged) + stub + gpu tier wiring` — include the Docker compile-check output in the body.

---

### Task 5: Dockerfile `:v-gpu` variant + Makefile build-gpu

**Files:** Modify `Dockerfile` (add a gpu build stage/target), `Makefile` (+build-gpu).

- [ ] **Step 1: Dockerfile gpu stage** — add a second builder+runtime producing the gpu image: builder `golang:1.26-bookworm` with `CGO_ENABLED=1 go build -tags gpu -o /out/nodevitals ./cmd/nodevitals`; runtime `gcr.io/distroless/cc-debian12:nonroot` (glibc, for the cgo/dlopen binary). Keep the existing static stage as the default. Use a build ARG or a separate target so `docker build` default = static and `--target gpu` (or a second Dockerfile.gpu) = the gpu image. Choose the simplest: a multi-target Dockerfile with `AS gpu` final stage; document the tag.
- [ ] **Step 2: Makefile build-gpu** — `build-gpu: docker build --target gpu -t ghcr.io/keiailab/nodevitals:dev-gpu .` (or the compile-check form). Add a `gpu-check` target = the docker compile-check from Task 4.
- [ ] **Step 3: verify** — `make build-gpu` produces the cc-debian12 image (or at least the gpu stage builds); the default `make docker` still produces the static image. Confirm the default `make all` is unaffected.
- [ ] **Step 4: commit** `build(docker): gpu image variant (cc-debian12) + make build-gpu`

---

### Task 6: chart gpu DaemonSet (opt-in) + docs + GPU-smoke issue

**Files:** Create `deploy/chart/templates/daemonset-gpu.yaml`, `configmap-gpu.yaml`. Modify `deploy/chart/values.yaml`. Modify README + design doc.

- [ ] **Step 1: chart gpu tier** — `{{- if .Values.tiers.gpu.enabled }}` (default false). DaemonSet `nodevitals-gpu` (component: gpu): image `{{ .Values.image.repository }}:{{ .Values.image.gpuTag | default (printf "%s-gpu" .Chart.AppVersion) }}` (the `:v-gpu` variant). `nodeSelector: {nvidia.com/gpu.present: "true"}`, `tolerations: [{key: nvidia.com/gpu, operator: Exists, effect: NoSchedule}]`, env `NVIDIA_VISIBLE_DEVICES=all` + `NVIDIA_DRIVER_CAPABILITIES=utility`, NO `resources.limits[nvidia.com/gpu]`, unprivileged securityContext (like core: runAsNonRoot, drop ALL, readOnlyRootFS — the toolkit injects the device, no caps needed), `automountServiceAccountToken: false`. ConfigMap `tier: gpu`, metrics sink, `gpuRules` from values. metrics port from `.Values.metrics.port`.
- [ ] **Step 2: values.yaml** — `image.gpuTag: ""`; `gpuRules` (device-wildcard, engine supports ""):
```yaml
gpuRules:
  - { metric: gpu_temperature_celsius, device: "", condition: gpu_hot,       severity: warning,  threshold: 85, enterFor: 3, exitFor: 3 }
  - { metric: gpu_ecc_uncorrected_total, device: "", condition: gpu_ecc,     severity: critical, threshold: 0,  enterFor: 1, exitFor: 1 }
```
  (XID events come via the async path, not rules — these gpuRules cover polled thresholds.)
- [ ] **Step 3: validate** — `helm template nv deploy/chart | grep -c "kind: DaemonSet"` → 1 (gpu off); `--set tiers.gpu.enabled=true` → 2, with `nvidia.com/gpu.present` nodeSelector + `:*-gpu` image; `kubeconform -strict` Valid both.
- [ ] **Step 4: docs + issue** — README status: GPU tier landed (mock-tested, real-GPU smoke pending). Design doc §4 pointer to m2b-gpu-design. (Controller will create the GPU-smoke GitHub issue after merge.)
- [ ] **Step 5: final regression + commit** — `make all` (default, macOS) green; `helm template ... | kubeconform` Valid; Docker `-tags gpu` build clean. `git commit -am "feat(chart): gpu-tier DaemonSet (opt-in) + docs"`

---

## Self-Review
- 스펙 커버(M2b 설계): seam(T1) / 콜렉터 Collect+Events(T2) / agent async drain(T3) / NVML impl+stub+wiring(T4) / 이미지 fork(T5) / 차트+문서(T6). 실 GPU 스모크 = 이슈 이연(명시).
- **CGO_ENABLED=0 신성 유지**: go-nvml import 는 T4 의 gpu-tagged 파일에만. T1-T3 로직은 fake reader 로 테스트(go-nvml 0). default build/test 는 macOS 에서 green 유지가 매 태스크 게이트.
- 타입 일관: gpuReader/gpuDevice/xidRaw(T1) → 콜렉터 소비(T2) → NVML/스텁 구현(T4). EventSource(T3) → 콜렉터가 구현(T2)·agent 소비(T3). Kind=KindCounter(ecc)=#1 계약.
- 검증 천장 정직: T4 는 Docker 컴파일 체크가 상한(실 NVML 런타임 불가). 실 GPU 스모크 이슈화.
- 리스크: go-nvml API 유동 → T4 구현자가 Docker 안 go doc 실측 후 적응(smart/anatol 패턴). agent 코어 변경 → fake source 테스트 + core/smart 무영향 확인.
