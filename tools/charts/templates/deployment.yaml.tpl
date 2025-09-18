apiVersion: apps/v1
kind: Deployment
metadata:
  name: {{ include "{{APP_NAME}}.fullname" . }}
  labels:
    {{- include "{{APP_NAME}}.labels" . | nindent 4 }}
spec:
  {{- if not .Values.autoscaling.enabled }}
  replicas: {{ .Values.replicaCount }}
  {{- end }}
  selector:
    matchLabels:
      {{- include "{{APP_NAME}}.selectorLabels" . | nindent 6 }}
  template:
    metadata:
      {{- with .Values.podAnnotations }}
      annotations:
        {{- toYaml . | nindent 8 }}
      {{- end }}
      labels:
        {{- include "{{APP_NAME}}.selectorLabels" . | nindent 8 }}
    spec:
      {{- with .Values.imagePullSecrets }}
      imagePullSecrets:
        {{- toYaml . | nindent 8 }}
      {{- end }}
      serviceAccountName: {{ include "{{APP_NAME}}.serviceAccountName" . }}
      securityContext:
        {{- toYaml .Values.podSecurityContext | nindent 8 }}
      containers:
        - name: {{ .Chart.Name }}
          securityContext:
            {{- toYaml .Values.securityContext | nindent 12 }}
          image: "{{ .Values.image.repository }}:{{ .Values.image.tag | default .Chart.AppVersion }}"
          imagePullPolicy: {{ .Values.image.pullPolicy }}
          ports:
            - name: http
              containerPort: {{ .Values.app.port }}
              protocol: TCP
          {{- if .Values.app.healthCheck.enabled }}
          livenessProbe:
            httpGet:
              path: {{ .Values.app.healthCheck.path }}
              port: http
            initialDelaySeconds: {{ .Values.app.healthCheck.initialDelaySeconds }}
            periodSeconds: {{ .Values.app.healthCheck.periodSeconds }}
          readinessProbe:
            httpGet:
              path: {{ .Values.app.healthCheck.path }}
              port: http
            initialDelaySeconds: {{ .Values.app.healthCheck.initialDelaySeconds }}
            periodSeconds: {{ .Values.app.healthCheck.periodSeconds }}
          {{- end }}
          {{- if .Values.app.env }}
          env:
            {{- range $key, $value := .Values.app.env }}
            - name: {{ $key }}
              value: {{ $value | quote }}
            {{- end }}
          {{- end }}
          resources:
            {{- toYaml .Values.resources | nindent 12 }}
      {{- with .Values.nodeSelector }}
      nodeSelector:
        {{- toYaml . | nindent 8 }}
      {{- end }}
      {{- with .Values.affinity }}
      affinity:
        {{- toYaml . | nindent 8 }}
      {{- end }}
      {{- with .Values.tolerations }}
      tolerations:
        {{- toYaml . | nindent 8 }}
      {{- end }}