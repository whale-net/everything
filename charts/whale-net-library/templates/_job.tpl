{{/*
Job template for whale-net apps (useful for migrations, etc.)
Usage:
  {{- include "whale-net.job" (dict "jobName" "migrations" "image" .Values.apps.api.image "command" ["python", "migrate.py"] "Chart" .Chart "Release" .Release) }}
*/}}
{{- define "whale-net.job" -}}
apiVersion: batch/v1
kind: Job
metadata:
  name: {{ include "whale-net.fullname" . }}-{{ .jobName }}
  labels:
    {{- include "whale-net.labels" . | nindent 4 }}
    app.kubernetes.io/component: {{ .jobName }}
  annotations:
    "helm.sh/hook": {{ .hookType | default "pre-install,pre-upgrade" }}
    "helm.sh/hook-weight": {{ .hookWeight | default "-10" | quote }}
    "helm.sh/hook-delete-policy": {{ .hookDeletePolicy | default "before-hook-creation" }}
spec:
  {{- if .ttlSecondsAfterFinished }}
  ttlSecondsAfterFinished: {{ .ttlSecondsAfterFinished }}
  {{- end }}
  {{- if .backoffLimit }}
  backoffLimit: {{ .backoffLimit }}
  {{- end }}
  template:
    metadata:
      labels:
        {{- include "whale-net.selectorLabels" . | nindent 8 }}
        app.kubernetes.io/component: {{ .jobName }}
    spec:
      restartPolicy: {{ .restartPolicy | default "Never" }}
      containers:
      - name: {{ .jobName }}
        image: "{{ .image.repository }}:{{ .image.tag | default .Chart.AppVersion }}"
        imagePullPolicy: {{ .image.pullPolicy | default "IfNotPresent" }}
        {{- if .command }}
        command:
          {{- toYaml .command | nindent 12 }}
        {{- end }}
        {{- if .args }}
        args:
          {{- toYaml .args | nindent 12 }}
        {{- end }}
        env:
        - name: JOB_NAME
          value: {{ .jobName | quote }}
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
        {{- if .resources }}
        resources:
          {{- toYaml .resources | nindent 12 }}
        {{- end }}
        {{- if .volumeMounts }}
        volumeMounts:
          {{- toYaml .volumeMounts | nindent 12 }}
        {{- end }}
      {{- if .volumes }}
      volumes:
        {{- toYaml .volumes | nindent 8 }}
      {{- end }}
{{- end }}