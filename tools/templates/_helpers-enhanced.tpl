{{/*
Enhanced multi-app Helm chart helpers with optional components
Supports PDB, HPA, Ingress as configurable features
*/}}

{{/*
Expand the chart name using domain-app pattern
*/}}
{{- define "common.chartname" -}}
{{- printf "%s-%s" .Chart.Name .Values.domain | trunc 63 | trimSuffix "-" }}
{{- end }}

{{/*
Create a default fully qualified app name for a specific app
*/}}
{{- define "common.fullname" -}}
{{- $chartName := include "common.chartname" . }}
{{- $appName := .appName }}
{{- printf "%s-%s" $chartName $appName | trunc 63 | trimSuffix "-" }}
{{- end }}

{{/*
Create a service name for an app
*/}}
{{- define "common.serviceName" -}}
{{- printf "%s-service" (include "common.fullname" .) }}
{{- end }}

{{/*
Common labels for all apps in the chart
*/}}
{{- define "common.labels" -}}
helm.sh/chart: {{ include "common.chartname" . }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
app.kubernetes.io/part-of: {{ .Values.domain }}
{{- if .Chart.AppVersion }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
{{- end }}
{{- end }}

{{/*
Selector labels for a specific app
*/}}
{{- define "common.selectorLabels" -}}
app.kubernetes.io/name: {{ include "common.chartname" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
app.kubernetes.io/component: {{ .appName }}
{{- end }}

{{/*
App-specific labels combining common and selector labels
*/}}
{{- define "common.appLabels" -}}
{{ include "common.labels" . }}
{{ include "common.selectorLabels" . }}
{{- end }}

{{/*
Generate image reference for an app
*/}}
{{- define "common.image" -}}
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
{{- define "common.appConfig" -}}
{{- $appName := .appName }}
{{- $domain := .Values.domain }}
{{- $appConfig := index .Values $domain "apps" $appName }}
{{- $appConfig }}
{{- end }}

{{/*
Check if an app is enabled
*/}}
{{- define "common.appEnabled" -}}
{{- $appName := .appName }}
{{- $domain := .Values.domain }}
{{- $appConfig := index .Values $domain "apps" $appName }}
{{- $appConfig.enabled | default true }}
{{- end }}

{{/*
Get app type (api, processor, job, etc.)
*/}}
{{- define "common.appType" -}}
{{- $appName := .appName }}
{{- $domain := .Values.domain }}
{{- $appConfig := index .Values $domain "apps" $appName }}
{{- $appConfig.type | default "api" }}
{{- end }}

{{/*
Check if app needs a service (API apps only)
*/}}
{{- define "common.needsService" -}}
{{- $appType := include "common.appType" . }}
{{- eq $appType "api" }}
{{- end }}

{{/*
Check if PDB is enabled for this app
*/}}
{{- define "common.pdbEnabled" -}}
{{- $appName := .appName }}
{{- $domain := .Values.domain }}
{{- $appConfig := index .Values $domain "apps" $appName }}
{{- $globalPdb := .Values.global.podDisruptionBudget.enabled | default false }}
{{- $appPdb := $appConfig.podDisruptionBudget.enabled | default $globalPdb }}
{{- and $appPdb (gt ($appConfig.replicas | default 1 | int) 1) }}
{{- end }}

{{/*
Check if HPA is enabled for this app
*/}}
{{- define "common.hpaEnabled" -}}
{{- $appName := .appName }}
{{- $domain := .Values.domain }}
{{- $appConfig := index .Values $domain "apps" $appName }}
{{- $globalHpa := .Values.global.autoscaling.enabled | default false }}
{{- $appHpa := $appConfig.autoscaling.enabled | default $globalHpa }}
{{- $appHpa }}
{{- end }}

{{/*
Check if Ingress is enabled and this app should be exposed
*/}}
{{- define "common.ingressEnabled" -}}
{{- $appName := .appName }}
{{- $domain := .Values.domain }}
{{- $appConfig := index .Values $domain "apps" $appName }}
{{- $globalIngress := .Values.ingress.enabled | default false }}
{{- $appIngress := $appConfig.ingress.enabled | default false }}
{{- $appType := include "common.appType" . }}
{{- and $globalIngress (eq $appType "api") (or $appIngress (has $appName .Values.ingress.exposedApps)) }}
{{- end }}

{{/*
Generate environment variables for an app
*/}}
{{- define "common.appEnv" -}}
{{- $appName := .appName }}
{{- $domain := .Values.domain }}
{{- $appConfig := index .Values $domain "apps" $appName }}
- name: APP_NAME
  value: {{ $appName | quote }}
- name: APP_DOMAIN  
  value: {{ $domain | quote }}
- name: APP_VERSION
  value: {{ $appConfig.version | default "latest" | quote }}
{{- if .Values.global.env }}
{{- range $key, $value := .Values.global.env }}
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
Generate resource configuration for an app
*/}}
{{- define "common.resources" -}}
{{- $appName := .appName }}
{{- $domain := .Values.domain }}
{{- $appConfig := index .Values $domain "apps" $appName }}
{{- if $appConfig.resources }}
{{- toYaml $appConfig.resources }}
{{- else if .Values.global.resources }}
{{- toYaml .Values.global.resources }}
{{- end }}
{{- end }}

{{/*
Generate service account name
*/}}
{{- define "common.serviceAccountName" -}}
{{- $appName := .appName }}
{{- $domain := .Values.domain }}
{{- $appConfig := index .Values $domain "apps" $appName }}
{{- if $appConfig.serviceAccount }}
{{- $appConfig.serviceAccount }}
{{- else }}
{{- printf "%s-sa" (include "common.fullname" .) }}
{{- end }}
{{- end }}

{{/*
Generate args for an app with common patterns
*/}}
{{- define "common.appArgs" -}}
{{- $appName := .appName }}
{{- $domain := .Values.domain }}
{{- $appConfig := index .Values $domain "apps" $appName }}
{{- if $appConfig.args }}
{{- toYaml $appConfig.args }}
{{- else if $appConfig.command }}
- {{ $domain }}
- {{ $appConfig.command }}
{{- if .Values.global.commonArgs }}
{{- toYaml .Values.global.commonArgs }}
{{- end }}
{{- end }}
{{- end }}

{{/*
Generate ingress path for an app
*/}}
{{- define "common.ingressPath" -}}
{{- $appName := .appName }}
{{- $domain := .Values.domain }}
{{- $appConfig := index .Values $domain "apps" $appName }}
{{- if $appConfig.ingress.path }}
{{- $appConfig.ingress.path }}
{{- else if index .Values.ingress.appPaths $appName }}
{{- index .Values.ingress.appPaths $appName }}
{{- else }}
{{- printf "/%s" $appName }}
{{- end }}
{{- end }}

{{/*
Generate health check path for an app
*/}}
{{- define "common.healthPath" -}}
{{- $appName := .appName }}
{{- $domain := .Values.domain }}
{{- $appConfig := index .Values $domain "apps" $appName }}
{{- $appConfig.healthPath | default "/health" }}
{{- end }}