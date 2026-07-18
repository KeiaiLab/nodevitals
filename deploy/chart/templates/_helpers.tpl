{{- define "nodevitals.name" -}}nodevitals{{- end -}}
{{- define "nodevitals.image" -}}
{{- $tag := .Values.image.tag | default .Chart.AppVersion -}}
{{ .Values.image.repository }}:{{ $tag }}
{{- end -}}
