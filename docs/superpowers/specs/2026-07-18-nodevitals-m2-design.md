# nodevitals M2 — 설계 스펙 (풀 디테일: core 완성 + GPU + SMART)

> **상태** Draft · 2026-07-18 · **repo** <https://github.com/KeiaiLab/nodevitals> · **선행** [M1 설계](2026-07-17-nodevitals-design.md)
> 코드 식별자·스키마·플래그는 영어, 서술은 한국어.

**한눈에** — M1(walking skeleton)이 파이프라인(collect → event → sink → Helm)을 완성했다. M2는 고객이 요구한 **풀 디테일**을 채운다: ① core tier 콜렉터 완성(cpu·mem·disk·net·hwmon) ② **GPU tier**(NVML 메트릭 + XID 이벤트 구독) ③ **SMART tier**(디스크 SMART + NVMe 마모). 세 리서치(NVML·SMART·XID/procfs, 2026-07-18 실측)로 기술 접근을 확정했다.

---

## 1. 결정적 제약 — 이미지가 갈라진다 (M2 최상위 결정)

M1은 `CGO_ENABLED=0` 순수 정적 바이너리(distroless/static, ~22MB)다. M2의 세 tier는 이 제약과 **다르게** 관계한다:

| tier | 의존 | cgo? | 이미지 |
|---|---|---|---|
| **core** | prometheus/procfs (순수 Go) | ✗ | **static** (M1 유지, ~22MB) |
| **smart** | anatol/smart.go (순수 Go ioctl) | ✗ | **static** (M1 유지) |
| **gpu** | NVIDIA/go-nvml | **✓ (dlopen via cgo)** | **glibc** (distroless/cc 또는 base-debian12) |

**근거 (실측 확정)**: go-nvml 의 `pkg/dl` 은 `import "C"` + `dlopen()` 으로 노드의 `libnvidia-ml.so.1` 을 **런타임 로드**한다. 빌드 시 lib 부재는 OK 지만 `import "C"` 때문에 **`CGO_ENABLED=0` 은 빌드 자체가 실패**한다. cgo 를 켜면 glibc 동적 링크가 되고, glibc 의 정적 `dlopen` 은 미지원(go-sqlite3#457 부류)이라 **static 으로 되돌릴 수 없다**. dcgm-exporter·GPUd 도 같은 이유로 `distroless/cc`·`cuda-runtime` 베이스를 쓴다.

### 해법: build tag 로 NVML 격리 → 이미지 2종 (단일 코드베이스 유지)

GPU 콜렉터 코드를 `//go:build gpu` 태그 뒤에 둔다:

```
CGO_ENABLED=0 go build              →  static 바이너리 (core + smart, GPU 제외) → ghcr.io/keiailab/nodevitals:<v>        (distroless/static)
CGO_ENABLED=1 go build -tags gpu    →  glibc  바이너리 (core + smart + gpu)     → ghcr.io/keiailab/nodevitals:<v>-gpu    (distroless/cc)
```

Helm 차트는 **tier 별로 다른 이미지 태그**를 참조한다 — core/smart DaemonSet 은 `:v` (작은 static), gpu DaemonSet 은 `:v-gpu` (glibc). "단일 이미지" 주장은 **"단일 코드베이스 · tier별 이미지 변형"** 으로 정밀화된다. 흔한 경로(core/smart)는 22MB static 이점을 지키고, GPU 만 glibc 비용을 낸다. GPU 없는 클러스터는 `:v-gpu` 를 아예 pull 하지 않는다.

> [!NOTE]
> M1 의 `internal/collector` 인터페이스(`Collect(ctx) ([]Sample, error)`)·event engine·sink·queue·httpapi 는 **전부 재사용**한다. M2 는 콜렉터 구현을 추가하고 tier 별 DaemonSet 을 채울 뿐, 파이프라인 코어는 불변.

---

## 2. Core tier 완성 (M2a) — prometheus/procfs 채택

M1 은 loadavg 하나를 손수 파싱했다. M2a 는 나머지 core 메트릭을 채우되 **`github.com/prometheus/procfs`** (Apache-2.0)로 전환한다.

**전환 근거**: procfs 는 `procfs.NewFS(mountPoint)` / `sysfs.NewFS(path)` 로 **루트 주입**을 1급 지원한다 — nodevitals 의 `procRoot` fixture 테스트 패턴과 정확히 일치(procfs 자체 테스트도 동일 fixture 방식). gopsutil 은 `HOST_PROC` 전역 env 라 `t.Parallel()` 레이스 위험 → 배제. node_exporter 가 procfs 를 쓰므로 파싱 정확성도 검증됨.

| 콜렉터 | 소스 | procfs API | metric |
|---|---|---|---|
| loadavg (M1 이관) | /proc/loadavg | `NewFS(root).LoadAvg()` | load1 |
| cpu-util | /proc/stat 델타 | `NewFS(root).Stat()` → CPUStat | cpu_util_pct (per-cpu + total) |
| mem | /proc/meminfo | `NewFS(root).Meminfo()` | mem_total_bytes, mem_used_bytes, mem_available_bytes, swap_used_bytes |
| net | /proc/net/dev | `NewFS(root).NetDev()` | net_rx/tx_bytes_total, net_rx/tx_errors_total |
| disk | /proc/diskstats | `blockdevice.NewFS(proc,sys).ProcDiskstats()` | disk_read_bytes_total, disk_write_bytes_total, disk_read_ios_total, disk_write_ios_total |
| **hwmon** | /sys/class/hwmon | **손수 파싱** (procfs 미지원) | temp_celsius, fan_rpm (in_volts 는 v0.2 이연) |

- **cpu-util 은 델타**라 이전 표본을 tier 상태로 보유(엔진처럼 순수하진 않음 — 콜렉터 내부 상태). loadavg 콜렉터의 stateless 패턴과 달리 `cpuCollector` 는 직전 `/proc/stat` 스냅샷을 보유하고 첫 tick 은 baseline(이벤트 없음).
- **hwmon 은 무권한**(sysfs read). NVMe 온도도 커널 5.5+ 에서 hwmon 으로 무권한 노출 → core tier 가 디스크 온도 일부를 특권 없이 얻는다.
- core tier 는 M1 이미지(static) 그대로. 새 의존은 prometheus/procfs 하나(cgo 없음).

---

## 3. SMART tier (M2c) — anatol/smart.go, 순수 Go ioctl

**라이브러리**: `github.com/anatol/smart.go` (MIT, 순수 Go, `go 1.26`, 2026-07 활성). SATA(SG_IO ATA passthrough)·SCSI·NVMe(admin passthrough) 커버. 태그 릴리스 없음 → **go.mod 에 커밋 SHA 핀**. smartmontools(GPL) 서브프로세스는 (a) distroless 에 smartctl/libstdc++6 부재 (b) GPL↔Apache 라이선스 충돌 — 이중으로 배제. 순수 Go 라 **static 이미지 유지**(cgo 없음).

**인터페이스** (M1 패턴 계승 — DeviceReader mock):
```
DeviceReader interface {  // 디바이스 1개 read 추상화 (테스트 mock 지점)
    Identify() (Info, error)
    ReadSMART() (raw []byte, error)
}
```
`smart.Open("/dev/sda")` 가 SATA/SCSI/NVMe 자동 판별. 실 파싱/임계 로직은 fixture 바이트 블롭(`testdata/*.bin`, 실 HW 또는 QEMU 로 1회 캡처)에 대해 하드웨어 0대로 테스트.

**권한 (smart tier DaemonSet — 특권)**:
```yaml
securityContext:
  runAsUser: 0            # 필수 — 비root+cap.add 는 k8s#56374 로 무효, KEP-2763 pre-alpha
  runAsNonRoot: false
  allowPrivilegeEscalation: false
  readOnlyRootFilesystem: true
  capabilities:
    drop: ["ALL"]
    add: ["SYS_RAWIO", "SYS_ADMIN"]   # SATA SMART=RAWIO, NVMe admin=ADMIN
volumeMounts:
  - { name: dev, mountPath: /dev, readOnly: true }   # privileged:true 대신 /dev ro
```

**속성 → 이벤트** (Backblaze/Google 실증 속성만):
| 신호 | 소스 | 이벤트 condition |
|---|---|---|
| Reallocated (5) 급증 | ATA SMART | `smart_reallocated_surge` (델타) |
| Reported uncorrectable (187) | ATA SMART | `smart_uncorrectable_detected` |
| Command timeout (188) | ATA SMART | `smart_command_timeout` |
| Pending (197)/Offline uncorrectable (198) | ATA SMART | `smart_pending_sector_surge` |
| NVMe percentage_used ≥ 임계 | NVMe log | `nvme_wearout_warning`/`_critical` |
| NVMe available_spare < threshold | NVMe log | `nvme_spare_exhausted` |
| NVMe media_errors 델타 | NVMe log | `nvme_media_error_detected` |
| NVMe critical_warning 비트 | NVMe log | `nvme_critical_warning` |

**구현 노트 (M2c 실제)**: 현 엔진은 threshold-on-value(§10 M1 엔진)라 "surge(델타)" 룰은 미지원 → SMART 이벤트는 **nonzero-threshold 룰**(`smart_pending_sectors > 0` 등, 대부분의 사전신호를 커버)로 배송한다. 진짜 델타 "surge" 룰타입은 엔진 확장 필요 → 별도 이슈로 이연. device 는 런타임 발견이라 **device-와일드카드 룰**(`device: ""`)로 전 디스크에 적용(per-device 히스테리시스, Task 5a).

> [!IMPORTANT]
> **정직성 (M1 스펙 §11 계승)**: SMART 는 실패의 **23~56% 를 무신호로 놓친다**(Backblaze 76.7%만 사전 신호 / Google FAST'07 56%+ 무신호). 이벤트 문구는 **"위험 상승 신호"** 이지 **"곧 고장"이 아니다**. `will_fail` 류 단정 금지.

**메트릭 네이밍 (native, #1 계약 정합)**: SMART/NVMe 메트릭은 전 메트릭 `nodevitals_hw_` 접두 + 네이티브 이름(`smart_temperature_celsius`·`smart_power_on_hours`·`smart_reallocated_sectors`·`smart_reported_uncorrectable`·`smart_command_timeout`·`smart_pending_sectors`·`smart_offline_uncorrectable`·`nvme_percentage_used`·`nvme_available_spare`·`nvme_available_spare_threshold`·`nvme_media_errors`·`nvme_critical_warning`)로 노출한다. smartctl_exporter 원명(`smartctl_device_*`) 에뮬레이션은 **하지 않는다** — #1에서 동결한 "전 표면 동일 이름 + 단일 접두" 계약이 sink의 접두 우회 특례를 금지하기 때문. 기존 smartctl_exporter 대시보드 이관은 [드롭인이 아니라 마이그레이션 가이드](#) 로 제공한다(메트릭명 대응은 위 목록 ↔ smartctl_device_* 매핑). 방치된 smartctl_exporter(15개월 무릴리스) 대체 가치는 유지.

---

## 4. GPU tier (M2b) — go-nvml, XID 이벤트 구독

**라이브러리**: `github.com/NVIDIA/go-nvml` (NVIDIA 공식, **Apache-2.0**, v0.13.x 2026-07 활성). 유일 실질 옵션(mindprince/gonvml 등 사장). cgo dlopen → **glibc 이미지(`:v-gpu`)**.

### 4.1 GPU 메트릭 (polled gauge)
`nvml.Init()` → `DeviceGetCount()` → 디바이스별: 사용률·VRAM(used/total)·온도·전력·클럭·ECC 카운터. `nvmlDeviceGetCurrentClocksThrottleReasons` 비트마스크로 스로틀 사유(폴링, 이벤트 API 없음).

### 4.2 XID 이벤트 구독 (유일한 이벤트 소스)
NVML 은 **XID 폴링 API 가 없다** — `EventSetWait` 블로킹 구독이 유일(M1 스펙 §10 정합). NVIDIA k8s-device-plugin 검증 패턴:
```go
es, _ := nvmlLib.EventSetCreate()
mask := nvml.EventTypeXidCriticalError | nvml.EventTypeDoubleBitEccError | nvml.EventTypeSingleBitEccError
dev.RegisterEvents(mask & supported, es)
go func() {                     // 별도 goroutine 블로킹 구독
  for {
    e, ret := es.Wait(5000)     // 5s 타임아웃
    if ret == nvml.ERROR_TIMEOUT { continue }
    xid := e.EventData          // XID 번호, e.Device = 해당 GPU
    // → event engine 으로 채널 전달, XID→severity 분류
  }
}()
```

### 4.3 XID → severity 분류표 (ship-ready, NVIDIA XID r590 실측)
| XID | 의미 | severity |
|---|---|---|
| 13/31/43 | 앱 예외·페이지폴트·SW 유발 hang | **benign** (앱 재시작; 반복+교차앱이면 승급) |
| 48 | Double-bit ECC (uncorrectable) | **critical** (GPU 리셋/drain) |
| 63 | Row-remap pending | warning (예약 리셋) |
| 64 | Row-remap **실패** | **critical** |
| 74 | NVLink 오류 | warning→critical(지속 시) |
| 79 | GPU fell off the bus (PCIe) | **critical** (노드 drain+reboot) |
| 92 | 높은 single-bit ECC rate | warning |
| 94 | Contained ECC | warning (해당 pod 재시작) |
| 95 | Uncontained ECC | **critical** |
| 119/120 | GSP RPC timeout / core 오류 | **critical** (리셋; 펌웨어 확인) |
→ `gpu_xid_error` 이벤트에 `detail.xid` + 분류 severity. 표는 `internal/gpu/xid.go` 상수 맵으로 코드화(문자열은 raw PDF 재확인 후 하드코딩 — confidence 주석).

### 4.4 GPU 배포 + 테스트
- **DaemonSet**: `nodeSelector: {nvidia.com/gpu.present: "true"}`, `tolerations: [{key: nvidia.com/gpu, operator: Exists, effect: NoSchedule}]`, env `NVIDIA_VISIBLE_DEVICES=all` + `NVIDIA_DRIVER_CAPABILITIES=utility`, glibc 이미지. **`resources.limits[nvidia.com/gpu]` 요청 안 함**(스케줄러블 GPU 소모 아닌 관리 컨테이너). nvidia-container-toolkit 이 lib+디바이스 주입(기본 가정). 토킷 없는 클러스터 = hostPath 폴백(문서화).
- **테스트**: go-nvml **1급 mock**(`pkg/nvml/mock`, A100/H100/H200/B200 시뮬 fixture) → `nvml.Interface` mock 주입, 하드웨어 0대 단위 테스트. 단 **실 GPU 스모크 1회**는 M2b ship 전 필수(driver reset/XID 79 시 EventSetWait·goroutine 취소는 mock 으로 미검증).

---

## 5. 테스트 전략 (하드웨어 0대 유지 — M1 원칙 계승)

| tier | 하드웨어 0대 방법 |
|---|---|
| core | procfs `NewFS(fixtureRoot)` + `testdata/proc·sys` fixture (procfs 자체 방식과 동일) |
| smart | `DeviceReader` mock + 캡처 SMART/NVMe 바이트 블롭 `testdata/*.bin` |
| gpu | go-nvml `mock.Interface` + 시뮬 GPU fixture; XID 는 합성 이벤트 주입 |

⇒ CI 는 전부 하드웨어 0대. **실 검증 2건만 별도**: GPU 스모크(driver reset 경로) + SMART 실디스크/QEMU 1회(fixture 캡처용).

---

## 6. 분해 + 빌드 순서 (M2a → M2c → M2b 권장)

| 계획 | tier | 이미지 | 위험 | 고객 가치 |
|---|---|---|---|---|
| **M2a** | core 완성 (procfs: cpu·mem·net·disk·hwmon) | static (불변) | 낮음 (cgo 없음, procfs 검증됨) | 즉시 — 실 노드 메트릭 |
| **M2c** | SMART tier (anatol + 특권 DaemonSet) | static (불변) | 중 (ioctl·특권·fixture 캡처) | 고객 디스크 요구 |
| **M2b** | GPU tier (go-nvml + XID + 이미지 fork) | **glibc 신규** | **높음** (cgo·이미지 fork·실 GPU 필요) | 고객 GPU 요구 (강조점) |

**권장 순서 = M2a → M2c → M2b**: 위험 오름차순 + M2a/M2c 는 static 이미지 불변(파급 최소), M2b 만 이미지 fork 라 마지막에 격리. **단 고객이 GPU 를 최우선했다면** M2b 를 앞당길 수 있으나, cgo/이미지-fork/실-GPU 의존이 크므로 M2a 로 procfs 패턴을 먼저 다진 뒤 진입 권장.

각 계획은 M1 처럼 TDD·서브에이전트 실행 → whole-branch 리뷰 → 머지.

---

## 7. 미확정 (사용자/고객 결정 필요)

- [ ] **고객 GPU 클러스터 셋업** — nvidia-container-toolkit / GPU Operator 존재? (toolkit 주입 vs hostPath 폴백 결정. §2.1① 고객 사실)
- [ ] **빌드 순서** — 위험 오름차순(M2a→c→b) vs 고객 GPU 최우선(M2b 앞당김)?
- [ ] **이미지 태그 전략 확정** — `:v` (static core+smart) + `:v-gpu` (glibc) 2-변형 승인?
- [ ] **arm64 × GPU** — GPU tier glibc 이미지도 arm64(GH200/Jetson) 빌드? (dcgm 은 함. cgo 크로스컴파일 복잡도 증가)

## 8. 리스크

| 리스크 | 완화 |
|---|---|
| GPU tier 이미지 fork 가 "단일 에이전트" 서사 약화 | "단일 코드베이스·tier별 변형" 으로 정직 재정의. core/smart 는 여전히 1 static |
| 실 GPU 없이 XID/reset 경로 미검증 | go-nvml mock 으로 로직 검증 + ship 전 실 GPU 스모크 1회 의무 |
| anatol/smart.go 태그 릴리스 없음 (API 유동) | SHA 핀 + 구현 시 HEAD API 재확인 |
| CAP_SYS_ADMIN(NVMe)은 near-root — 특권 tier 채택 저항 | tier 분리라 core/smart-core 는 무특권. SMART 는 옵트인(`tiers.smart.enabled`) |
| cgo cross-compile(arm64 GPU) 복잡 | v0.2 는 amd64 GPU 우선, arm64 GPU 는 후속(미확정 #4) |
| XID 문자열/severity 일부 confidence:low | raw XID PDF 재확인 후 코드 상수화, confidence 주석 |

## 9. 참조 (실측 근거, 2026-07-18)
- NVIDIA/go-nvml (Apache-2.0, cgo dlopen, `pkg/nvml/mock`) · k8s-device-plugin `internal/rm/health.go`(EventSetWait 패턴)
- anatol/smart.go (MIT, SATA/SCSI/NVMe, `anatol/vmtest`) · prometheus-community/smartctl_exporter (metric 호환)
- prometheus/procfs (Apache-2.0, `NewFS(root)`·`sysfs.NewFS`) — hwmon 미지원 확인
- NVIDIA XID Errors r590 (docs.nvidia.com/deploy/xid-errors) · k8s#56374 · KEP-2763 (pre-alpha)
- Backblaze SMART stats · Google FAST'07 (SMART 예측력 한계)
- 선행: [M1 설계](2026-07-17-nodevitals-design.md)
