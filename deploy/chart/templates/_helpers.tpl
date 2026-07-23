{{- define "nodevitals.name" -}}nodevitals{{- end -}}
{{- define "nodevitals.image" -}}
{{- $tag := .Values.image.tag | default .Chart.AppVersion -}}
{{ .Values.image.repository }}:{{ $tag }}
{{- end -}}

{{/*
Render the webhook sink list for a ConfigMap. The signing secret is emitted as
a ${WEBHOOK_SECRET_N} placeholder — NEVER the plaintext value — so no secret
material ever lands in a ConfigMap. The agent expands ${ENV} at load time from
the env var injected by secretKeyRef (see nodevitals.webhookSecretEnv). Empty
webhooks list -> empty output.
*/}}
{{- define "nodevitals.webhookConfig" -}}
{{- range $i, $w := .Values.webhooks }}
- url: {{ $w.url | quote }}
{{- if $w.secret }}
  secret: ${WEBHOOK_SECRET_{{ $i }}}
{{- end }}
{{- end }}
{{- end -}}

{{/*
Render secretKeyRef env vars (WEBHOOK_SECRET_N) for a DaemonSet container, one
per webhook that has a secret, sourced from the nodevitals-webhooks Secret.
Empty when no webhook has a secret.
*/}}
{{- define "nodevitals.webhookSecretEnv" -}}
{{- range $i, $w := .Values.webhooks }}
{{- if $w.secret }}
- name: WEBHOOK_SECRET_{{ $i }}
  valueFrom:
    secretKeyRef:
      name: nodevitals-webhooks
      key: secret-{{ $i }}
{{- end }}
{{- end }}
{{- end -}}

{{/*
The enabled tiers, in a fixed order, as a space-separated string. Fixed order
keeps the rendered config (and therefore its checksum) stable across upgrades.
*/}}
{{- define "nodevitals.enabledTiers" -}}
{{- $t := list -}}
{{- if .Values.tiers.core.enabled }}{{- $t = append $t "core" }}{{- end }}
{{- if .Values.tiers.smart.enabled }}{{- $t = append $t "smart" }}{{- end }}
{{- if .Values.tiers.gpu.enabled }}{{- $t = append $t "gpu" }}{{- end }}
{{- join " " $t -}}
{{- end -}}

{{/*
Pod-template annotations that roll a tier's DaemonSet when its config changes.

The agent reads /etc/nodevitals/config.yaml once at startup and never re-reads
it, and a webhook secret is resolved through env at the same moment. Without
these checksums a `helm upgrade` that only edits rules, thresholds, or a
signing secret rewrites the ConfigMap/Secret but leaves the pod template
untouched — so no rollout happens and the change is silently ignored with no
error anywhere. Hashing the rendered ConfigMap and Secret into the template
makes the content part of the pod spec, so any edit triggers a normal rolling
restart.

Call with (dict "ctx" . "tier" "<core|smart|gpu>").
*/}}
{{- define "nodevitals.configChecksums" -}}
{{- $ctx := .ctx -}}
{{- $suffix := ternary "" (printf "-%s" .tier) (eq .tier "core") -}}
checksum/config: {{ include (print $ctx.Template.BasePath "/configmap" $suffix ".yaml") $ctx | sha256sum }}
checksum/webhook-secret: {{ include (print $ctx.Template.BasePath "/secret.yaml") $ctx | sha256sum }}
{{- end -}}

{{/*
hostNetwork for a pod spec. /proc/net resolves against the *reading task's*
network namespace, not the mounted path — so a pod-network container reading
/host/proc/net/dev sees its own eth0 instead of the host's interfaces. The
embedded node_exporter's netdev/netclass/sockstat collectors are therefore
wrong without this, which is why upstream node_exporter runs host-networked.
*/}}
{{- define "nodevitals.hostNetwork" -}}
{{- if or .Values.hostNetwork .Values.nodeExporter.enabled }}
hostNetwork: true
dnsPolicy: ClusterFirstWithHostNet
{{- end }}
{{- end -}}

{{/*
Extra volumeMounts the embedded node_exporter needs: the host root for the
filesystem collector, and the textfile directory an external emitter (e.g. an
ansible role writing SMART) drops .prom files into.
*/}}
{{- define "nodevitals.nodeExporterMounts" -}}
{{- if .Values.nodeExporter.enabled }}
{{- if .Values.nodeExporter.mountRootFS }}
- name: rootfs
  mountPath: /host/root
  readOnly: true
  mountPropagation: HostToContainer
{{- end }}
{{- with .Values.nodeExporter.textfileDir }}
- name: textfile
  mountPath: {{ . | quote }}
  readOnly: true
{{- end }}
{{- if .Values.nodeExporter.mountUdev }}
- name: udev
  mountPath: /run/udev
  readOnly: true
{{- end }}
{{- end }}
{{- end -}}

{{- define "nodevitals.nodeExporterVolumes" -}}
{{- if .Values.nodeExporter.enabled }}
{{- if .Values.nodeExporter.mountRootFS }}
- name: rootfs
  hostPath:
    path: /
{{- end }}
{{- with .Values.nodeExporter.textfileDir }}
- name: textfile
  hostPath:
    path: {{ . | quote }}
    type: DirectoryOrCreate
{{- end }}
{{- if .Values.nodeExporter.mountUdev }}
- name: udev
  hostPath:
    path: /run/udev
    type: DirectoryOrCreate
{{- end }}
{{- end }}
{{- end -}}

{{/*
Writable hostPath for the long-term downsampled history store (bbolt file).
Unlike every other mount (proc/sys/dev/rootfs/textfile/udev), this one is
read-write — the container writes its own history.db here. The pod-level
securityContext relaxes to root exactly when this is enabled (see
daemonset-single.yaml / daemonset-gpu.yaml), which is what makes writing to
a root-owned DirectoryOrCreate hostPath work without an extra init container.
*/}}
{{- define "nodevitals.historyMounts" -}}
{{- if .Values.history.enabled }}
- name: history
  mountPath: {{ .Values.history.mountPath | quote }}
{{- end }}
{{- end -}}

{{- define "nodevitals.historyVolumes" -}}
{{- if .Values.history.enabled }}
- name: history
  hostPath:
    path: {{ .Values.history.hostPath | quote }}
    type: DirectoryOrCreate
{{- end }}
{{- end -}}
