{{/*
Whale-net library chart helpers for app composition
*/}}

{{/*
Expand the name of the chart.
*/}}
{{- define "whale-net.name" -}}
{{- default .Chart.Name .Values.nameOverride | trunc 63 | trimSuffix "-" }}
{{- end }}

{{/*
Create a default fully qualified app name.
*/}}
{{- define "whale-net.fullname" -}}
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
Create a fully qualified app name for a specific app.
*/}}
{{- define "whale-net.app.fullname" -}}
{{- $appName := .appName -}}
{{- if .Values.fullnameOverride }}
{{- printf "%s-%s" .Values.fullnameOverride $appName | trunc 63 | trimSuffix "-" }}
{{- else }}
{{- $name := default .Chart.Name .Values.nameOverride }}
{{- if contains $name .Release.Name }}
{{- printf "%s-%s" .Release.Name $appName | trunc 63 | trimSuffix "-" }}
{{- else }}
{{- printf "%s-%s-%s" .Release.Name $name $appName | trunc 63 | trimSuffix "-" }}
{{- end }}
{{- end }}
{{- end }}

{{/*
Create chart name and version as used by the chart label.
*/}}
{{- define "whale-net.chart" -}}
{{- printf "%s-%s" .Chart.Name .Chart.Version | replace "+" "_" | trunc 63 | trimSuffix "-" }}
{{- end }}

{{/*
Common labels
*/}}
{{- define "whale-net.labels" -}}
helm.sh/chart: {{ include "whale-net.chart" . }}
{{ include "whale-net.selectorLabels" . }}
{{- if .Chart.AppVersion }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
{{- end }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
{{- end }}

{{/*
Selector labels
*/}}
{{- define "whale-net.selectorLabels" -}}
app.kubernetes.io/name: {{ include "whale-net.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end }}

{{/*
App-specific selector labels
*/}}
{{- define "whale-net.app.selectorLabels" -}}
app.kubernetes.io/name: {{ include "whale-net.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
app.kubernetes.io/component: {{ .appName }}
{{- end }}

{{/*
App-specific labels
*/}}
{{- define "whale-net.app.labels" -}}
{{ include "whale-net.labels" . }}
app.kubernetes.io/component: {{ .appName }}
{{- end }}