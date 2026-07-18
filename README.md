# nodevitals

**Unified hardware telemetry for Kubernetes nodes — one agent instead of three exporters.**

[![Go Report Card](https://goreportcard.com/badge/github.com/KeiaiLab/nodevitals)](https://goreportcard.com/report/github.com/KeiaiLab/nodevitals)
[![Go version](https://img.shields.io/github/go-mod/go-version/KeiaiLab/nodevitals)](go.mod)
[![Release](https://img.shields.io/github/v/release/KeiaiLab/nodevitals?include_prereleases&sort=semver)](https://github.com/KeiaiLab/nodevitals/releases)
[![License](https://img.shields.io/github/license/KeiaiLab/nodevitals)](LICENSE)

nodevitals is a single Go agent that reads deep hardware state from each Kubernetes node —
CPU, memory, disk/SMART, NIC, sensors, and NVIDIA GPUs — turns threshold crossings into
**state-transition events**, and delivers them three ways: a **webhook push** to your own
backend, a **REST snapshot**, and a **Prometheus `/metrics`** endpoint. One binary and one
Helm chart replace the `node_exporter` + `dcgm-exporter` + `smartctl_exporter` wiring.

> [!NOTE]
> **Status: early development (v0.2-dev).** The pipeline (collect → event engine →
> webhook/REST/metrics → Helm) works end-to-end, and all three tiers are implemented:
> **core** (load, CPU, memory, disk I/O, network, hwmon — unprivileged), **SMART**
> (SATA/NVMe disk health via a privileged, opt-in DaemonSet), and **GPU** (NVIDIA
> NVML metrics + async XID error events via an unprivileged, opt-in DaemonSet,
> shipped as a separate glibc/cgo `:v-gpu` image since the go-nvml binding needs
> cgo while core/smart stay static). The GPU tier's NVML path is unit- and
> compile-checked without hardware — a real-GPU smoke test is still pending. See
> the [design doc](docs/superpowers/specs/2026-07-17-nodevitals-design.md),
> [M2 design](docs/superpowers/specs/2026-07-18-nodevitals-m2-design.md), and
> [M2b GPU design](docs/superpowers/specs/2026-07-18-nodevitals-m2b-gpu-design.md).

## Why

Getting hardware telemetry off a Kubernetes node today usually means running three separate
things: `node_exporter` for core metrics (it ships **no** SMART collector and defers GPUs to
dcgm), `dcgm-exporter` for NVIDIA GPUs, and `smartctl_exporter` for disks. Three DaemonSets,
three configs, three release cadences — and all of them **scrape-only**, so there is no push
path to your own backend and no first-class notion of an event.

nodevitals collapses that into one agent, and adds an **event-first** model on top of the
usual `/metrics` scrape:

| Before                                                   | With nodevitals                         |
| -------------------------------------------------------- | --------------------------------------- |
| `node_exporter` + `dcgm-exporter` + `smartctl_exporter`  | one agent                               |
| 3 DaemonSets · 3 configs · scrape-only                   | 1 Helm chart · 1 config · push + scrape |

It does **not** try to be a dashboard — you keep your own Grafana/backend. It replaces the
*collection and delivery* layer, not the visualization one.

## Architecture

The pipeline is deliberately linear and each stage is independently testable — the whole thing
runs with zero real hardware in CI, using fixture filesystems and mocks:

```mermaid
flowchart LR
    subgraph agent["nodevitals agent · single Go binary"]
        direction LR
        C["collectors<br/>/proc · /sys · NVML · SMART"]
        E["event engine<br/>state-transition<br/>+ hysteresis"]
        Q["delivery queue<br/>backoff · jitter"]
        C --> E --> Q
    end
    Q ==>|"CloudEvents 1.0<br/>+ HMAC signature"| WH["webhook<br/>your backend"]
    C -.->|"on demand"| REST["REST · GET /v1/state"]
    C -.->|"scrape"| PROM["Prometheus · /metrics"]
```

Kubernetes' Pod Security Admission is evaluated per-pod, and reading SMART needs elevated
privileges while `/proc`·`/sys` do not — so a single privileged pod would forfeit the
unprivileged benefit. nodevitals resolves this with a **tiered single-agent** design: one
codebase, one image, one chart, rendering **1–3 DaemonSets by privilege tier**:

```mermaid
flowchart TD
    Chart["one Helm chart<br/>values.tiers.*"]
    Chart --> Core["core tier · unprivileged<br/>CPU · mem · disk · NIC · sensors"]
    Chart --> Gpu["gpu tier · device access<br/>NVML metrics + XID events"]
    Chart --> Smart["smart tier · privileged<br/>disk SMART · NVMe wear"]
```

## Quickstart

```bash
# The core/smart tiers read the host's /proc, /sys, /dev via hostPath, which
# BOTH the PSA Baseline and Restricted profiles forbid — label the namespace
# to the privileged level first (the gpu tier does not need this):
kubectl label namespace default pod-security.kubernetes.io/enforce=privileged --overwrite

# Install the core tier via Helm. The webhook signing secret is stored in a
# Kubernetes Secret and injected via env — never written to a ConfigMap. Prefer
# --set-file (reads the key from a file, keeping it out of shell history) or an
# external secret store over an inline --set for the secret value.
printf %s "$WEBHOOK_SIGNING_KEY" > /tmp/wh0.secret
helm install nodevitals ./deploy/chart \
  --set 'webhooks[0].url=https://your-backend.example/hooks/hardware' \
  --set-file 'webhooks[0].secret=/tmp/wh0.secret'

# Verify
kubectl get daemonset nodevitals-core
curl http://<pod-ip>:9847/metrics | grep nodevitals_hw_
```

> [!IMPORTANT]
> **Pod Security Admission:** core and smart mount hostPath (`/proc`, `/sys`,
> `/dev`), which is forbidden by both PSA Baseline and Restricted — those tiers
> require a namespace labeled `pod-security.kubernetes.io/enforce=privileged`
> (see the chart's `NOTES.txt`). The **gpu tier is Restricted-compliant**. This
> is inherent to node-level hardware telemetry, not a hardening gap — see the
> [production-readiness report](docs/production-readiness.md).

Or run the binary directly against a config file:

```bash
go install github.com/KeiaiLab/nodevitals/cmd/nodevitals@latest
nodevitals -config ./config.yaml
```

## Configuration

One YAML file drives collection, event rules, and delivery sinks:

```yaml
tier: core
intervalSeconds: 15
procRoot: /host/proc            # node's /proc, mounted read-only

rules:                           # hardware state-transition rules
  - metric: load1
    device: cpu
    condition: load_high
    severity: warning
    threshold: 8.0
    enterFor: 3                  # 3 consecutive breaches → ENTER event
    exitFor: 3                   # 3 consecutive clears  → EXIT event

sinks:
  webhook:                       # push to your backend (CloudEvents + HMAC)
    - url: https://your-backend.example/hooks/hardware
      secret: whsec_...          # HMAC signing key
  metrics:                       # Prometheus scrape endpoint
    enabled: true
    listenAddr: ":9847"
```

(The Helm chart exposes the metrics port as `metrics.port` in `values.yaml` and renders the
matching `listenAddr` into the pod's ConfigMap for you.)

Events are delivered as [CloudEvents 1.0](https://cloudevents.io/) envelopes signed with
[Standard Webhooks](https://www.standardwebhooks.com/) HMAC-SHA256, so any conformant receiver
can verify them.

## Delivery surfaces

| Surface              | Endpoint / transport                | Use                                            |
| -------------------- | ----------------------------------- | ---------------------------------------------- |
| **Webhook push**     | CloudEvents 1.0 + HMAC → your URL   | primary — hardware events to your own backend  |
| **REST snapshot**    | `GET /v1/state`                     | on-demand current state (debugging)            |
| **Prometheus**       | `GET /metrics`                      | drop into an existing Prometheus/Grafana stack |

## Building

```bash
make all         # go vet + go test + build
make docker      # build the distroless/static image (~22 MB) — core & smart tiers
make build-gpu   # build the glibc :v-gpu image (GPU tier — go-nvml needs cgo)
make chart-lint  # helm template | kubeconform
```

Requirements: Go 1.26+, and (for the chart) Helm 3 + kubeconform. The core/smart static image
is built for `linux/amd64` and `linux/arm64`; the GPU `:v-gpu` image is `linux/amd64`-only
(the go-nvml binding needs cgo, and arm64 GPU support is deferred).

## Contributing

Issues and pull requests are welcome. The codebase is small, fully unit-tested without
hardware, and follows a strict collect → event → sink layering — see the
[design doc](docs/superpowers/specs/2026-07-17-nodevitals-design.md) before adding a
collector or sink. Please run `make all` before opening a PR.

## License

[Apache-2.0](LICENSE)
