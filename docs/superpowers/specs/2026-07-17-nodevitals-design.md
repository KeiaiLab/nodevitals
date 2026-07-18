# nodevitals — 설계 스펙 (v0.1)

> 상태: Draft · 작성 2026-07-17 · 브레인스토밍 산출물
> 코드 식별자·스키마 필드·CLI 플래그는 영어, 서술은 한국어.

---

## 1. Context (왜 만드는가)

**고객사 요구가 기원이다.** 확정 고객이 Kubernetes 노드의 하드웨어 상태 실측을 필요로 한다 — GPU(온도·사용량·전력), 디스크, CPU/RAM/NIC/센서까지 **모든 하드웨어**. 이 데이터를 **자체 백엔드로 받고**, 하드웨어 **장애·임계를 이벤트로 알림**받길 원한다.

현재 이 요구를 충족하려면 **서비스 3개를 엮어야 한다**:
- `node_exporter` — 코어 메트릭 (SMART 콜렉터 0, GPU 는 dcgm 에 명시 위임)
- `dcgm-exporter` — NVIDIA GPU (무겁고 NVIDIA 전용)
- `smartctl_exporter` — 디스크 SMART (15개월 무릴리스, 차트가 `privileged:true` 하드코딩)

각각 별도 DaemonSet · 별도 설정 · 별도 배포 · Prometheus scrape 기반이라 **이벤트 푸시가 없다**. 세 개를 배선하는 운영 부담이 고객의 실제 고통이다.

**nodevitals 는 이 3-서비스 배선을 단일 Go 에이전트 + 단일 Helm 차트로 대체**하고, scrape 폴링 대신 **이벤트 우선(event-first)** 모델로 하드웨어 상태 변화를 고객 백엔드에 즉시 푸시한다.

### 전략 프레이밍 (중요)

두 차례의 다중 에이전트 리서치(총 40+ 에이전트, 520만 토큰)가 이 카테고리에 **방어 가능한 OSS 해자가 없음**을 실측 확정했다 — beszel(23.5k★)·netdata·node_exporter·GPUd 가 표면을 덮었고, 통합·심층·이벤트 웹훅 모두 부분적으로 선점돼 있다.

그러나 이 판정은 **"투기적 OSS 채택"** 기준이다. nodevitals 는 **확정 고객이 자금을 대는 build 를 OSS 로 공개**하는 것이므로 계산이 다르다:
- **1차 가치 = 고객 요구 충족** (별점 0개여도 전달됨 — 바닥값 확실)
- "강자가 1 PR 로 흡수" = 무의미 (고객은 지금 필요, 재사용 IP 확보)
- **OSS 공개 = 평판 보너스** (modest 채택 = 현실 천장, 프로젝트 성패와 무관)

⇒ **설계 원칙: 차별화 전쟁용 과설계 금지. 고객 요구를 가장 깨끗이·완성도 높게 푸는 통합 수집기.**

---

## 2. Goals / Non-Goals

### Goals (v0.1)
- 단일 Go 바이너리로 코어 + NVIDIA GPU + 디스크 SMART 를 수집
- 전달 표면: ① 고객 백엔드 push(웹훅) ② 하드웨어 이벤트 웹훅 — 이상 고객 1순위. ③ Prometheus `/metrics`(OSS 보너스) ④ REST 스냅샷(디버깅 보조)
- 상태전이(ENTER/EXIT) 이벤트 엔진 — gauge 가 아닌 이벤트 우선
- 단일 Helm 차트가 권한 tier 별로 1~3 DaemonSet 렌더
- 하드웨어 0대에서 결정론 개발·테스트 (fixture + mock + replay)
- amd64 + arm64 이미지, ghcr.io 퍼블리시

### Non-Goals (v0.1 — 의도적 제외, YAGNI)
- **턴키 대시보드/UI** — 고객이 자체 UI 보유 (beszel/netdata 영역, 재발명 금지)
- **중앙 collector/집계 서버** — v0.1 은 에이전트 직접 push. 단 와이어포맷을 동일하게 설계해 후일 무변경 삽입 (§7)
- **크로스노드 상관** — 정의상 노드에서 불가 (상위 계층 필요, v0.1 밖)
- **AMD ROCm / Intel GPU** — NVIDIA-first, 로드맵 (§13 가정)
- **디스크 큐 영속화** — 유계 융합 메모리 큐로 충분 (§8, 근거 있음)
- **자동 remediation** (cordon/drain 등) — 이벤트 전달까지만, 대응은 고객 몫

---

## 3. 성공 기준

- **고객 (1차)**: 고객 클러스터에서 단일 차트 설치로 GPU+디스크+코어 텔레메트리가 고객 백엔드에 도달하고, GPU/디스크 장애가 이벤트로 푸시됨을 실측 확인
- **기술 (DoD)**: 하드웨어 0대 CI 에서 전 콜렉터·이벤트 엔진·sink 결정론 테스트 PASS. amd64+arm64 이미지 빌드. `helm template | kubeconform` 통과
- **OSS (보너스)**: README + 벤치마크 + 3-서비스 대체 마이그레이션 가이드. modest 채택 기대

---

## 4. 아키텍처 개요 — Tiered Single-Agent

```
                    ┌──────────────────────── nodevitals (단일 Go 코드베이스/이미지) ────────────────────────┐
                    │                                                                                        │
  ┌── DaemonSet: tier=core ──┐   ┌── DaemonSet: tier=gpu ──┐   ┌── DaemonSet: tier=smart ──┐
  │ (무특권, baseline-hardened)│   │ (NVIDIA 디바이스 접근)  │   │ (특권 runAsUser:0)        │
  │  collectors:             │   │  collectors:            │   │  collectors:              │
  │   cpu·mem·disk-usage     │   │   nvml-metrics          │   │   smart·nvme-wear         │
  │   net·hwmon(sensors)     │   │   nvml-events(XID 구독) │   │                           │
  └──────────┬───────────────┘   └───────────┬─────────────┘   └────────────┬──────────────┘
             │                                │                              │
             └────────────────┬───────────────┴──────────────────────────────┘
                              ▼
                      ┌─────────────────┐
                      │  event engine   │  상태전이(ENTER/EXIT) + 히스테리시스/디바운스
                      │  (순수 함수)    │  임계 룰 평가 → 이벤트 생성
                      └───┬─────┬─────┬──┘
                          │     │     │
              ┌───────────┘     │     └────────────┐
              ▼                 ▼                  ▼
      ┌──────────────┐  ┌──────────────┐  ┌──────────────┐
      │ sink:webhook │  │ sink:rest    │  │ sink:metrics │
      │ (고객백엔드) │  │ (스냅샷조회) │  │ (Prometheus) │
      │ CloudEvents  │  │ GET /v1/state│  │ /metrics     │
      │ +HMAC 서명   │  │              │  │              │
      └──────────────┘  └──────────────┘  └──────────────┘
```

**핵심 결정 근거**: PSA(Pod Security Admission)는 **파드 단위** 평가다. SMART 는 커널상 `runAsUser:0 + CAP_SYS_RAWIO`(SATA) / `CAP_SYS_ADMIN`(NVMe) 를 요구하는데(비-root + `capabilities.add` 는 k8s#56374 로 무효, KEP-2763 ambient caps 는 2026-07 미구현), 코어와 SMART 를 한 파드에 묶으면 **전체가 특권으로 격상**돼 무특권 이점이 소각된다. 따라서 tier 별 DaemonSet 분리가 불가피하다 — 단 **단일 코드베이스·이미지·config 스키마·Helm 차트**가 `values.tiers.*` 로 이를 렌더링해 "통합" 을 유지한다.

---

## 5. 컴포넌트 (각각 단일 목적 + 명확한 인터페이스)

| 컴포넌트 | 목적 | 인터페이스 / 의존 |
|---|---|---|
| **collector** (interface) | 하드웨어 1개 도메인 표본 수집 | `Collect(ctx) ([]Sample, error)` — 순수 read. 도메인별 구현: cpu/mem/disk/net/hwmon/nvml-metrics/nvml-events/smart/nvme-wear |
| **registry** | 활성 tier 의 collector 집합 관리 | config 로 활성화. 미가용 하드웨어(예: GPU 0대)는 graceful skip |
| **event engine** | Sample → 상태전이 이벤트 | **순수 함수**: `Evaluate(prev, cur, rules) []Event`. 히스테리시스/디바운스 상태 보유. 하드웨어 0대 테스트의 핵심 |
| **rule set** | 임계·상태전이 룰 정의 | YAML config. 심각도·ENTER/EXIT·디바운스 창 |
| **sink** (interface) | 이벤트/메트릭 1개 목적지 전달 | `Emit(ctx, []Event/Sample) error`. 구현: webhook/rest/metrics |
| **delivery queue** | sink 앞단 유계 큐 + 재시도 | (node,device,metric) 융합, Full Jitter 백오프, 재시도예산 |
| **agent core** | 수명주기·스케줄·config·헬스 | collector 폴링 주기 + nvml-events 블로킹 구독 스레드. self-metrics 노출 |
| **config loader** | 단일 config 스키마 | YAML. tier·collectors·rules·sinks·간격 |

---

## 6. 데이터 모델

### Sample (수집 표본)
```
Sample {
  node      string        # 노드명
  tier      string        # core|gpu|smart
  device    string        # gpu0, /dev/nvme0n1, cpu, ...
  metric    string        # temperature_c, utilization_pct, power_w, wear_pct, ...
  value     float64
  labels    map[string]string
  timestamp time.Time
}
```

### Event (상태전이)
```
Event {
  id         string        # 배송 멱등 키 = fingerprint(node·tier·device·condition) + phase + seq → 전이마다 고유 (Webhook-Id/CloudEvents id). ENTER/EXIT 가 같은 id 를 갖지 않아야 수신측 dedup 이 EXIT 를 안 버림
  node       string
  tier       string
  device     string
  condition  string        # gpu_thermal_throttle, xid_error, smart_reallocated_surge, nvme_wearout, ...
  phase      string        # ENTER | EXIT
  severity   string        # info | warning | critical
  seq        uint64        # 룰별 단조 증가 (ENTER→EXIT 짝·갭 복구용). fingerprint = condition 그룹핑 키(불변)
  started_at time.Time     # ENTER 시각
  ended_at   time.Time     # EXIT 시각 (EXIT phase 에만)
  detail     map[string]any # XID 코드, SMART 속성값, 온도 등
}
```

### 와이어 포맷 — CloudEvents 1.0 (structured 모드)
- `structured` 모드 채택 (binary 는 헤더 8KiB 한계로 SMART 덤프 불가)
- `type`: `com.nodevitals.hw.event.v1` / `com.nodevitals.hw.sample.v1`
- **Standard Webhooks** 서명: `webhook-id` / `webhook-timestamp` / `webhook-signature` (HMAC-SHA256, 다중서명 로테이션 지원)
- 스키마 버전 명시 (`v1`) — 진화 대비

---

## 7. 전달 표면 (3종)

| 표면 | 우선순위 | 형식 | 설명 |
|---|---|---|---|
| **webhook (push)** | 1순위 (고객) | CloudEvents 1.0 + HMAC | 고객 백엔드로 이벤트/샘플 push. 다중 endpoint. **1순위 sink — config 에서 고객 endpoint 로 구성·활성** (고객 우선 전략) |
| **event webhook** | 1순위 (고객) | 위와 동일 (이벤트만) | 임계·장애 상태전이만. 룰 매칭 이벤트를 push |
| **REST snapshot** | 2순위 | JSON | `GET /v1/state` 현재 상태 스냅샷 (디버깅·온디맨드) |
| **Prometheus** | 보너스 (OSS) | exposition | `/metrics` — 기존 스택 점진 도입. `smartctl_device_*` 등 호환 메트릭명으로 3-서비스 대체 쐐기 |

**동일 와이어포맷 원칙**: 에이전트→webhook 과 에이전트→(미래)collector 를 같은 CloudEvents 포맷으로 두면, 플릿이 커져 중앙 collector tier 가 필요해질 때 **에이전트 코드 변경 0** 으로 삽입된다.

---

## 8. 신뢰성 (전달 실패 모드 대응)

리서치가 경고한 노드 직접 push 의 실패 모드를 v0.1 부터 설계에 반영:

- **디스크 큐 금지** — 노드 영구 사망 시 큐 고립, 재부팅 시 stale 오경보, SMART 감시 디스크에 쓰는 자기모순. 대신:
- **유계 융합 큐** — `(node, device, metric)` 키로 최신값 융합 → 하드웨어 개수로 자연 유계 (무한 성장 불가)
- **치명 이벤트 전용 레인** — critical 이벤트는 융합 금지 (유실 방지)
- **드롭 회계** — `nodevitals_delivery_dropped_total` + 갭 마커(수신측이 유실 인지)
- **Full Jitter 백오프** + Google SRE 재시도 예산(10%) + adaptive throttling
- **노드별 랜덤 위상 오프셋** — fan-out 동시 발화 방지 (노드 1000개 thundering herd 회피)
- **엔드포인트가 상한** — Slack 류(채널당 초당 1건)는 고객 백엔드가 아니라 별도 sink 로 취급, rate 제어 문서화

---

## 9. 수집 상세 (도메인별)

### core tier (무특권 — `/proc`·`/sys` read)
- **CPU**: 사용률·load·주파수·throttle (`/proc/stat`, `/sys/devices/system/cpu`)
- **Memory**: 사용량·swap (`/proc/meminfo`)
- **Disk usage**: 용량·IO (`/proc/diskstats`) — ⚠️ 파일시스템 용량은 hostPath 필요할 수 있음 (마운트 전략 §12)
- **Network**: NIC 통계·에러 (`/proc/net/dev`, ethtool 는 GPL C 확장 금지 → netlink 직접)
- **Sensors/hwmon**: 온도·팬·전압 (`/sys/class/hwmon`) — NVMe 온도는 커널 5.5+ hwmon 으로 **무권한** 획득 가능

### gpu tier (NVIDIA 디바이스 접근)
- **nvml-metrics**: 사용률·VRAM·온도·전력·클럭·ECC (`nvidia-ml-py` 상응 Go 바인딩 또는 cgo NVML)
- **nvml-events**: **XID 이벤트 구독** — `nvmlEventSetWait_v2` 블로킹 구독 (⚠️ NVML 에 XID 폴링 API 없음. 이벤트 구독이 유일한 정답. dcgm gauge 방식은 복구 미표현·Xid62 누락 = 구조적 결함, NVIDIA/dcgm-exporter#500). 별도 goroutine 블로킹 구독 → 채널로 event engine 전달
- 보조: `dmesg`/kmsg XID 파싱(문서화된 결정론 포맷) — NVML 이벤트 보강 + 테스트 리플레이 소스

### smart tier (특권 `runAsUser:0` + `CAP_SYS_RAWIO`/`CAP_SYS_ADMIN`)
- **SMART**: `anatol/smart.go` (MIT, 순수 Go, no cgo, SATA/SCSI/NVMe) — smartctl 서브프로세스 불필요 (distroless libstdc++6 부재·GPL 회피). ⚠️ smartmontools drivedb.h(20년 벤더 quirk DB) 부재 → 메트릭명은 `smartctl_device_*` 호환하되 값 차이 가능성 문서화
- **NVMe wear**: `DEVICE_LIFE_TIME_EST` / percentage used
- ⚠️ SMART 예측력 한계 명시: Backblaze 실패 드라이브 23% 무신호 / Google FAST'07 실패의 36% 핵심변수 0 → "신호 부재 ≠ 건강" 을 이벤트 detail 에 반영, `will_fail` 단정 금지

---

## 10. 이벤트 엔진 (가치 있는 이벤트만)

단순 CPU 임계는 Prometheus 도 잘한다. nodevitals 만이 낼 수 있는 **저수준 하드웨어 상태전이**에 집중:

| condition | 트리거 | severity |
|---|---|---|
| `gpu_xid_error` | XID 이벤트 (코드별 분류 — 복구가능/치명) | XID 코드 의존 |
| `gpu_thermal_throttle` | throttle 사유 비트 ENTER/EXIT | warning |
| `gpu_ecc_surge` | ECC 정정 급증 | warning→critical |
| `gpu_power_cap` | 전력 임계 지속 | warning |
| `smart_reallocated_surge` | 예비섹터 재할당 급증 | critical |
| `nvme_wearout` | 마모도 임계 | warning→critical |
| `disk_temp` / `gpu_temp` | 온도 임계 (히스테리시스) | warning |

- **상태전이 시맨틱**: ENTER(진입) + EXIT(복구) + `seq` 단조증가 + `started_at`/`ended_at` — gauge 가 원리적으로 못 내는 것
- **히스테리시스/디바운스**: 플래핑 억제 (예: 임계 초과 후 N초 지속 시 ENTER, 하회 후 M초 지속 시 EXIT)
- **멱등 fingerprint**: 중복 제거·수신측 dedup

---

## 11. 보안 모델

- **tier 별 최소 권한**: core=무특권(hostNetwork/hostPID 불필요 — 이게 node_exporter 차트 기본값 대비 실측 우위) / gpu=디바이스 접근 / smart=특권 격리
- SMART tier: `runAsUser:0` + `drop:[ALL]` + `add:[SYS_RAWIO, SYS_ADMIN]` + `readOnlyRootFilesystem` + `/dev` 전체 대신 특정 블록 디바이스 노드만
- **정직한 표기**: "privileged 없는 SMART" 는 **거짓** (PSA baseline 허용 캡 13종에 SYS_RAWIO 없음). core tier 는 **"PSA restricted 통과" 가 아니라 "baseline-hardened"** 로 표기 (2026-07-18 whole-branch 리뷰 확정) — core DaemonSet 이 `hostPath: /proc` 를 마운트하는데 PSS **restricted** 프로파일의 Volume Types 컨트롤은 hostPath 를 금지하므로 restricted 네임스페이스는 이 파드를 거부한다. 즉 drop-ALL·readOnlyRootFS·no-host-namespace 로 restricted 에 *근접*하나 hostPath 때문에 restricted 자체는 아니다. 카피는 "baseline 초과 하드닝" 으로만.
- 시크릿(webhook HMAC 키): k8s Secret 마운트, 평문 금지

---

## 12. 패키징

- **언어/빌드**: Go (버전은 구현 시 최신 stable). `CGO_ENABLED=0` 우선 — 단 NVML 이 cgo 요구 시 gpu tier 만 cgo 빌드 분리 검토
- **이미지**: `FROM scratch` 또는 `gcr.io/distroless/static` (Go 단일 정적 바이너리 → ~15-30MB, 기존 Python distroless 208MB 대체). **arm64 + amd64** (ADR 예외)
- **현 Dockerfile 교체**: 기존 Python distroless Dockerfile(검증됐으나 Python 기반) → Go 멀티스테이지 빌드로 교체
- **Helm 차트**: `deploy/chart/` — `values.tiers.{core,gpu,smart}.enabled` 로 1~3 DaemonSet 렌더. ArtifactHub 등록. `helm template | kubeconform` 게이트
- **레지스트리**: ghcr.io (GitHub canonical). cosign 서명 + SBOM (OSS 신뢰)
- **라이선스**: **Apache-2.0** (node_exporter 정합 + 특허 grant). AGPL 배제

---

## 13. 하드웨어 0대 테스트 전략 (1인 개발 생존 조건)

| 대상 | 테스트 방법 |
|---|---|
| core collectors | **fixture-root**: 합성 `/proc`·`/sys` 트리를 테스트 픽스처로 주입 |
| nvml-metrics/events | **NVML mock**: 인터페이스 추상화 → mock 구현으로 XID·온도·전력 시나리오 재현 |
| GPU XID 이벤트 | **kmsg 리플레이**: 문서화된 XID dmesg 포맷을 합성 로그로 리플레이 |
| smart | **디바이스 ioctl mock** / 캡처된 SMART 응답 픽스처 |
| event engine | **순수 함수 단위 테스트**: prev/cur Sample → 기대 Event (하드웨어 무관) |
| sink | **로컬 httptest 서버**: webhook 수신·HMAC 검증·재시도·백오프 결정론 재현 |
| delivery queue | 융합·드롭·백오프 결정론 테스트 (deterministic clock, sleep 금지) |

⇒ CI 는 하드웨어 0대에서 전 로직 커버. 실 GPU/디스크 검증은 고객 클러스터 또는 개발자 로컬(보유 시) 스모크로 별도.

---

## 14. v0.1 범위 / 단계

**v0.1 (풀스코프 — 고객 '모든 하드웨어')**: core + gpu + smart 3 tier + webhook/REST/metrics 3 sink + 이벤트 엔진 + Helm 차트(amd64+arm64) + 하드웨어 0대 테스트. 예상 공수 12-16주(1인).

**의도적 v0.2+ 이연**: 중앙 collector tier / AMD ROCm·Intel GPU / 크로스노드 상관 / 자동 remediation / 플래시 쓰기예산.

**구현 순서 제안** (writing-plans 에서 확정):
1. Python 스캐폴드 제거 + Go 모듈 초기화 + Dockerfile 교체 + ADR(arm64 예외) + LICENSE(Apache-2.0)
2. collector 인터페이스 + core tier + fixture-root 테스트
3. event engine (순수 함수) + rule set + 단위 테스트
4. sink 인터페이스 + webhook(CloudEvents+HMAC) + delivery queue + httptest
5. REST snapshot + /metrics
6. gpu tier (nvml-metrics + nvml-events 구독 + mock)
7. smart tier (anatol/smart.go + 특권 DaemonSet + mock)
8. Helm 차트(tier 렌더) + 이미지(멀티아치) + CI(하드웨어0대) + README·마이그레이션 가이드

---

## 15. 가정 (명시)

- **GPU 벤더 = NVIDIA-first**. AMD ROCm/Intel 은 로드맵. (고객이 특정 벤더 명시 시 조정)
- **플릿 = 에이전트 직접 push (v0.1)**. 규모 성장 시 collector tier — 동일 와이어포맷이라 무변경 삽입.
- **고객 백엔드가 CloudEvents+HMAC 수신 가능**. (고객 수신 스펙 확인 필요 — 구현 전 확정)
- **이름 nodevitals** 의 최종 도메인·USPTO 상표는 repo 생성 직전 재확인.

---

## 16. 리스크 (정직 — 워크플로 실측)

| 리스크 | 완화 |
|---|---|
| OSS 채택 천장 낮음 (해자 얕음) | 고객 우선 프레이밍 — 채택은 보너스. 바닥값(고객 전달)이 성패 결정 |
| SMART tier 특권이 일부 클러스터에서 거부 | tier 분리 배포 — core+gpu 만으로도 유용. 고객은 특권 수용 확인됨 |
| NVML XID 를 실 GPU 없이 완전 검증 불가 | mock + kmsg 리플레이로 로직 검증, 실 검증은 고객/로컬 스모크 |
| SMART drivedb 부재로 값 불일치 | 메트릭명 호환 + 값 차이 문서화, "드롭인 대체" 과장 금지 |
| 강자(beszel/GPUd)가 유사 기능 흡수 | 고객 build 라 무관. OSS 는 흡수돼도 고객 가치 불변 |
| Go NVML 바인딩 성숙도 | 구현 시 Context7 로 최신 바인딩 조사 (공식 nvidia Go 바인딩 vs 서드파티) |

---

## 17. 참조 (실측 근거)

- NVIDIA/dcgm-exporter#500 + NVIDIA/DCGM#235 (XID gauge 복구 미표현·Xid62 누락)
- k8s#56374 (비-root + capabilities.add 무효), KEP-2763 (ambient caps 미구현)
- `anatol/smart.go` (MIT, no-cgo SATA/SCSI/NVMe)
- Standard Webhooks 스펙 / CloudEvents 1.0
- Backblaze drive stats / Google FAST'07 (SMART 예측력 한계)
- 브레인스토밍 플랜: `~/.claude/plans/witty-painting-dream.md`
- 워크플로 저널: `wf_3cd2219a-fb2` (설계 3안 채점), `wf_33f3c559-aed` (공백 사냥 + 네이밍)
