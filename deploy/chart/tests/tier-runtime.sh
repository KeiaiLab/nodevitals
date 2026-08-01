#!/usr/bin/env bash
# Regression guard for the three deployment invariants that a live k3s rollout
# proved the chart could not express (2026-07-21):
#
#   1. gpu tier on a cluster whose default runtime is runc needs a
#      runtimeClassName, or NVML is never injected and the agent CrashLoops
#      with "nvml init: ERROR_LIBRARY_NOT_FOUND".
#   2. smart tier needs privileged to open /host/dev block devices — the device
#      cgroup denies it otherwise, and probeDevice's silent skip turns that into
#      zero series with no error.
#   3. The agent reads its config once at startup, so a ConfigMap/Secret-only
#      change must roll the pods — otherwise `helm upgrade` silently no-ops.
#
# Each is off/inert by default: an unset runtimeClassName and privileged=false
# must render exactly the pre-existing hardened spec. Run: make chart-test
set -euo pipefail
CHART="$(cd "$(dirname "$0")/.." && pwd)"

fail() { echo "FAIL: $1"; exit 1; }
render() { helm template nv "$CHART" "$@"; }
# One tier's DaemonSet only — -s keeps sibling templates out so an annotation
# grep can't accidentally match another tier's pod template.
tier_file() { case "$1" in core) echo daemonset.yaml ;; *) echo "daemonset-$1.yaml" ;; esac; }
sum() { # sum <component> <annotation> [helm --set args...]
  local tier="$1" key="$2"; shift 2
  helm template nv "$CHART" -s "templates/$(tier_file "$tier")" "$@" \
    | grep -m1 "$key:" | awk '{print $2}'
}

ON="$(render --set tiers.gpu.enabled=true --set tiers.smart.enabled=true \
  --set tiers.gpu.runtimeClassName=nvidia --set tiers.smart.privileged=true)"
OFF="$(render --set tiers.gpu.enabled=true --set tiers.smart.enabled=true)"

# 1. runtimeClassName renders only when set.
printf '%s\n' "$ON" | grep -q 'runtimeClassName: "nvidia"' \
  || fail "gpu tier dropped runtimeClassName when it was set"
printf '%s\n' "$OFF" | grep -q 'runtimeClassName' \
  && fail "runtimeClassName must be absent by default (cluster default runtime)"

# 2. privileged pairs with allowPrivilegeEscalation — the API rejects
#    privileged:true alongside allowPrivilegeEscalation:false.
printf '%s\n' "$ON" | grep -q 'privileged: true' \
  || fail "smart tier dropped privileged when it was set"
printf '%s\n' "$ON" | grep -q 'allowPrivilegeEscalation: true' \
  || fail "privileged:true rendered without allowPrivilegeEscalation:true (API validation would reject)"
printf '%s\n' "$OFF" | grep -q 'privileged: true' \
  && fail "smart tier must stay unprivileged by default"
printf '%s\n' "$OFF" | grep -q 'allowPrivilegeEscalation: false' \
  || fail "default spec lost allowPrivilegeEscalation:false"

# 3. Config and secret content must be hashed into every tier's pod template,
#    and the hash must move when — and only when — that content changes.
BASE=(--set tiers.smart.enabled=true --set tiers.gpu.enabled=true
  --set "webhooks[0].url=https://a.example/h" --set "webhooks[0].secret=s1")

for tier in core smart gpu; do
  [ -n "$(sum "$tier" checksum/config "${BASE[@]}")" ] \
    || fail "$tier tier pod template missing checksum/config"
  [ -n "$(sum "$tier" checksum/webhook-secret "${BASE[@]}")" ] \
    || fail "$tier tier pod template missing checksum/webhook-secret"
done

[ "$(sum core checksum/config "${BASE[@]}")" = "$(sum core checksum/config "${BASE[@]}")" ] \
  || fail "checksum/config is not deterministic"
[ "$(sum core checksum/config "${BASE[@]}")" \
  != "$(sum core checksum/config "${BASE[@]}" --set "rules[0].threshold=99")" ] \
  || fail "editing a rule did not change checksum/config — the upgrade would silently no-op"
[ "$(sum core checksum/webhook-secret "${BASE[@]}")" \
  != "$(sum core checksum/webhook-secret --set tiers.smart.enabled=true --set tiers.gpu.enabled=true \
        --set "webhooks[0].url=https://a.example/h" --set "webhooks[0].secret=s2")" ] \
  || fail "rotating a webhook secret did not change checksum/webhook-secret"

# 4. singlePod collapses the per-tier DaemonSets into one pod per node. The
#    tier label lives on the sample, not the config, so the metric surface is
#    unchanged — what must hold here is that the one pod actually carries every
#    enabled tier's rules, mounts, and runtime requirements.
SINGLE="$(render --set singlePod=true --set tiers.gpu.enabled=true \
  --set tiers.smart.enabled=true --set tiers.smart.privileged=true \
  --set tiers.gpu.runtimeClassName=nvidia)"

ds=$(printf '%s\n' "$SINGLE" | grep -c '^kind: DaemonSet' || true)
[ "$ds" -eq 1 ] || fail "singlePod must render exactly 1 DaemonSet, got $ds"
cm=$(printf '%s\n' "$SINGLE" | grep -c '^kind: ConfigMap' || true)
[ "$cm" -eq 1 ] || fail "singlePod must render exactly 1 ConfigMap, got $cm"

printf '%s\n' "$SINGLE" | grep -q 'tiers: \[core, smart, gpu\]' \
  || fail "singlePod config must list every enabled tier"
# One image serves every tier — a reintroduced `-gpu` variant would put an
# operator back to picking between artifacts, and split the signing surface.
printf '%s\n' "$SINGLE" | grep -q -- '-gpu"' \
  && fail "there is one image for all tiers; no -gpu variant should be referenced"
for path in /proc /sys /dev; do
  printf '%s\n' "$SINGLE" | grep -q "path: $path$" \
    || fail "singlePod is missing the $path hostPath needed by an enabled tier"
done
for m in load1 smart_pending_sectors gpu_temperature_celsius; do
  printf '%s\n' "$SINGLE" | grep -q "metric: $m" \
    || fail "singlePod config dropped the $m rule"
done
# core/smart on a mixed fleet must not be pinned to GPU nodes; the agent skips
# the gpu collector where NVML is absent.
printf '%s\n' "$SINGLE" | grep -q 'nvidia.com/gpu.present' \
  && fail "singlePod with core enabled must not nodeSelector onto GPU nodes"
GPUONLY="$(render --set singlePod=true --set tiers.core.enabled=false --set tiers.gpu.enabled=true)"
printf '%s\n' "$GPUONLY" | grep -q 'nvidia.com/gpu.present' \
  || fail "gpu-only singlePod should still pin to GPU nodes"

# 5. nodeExporter 는 node_* 전체 표면을 같은 파드에서 낸다. 켰을 때 필요한
#    호스트 접근이 전부 붙는지, 껐을 때 아무것도 새지 않는지를 함께 본다.
#    특히 hostNetwork: /proc/net 은 마운트 경로가 아니라 읽는 프로세스의 netns 로
#    해석되므로, 이게 빠지면 netdev 가 호스트가 아닌 파드 인터페이스를 보고도
#    아무 에러 없이 "정상"으로 보인다.
NE="$(render --set nodeExporter.enabled=true --set singlePod=true)"
printf '%s\n' "$NE" | grep -q 'hostNetwork: true' \
  || fail "nodeExporter 활성 시 hostNetwork 가 없으면 netdev 가 파드 netns 를 본다"
printf '%s\n' "$NE" | grep -q 'dnsPolicy: ClusterFirstWithHostNet' \
  || fail "hostNetwork 를 켜면 dnsPolicy 도 함께 바꿔야 클러스터 DNS 가 유지된다"
printf '%s\n' "$NE" | grep -q 'mountPath: /host/root' \
  || fail "filesystem collector 가 볼 호스트 루트 마운트 누락"
printf '%s\n' "$NE" | grep -q 'textfile_collector' \
  || fail "textfile collector 디렉터리 마운트 누락 (node_smart_attr 소실)"
printf '%s\n' "$NE" | grep -q 'rootfsPath: /host/root' \
  || fail "config 에 rootfsPath 가 전달되지 않음"
# diskstats 는 /run/udev/data 에서 디바이스 속성을 읽어 device_mapper_info /
# filesystem_info 를 만든다. 없으면 경고 한 줄만 남기고 두 family 가 빠진다.
printf '%s\n' "$NE" | grep -q 'mountPath: /run/udev' \
  || fail "udev 마운트 누락 — node_disk_*_info 2종이 조용히 빠진다"

# systemd collector 는 /run/systemd/private 소켓으로만 유닛 상태를 읽는다.
# --collector.systemd 를 켜고 이 마운트를 빠뜨리면 collector 가 등록만 되고 매
# 스크레이프 실패하며 node_systemd_* 9 family(라이브 실측 1,591 시리즈)가 통째로
# 빠진다 — /metrics 는 200 을 계속 주므로 로그를 안 보면 드러나지 않는다.
NE_SYSTEMD="$(render --set nodeExporter.enabled=true --set nodeExporter.mountSystemd=true)"
printf '%s\n' "$NE_SYSTEMD" | grep -q 'mountPath: /run/systemd' \
  || fail "mountSystemd=true 인데 /run/systemd 마운트가 렌더되지 않음"
printf '%s\n' "$NE_SYSTEMD" | grep -q 'path: /run/systemd' \
  || fail "mountSystemd=true 인데 /run/systemd hostPath 볼륨이 렌더되지 않음"
# 기본값(false)에서는 나오지 않아야 한다 — systemd 없는 노드에 빈 디렉터리를
# 붙이면 그것대로 매 스크레이프 실패가 된다.
printf '%s\n' "$NE" | grep -q '/run/systemd' \
  && fail "mountSystemd 기본값이 false 인데 /run/systemd 가 렌더됨"

# 호스트 루트 마운트는 컨테이너에 호스트 파일시스템 전체 읽기를 준다. 끄면
# 마운트와 rootfsPath 가 함께 사라져야 하고(권한 축소), 그 경우 에이전트가
# filesystem collector 를 끄므로 "다른 기계를 잰 값"이 나오지 않는다.
NE_NOROOT="$(render --set nodeExporter.enabled=true --set nodeExporter.mountRootFS=false)"
printf '%s\n' "$NE_NOROOT" | grep -q '/host/root' \
  && fail "mountRootFS=false 인데 호스트 루트가 여전히 마운트된다"
printf '%s\n' "$NE_NOROOT" | grep -q 'rootfsPath' \
  && fail "mountRootFS=false 인데 rootfsPath 가 config 에 남아 filesystem collector 가 컨테이너를 잰다"

# extraFlags 오버라이드가 nativeCollectors ↔ --no-collector.* 커플링을 끊으면 안 된다.
# Helm 은 리스트를 병합하지 않고 통째로 치환하므로, 배포 레포가 자기 extraFlags 를
# values 에 갖고 있어도(예: --collector.processes 를 직접 켜는 값) nativeCollectors 가
# true 면 7종이 렌더 시점에 항상 합류해야 한다 — 이게 깨지면 임베드·네이티브 컬렉터가
# 같은 이름을 동시에 내 registry 가 /metrics 스크레이프 전체를 거부한다. values 기본값에
# 이 7종을 두는 방식(컨벤션)은 오버라이드에 깨질 수 있어 렌더 시점 결합만이 유효하다.
NE_OVERRIDE="$(render --set nodeExporter.enabled=true --set nodeExporter.nativeCollectors=true \
  --set 'nodeExporter.extraFlags[0]=--collector.processes' --set 'nodeExporter.extraFlags[1]=--collector.systemd')"
printf '%s\n' "$NE_OVERRIDE" | grep -q -- '- --collector.processes$' \
  || fail "nativeCollectors=true 인데 extraFlags 오버라이드 항목(--collector.processes)이 사라졌다"
printf '%s\n' "$NE_OVERRIDE" | grep -q -- '- --collector.systemd$' \
  || fail "nativeCollectors=true 인데 extraFlags 오버라이드 항목(--collector.systemd)이 사라졌다"
noc=$(printf '%s\n' "$NE_OVERRIDE" | grep -c -- '- --no-collector\.' || true)
[ "$noc" -eq 7 ] || fail "extraFlags 오버라이드가 --no-collector.* 7종 커플링을 끊었다 (got $noc, want 7)"

# nativeCollectors 가 꺼져 있으면(옵트인 안 한 업그레이드 기본값) 오버라이드는 그대로
# 통과하고 --no-collector.* 는 하나도 섞이지 않아야 한다 — 섞이면 opt-in 하지 않은
# 배포가 helm upgrade 만으로 7개 metric family 를 조용히 잃는다.
NE_OVERRIDE_OFF="$(render --set nodeExporter.enabled=true \
  --set 'nodeExporter.extraFlags[0]=--collector.processes' --set 'nodeExporter.extraFlags[1]=--collector.systemd')"
printf '%s\n' "$NE_OVERRIDE_OFF" | grep -q -- '- --collector.processes$' \
  || fail "nativeCollectors=false 인데 extraFlags 오버라이드 항목이 사라졌다"
printf '%s\n' "$NE_OVERRIDE_OFF" | grep -q -- '\-\-no-collector\.' \
  && fail "nativeCollectors=false(기본값)인데 --no-collector.* 가 섞여 렌더됐다 — opt-in 안 한 업그레이드가 metric 을 잃는다"

# appVersion 은 실제로 발행된 이미지 태그여야 한다. 차트만 고치면서 appVersion
# 까지 올리면 존재하지 않는 태그를 가리켜 ImagePullBackOff 가 난다(라이브 사고
# 2026-07-22: 차트 0.4.1 이 미발행 이미지 0.4.1 을 참조). 발행 여부는 네트워크
# 없이 알 수 없으므로, 최소한 "appVersion 을 바꿀 때 의도했는지" 를 눈에 띄게
# 만든다 — 이미지 태그가 appVersion 을 그대로 따르는지 고정한다.
APPV="$(awk '/^appVersion:/{gsub(/"/,"",$2); print $2}' "$CHART/Chart.yaml")"
printf '%s\n' "$(render)" | grep -q "image: \"ghcr.io/keiailab/nodevitals:${APPV}\"" \
  || fail "렌더된 이미지 태그가 appVersion($APPV) 과 다르다 — 발행되지 않은 태그를 가리킬 위험"

OFF_NE="$(render --set singlePod=true)"
for token in 'hostNetwork' '/host/root' 'textfile_collector' '/run/udev' 'nodeExporter:'; do
  printf '%s\n' "$OFF_NE" | grep -q -- "$token" \
    && fail "nodeExporter 기본 off 인데 $token 이 렌더됐다"
done

echo "PASS: gpu runtimeClass + smart privileged opt-in (inert by default), config/secret checksums roll all 3 tiers, singlePod merges tiers into 1 DaemonSet, nodeExporter wires hostNetwork+rootfs+textfile"
