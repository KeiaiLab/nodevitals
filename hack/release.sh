#!/usr/bin/env bash
#
# Release runbook (ADR-0002), in executable form.
#
# ADR-0002 retired the auto push/sign *make targets* because fire-and-forget
# automation is a poor fit for irreversible outward operations. This script does
# not reverse that decision:
#
#   - it is not a Makefile target and no CI job invokes it,
#   - the maintainer runs it explicitly at release time,
#   - every step is idempotent, so artifacts that are already published are
#     skipped rather than overwritten,
#   - the fail-closed scan gate (`make release-verify`) runs *before* anything
#     can leave the machine.
#
# Versions come from deploy/chart/Chart.yaml — `version:` drives the chart and
# `appVersion:` drives the image tags. Both are validated as semver so a
# malformed Chart.yaml can never become shell.
#
# Usage: bash hack/release.sh
#
set -euo pipefail

cd "$(dirname "${BASH_SOURCE[0]}")/.."

IMG=ghcr.io/keiailab/nodevitals
CHART_DIR=deploy/chart
GH_USER=${GH_USER:-KeiaiLab-PHIL}

SEMVER='^[0-9]+\.[0-9]+\.[0-9]+([-.+][0-9A-Za-z.-]+)?$'
CHART_VER=$(awk '/^version:/{print $2; exit}' "$CHART_DIR/Chart.yaml" | grep -E "$SEMVER")
APP_VER=$(awk '/^appVersion:/{gsub(/"/,"",$2); print $2; exit}' "$CHART_DIR/Chart.yaml" | grep -E "$SEMVER")

step() { printf '\n\033[1;36m==> %s\033[0m\n' "$1"; }
skip() { printf '\033[1;33m    skip — %s\033[0m\n' "$1"; }
ok()   { printf '\033[1;32m    ok — %s\033[0m\n' "$1"; }

for bin in docker helm cosign gh; do
	command -v "$bin" >/dev/null || { echo "ERROR: $bin is required." >&2; exit 1; }
done

echo "chart $CHART_VER · app $APP_VER · $IMG"

step "1/5 registry login"
gh auth token | docker login ghcr.io -u "$GH_USER" --password-stdin
gh auth token | helm registry login ghcr.io -u "$GH_USER" --password-stdin

step "2/5 image ($APP_VER)"
if docker buildx imagetools inspect "$IMG:$APP_VER" >/dev/null 2>&1; then
	skip "$IMG:$APP_VER already published"
else
	# Fail-closed: the image is built and scanned before any push. A vulnerable
	# image must never reach ghcr.io (ADR-0002).
	make release-verify

	if docker buildx inspect nodevitals-release >/dev/null 2>&1; then
		docker buildx use nodevitals-release
	else
		docker buildx create --name nodevitals-release --driver docker-container --use
	fi

	# One image for every tier. It is cgo/glibc because the gpu tier's NVML
	# binding needs cgo, and NVML is dlopen'd at runtime so the same image runs
	# on GPU-less nodes. cgo also means amd64 only — a cross-built arm64 cgo
	# image would need a full cross toolchain for no current consumer.
	docker buildx build --platform linux/amd64 \
		--provenance=true --sbom=true -t "$IMG:$APP_VER" --push .
	ok "image pushed"
fi

step "3/5 cosign signatures (by digest, never by mutable tag)"
DIG=$(docker buildx imagetools inspect "$IMG:$APP_VER" --format '{{.Manifest.Digest}}')
verify_one() {
	cosign verify "$1" --certificate-identity-regexp='.+' \
		--certificate-oidc-issuer-regexp='.+' >/dev/null 2>&1
}
if verify_one "$IMG@$DIG"; then
	skip "digest already signed"
else
	echo "    a browser window opens for keyless OIDC — authenticate promptly,"
	echo "    the sigstore code expires within seconds."
	cosign sign --yes "$IMG@$DIG"
	verify_one "$IMG@$DIG"
	ok "signed and verified"
fi

step "4/5 helm chart ($CHART_VER)"
if helm show chart "oci://ghcr.io/keiailab/charts/nodevitals" --version "$CHART_VER" >/dev/null 2>&1; then
	skip "chart $CHART_VER already published"
else
	helm package "$CHART_DIR" --destination dist
	helm push "dist/nodevitals-$CHART_VER.tgz" oci://ghcr.io/keiailab/charts
	ok "chart pushed"
fi

step "5/5 package visibility"
# A ghcr package created by the first push defaults to 'internal'. Artifact Hub
# pulls anonymously, so an internal package fails its index job with a 401 —
# and GitHub exposes no API to flip visibility, only the settings UI. Checking
# it here turns a silent downstream failure into an actionable line.
needs_attention=0
check_public() {
	local pkg=$1 enc vis
	enc=${pkg//\//%2F}
	vis=$(gh api "orgs/KeiaiLab/packages/container/$enc" --jq '.visibility' 2>/dev/null || echo unknown)
	if [ "$vis" = public ]; then
		ok "$pkg is public"
	else
		printf '\033[1;31m    %s is %s — Artifact Hub will 401 on anonymous pull\033[0m\n' "$pkg" "$vis"
		echo "      https://github.com/orgs/KeiaiLab/packages/container/package/$enc"
		echo "      → Package settings → Danger Zone → Change visibility → Public"
		needs_attention=1
	fi
}
check_public nodevitals
check_public charts/nodevitals

step "summary"
cat <<EOF
    image  $IMG:$APP_VER  @ $DIG
    chart  oci://ghcr.io/keiailab/charts/nodevitals:$CHART_VER

Next, outside this script:
  - register chart $CHART_VER in KeiaiLab/charts catalog.yaml
  - Artifact Hub picks it up on its own 30-minute tracker cycle; there is no
    public API to force a rescan, so the wait is expected, not a failure
EOF
[ "$needs_attention" -eq 0 ] || echo "
    ^ fix the visibility above first, or the catalog index job fails."
