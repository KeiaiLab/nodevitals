.PHONY: test vet fmt build docker chart-lint chart-test all build-gpu gpu-check scan sbom sign release

# Supply chain (ADR-0002): local make-target release — trivy scan+SBOM, cosign keyless.
IMG ?= ghcr.io/keiailab/nodevitals
TAG ?= dev
VERSION ?= $(shell awk '/^appVersion:/{gsub(/"/,"",$$2); print $$2}' deploy/chart/Chart.yaml)

test:
	go test ./...

vet:
	go vet ./...

fmt:
	@out=$$(gofmt -l ./cmd ./internal); if [ -n "$$out" ]; then echo "gofmt needed:"; echo "$$out"; exit 1; fi

build:
	CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o dist/nodevitals ./cmd/nodevitals

docker:
	docker build -t ghcr.io/keiailab/nodevitals:dev .

build-gpu:
	docker build --platform=linux/amd64 --target gpu -t ghcr.io/keiailab/nodevitals:dev-gpu .

gpu-check:
	docker run --rm --platform=linux/amd64 -v "$$PWD":/src -w /src golang:1.26-bookworm sh -c 'CGO_ENABLED=1 go build -tags gpu ./...'

chart-lint:
	helm template nv deploy/chart | kubeconform -strict -summary

chart-test:
	bash deploy/chart/tests/secret-isolation.sh

# Fail the build on HIGH/CRITICAL CVEs (run `make docker` first).
scan:
	trivy image --scanners vuln --severity HIGH,CRITICAL --exit-code 1 --quiet $(IMG):$(TAG)

# CycloneDX SBOM → dist/ (gitignored).
sbom:
	@mkdir -p dist
	trivy image --format cyclonedx --quiet -o dist/sbom-$(VERSION).cdx.json $(IMG):$(TAG)
	@echo "SBOM -> dist/sbom-$(VERSION).cdx.json"

# cosign keyless-sign the PUSHED images (needs cosign + registry push + OIDC).
sign:
	cosign sign --yes $(IMG):$(VERSION)
	cosign sign --yes $(IMG):$(VERSION)-gpu

# Maintainer release on a v* tag: build (multi-arch static + amd64 gpu) -> push
# -> scan -> sbom -> sign. Prereqs: ghcr.io login, cosign, and a docker-container
# buildx driver for attestations (`docker buildx create --driver docker-container --use`).
release:
	docker buildx build --platform linux/amd64,linux/arm64 -t $(IMG):$(VERSION) --push .
	docker buildx build --platform linux/amd64 --target gpu -t $(IMG):$(VERSION)-gpu --push .
	$(MAKE) scan TAG=$(VERSION)
	$(MAKE) sbom TAG=$(VERSION)
	$(MAKE) sign

all: fmt vet test build
