# ADR-0001: arm64 멀티아키텍처 — OSS 예외

- 상태: Accepted
- 날짜: 2026-07-17

## 맥락
거버넌스 §2.3 은 컨테이너 이미지를 `linux/amd64` 단일 아키텍처로 강제하고 멀티아키텍처를 금지한다. 이는 내부 클러스터 자산 전제의 규칙이다.

## 결정
nodevitals 는 공개 OSS 노드 에이전트다. 비교군(node-exporter/dcgm-exporter/telegraf/netdata/grafana-agent) 전부 arm64 를 배포하며, dcgm-exporter 는 GH200/Grace-Hopper/Jetson 때문에 arm64 를 낸다. 따라서 nodevitals 는 **amd64 + arm64** 이미지를 배포한다. §2.8(OSS = GitHub canonical)에 근거한 예외.

## 결과
- Go 크로스컴파일(`GOOS=linux GOARCH={amd64,arm64}`)로 단일 소스에서 양 아키 바이너리 생성.
- 멀티아치 이미지 매니페스트 발행은 릴리스 단계 작업(후속 마일스톤).
- 내부 클러스터 배포 대상이 아니므로 §2.3 의 원래 우려(내부 빌드 SPOF)와 무관.
