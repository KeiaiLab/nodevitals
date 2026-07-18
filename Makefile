.PHONY: test vet fmt build docker chart-lint chart-test all build-gpu gpu-check

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

all: fmt vet test build
