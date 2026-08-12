#!/usr/bin/env bash
# deploy/chart/tests/compatibility-check.sh
# Verifies that Helm templates render expected compatibility annotations and settings
# for gpu-operator, VictoriaMetrics (vmagent), nodeExporter, dcgmCompat, smartctlCompat, and ksmCompat.
set -euo pipefail

CHART_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"

echo "=== 1. Checking default template rendering ==="
rendered="$(helm template nodevitals "$CHART_DIR")"

echo "=== 2. Checking vmagent auto-discovery annotations ==="
echo "$rendered" | grep -q 'prometheus.io/scrape: "true"' || { echo "FAIL: missing prometheus.io/scrape annotation"; exit 1; }
echo "$rendered" | grep -q 'prometheus.io/port: "9847"' || { echo "FAIL: missing prometheus.io/port annotation"; exit 1; }
echo "PASS: vmagent annotations present"

echo "=== 3. Checking gpu-operator & dcgmCompat rendering ==="
gpu_rendered="$(helm template nodevitals "$CHART_DIR" --set tiers.gpu.enabled=true --set tiers.gpu.runtimeClassName=nvidia --set dcgmCompat.enabled=true)"
echo "$gpu_rendered" | grep -q 'runtimeClassName:.*nvidia' || { echo "FAIL: runtimeClassName nvidia not rendered"; exit 1; }
echo "$gpu_rendered" | grep -q 'dcgmCompat:' || { echo "FAIL: dcgmCompat section missing in configmap"; exit 1; }
echo "PASS: gpu-operator & dcgmCompat rendering valid"

echo "=== 4. Checking smartctlCompat rendering ==="
smart_rendered="$(helm template nodevitals "$CHART_DIR" --set tiers.smart.enabled=true --set tiers.smart.privileged=true --set smartctlCompat.enabled=true)"
echo "$smart_rendered" | grep -q 'privileged: true' || { echo "FAIL: privileged true not rendered for smart tier"; exit 1; }
echo "$smart_rendered" | grep -q 'smartctlCompat:' || { echo "FAIL: smartctlCompat section missing in configmap"; exit 1; }
echo "PASS: smartctlCompat rendering valid"

echo "=== 5. Checking nativeCollectors rendering ==="
node_rendered="$(helm template nodevitals "$CHART_DIR" --set nodeExporter.enabled=true --set nodeExporter.nativeCollectors=true)"
echo "$node_rendered" | grep -q 'nativeCollectors: true' || { echo "FAIL: nativeCollectors true not rendered"; exit 1; }
echo "PASS: nativeCollectors rendering valid"

echo "=== 6. Checking ksmCompat rendering ==="
node_ksm="$(helm template nodevitals "$CHART_DIR" --set ksmCompat.enabled=true)"
echo "$node_ksm" | grep -q 'ksmCompat:' || { echo "FAIL: ksmCompat section missing in configmap"; exit 1; }
echo "$node_ksm" | grep -q 'mode: "node"' || { echo "FAIL: ksmCompat mode node not rendered"; exit 1; }
# cluster 는 배포 전에 막는다 — DaemonSet 이라 노드마다 전역 수집이 돌아 모든
# 시리즈가 노드 수만큼 중복된다. 에이전트도 거부하지만, 롤아웃 후 CrashLoop 로
# 알게 되는 것보다 helm 단계에서 멈추는 편이 낫다.
if helm template nodevitals "$CHART_DIR" --set ksmCompat.enabled=true --set ksmCompat.mode=cluster >/dev/null 2>&1; then
  echo "FAIL: ksmCompat.mode=cluster rendered successfully; it would duplicate every series by the node count"
  exit 1
fi
echo "PASS: ksmCompat renders node mode and refuses cluster mode"

echo "=== 7. Checking ksmCompat RBAC is gated and minimal ==="
# 꺼져 있으면 자격 자체가 없어야 한다 — 다른 tier 는 /proc·/sys·/dev 와 NVML 만
# 읽으므로 API 토큰을 들고 있을 이유가 없다.
if echo "$rendered" | grep -qE '^kind: (ServiceAccount|ClusterRole|ClusterRoleBinding)'; then
  echo "FAIL: RBAC objects render with ksmCompat off; the agent would hold cluster credentials it never uses"
  exit 1
fi
echo "$rendered" | grep -q 'automountServiceAccountToken: false' \
  || { echo "FAIL: pods mount a service-account token with ksmCompat off"; exit 1; }
echo "PASS: no cluster credentials unless ksmCompat is on"

ksm_rendered="$(helm template nodevitals "$CHART_DIR" --set ksmCompat.enabled=true)"
for k in ServiceAccount ClusterRole ClusterRoleBinding; do
  echo "$ksm_rendered" | grep -q "^kind: $k" || { echo "FAIL: $k missing with ksmCompat on"; exit 1; }
done
# 권한은 이 에이전트가 실제로 읽는 두 리소스로 한정한다. kube-state-metrics 의
# 20+ 리소스 목록을 베끼면 쓰지 않는 열람 권한이 그대로 공격 표면이 된다.
rules="$(echo "$ksm_rendered" | awk '/^kind: ClusterRole$/,/^---$/' | grep -A2 'resources:')"
for forbidden in secrets configmaps deployments statefulsets; do
  echo "$rules" | grep -q "\"$forbidden\"" && { echo "FAIL: ClusterRole grants $forbidden, which ksmcompat never reads"; exit 1; }
done
echo "$ksm_rendered" | grep -q 'automountServiceAccountToken: true' \
  || { echo "FAIL: ksmCompat on but no pod mounts a token — the agent cannot authenticate"; exit 1; }
echo "$ksm_rendered" | grep -q 'serviceAccountName: nodevitals' \
  || { echo "FAIL: ksmCompat on but pods still use the default ServiceAccount"; exit 1; }
echo "PASS: ksmCompat RBAC gated, scoped to nodes+pods, and bound to the pods"

echo "SUCCESS: All service compatibility assertions PASSED!"
