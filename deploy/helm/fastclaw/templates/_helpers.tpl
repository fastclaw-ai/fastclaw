{{- define "fastclaw.fullname" -}}
{{- .Release.Name | trunc 63 | trimSuffix "-" -}}
{{- end -}}

{{- define "fastclaw.labels" -}}
app.kubernetes.io/name: fastclaw
app.kubernetes.io/instance: {{ .Release.Name }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
{{- end -}}

{{- define "fastclaw.dsn" -}}
{{- if .Values.externalDSN -}}
{{ .Values.externalDSN }}
{{- else -}}
postgres://fastclaw:{{ .Values.postgres.password }}@{{ include "fastclaw.fullname" . }}-db:5432/fastclaw?sslmode=disable
{{- end -}}
{{- end -}}

{{- define "fastclaw.adminToken" -}}
{{- if .Values.gateway.adminToken -}}
{{ .Values.gateway.adminToken }}
{{- else -}}
{{ randAlphaNum 64 }}
{{- end -}}
{{- end -}}
