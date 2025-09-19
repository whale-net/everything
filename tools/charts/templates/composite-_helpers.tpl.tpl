{{/* Composite chart helpers */}}
{{/*
Expand the name of the chart.
*/}}
{{- define "{{COMPOSITE_NAME}}.name" -}}
{{- default .Chart.Name .Values.nameOverride | trunc 63 | trimSuffix "-" }}
{{- end }}

{{/*
Create a default fully qualified app name.
*/}}
{{- define "{{COMPOSITE_NAME}}.fullname" -}}
{{- if .Values.fullnameOverride }}
{{- .Values.fullnameOverride | trunc 63 | trimSuffix "-" }}
{{- else }}
{{- $name := default .Chart.Name .Values.nameOverride }}
{{- if contains $name .Release.Name }}
{{- .Release.Name | trunc 63 | trimSuffix "-" }}
{{- else }}
{{- printf "%s-%s" .Release.Name $name | trunc 63 | trimSuffix "-" }}
{{- end }}
{{- end }}
{{- end }}

{{/*
Create chart name and version as used by the chart label.
*/}}
{{- define "{{COMPOSITE_NAME}}.chart" -}}
{{- printf "%s-%s" .Chart.Name .Chart.Version | replace "+" "_" | trunc 63 | trimSuffix "-" }}
{{- end }}

{{/*
Common labels for all apps
*/}}
{{- define "{{COMPOSITE_NAME}}.labels" -}}
helm.sh/chart: {{ include "{{COMPOSITE_NAME}}.chart" . }}
{{- if .Chart.AppVersion }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
{{- end }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
whale-net.io/domain: {{DOMAIN}}
whale-net.io/type: composite
{{- end }}

{{/*
Create the name of the service account to use
*/}}
{{- define "{{COMPOSITE_NAME}}.serviceAccountName" -}}
{{- if .Values.sharedResources.serviceAccount.create }}
{{- default (include "{{COMPOSITE_NAME}}.fullname" .) .Values.sharedResources.serviceAccount.name }}
{{- else }}
{{- default "default" .Values.sharedResources.serviceAccount.name }}
{{- end }}
{{- end }}

{{/*
Generate selector labels for a specific app
*/}}
{{- define "{{COMPOSITE_NAME}}.appSelectorLabels" -}}
app.kubernetes.io/name: {{ .appName }}
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end }}

{{/*
Generate labels for a specific app
*/}}
{{- define "{{COMPOSITE_NAME}}.appLabels" -}}
{{ include "{{COMPOSITE_NAME}}.labels" . }}
{{ include "{{COMPOSITE_NAME}}.appSelectorLabels" . }}
whale-net.io/app: {{ .appName }}
{{- end }}