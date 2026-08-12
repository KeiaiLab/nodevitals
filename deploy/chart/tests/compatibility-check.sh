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
ksm_rendered="$(helm template nodevitals "$CHART_DIR" --set ksmCompat.enabled=true --set ksmCompat.mode=cluster)"
echo "$ksm_rendered" | grep -q 'ksmCompat:' || { echo "FAIL: ksmCompat section missing in configmap"; exit 1; }
echo "$ksm_rendered" | grep -q 'mode: "cluster"' || { echo "FAIL: ksmCompat mode cluster not rendered"; exit 1; }
echo "PASS: ksmCompat rendering valid"

echo "SUCCESS: All service compatibility assertions PASSED!"
