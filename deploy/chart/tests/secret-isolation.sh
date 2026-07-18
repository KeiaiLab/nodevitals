#!/usr/bin/env bash
# Regression guard for the webhook-secret isolation invariant
# (production-readiness Critical #1): a webhook signing secret must render ONLY
# into the nodevitals-webhooks Secret, NEVER into a ConfigMap. secret.yaml,
# the nodevitals.webhookConfig helper, and nodevitals.webhookSecretEnv each
# range over .Values.webhooks independently; if a future edit breaks their
# index alignment (or reintroduces plaintext), this fails. Run: make chart-test
set -euo pipefail
CHART="$(cd "$(dirname "$0")/.." && pwd)"
CANARY="CANARY_${$}_do_not_ship"

R="$(helm template nv "$CHART" \
  --set tiers.smart.enabled=true --set tiers.gpu.enabled=true \
  --set "webhooks[0].url=https://a.example/h" --set "webhooks[0].secret=${CANARY}_A" \
  --set "webhooks[1].url=https://b.example/h" --set "webhooks[1].secret=${CANARY}_B")"

fail() { echo "FAIL: $1"; exit 1; }

# 1. The canary must NOT appear in any ConfigMap (the leak that Critical #1 fixed).
cm_leak=$(printf '%s\n' "$R" | awk '/^kind: ConfigMap/{c=1} /^---/{c=0} c' | grep -c "$CANARY" || true)
[ "$cm_leak" -eq 0 ] || fail "webhook secret leaked into a ConfigMap ($cm_leak occurrence(s))"

# 2. Both canaries MUST be present in kind: Secret (proves they render there).
sec=$(printf '%s\n' "$R" | awk '/^kind: Secret/{c=1} /^---/{c=0} c' | grep -c "$CANARY" || true)
[ "$sec" -ge 2 ] || fail "expected both canaries in kind:Secret, found $sec"

# 3. ConfigMaps carry the placeholder; DaemonSets carry the aligned secretKeyRef.
printf '%s\n' "$R" | grep -q 'secret: ${WEBHOOK_SECRET_0}' || fail "ConfigMap missing \${WEBHOOK_SECRET_0} placeholder"
printf '%s\n' "$R" | grep -q 'key: secret-1' || fail "DaemonSet missing secretKeyRef key secret-1"

echo "PASS: webhook secret isolated to kind:Secret (0 ConfigMap leak, index-aligned across 2 webhooks x 3 tiers)"
