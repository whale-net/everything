{{/*
Common labels for all ManMan services
*/}}
{{- define "manman.labels" -}}
helm.sh/chart: {{ include "manman.chart" . }}
app.kubernetes.io/name: {{ include "manman.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
{{- end }}

{{/*
Selector labels
*/}}
{{- define "manman.selectorLabels" -}}
app.kubernetes.io/name: {{ include "manman.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end }}

{{/*
Chart name and version as used by the chart label
*/}}
{{- define "manman.chart" -}}
{{- printf "%s-%s" .Chart.Name .Chart.Version | replace "+" "_" | trunc 63 | trimSuffix "-" }}
{{- end }}

{{/*
Common name for resources
*/}}
{{- define "manman.name" -}}
{{- default .Chart.Name .Values.nameOverride | trunc 63 | trimSuffix "-" }}
{{- end }}

{{/*
Full name generator for services - includes app_env
*/}}
{{- define "manman.serviceName" -}}
{{- $serviceName := .serviceName -}}
{{- $appEnv := .Values.env.app_env -}}
{{- printf "%s-%s" $serviceName $appEnv | trunc 63 | trimSuffix "-" }}
{{- end }}

{{/*
Service selector labels for a specific service
*/}}
{{- define "manman.serviceSelectorLabels" -}}
{{- $serviceName := .serviceName -}}
{{- $appEnv := .Values.env.app_env -}}
app: {{ printf "%s-%s" $serviceName $appEnv }}
{{- end }}

{{/*
Common environment variables for all services
*/}}
{{- define "manman.commonEnv" -}}
- name: MANMAN_POSTGRES_URL
  value: {{ .Values.env.db.url }}
- name: MANMAN_RABBITMQ_HOST
  value: {{ .Values.env.rabbitmq.host }}
- name: MANMAN_RABBITMQ_PORT
  value: {{ .Values.env.rabbitmq.port | quote }}
- name: MANMAN_RABBITMQ_USER
  value: {{ .Values.env.rabbitmq.user }}
- name: MANMAN_RABBITMQ_PASSWORD
  value: {{ .Values.env.rabbitmq.password }}
- name: MANMAN_RABBITMQ_ENABLE_SSL
  value: {{ .Values.env.rabbitmq.enable_ssl | quote }}
- name: MANMAN_RABBITMQ_SSL_HOSTNAME
  value: {{ .Values.env.rabbitmq.ssl_hostname | quote }}
- name: APP_ENV
  value: {{ .Values.env.app_env }}
{{- if .Values.env.otel.logging_enabled }}
- name: OTEL_SERVICE_NAME
  value: {{ include "manman.serviceName" . }}
- name: OTEL_RESOURCE_ATTRIBUTES
  value: "deployment-name={{ include "manman.serviceName" . }}"
- name: OTEL_EXPORTER_OTLP_LOGS_ENDPOINT
  value: {{ .Values.env.otelCollector.logs.endpoint }}
- name: OTEL_EXPORTER_OTLP_TRACES_ENDPOINT
  value: {{ .Values.env.otelCollector.traces.endpoint }}
{{- end }}
{{- end }}

{{/*
Common args for services
*/}}
{{- define "manman.commonArgs" -}}
{{- $service := . -}}
- host
- {{ $service.command }}
- --no-should-run-migration-check
{{- if and $.Values.env.rabbitmq.createVhost (eq $.Values.env.app_env "dev") }}
- --create-vhost
{{- end }}
{{- if $.Values.env.otel.logging_enabled }}
- --log-otlp
{{- end }}
{{- end }}

{{/*
Default resource limits
*/}}
{{- define "manman.defaultResources" -}}
requests:
  cpu: 50m
  memory: 256Mi
limits:
  cpu: 100m
  memory: 512Mi
{{- end }}

{{/*
Generate service full name with suffix
*/}}
{{- define "manman.serviceFullName" -}}
{{- $serviceName := .serviceName -}}
{{- $appEnv := .Values.env.app_env -}}
{{- $suffix := .suffix | default "" -}}
{{- if $suffix }}
{{- printf "%s-%s-%s" $serviceName $appEnv $suffix | trunc 63 | trimSuffix "-" }}
{{- else }}
{{- printf "%s-%s" $serviceName $appEnv | trunc 63 | trimSuffix "-" }}
{{- end }}
{{- end }}