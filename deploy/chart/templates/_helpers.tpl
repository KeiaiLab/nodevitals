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
