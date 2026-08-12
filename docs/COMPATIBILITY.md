# nodevitals — 서비스 전수 호환성 및 연동 명세서 (Compatibility Matrix)

> 저장소: [`github.com/KeiaiLab/nodevitals`](https://github.com/KeiaiLab/nodevitals)  
> 기준 버전: `v0.8.5` (Chart v0.8.6)  
> 최종 검증 일시: 2026년 8월 12일  

본 문서는 `nodevitals`가 연동되는 주요 인프라 서비스, 관측 플랫폼, GPU 오퍼레이터, 가상머신(VM) 환경 간의 명시적 호환성 계약(Compatibility Contract)과 실측 검증 결과를 제공합니다.

---

## 1. 전수 호환성 매트릭스 (Full Service Compatibility Matrix)

| 연동 대상 서비스 / 솔루션 | 호환성 상태 | 연동 메커니즘 & 수집 방식 | 비고 / 주요 구성 |
|---|---|---|---|
| **NVIDIA GPU-Operator** | **100% 호환 (DCGM 대체)** | `dcgmCompat.enabled: true` | `dcgmExporter.enabled: false` 설정 후 `DCGM_FI_*` 18개 메트릭 승계 |
| **VictoriaMetrics (vmagent)** | **100% 호환 (자동 탐지)** | Pod Annotation (`prometheus.io/scrape: "true"`) | `vmagent` kubernetes-pods 잡 자동 수집 (`port: 9847`) |
| **VictoriaMetrics (vmsingle/cluster)** | **100% 호환** | TSDB Scrape & Remote Write | Prometheus TSDB 1.0 표준 데이터 100% 수용 |
| **Prometheus Operator / Alertmanager** | **100% 호환** | `/metrics` + Service/PodMonitor | `node_*`, `DCGM_FI_*`, `smartctl_*` 기존 알림 룰 그대로 동작 |
| **Grafana Dashboard Stack** | **100% 호환** | PromQL 드롭인 쿼리 | 기존 `node_exporter`, `dcgm`, `smartctl` 전용 대시보드 변경 0 |
| **Linux Standalone VM / 베어메탈** | **100% 호환** | `systemd` 데몬 / `nodevitals -config` | K8s 없이 호스트 OS 단독 실행 (`/etc/nodevitals/config.yaml`) |
| **KubeVirt / VM 가상화 노드** | **100% 호환** | K8s DaemonSet 또는 VM 헬퍼 데몬 | KubeVirt 워커 노드 및 가상머신 내부 수집 지원 |
| **Pod Security Admission (PSA)** | **100% 호환 (Tier별 분리)** | Tiered Single-Agent | GPU Tier: Restricted 호환 / Core&Smart: Privileged 안내 |

---

## 2. 세부 서비스별 연동 계약 및 가이드

### 2.1 NVIDIA `gpu-operator` 연동
`gpu-operator` 환경에서 기존 `dcgm-exporter` 팟을 은퇴시키고 `nodevitals`로 대체하는 방법입니다.

- **`gpu-operator` 설정 (`values.yaml`)**:
  ```yaml
  dcgmExporter:
    enabled: false  # dcgm-exporter 파드 기동 중단 (노드당 150MB+ RSS 절감)
  ```
- **`nodevitals` 설정 (`values.yaml`)**:
  ```yaml
  tiers:
    gpu:
      enabled: true
      runtimeClassName: nvidia  # NVIDIA Container Toolkit 연동 (libnvidia-ml.so 주입)
  dcgmCompat:
    enabled: true  # DCGM_FI_* 18개 메트릭 드롭인 방출
  ```
- **메트릭 정합성 검증**:
  - `DCGM_FI_DEV_GPU_UTIL`, `DCGM_FI_DEV_FB_USED`, `DCGM_FI_DEV_GPU_TEMP` 등 18개 메트릭이 기존과 동일한 라벨(`gpu`, `UUID`, `pci_bus_id`, `device`, `modelName`)로 제공됩니다.

### 2.2 VictoriaMetrics (`vmagent`) 연동
`keiailab-platform`과 같이 Prometheus Operator CRD 대신 `vmagent` 정적 수집 스택을 사용하는 환경의 호환성입니다.

- **자동 발견 어노테이션 (Auto-Discovery Pod Annotations)**:
  `nodevitals` 파드 템플릿에 아래 어노테이션이 기본 렌더링됩니다:
  ```yaml
  metadata:
    annotations:
      prometheus.io/scrape: "true"
      prometheus.io/port: "9847"
      prometheus.io/path: "/metrics"
  ```
- **`vmagent` 수집 동기화**: `vmagent`의 `kubernetes-pods` 메트릭 수집 작업이 해당 어노테이션을 감지하여 별도의 CRD 등록 없이 즉시 `/metrics` 수집을 시작합니다.

### 2.3 Standalone Linux VM / 베어메탈 호스트 연동
Kubernetes 클러스터 외부의 독립 Linux 가상머신(VM) 또는 베어메탈 전용 장비에서의 기동 가이드입니다.

- **실행 바이너리 기동**:
  ```bash
  # 노드 설정 파일 지정 기동
  nodevitals -config /etc/nodevitals/config.yaml
  ```
- **Systemd 서비스 등록 (`/etc/systemd/system/nodevitals.service`)**:
  ```ini
  [Unit]
  Description=nodevitals unified hardware telemetry agent
  After=network.target

  [Service]
  ExecStart=/usr/local/bin/nodevitals -config /etc/nodevitals/config.yaml
  Restart=always
  RestartSec=5s
  LimitNOFILE=65536

  [Install]
  WantedBy=multi-user.target
  ```

---

## 3. 검증 툴킷 및 스크립트

`deploy/chart/tests/compatibility-check.sh` 스크립트를 통해 Helm 템플릿의 호환성 어노테이션 및 렌더링 정합성을 자동으로 검증할 수 있습니다:

```bash
bash deploy/chart/tests/compatibility-check.sh
```
