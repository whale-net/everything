{{/*
Multi-app Helm chart helpers for domain-based deployments
*/}}

{{/*
Expand the chart name using domain-app pattern
*/}}
{{- define "multiapp.chartname" -}}
{{- printf "%s-%s" .Chart.Name .Values.domain | trunc 63 | trimSuffix "-" }}
{{- end }}

{{/*
Create a default fully qualified app name for a specific app
*/}}
{{- define "multiapp.fullname" -}}
{{- $chartName := include "multiapp.chartname" . }}
{{- $appName := .appName }}
{{- printf "%s-%s" $chartName $appName | trunc 63 | trimSuffix "-" }}
{{- end }}

{{/*
Common labels for all apps in the chart
*/}}
{{- define "multiapp.labels" -}}
helm.sh/chart: {{ include "multiapp.chartname" . }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
app.kubernetes.io/part-of: {{ .Values.domain }}
{{- if .Chart.AppVersion }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
{{- end }}
{{- end }}

{{/*
Selector labels for a specific app
*/}}
{{- define "multiapp.selectorLabels" -}}
app.kubernetes.io/name: {{ include "multiapp.chartname" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
app.kubernetes.io/component: {{ .appName }}
{{- end }}

{{/*
App-specific labels combining common and selector labels
*/}}
{{- define "multiapp.appLabels" -}}
{{ include "multiapp.labels" . }}
{{ include "multiapp.selectorLabels" . }}
{{- end }}

{{/*
Generate image reference for an app
*/}}
{{- define "multiapp.image" -}}
{{- $appName := .appName }}
{{- $appImage := index .Values.images $appName }}
{{- if $appImage.digest }}
{{- printf "%s@%s" $appImage.name $appImage.digest }}
{{- else }}
{{- printf "%s:%s" $appImage.name $appImage.tag }}
{{- end }}
{{- end }}

{{/*
Get app configuration with defaults
*/}}
{{- define "multiapp.appConfig" -}}
{{- $appName := .appName }}
{{- $domain := .Values.domain }}
{{- $appConfig := index .Values $domain "apps" $appName }}
{{- $appConfig | toYaml }}
{{- end }}

{{/*
Check if an app is enabled
*/}}
{{- define "multiapp.appEnabled" -}}
{{- $appName := .appName }}
{{- $domain := .Values.domain }}
{{- $appConfig := index .Values $domain "apps" $appName }}
{{- $appConfig.enabled | default true }}
{{- end }}

{{/*
Generate environment variables for an app
*/}}
{{- define "multiapp.appEnv" -}}
{{- $appName := .appName }}
{{- $domain := .Values.domain }}
{{- $appConfig := index .Values $domain "apps" $appName }}
- name: APP_NAME
  value: {{ $appName | quote }}
- name: APP_DOMAIN  
  value: {{ $domain | quote }}
- name: APP_VERSION
  value: {{ $appConfig.version | default "latest" | quote }}
{{- if .Values.env }}
{{- range $key, $value := .Values.env }}
- name: {{ $key }}
  value: {{ $value | quote }}
{{- end }}
{{- end }}
{{- if $appConfig.env }}
{{- range $key, $value := $appConfig.env }}
- name: {{ $key }}
  value: {{ $value | quote }}
{{- end }}
{{- end }}
{{- end }}

{{/*
Generate resource specifications for an app
*/}}
{{- define "multiapp.resources" -}}
{{- $appName := .appName }}
{{- $domain := .Values.domain }}
{{- $appConfig := index .Values $domain "apps" $appName }}
{{- if $appConfig.resources }}
{{- $appConfig.resources | toYaml }}
{{- else if .Values.resources }}
{{- .Values.resources | toYaml }}
{{- end }}
{{- end }}

{{/*
Generate service account name for an app
*/}}
{{- define "multiapp.serviceAccountName" -}}
{{- $appName := .appName }}
{{- $domain := .Values.domain }}
{{- $appConfig := index .Values $domain "apps" $appName }}
{{- if $appConfig.serviceAccount }}
{{- if $appConfig.serviceAccount.create }}
{{- default (include "multiapp.fullname" .) $appConfig.serviceAccount.name }}
{{- else }}
{{- default "default" $appConfig.serviceAccount.name }}
{{- end }}
{{- else }}
{{- "default" }}
{{- end }}
{{- end }}