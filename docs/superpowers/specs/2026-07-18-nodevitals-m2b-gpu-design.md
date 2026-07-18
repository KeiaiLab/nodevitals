# nodevitals M2b — GPU tier 설계 (go-nvml + XID 이벤트, 실측 정정)

> **상태** Draft · 2026-07-18 · **repo** <https://github.com/KeiaiLab/nodevitals> · **선행** [M2 설계 §4](2026-07-18-nodevitals-m2-design.md)
> 코드 식별자·스키마·플래그는 영어, 서술은 한국어. 본 문서는 M2 §4 를 **실측으로 정정·구체화**한다.

**한눈에** — NVIDIA GPU 텔레메트리 tier. polled 메트릭(사용률·VRAM·온도·전력·throttle) + **비동기 XID 이벤트 구독**. go-nvml 은 cgo 라 `//go:build gpu` + glibc 이미지(`:v-gpu`)로 격리. **핵심 제약 2건**(아래)이 아키텍처를 좌우한다.

---

## 0. 실측으로 확정된 두 제약 (M2b 설계의 뿌리)

### 제약 1 — `CGO_ENABLED=0` 은 go-nvml import 시 불가 (mock 포함)
실측(`go build` 직접): go-nvml 에는 **순수 Go 인터페이스 패키지가 없다**. `pkg/nvml` 은 단일 패키지이고 `const.go`·`nvml.go` 가 `import "C"` 를 포함해, `Return`/`EccCounterType`/`EventType*` 등 코어 타입이 cgo 파일에만 있다. `pkg/nvml/mock` 조차 `pkg/nvml` 을 import 하므로 **mock 을 쓰는 테스트도 cgo 강제**. ⇒ go-nvml 을 import 하는 어떤 코드도 CGO_ENABLED=0 으로 못 짓는다.

**귀결 — 자체 seam 필수** (smart tier `smartProbe` 패턴 재현): nodevitals 자체 `gpuReader` 인터페이스(plain struct 반환, go-nvml 타입 미노출)를 두고, NVML 구현만 `//go:build gpu` 뒤에 격리. **콜렉터 로직·XID 분류·throttle 디코딩은 전부 untagged 순수 Go** → fake `gpuReader` 로 CGO_ENABLED=0 하드웨어 0대 테스트(macOS 포함). go-nvml 은 테스트 경로에 **절대 import 안 함**.

### 제약 2 — cgo 빌드는 CUDA/드라이버 불요, 단 macOS 크로스컴파일 불가
실측: `-tags gpu` cgo 빌드는 vendored `pkg/nvml/nvml.h` + C 컴파일러만 필요(런타임 `dlopen(libnvidia-ml.so)` — 링크타임 아님). NVIDIA 베이스이미지·CUDA 불요, plain `golang:1.26-bookworm` 로 빌드됨. 단 macOS→linux cgo 크로스컴파일은 실패(clang 이 linux cgo prologue 어셈블 불가). ⇒ **gpu 빌드 컴파일 체크 = Docker `golang:1.26` 컨테이너**.

> [!IMPORTANT]
> **검증 천장 (정직)**: 이 개발 머신엔 NVIDIA GPU 가 없다. M2b 는 (a) 로직(메트릭 매핑·XID 분류·이벤트 변환)을 fake 로 CGO=0 **완전 단위테스트** (b) gpu 빌드를 Docker 로 **컴파일 체크** 까지만 여기서 검증한다. **실 NVML 호출·EventSetWait 런타임·driver-reset 경로는 실 GPU 스모크가 유일 검증** → 사용자/GPU CI 러너로 **이연**(ship 전 필수 항목).

---

## 1. 파이프라인 코어 변경 — 비동기 이벤트 소스 (M2 "코어 불변" 가정 수정)

M1~M2c 는 전부 **poll 기반**(agent.Tick → CollectAll → samples → engine.Evaluate → events). 그러나 **XID 는 `EventSetWait` 블로킹 구독**(비동기 goroutine)이라 tick 과 무관하게 도착하고, **이미 "이벤트"**(임계 평가 불필요, XID→severity 는 분류표 lookup)다. ⇒ engine 을 우회해 **sink 로 직결**해야 한다.

### 결정: `EventSource` seam + agent 드레인
```go
// internal/collector (untagged) — 비동기 이벤트를 내는 콜렉터의 옵트인 인터페이스
type EventSource interface {
    Events() <-chan model.Event
}
```
- GPU 콜렉터는 `Collector`(polled 메트릭)이자 `EventSource`(비동기 XID)다.
- `Registry.EventSources() []EventSource` (등록 콜렉터를 type-assert) 추가.
- `agent.Run(ctx)` 가 tick 루프 시작 전, **event source 마다 드레인 goroutine** 기동: `for ev := range src.Events() { <각 webhook 으로 DeliverWithRetry> }`. ctx.Done 시 종료(소스가 채널 close).
- XID 이벤트는 **webhook sink 만** 탄다(metrics/REST 는 sample 스냅샷 기반 — 이벤트는 상태 스냅샷이 아님). polled GPU 메트릭은 기존 tick 경로로 /metrics·REST 에 그대로.

**영향 최소화**: `EventSource` 미구현 콜렉터(core/smart 전부)는 무영향 — agent 는 드레인할 소스가 0개. GPU tier 만 이 경로를 쓴다.

---

## 2. gpuReader seam (untagged 인터페이스 + gpu-tagged NVML + !gpu 스텁)

```go
// internal/collector/gpu.go (untagged) — go-nvml 타입 미노출
type gpuDevice struct {
    Index        int
    UUID         string
    Name         string
    UtilGPU      float64  // %
    MemUsedBytes float64
    MemTotalBytes float64
    TempC        float64
    PowerW       float64
    ThrottleReasons uint64 // 비트마스크 (디코딩은 §4)
    EccUncorrected float64 // aggregate DBE count
}
type xidRaw struct { DeviceIndex int; UUID string; Xid uint64 }
type gpuReader interface {
    Read(ctx context.Context) ([]gpuDevice, error) // polled 스냅샷
    XidEvents() <-chan xidRaw                        // 비동기 XID (구독 goroutine 이 feed)
    Close() error
}
func NewGPUCollector(node string, r gpuReader) Collector /* + EventSource */
```
- **NVML 구현** `gpu_nvml.go` (`//go:build gpu`, cgo): `NewNVMLReader()` → `nvml.Init()` → 디바이스 핸들. `Read` 는 디바이스별 NVML getter 호출. 생성 시 **XID 구독 goroutine** 기동(§3) → `xidRaw` 채널 feed. `Close` 는 goroutine 중단 + `nvml.Shutdown()`.
- **스텁** `gpu_stub.go` (`//go:build !gpu`): `NewNVMLReader()` 가 빈 리더(Read→nil,nil / XidEvents→닫힌 채널) 반환 → main.go 가 tag 없이도 컴파일.
- **콜렉터**(untagged): `Collect` = `r.Read()` → gpuDevice → samples(`gpu_utilization_pct`·`gpu_mem_used_bytes`·`gpu_mem_total_bytes`·`gpu_temperature_celsius`·`gpu_power_watts`·`gpu_ecc_uncorrected_total`(counter, Kind=KindCounter)·throttle §4). `Events()` = `r.XidEvents()` 를 §3 분류표로 `model.Event` 변환해 내보내는 채널. Device=`gpu<Index>`(또는 UUID), Tier="gpu".

**테스트**: fake `gpuReader`(canned gpuDevices + XidEvents 채널에 canned xidRaw push) → 콜렉터 Collect·Events 를 CGO_ENABLED=0 로 완전 단위테스트. go-nvml import 0.

---

## 3. XID 구독 + 분류 (실측 API)

### 구독 (gpu-tagged, cgo, 실 GPU 스모크만 런타임 검증)
`EventData` 실측 구조(`nvml.h:3243`): `{Device, EventType uint64, EventData uint64(=XID), GpuInstanceId, ComputeInstanceId}`. XID 번호는 **`EventData.EventData`** 필드, `EventType` 에 `EventTypeXidCriticalError=8` 비트, 미상 XID=999.
```go
es, _ := nvml.EventSetCreate()
dev.RegisterEvents(nvml.EventTypeXidCriticalError|nvml.EventTypeDoubleBitEccError|nvml.EventTypeSingleBitEccError, es)
for {                            // gpu-tagged goroutine, ctx 로 종료
    e, ret := es.Wait(5000)
    if ret == nvml.ERROR_TIMEOUT { continue }
    if ret != nvml.SUCCESS { /* 로그 후 continue */ }
    ch <- xidRaw{Xid: e.EventData, UUID: uuidOf(e.Device)}
}
```

### 분류표 (untagged 순수 Go — CGO_ENABLED=0 단위테스트)
`internal/collector/xid.go` (untagged): `map[uint64]xidClass{severity, condition}`. M2 §4.3 표 그대로:
| XID | severity |
|---|---|
| 13/31/43 | info(benign) |
| 48/64/79/95/119/120 | critical |
| 63/74/92/94 | warning |
| 미등록(예: 999) | warning(보수적 default) |
→ `gpu_xid_error` 이벤트, `detail.xid` + `detail.description`, severity=분류. seq/fingerprint 는 기존 event.go 규약(Device 포함 hash) — 단 XID 는 engine 우회라 콜렉터가 직접 Event 구성(Fingerprint()+phase(항상 ENTER, 순간 이벤트)+콜렉터-로컬 seq). **문자열 설명은 raw XID PDF 재확인 후 하드코딩**(confidence 주석).

---

## 4. Throttle-reason 디코딩 (untagged 순수 Go — 테스트)
`GetCurrentClocksThrottleReasons` 비트마스크(폴링 gauge). untagged `decodeThrottle(mask uint64) []string` + 심각 비트만 이벤트화(옵션). 비트: benign(`GpuIdle=0x1`·`AppClocks=0x2`·`SwPowerCap=0x4`·`SyncBoost=0x10`·`DisplayClock=0x100`) vs warning(`SwThermal=0x20`·`HwThermal=0x40`·`HwPowerBrake=0x80`). v0.1 은 **gauge 노출만**(`gpu_throttle_reasons` raw mask); throttle→이벤트 룰은 후속(사용자 룰로 가능).

---

## 5. 빌드 · 이미지 fork · 배포

- **build tags**: 기본(no tag, CGO_ENABLED=0) = core+smart+gpu-stub(static `:v`). `-tags gpu`(CGO_ENABLED=1) = +NVML(glibc `:v-gpu`).
- **Dockerfile**: 기존 static stage 유지 + **gpu variant stage** 추가(`golang:1.26-bookworm` 빌더 + `CGO_ENABLED=1 -tags gpu` → `gcr.io/distroless/cc-debian12:nonroot` 런타임). 별도 태그 `:v-gpu`. amd64 우선(arm64 GPU=GH200 후속).
- **컴파일 체크(CI/로컬)**: `docker run --rm -v $PWD:/src -w /src golang:1.26-bookworm sh -c 'go build -tags gpu ./...'` (드라이버 불요). Makefile `build-gpu` 타깃.
- **차트 gpu DaemonSet**(`tiers.gpu.enabled`, 기본 false): 이미지 `:v-gpu`(별도 값), `nodeSelector:{nvidia.com/gpu.present:"true"}`, `tolerations:[{key:nvidia.com/gpu,operator:Exists,effect:NoSchedule}]`, env `NVIDIA_VISIBLE_DEVICES=all`+`NVIDIA_DRIVER_CAPABILITIES=utility`, **`resources.limits[nvidia.com/gpu]` 미요청**(관리 컨테이너), 무특권(디바이스는 toolkit 주입). config `tier: gpu`.

---

## 6. 테스트 전략 (3-tier 검증 — 정직)
| 대상 | 방법 | 여기서? |
|---|---|---|
| 콜렉터 로직(메트릭 매핑·Collect·Events 변환) | fake `gpuReader` + CGO_ENABLED=0 | ✅ 완전 |
| XID 분류표·throttle 디코딩 | 순수 Go 단위테스트(XID int 리터럴) | ✅ 완전 |
| agent EventSource 드레인 | fake EventSource + 기존 agent 테스트 | ✅ 완전 |
| gpu-tagged NVML 바인딩 | `docker run golang:1.26 go build -tags gpu` 컴파일 체크 | ✅ 컴파일만 |
| 실 NVML 호출·XID Wait·driver-reset | **실 GPU 스모크** | ❌ 이연(사용자/GPU CI) |

---

## 7. 분해 (구현 계획 태스크)
1. gpuReader seam(인터페이스+gpuDevice/xidRaw) + XID 분류표(untagged) + throttle 디코딩 — 순수 로직·테스트
2. GPU 콜렉터(Collect 메트릭 매핑 + Events XID 변환) — fake reader 테스트
3. agent EventSource 드레인 + Registry.EventSources() — fake source 테스트
4. NVML 구현(`gpu_nvml.go` gpu-tagged) + 스텁(`gpu_stub.go`) + main.go tier=gpu 배선 — Docker 컴파일 체크
5. Dockerfile `:v-gpu` variant + Makefile build-gpu(Docker) — 이미지 빌드
6. 차트 gpu DaemonSet(opt-in) + 문서 + 실 GPU 스모크 항목 이슈화

## 8. 리스크
| 리스크 | 완화 |
|---|---|
| 실 GPU 없이 NVML 런타임 미검증 | 로직 완전 단위테스트 + Docker 컴파일 체크 + ship 전 실 GPU 스모크 의무(이슈) |
| agent 코어 변경(async source) 회귀 | fake EventSource 테스트 + core/smart 무영향(소스 0) 확인 + whole-branch 리뷰 |
| cgo cross-compile 불가 | Docker 컴파일 체크로 대체(정직 명시) |
| XID 문자열 설명 confidence:low | raw PDF 재확인 후 하드코딩, 미상=999 보수 default |
| :v-gpu 이미지 glibc(작지 않음) | GPU 없는 클러스터는 미pull, core/smart static 이점 불변 |

## 9. 참조
- go-nvml v0.13.3-1 (Apache-2.0, cgo dlopen, `pkg/nvml/mock` moq) — 실측: CGO=0 불가/nvml.h EventData.EventData=XID/plain golang 이미지 빌드
- M2 설계 §4 (선행 — 본 문서가 실측 정정) · M1 §10 (이벤트 엔진) · smart tier(seam 패턴 선례)
