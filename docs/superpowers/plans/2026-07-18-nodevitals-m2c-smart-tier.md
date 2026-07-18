# nodevitals M2c — SMART Tier Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: superpowers:subagent-driven-development. Steps use checkbox syntax.

**Goal:** privileged smart-tier — 디스크 SMART(SATA)·NVMe 헬스를 순수 Go(anatol/smart.go)로 수집, tier 선택 배선(core vs smart), 특권 DaemonSet 차트. 하드웨어 0대 테스트 유지.

**Architecture:** anatol/smart.go 는 ioctl 에 결합돼 있어 mock 이 불가 → **probe seam** 을 그 위에 둔다: `smartProbe func(ctx) ([]smartDevice, error)` 가 중립 파싱 결과(smartDevice 구조체)를 반환하고, 콜렉터는 probe 만 소비한다. 프로덕션 probe = `<devRoot>` glob 디스커버리 + anatol 호출(linux 전용 파일). 테스트 = fake probe 주입. 이벤트는 기존 엔진의 nonzero-threshold 룰로 커버(델타 "surge" 룰은 엔진 확장 필요 → 별도 이슈로 이연).

**Tech Stack:** Go 1.26 · `github.com/anatol/smart.go`(MIT, 태그 릴리스 없음 → `go get @latest` 가 pseudo-version 으로 SHA 핀) · 순수 Go(cgo 없음 — **static 이미지 유지**).

## Global Constraints

- Module `github.com/KeiaiLab/nodevitals`, go-directive `1.26` 불변. 새 의존 = anatol/smart.go 하나(MIT). `go mod tidy` 후 직접 의존 확인.
- **네이밍 계약 (#1 에서 동결)**: 전 메트릭 `nodevitals_hw_` 접두 + 전 표면 동일 이름. smartctl_exporter 원명 에뮬레이션 **안 함** — 네이티브 `smart_*`/`nvme_*` + 문서 매핑표 (본 계획 Task 6 이 M2 설계 §3 해당 단락 amend).
- SMART/NVMe 값은 전부 **gauge**(수명 odometer·레벨 값 — smartctl_exporter 도 gauge 관례). `Kind` 미설정(zero=gauge).
- Sample 정체: `Tier:"smart"`, `Device:"<name>"`(sda, nvme0n1), `Node`, `Timestamp`.
- 하드웨어 0대: 콜렉터 로직은 fake probe 주입으로 결정론 테스트. anatol 파싱 정확성은 라이브러리 책임(+실 디스크 배포 스모크는 후속 검증 항목으로 명시). `go build ./...` 는 macOS 에서도 성공해야 함 — anatol 이 darwin 컴파일 안 되면 프로덕션 probe 를 `//go:build linux` + darwin 스텁으로 분리.
- gofmt clean (`make fmt` 게이트 존재). Conventional Commits. 커밋 본문에 검증 인용.

## File Structure (M2c 종료 시점 추가분)

```
internal/collector/
  smart.go        smart_test.go       # 콜렉터 + probe seam + 매핑 (OS 무관)
  smart_probe_linux.go                # 프로덕션 probe: <devRoot> glob + anatol (linux)
  smart_probe_stub.go                 # //go:build !linux — 빈 probe (컴파일용)
internal/config/config.go             # DevRoot 추가 (기본 /dev)
cmd/nodevitals/main.go                # tier 기반 콜렉터 선택 (core vs smart)
deploy/chart/templates/daemonset-smart.yaml   # 특권 smart tier (기본 disabled)
deploy/chart/templates/configmap-smart.yaml
deploy/chart/values.yaml              # tiers.smart 활성 시 렌더 + 기본 nonzero 룰
docs/superpowers/specs/2026-07-18-nodevitals-m2-design.md  # §3 compat 단락 amend
```

**Task 순서**: 1 seam+SATA 매핑 → 2 NVMe 매핑 → 3 프로덕션 probe(디스커버리+anatol) → 4 config DevRoot + tier 배선 → 5 차트 smart tier → 6 문서 amend + 최종 회귀.

---

### Task 1: probe seam + SATA 매핑 (TDD)

**Files:** Create `internal/collector/smart.go`, `internal/collector/smart_test.go`.

**Interfaces (Produces):**
```go
// 중립 파싱 결과 — anatol 타입이 collector 표면에 새지 않게 함
type smartDevice struct {
	Name        string   // "sda", "nvme0n1"
	Transport   string   // "sata" | "nvme"
	Temperature *float64 // °C (없으면 nil)
	PowerOnHours *uint64
	ATAAttrs    map[uint8]uint64 // SATA raw 값: 5,187,188,197,198 만 채움
	NVMe        *nvmeHealth      // nvme 전용 (Task 2)
}
type nvmeHealth struct {
	PercentageUsed, AvailableSpare, SpareThreshold float64
	MediaErrors, CriticalWarning                    float64
}
type smartProbe func(ctx context.Context) ([]smartDevice, error)

func NewSmart(node string, probe smartProbe) Collector   // probe 주입
```

**매핑 (SATA — smartDevice → Samples, Tier:"smart", Device:Name):**
| 소스 | metric |
|---|---|
| Temperature | `smart_temperature_celsius` |
| PowerOnHours | `smart_power_on_hours` |
| ATAAttrs[5] | `smart_reallocated_sectors` |
| ATAAttrs[187] | `smart_reported_uncorrectable` |
| ATAAttrs[188] | `smart_command_timeout` |
| ATAAttrs[197] | `smart_pending_sectors` |
| ATAAttrs[198] | `smart_offline_uncorrectable` |

nil/부재 필드는 생략(샘플 미방출). probe 에러는 콜렉터 에러로 wrap. probe 가 0 디바이스 → 0 샘플(에러 아님).

- [ ] 실패 테스트: fake probe 가 sda(temp 36.0, poh 1000, attrs 5:0,187:0,188:2,197:5,198:0) 반환 → 샘플 7개, `smart_pending_sectors==5`·`smart_temperature_celsius==36`·전부 Tier "smart"/Device "sda"/Kind gauge 검증. probe 에러 → Collect 에러. 0 디바이스 → 0 샘플 무에러.
- [ ] 구현 → PASS → `gofmt -l` 빈출력 → commit `feat(collector): smart probe seam + SATA attribute mapping`

### Task 2: NVMe 매핑 (TDD)

**Files:** Modify `internal/collector/smart.go`(+매핑), `smart_test.go`(+테스트).

**매핑 (NVMe — smartDevice.NVMe 비nil 시):**
| 소스 | metric |
|---|---|
| PercentageUsed | `nvme_percentage_used` |
| AvailableSpare | `nvme_available_spare` |
| SpareThreshold | `nvme_available_spare_threshold` |
| MediaErrors | `nvme_media_errors` |
| CriticalWarning | `nvme_critical_warning` (비트마스크 raw 값) |
(+ Temperature/PowerOnHours 는 Task 1 공통 매핑 그대로)

- [ ] 실패 테스트: fake probe 가 nvme0n1(NVMe{PercentageUsed:3, AvailableSpare:100, SpareThreshold:10, MediaErrors:0, CriticalWarning:0}, temp 41) → `nvme_percentage_used==3` 등 + SATA/NVMe 혼합 probe 시 두 디바이스 모두 방출 검증.
- [ ] 구현 → PASS → commit `feat(collector): NVMe health mapping for smart tier`

### Task 3: 프로덕션 probe (디스커버리 + anatol)

**Files:** Create `internal/collector/smart_probe_linux.go`, `smart_probe_stub.go`. `go get github.com/anatol/smart.go@latest` (pseudo-version SHA 핀).

- `NewDevProbe(devRoot string) smartProbe` — `<devRoot>` 에서 whole-device 만 글롭: `sd[a-z]+$`(파티션 sda1 제외), `nvme\d+n\d+$`(파티션 nvme0n1p1 제외). 각 디바이스 `smart.Open(path)` → type-switch (`*smart.SataDevice`/`*smart.NVMeDevice`) → smartDevice 로 변환. **열기/읽기 실패한 디바이스는 skip**(로그 없이 — CollectAll 관례 정합) — 특권 부족·USB 브리지 등에서 부분 성공 허용.
- **구현 전 `go doc github.com/anatol/smart.go` 로 실제 API 확인 의무**(태그 없음·API 유동 — ReadGenericAttributes/ReadSMARTData 시그니처·반환 타입을 실제에 맞춤. ATA raw 값 추출: 속성 테이블에서 id→raw). darwin 컴파일 실패 시 linux 태그 + `smart_probe_stub.go`(`//go:build !linux`, 빈 probe 반환) 분리 — macOS 에서 `go build ./...` 유지가 하드 요구.
- 디스커버리 자체는 OS 무관 테스트 가능: 글롭 필터 함수(`isWholeDevice(name string) bool`)를 분리해 단위 테스트(sda ✓ sda1 ✗ nvme0n1 ✓ nvme0n1p1 ✗ loop0 ✗).
- [ ] isWholeDevice 테스트 → 구현 → PASS → `go build ./...`(macOS) green → commit `feat(collector): production SMART probe with device discovery (linux)`

### Task 4: config DevRoot + tier 기반 배선

**Files:** Modify `internal/config/config.go`(+DevRoot, 기본 `/dev`), `config_test.go`(+기본값 검증), `cmd/nodevitals/main.go`.

main.go — tier 로 콜렉터 집합 선택 (기존: core 무조건 등록):
```go
	var reg collector.Registry
	switch cfg.Tier {
	case "smart":
		reg.Add(collector.NewSmart(cfg.Node, collector.NewDevProbe(cfg.DevRoot)))
	default: // "core" (기본)
		reg.Add(collector.NewLoadAvg(cfg.Node, cfg.ProcRoot))
		reg.Add(collector.NewCPU(cfg.Node, cfg.ProcRoot))
		reg.Add(collector.NewMem(cfg.Node, cfg.ProcRoot))
		reg.Add(collector.NewNet(cfg.Node, cfg.ProcRoot))
		reg.Add(collector.NewDisk(cfg.Node, cfg.ProcRoot, cfg.SysRoot))
		reg.Add(collector.NewHwmon(cfg.Node, cfg.SysRoot))
	}
```
- [ ] config 기본값 테스트 → 배선 → 전체 회귀 green → commit `feat(cmd,config): devRoot + tier-selected collector registration`

### Task 5: 차트 smart tier (특권, 기본 disabled)

**Files:** Create `deploy/chart/templates/daemonset-smart.yaml`, `configmap-smart.yaml`. Modify `values.yaml`.

- `{{- if .Values.tiers.smart.enabled }}` 가드(기본 **false** — 특권은 옵트인). M2 설계 §3 securityContext 그대로: `runAsUser:0`, `runAsNonRoot:false`, `allowPrivilegeEscalation:false`, `readOnlyRootFilesystem:true`, `drop:["ALL"]`, `add:["SYS_RAWIO","SYS_ADMIN"]`. `/dev` hostPath **readOnly** 마운트(`devRoot: /host/dev` 로 ConfigMap 렌더... 주의: glob 대상이 `<devRoot>` 이므로 마운트 경로와 일치시킬 것). metrics port 는 core 와 동일 값 재사용(파드 분리라 충돌 없음). 이미지 = 동일 static 이미지(순수 Go — `:v-gpu` 불요).
- values.yaml: `tiers.smart.enabled: false` (이미 존재 — 확인) + smart 전용 기본 룰(비활성 tier 라 룰만 정의):
```yaml
smartRules:
  - { metric: smart_pending_sectors,        device: "", condition: smart_pending_sectors,  severity: critical, threshold: 0, enterFor: 1, exitFor: 1 }
  - { metric: smart_reallocated_sectors,    device: "", condition: smart_reallocated,      severity: warning,  threshold: 0, enterFor: 1, exitFor: 1 }
  - { metric: nvme_percentage_used,         device: "", condition: nvme_wearout,           severity: warning,  threshold: 90, enterFor: 1, exitFor: 1 }
  - { metric: nvme_critical_warning,        device: "", condition: nvme_critical_warning,  severity: critical, threshold: 0, enterFor: 1, exitFor: 1 }
```
  **주의**: 엔진 룰 매칭은 `(Metric, Device)` 정확 일치 — Device 가 동적(sda…)이라 빈 device 룰은 매칭 안 됨! → **Task 5 는 엔진에 device 와일드카드 추가가 선행 필요**: `internal/event/event.go` 매칭을 `st.rule.Device == "" || st.rule.Device == s.Device` 로 확장(빈 = 와일드카드) + per-(rule×device) 상태 분리 필요 여부 검토. **상태 분리가 non-trivial 하면**: 와일드카드 룰 1개가 여러 device 를 하나의 히스테리시스 상태로 뭉개므로 — rule state 를 `map[device]*state` 로 확장하는 작은 리팩터를 이 Task 에 포함(TDD: 두 디바이스가 독립 ENTER 하는 테스트).
- [ ] 엔진 와일드카드+per-device 상태(TDD) → 차트 → `helm template`(smart off=기존 2리소스 / `--set tiers.smart.enabled=true`=+2) + kubeconform Valid → commit `feat(chart,event): smart tier DaemonSet + device-wildcard rules`

### Task 6: 문서 amend + 최종 회귀

- M2 설계 §3 "/metrics 호환" 단락 → 네이티브 네이밍 결정으로 교체(이유: #1 동결 계약과 충돌) + smartctl_exporter → nodevitals 매핑표(위 메트릭표 재사용). README smart tier 상태 갱신(NOTE 블록: SMART tier landed, GPU next). 새 GitHub 이슈용 노트: "delta/surge rule type" (엔진 확장 — 컨트롤러가 push 후 생성).
- [ ] 문서 → `make all` + `go test ./...` + helm/kubeconform 전부 green → commit `docs: SMART tier native naming decision + README status`

## Self-Review
- 스펙 커버: M2 설계 §3 의 라이브러리/권한/속성→이벤트/정직성 전부 매핑. 단 ① smartctl 원명 호환 → 네이티브+매핑표로 **의도적 수정**(#1 계약 우선, Task 6 이 설계문서 amend) ② surge 델타 룰 → 엔진 확장 이슈로 이연 ③ fixture .bin 바이트 블롭 → probe-seam 구조체 fixture 로 **의도적 대체**(anatol 파싱이 ioctl 결합이라 바이트 재생 불가 — 실 디스크 검증은 배포 스모크 항목).
- 타입 일관: smartProbe/smartDevice/NewSmart/NewDevProbe 시그니처 Task 1→3→4 일치. Kind 미설정=gauge(#1 계약).
- 리스크: anatol API confidence:medium → Task 3 구현자가 go doc 실측 후 적응(M2a procfs 패턴). 엔진 와일드카드는 상태 모델 변경이라 Task 5 에서 TDD 필수.
