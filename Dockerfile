# syntax=docker/dockerfile:1
FROM golang:1.26-bookworm AS builder
WORKDIR /src
COPY go.mod go.sum* ./
RUN go mod download
COPY . .
ARG TARGETARCH=amd64
RUN CGO_ENABLED=0 GOOS=linux GOARCH=${TARGETARCH} \
    go build -trimpath -ldflags="-s -w" -o /out/nodevitals ./cmd/nodevitals

FROM golang:1.26-bookworm AS builder-gpu
WORKDIR /src
COPY go.mod go.sum* ./
RUN go mod download
COPY . .
ARG TARGETARCH=amd64
RUN CGO_ENABLED=1 GOOS=linux GOARCH=${TARGETARCH} \
    go build -trimpath -ldflags="-s -w" -tags gpu -o /out/nodevitals ./cmd/nodevitals

FROM gcr.io/distroless/cc-debian12:nonroot AS gpu
COPY --from=builder-gpu /out/nodevitals /nodevitals
USER nonroot
ENTRYPOINT ["/nodevitals"]

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=builder /out/nodevitals /nodevitals
USER nonroot
ENTRYPOINT ["/nodevitals"]
