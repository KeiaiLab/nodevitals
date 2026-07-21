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

echo "PASS: gpu runtimeClass + smart privileged opt-in (inert by default), config/secret checksums roll all 3 tiers, singlePod merges tiers into 1 DaemonSet"
