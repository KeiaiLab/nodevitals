.PHONY: test vet build docker chart-lint all

test:
	go test ./...

vet:
	go vet ./...

build:
	CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o dist/nodevitals ./cmd/nodevitals

docker:
	docker build -t ghcr.io/keiailab/nodevitals:dev .

chart-lint:
	helm template nv deploy/chart | kubeconform -strict -summary

all: vet test build
