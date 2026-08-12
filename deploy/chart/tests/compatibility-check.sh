#!/usr/bin/env bash
# deploy/chart/tests/compatibility-check.sh
# Verifies that Helm templates render expected compatibility annotations and settings
# for gpu-operator, VictoriaMetrics (vmagent), nodeExporter, dcgmCompat, smartctlCompat, and ksmCompat.
set -euo pipefail

CHART_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"

echo "=== 1. Checking default template rendering ==="
rendered="$(helm template nodevitals "$CHART_DIR")"

echo "=== 2. Checking vmagent auto-discovery annotations are opt-in ==="
# 기본 렌더에는 없어야 한다. 이 차트를 이미 Service/ServiceMonitor 로 수집하던
# 클러스터에서는, 업그레이드만으로 role:pod 잡이 같은 파드를 한 벌 더 긁기
# 시작해 모든 시리즈가 2벌이 된다 — 에러 없이, 청구서와 카디널리티로만 드러난다.
if echo "$rendered" | grep -q 'prometheus.io/scrape'; then
  echo "FAIL: scrape annotations render by default; a chart user already scraping via Service would silently double-collect after an upgrade"
  exit 1
fi
echo "PASS: no scrape annotations unless asked for"

# 켠 경우에는 **파드 템플릿 안**이어야 한다. DaemonSet 객체에 붙은 annotation 은
# 파드로 전파되지 않으므로 role:pod 발견은 그것을 영영 보지 못한다.
disc_rendered="$(helm template nodevitals "$CHART_DIR" --set scrapeAnnotations.enabled=true)"
pod_meta="$(echo "$disc_rendered" | awk '/^  template:/,/^    spec:/')"
echo "$pod_meta" | grep -q 'prometheus.io/scrape: "true"' || { echo "FAIL: scrape annotation is not inside the pod template; role:pod discovery cannot see it"; exit 1; }
echo "$pod_meta" | grep -q 'prometheus.io/port: "9847"' || { echo "FAIL: port annotation is not inside the pod template"; exit 1; }
echo "PASS: scrape annotations land in the pod template when enabled"

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
ksm_rendered="$(helm template nodevitals "$CHART_DIR" --set ksmCompat.enabled=true --set ksmCompat.mode=cluster)"
echo "$ksm_rendered" | grep -q 'ksmCompat:' || { echo "FAIL: ksmCompat section missing in configmap"; exit 1; }
echo "$ksm_rendered" | grep -q 'mode: "cluster"' || { echo "FAIL: ksmCompat mode cluster not rendered"; exit 1; }
echo "PASS: ksmCompat rendering valid"

echo "SUCCESS: All service compatibility assertions PASSED!"
