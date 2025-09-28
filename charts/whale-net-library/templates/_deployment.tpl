{{/*
Deployment template for whale-net apps
Usage: 
  {{- include "whale-net.app.deployment" (merge (dict "appName" "api") .Values.apps.api .) }}
*/}}
{{- define "whale-net.app.deployment" -}}
apiVersion: apps/v1
kind: Deployment
metadata:
  name: {{ include "whale-net.app.fullname" . }}
  labels:
    {{- include "whale-net.app.labels" . | nindent 4 }}
spec:
  {{- if not .autoscaling.enabled }}
  replicas: {{ .replicas | default 1 }}
  {{- end }}
  selector:
    matchLabels:
      {{- include "whale-net.app.selectorLabels" . | nindent 6 }}
  template:
    metadata:
      annotations:
        checksum/config: {{ include (print $.Template.BasePath "/configmap.yaml") . | sha256sum }}
      labels:
        {{- include "whale-net.app.selectorLabels" . | nindent 8 }}
    spec:
      {{- with .imagePullSecrets }}
      imagePullSecrets:
        {{- toYaml . | nindent 8 }}
      {{- end }}
      securityContext:
        {{- toYaml .podSecurityContext | nindent 8 }}
      containers:
      - name: {{ .appName }}
        securityContext:
          {{- toYaml .securityContext | nindent 12 }}
        image: "{{ .image.repository }}:{{ .image.tag | default .Chart.AppVersion }}"
        imagePullPolicy: {{ .image.pullPolicy | default "IfNotPresent" }}
        ports:
        - name: http
          containerPort: {{ .service.port | default 8000 }}
          protocol: TCP
        {{- if .healthcheck.enabled | default true }}
        livenessProbe:
          httpGet:
            path: {{ .healthcheck.path | default "/health" }}
            port: http
          initialDelaySeconds: {{ .healthcheck.initialDelaySeconds | default 30 }}
          periodSeconds: {{ .healthcheck.periodSeconds | default 10 }}
        readinessProbe:
          httpGet:
            path: {{ .healthcheck.path | default "/health" }}
            port: http
          initialDelaySeconds: {{ .healthcheck.initialDelaySeconds | default 5 }}
          periodSeconds: {{ .healthcheck.periodSeconds | default 5 }}
        {{- end }}
        resources:
          {{- toYaml .resources | nindent 12 }}
        env:
        - name: APP_NAME
          value: {{ .appName | quote }}
        - name: APP_ENV
          value: {{ .global.env | default "dev" | quote }}
        {{- if .env }}
        {{- range $key, $value := .env }}
        - name: {{ $key | upper }}
          value: {{ $value | quote }}
        {{- end }}
        {{- end }}
        {{- if .envFrom }}
        envFrom:
          {{- toYaml .envFrom | nindent 12 }}
        {{- end }}
        {{- if .volumeMounts }}
        volumeMounts:
          {{- toYaml .volumeMounts | nindent 12 }}
        {{- end }}
      {{- if .volumes }}
      volumes:
        {{- toYaml .volumes | nindent 8 }}
      {{- end }}
      {{- with .nodeSelector }}
      nodeSelector:
        {{- toYaml . | nindent 8 }}
      {{- end }}
      {{- with .affinity }}
      affinity:
        {{- toYaml . | nindent 8 }}
      {{- end }}
      {{- with .tolerations }}
      tolerations:
        {{- toYaml . | nindent 8 }}
      {{- end }}
{{- end }}