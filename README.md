<p align="center">
  <a href="https://keiailab.com">
    <img src="docs/branding/symbol.png" alt="keiailab" width="96"/>
  </a>
</p>

# nodevitals

**Unified hardware telemetry for Kubernetes nodes — one agent instead of three exporters.**

[![License: MIT](https://img.shields.io/badge/License-MIT-blue.svg)](LICENSE)
[![Go](https://img.shields.io/badge/Go-1.26-00ADD8?logo=go)](go.mod)
[![Kubernetes](https://img.shields.io/badge/Kubernetes-1.26%2B-326CE5?logo=kubernetes)](https://kubernetes.io/)
[![Release](https://img.shields.io/github/v/release/KeiaiLab/nodevitals?include_prereleases&sort=semver)](https://github.com/KeiaiLab/nodevitals/releases)

## Design assets

| Asset | Path | Usage |
|---|---|---|
| Centered service symbol | [`docs/branding/symbol.png`](docs/branding/symbol.png) | GitHub README, Artifact Hub icon/screenshot |
| Keiailab base symbol | [`docs/branding/base-symbol.png`](docs/branding/base-symbol.png) | Source reference for the outer rotating-arrow mark |
| Branding guide | [`docs/BRANDING.md`](docs/BRANDING.md) | Public visual usage rules |

nodevitals is a single Go agent that reads deep hardware state from each Kubernetes node —
CPU, memory, disk/SMART, NIC, sensors, and NVIDIA GPUs — turns threshold crossings into
**state-transition events**, and delivers them three ways: a **webhook push** to your own
backend, a **REST snapshot**, and a **Prometheus `/metrics`** endpoint. One binary and one
Helm chart replace the `node_exporter` + `dcgm-exporter` + `smartctl_exporter` wiring.

> [!NOTE]
> **Status: early development (v0.3-dev).** The pipeline (collect → event engine →
> webhook/REST/metrics → Helm) works end-to-end, and all three tiers are implemented:
> **core** (load, CPU, memory, disk I/O, network, hwmon, PSI pressure, RAPL power —
> unprivileged), **SMART** (SATA/NVMe disk health, needs device access), and **GPU**
> (NVIDIA NVML metrics + async XID error events, opt-in). **One image serves every
> tier**: it is cgo/glibc because the go-nvml binding needs cgo, but NVML is
> `dlopen`'d at runtime, so the same image runs unchanged on nodes with no GPU —
> the agent logs that it is skipping the gpu tier and collects everything else. See
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
runs with zero real hardware (fixture filesystems and mocks), so the local pre-push gate is a
fast `go test`:

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

### Tier runtime prerequisites

The optional tiers each need one cluster-specific value. Both default to off so
core stays a drop-in, and both fail *quietly* when they are needed but unset —
so set them deliberately rather than waiting for a symptom:

| Tier | Value | When you need it | Symptom if missing |
|---|---|---|---|
| gpu | `tiers.gpu.runtimeClassName` | The NVIDIA runtime is exposed as a RuntimeClass rather than the node default — the usual gpu-operator/k3s setup. Check `kubectl get runtimeclass`; the name is typically `nvidia`. | Pod CrashLoops with `nvml init: ERROR_LIBRARY_NOT_FOUND` — `NVIDIA_VISIBLE_DEVICES` alone does not trigger the runtime hook that injects `libnvidia-ml.so`. |
| smart | `tiers.smart.privileged` | Always, to read real disks. The device cgroup denies a non-privileged container's `open()` on `/host/dev/*`, and `SYS_RAWIO`/`SYS_ADMIN` do not lift it. | **Silent**: the probe skips unreadable devices by design, so you get a healthy pod, `/healthz` ok, and zero `nodevitals_hw_smart_*` series. |

```bash
helm install nodevitals ./deploy/chart \
  --set tiers.gpu.enabled=true   --set tiers.gpu.runtimeClassName=nvidia \
  --set tiers.smart.enabled=true --set tiers.smart.privileged=true
```

### One pod per node, or one per tier

By default each enabled tier gets its own DaemonSet, so a node runs as many
pods as it has tiers. `singlePod: true` collapses them into a single DaemonSet
— one pod, one process, every enabled collector:

```bash
helm install nodevitals ./deploy/chart --set singlePod=true \
  --set tiers.smart.enabled=true --set tiers.smart.privileged=true \
  --set tiers.gpu.enabled=true   --set tiers.gpu.runtimeClassName=nvidia
# 10 nodes: 3 DaemonSets / 30 pods  ->  1 DaemonSet / 10 pods
```

The metric surface is identical either way — the `tier` label is set by the
collector that produced the sample, not by which pod it ran in — and the merged
pod exposes one `/metrics` and one `/v1/state` covering all tiers.

What you trade for it:

- **One container means one `securityContext`.** With smart enabled, `/proc`
  and `/sys` collection runs as root (and privileged) too. Keep `singlePod`
  off where core's unprivileged posture matters.
- **`runtimeClassName` is pod-level**, so with gpu enabled the whole pod runs
  under the NVIDIA runtime — harmless for the other tiers, but it means the
  gpu image (the NVML-linked build) is used for everything.
- **Mixed fleets are fine.** A node without a GPU logs `gpu reader init failed
  — skipping gpu tier` and keeps serving core/smart. Only a *gpu-only* pod
  still fails hard, and only that layout pins itself to GPU nodes.

Rules, thresholds, and webhook secrets are hashed into each DaemonSet's pod
template, so editing them rolls the pods — the agent reads its config once at
startup, and without that hash a `helm upgrade` would rewrite the ConfigMap and
change nothing.

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

### Drop-in exporter compatibility

Two opt-in blocks make `/metrics` a drop-in replacement for the exporters nodevitals retires,
so existing dashboards and alert rules keep working unchanged:

- `nodeExporter.enabled` embeds upstream node_exporter's collectors — the full `node_*`
  surface, identical names/labels/semantics (see [Third-party notices](#third-party-notices)).
- `dcgmCompat.enabled` re-emits the gpu tier's NVML snapshot as the 18-metric
  `DCGM_FI_*` surface of dcgm-exporter — names, HELP text, value types, units
  (framebuffer MiB, energy mJ) and identity labels
  (`gpu`/`UUID`/`pci_bus_id`/`device`/`modelName`/`Hostname`/`DCGM_FI_DRIVER_VERSION`)
  matched against a live dcgm-exporter 4.x. Fields the hardware can't answer
  (memory temp / remapped rows on consumer GPUs, vGPU license) read 0, exactly as
  dcgm-exporter reports them. Not carried (yet): the `container`/`namespace`/`pod`
  GPU-attribution labels dcgm-exporter derives from the kubelet pod-resources socket —
  an unallocated GPU's empty labels are dropped at ingestion anyway, so those series are
  byte-identical; real attribution is a planned increment behind its own flag.

Events are delivered as [CloudEvents 1.0](https://cloudevents.io/) envelopes signed with
[Standard Webhooks](https://www.standardwebhooks.com/) HMAC-SHA256, so any conformant receiver
can verify them.

### Long-term downsampled history

Prometheus/VictoriaMetrics retention is typically weeks, not years — a scrape stack has no
way to answer "how has this GPU's utilization trended over the last 6 months" once the raw
samples have expired. `history.enabled` closes that gap without a second collector: an
in-process store ([internal/history](internal/history)) accumulates an allowlist of samples in
memory and flushes one averaged point per series to a local [bbolt](https://github.com/etcd-io/bbolt)
file every `history.intervalMinutes` (default 5), pruning points older than
`history.retentionDays` (default 1825 — a 5-year hardware lifecycle horizon) on the same pass.
Only the rollup lands on disk, never the raw scrape-interval samples, so years of history at
5-minute resolution stays a few MB.

History is **per-node, not fleet-aggregated** — each pod keeps its own file and serves it from
its own `GET /v1/history/{metric}?range=90d&node=...&device=...` (`range` accepts `90d` or
anything `time.ParseDuration` understands; omit it for the full retained window). The default
metric allowlist is the GPU health quartet (`gpu_utilization_pct`, `gpu_mem_used_bytes`,
`gpu_mem_total_bytes`, `gpu_temperature_celsius`, `gpu_power_watts`) — override
`history.metrics` in values to track anything else `nodevitals_hw_*` emits.

The chart backs this with a writable hostPath (`history.hostPath`, default
`/var/lib/nodevitals/history`) so data survives pod restarts, not just process restarts — a
DaemonSet pod restarts on node reboots and image upgrades, and in-memory-only history would
reset on every one. Because a fresh hostPath directory is root-owned, enabling `history`
relaxes the container to root the same way `tiers.smart.enabled` already does (no separate
init container).

## Delivery surfaces

| Surface              | Endpoint / transport                | Use                                            |
| -------------------- | ----------------------------------- | ---------------------------------------------- |
| **Webhook push**     | CloudEvents 1.0 + HMAC → your URL   | primary — hardware events to your own backend  |
| **REST snapshot**    | `GET /v1/state`                     | on-demand current state (debugging)            |
| **REST history**     | `GET /v1/history/{metric}`          | long-term downsampled trend (per node, `history.enabled`) |
| **Prometheus**       | `GET /metrics`                      | drop into an existing Prometheus/Grafana stack |

## Building

```bash
make all         # go vet + go test + build
make docker      # build the image (distroless/cc, cgo) — serves every tier
make chart-lint  # helm template | kubeconform
```

Requirements: Go 1.26+, and (for the chart) Helm 3 + kubeconform. There is one
image for all tiers, built for `linux/amd64`. It is cgo/glibc rather than static
because the gpu tier's go-nvml binding needs cgo; that also makes arm64 a cross
-toolchain problem with no current consumer, so arm64 is deferred.

## Verifying a release

Every published image is signed. The public key lives in this repository, so
verification needs nothing but cosign:

```bash
cosign verify --key cosign.pub ghcr.io/keiailab/nodevitals:<version>
```

Signing uses a key held in a HashiCorp Vault / OpenBao transit engine rather
than sigstore's keyless flow. Keyless requires an OIDC token from an issuer
public Fulcio trusts, and every one of those is either a browser login or a
hosted-CI workload identity — neither of which a self-hosted release pipeline
can produce unattended. The signature is still recorded in Rekor, so public
auditability is unchanged; what differs is that the certificate binds a key
rather than a person.

Releases built by the GitHub Actions pipeline (`.github/workflows/release.yml`)
are signed keyless with the workflow's own OIDC identity — the runner cannot
reach the private OpenBao, and GitHub's workload issuer is one of the few
public Fulcio trusts, so this is the unattended path *on hosted CI*:

```bash
cosign verify ghcr.io/keiailab/nodevitals:<version> \
  --certificate-identity-regexp='^https://github.com/KeiaiLab/nodevitals/' \
  --certificate-oidc-issuer=https://token.actions.githubusercontent.com
```

Releases before `0.5.0` were signed keyless by a human login and verify with
the loose identity flags instead:

```bash
cosign verify ghcr.io/keiailab/nodevitals:0.4.0 \
  --certificate-identity-regexp='.+' --certificate-oidc-issuer-regexp='.+'
```

## Third-party notices

nodevitals bundles node_exporter's collectors (Apache-2.0) to serve the full
`node_*` surface — see [`NOTICE`](NOTICE). nodevitals' own code is MIT.

## Supply chain

Supply-chain gates are local `make` targets (not CI — see [ADR-0002](docs/kb/adr/0002-supply-chain-and-release.md)):

```bash
make scan            # trivy vuln scan of $IMGREF — fails on HIGH/CRITICAL
make sbom            # CycloneDX SBOM → dist/sbom-<version>.cdx.json
make release-verify  # build both images, scan BOTH, emit SBOMs — fail-closed, no publish
```

Publishing + cosign signing are a maintainer runbook (ADR-0002): `release-verify` scans
**before** any push (a vulnerable image never reaches the registry), then the maintainer pushes
and signs **by digest** (`cosign sign $IMG@sha256:...`, never a mutable tag).

That runbook is executable as [`hack/release.sh`](hack/release.sh) — deliberately *not* a
`make` target and never invoked by CI, so publishing stays an explicit maintainer action.
Every step is idempotent: already-published images, signatures, and chart versions are
skipped, so a re-run only does what is actually missing.

```bash
bash hack/release.sh   # versions are read from deploy/chart/Chart.yaml
```

The distroless/static image carries no OS package surface: a `trivy` scan of the current build
reports **0 HIGH/CRITICAL vulnerabilities** (debian-base 0, Go binary 0). The GPU image adds a
glibc (cc-debian12) base for the cgo/NVML binding.

## Contributing

Issues and pull requests are welcome. The codebase is small, fully unit-tested without
hardware, and follows a strict collect → event → sink layering — see the
[design doc](docs/superpowers/specs/2026-07-17-nodevitals-design.md) before adding a
collector or sink.

Quality gates run as **git hooks** ([lefthook](https://github.com/evilmartians/lefthook)),
not a remote CI pipeline ([ADR-0002](docs/kb/adr/0002-supply-chain-and-release.md)). Activate
them once after cloning:

```bash
lefthook install     # pre-commit: gofmt · pre-push: go vet + go test -race + helm/kubeconform + chart-test
```

`make all` runs the same Go gates by hand. The chart gates fire only when `deploy/chart/**`
changes; bypass in an emergency with `LEFTHOOK=0 git push`.

## License

[MIT](LICENSE)
