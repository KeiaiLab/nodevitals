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

echo "PASS: gpu runtimeClass + smart privileged opt-in (inert by default), config/secret checksums roll all 3 tiers"
