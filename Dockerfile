# syntax=docker/dockerfile:1
#
# One image, every tier. The gpu tier needs cgo — go-nvml dlopen's
# libnvidia-ml.so through it — so the whole binary is built with CGO_ENABLED=1
# and the `gpu` tag on a glibc base. NVML is resolved at *runtime*, not link
# time, so this same image runs unchanged on nodes with no GPU: the agent logs
# that it is skipping the gpu tier and keeps collecting everything else.
#
# The previous split (a static image plus a separate `-gpu` variant) meant two
# artifacts, two digests to sign, and a values field to pick between them —
# for a difference no operator wanted to reason about.
FROM golang:1.26-bookworm AS builder
WORKDIR /src
COPY go.mod go.sum* ./
RUN go mod download
COPY . .
ARG TARGETARCH=amd64
RUN CGO_ENABLED=1 GOOS=linux GOARCH=${TARGETARCH} \
    go build -trimpath -ldflags="-s -w" -tags gpu -o /out/nodevitals ./cmd/nodevitals

FROM gcr.io/distroless/cc-debian12:nonroot
# Links the ghcr package to this repository, so the image shows up under the
# repo's Packages and its provenance is traceable from the registry alone.
LABEL org.opencontainers.image.source=https://github.com/KeiaiLab/nodevitals
COPY --from=builder /out/nodevitals /nodevitals
USER nonroot
ENTRYPOINT ["/nodevitals"]
