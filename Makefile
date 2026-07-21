.PHONY: test vet fmt build docker chart-lint chart-test all gpu-check scan sbom release-verify

# Supply chain (ADR-0002): scan/sbom/release-verify run locally, fail-closed.
# Publishing + cosign signing is a documented maintainer runbook (ADR-0002),
# NOT an auto-push target — a live registry push is irreversible and outward.
IMG ?= ghcr.io/keiailab/nodevitals
IMGREF ?= $(IMG):dev
# appVersion, validated as semver so a malformed Chart.yaml value can never
# become shell (it flows into recipe command lines).
VERSION := $(shell awk '/^appVersion:/{gsub(/"/,"",$$2);print $$2}' deploy/chart/Chart.yaml | grep -E '^[0-9]+\.[0-9]+\.[0-9]+([-.+][0-9A-Za-z.-]+)?$$')

test:
	go test ./...

vet:
	go vet ./...

fmt:
	@out=$$(gofmt -l ./cmd ./internal); if [ -n "$$out" ]; then echo "gofmt needed:"; echo "$$out"; exit 1; fi

build:
	CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o dist/nodevitals ./cmd/nodevitals

docker:
	docker build --platform=linux/amd64 -t ghcr.io/keiailab/nodevitals:dev .

gpu-check:
	docker run --rm --platform=linux/amd64 -v "$$PWD":/src -w /src golang:1.26-bookworm sh -c 'CGO_ENABLED=1 go build -tags gpu ./...'

chart-lint:
	helm template nv deploy/chart | kubeconform -strict -summary

chart-test:
	bash deploy/chart/tests/secret-isolation.sh
	bash deploy/chart/tests/tier-runtime.sh

# Vuln-scan IMGREF, failing on HIGH/CRITICAL. Override IMGREF for the gpu image.
scan:
	trivy image --scanners vuln --severity HIGH,CRITICAL --exit-code 1 --quiet "$(IMGREF)"

# CycloneDX SBOM for IMGREF → dist/ (gitignored).
sbom:
	@mkdir -p dist
	trivy image --format cyclonedx --quiet -o "dist/sbom-$(VERSION).cdx.json" "$(IMGREF)"
	@echo "SBOM -> dist/sbom-$(VERSION).cdx.json ($(IMGREF))"

# Fail-closed release gate — NO publish. Builds BOTH images and scans BOTH
# before anything could leave the machine, then emits SBOMs. Publishing and
# cosign-sign-BY-DIGEST are a separate maintainer step (ADR-0002); scanning
# before any push is deliberate — a vulnerable image must never reach ghcr.io.
release-verify: docker
	$(MAKE) scan IMGREF=$(IMG):dev
	$(MAKE) sbom IMGREF=$(IMG):dev
	@echo "release-verify OK: image scanned clean + SBOM'd. Publish per ADR-0002."

all: fmt vet test build
