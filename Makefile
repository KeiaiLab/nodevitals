.PHONY: test vet fmt build docker chart-lint all

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

chart-lint:
	helm template nv deploy/chart | kubeconform -strict -summary

all: fmt vet test build
