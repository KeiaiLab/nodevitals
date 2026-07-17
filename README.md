# nodevitals

Unified hardware telemetry agent for Kubernetes nodes — collects deep hardware
state (CPU, memory, GPU, disk/SMART, sensors), evaluates state-transition events,
and delivers via webhook push, REST snapshot, and Prometheus `/metrics`. One agent
and one Helm chart replace the node-exporter + dcgm-exporter + smartctl-exporter wiring.

Status: early development (v0.1 walking skeleton).

License: Apache-2.0
