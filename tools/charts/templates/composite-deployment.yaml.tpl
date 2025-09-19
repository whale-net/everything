{{/* Generate deployments for each enabled app */}}
{{#APPS}}
{{- if .Values.apps.{{APP_NAME}}.enabled }}
---
apiVersion: apps/v1
kind: Deployment
metadata:
  name: {{ include "{{COMPOSITE_NAME}}.fullname" . }}-{{APP_NAME}}
  labels:
    {{- include "{{COMPOSITE_NAME}}.appLabels" (dict "appName" "{{APP_NAME}}" "Chart" .Chart "Release" .Release "Values" .Values) | nindent 4 }}
spec:
  {{- if not .Values.apps.{{APP_NAME}}.autoscaling.enabled }}
  replicas: {{ .Values.apps.{{APP_NAME}}.replicaCount }}
  {{- end }}
  selector:
    matchLabels:
      {{- include "{{COMPOSITE_NAME}}.appSelectorLabels" (dict "appName" "{{APP_NAME}}" "Release" .Release) | nindent 6 }}
  template:
    metadata:
      labels:
        {{- include "{{COMPOSITE_NAME}}.appSelectorLabels" (dict "appName" "{{APP_NAME}}" "Release" .Release) | nindent 8 }}
    spec:
      {{- with .Values.global.imagePullSecrets }}
      imagePullSecrets:
        {{- toYaml . | nindent 8 }}
      {{- end }}
      serviceAccountName: {{ include "{{COMPOSITE_NAME}}.serviceAccountName" . }}
      securityContext:
        {{- toYaml .Values.sharedResources.podSecurityContext | nindent 8 }}
      containers:
        - name: {{APP_NAME}}
          securityContext:
            {{- toYaml .Values.sharedResources.securityContext | nindent 12 }}
          image: "{{ .Values.apps.{{APP_NAME}}.image.repository }}:{{ .Values.apps.{{APP_NAME}}.image.tag }}"
          imagePullPolicy: {{ .Values.apps.{{APP_NAME}}.image.pullPolicy }}
          ports:
            - name: http
              containerPort: {{ .Values.apps.{{APP_NAME}}.config.port }}
              protocol: TCP
          {{- if .Values.apps.{{APP_NAME}}.healthCheck.enabled }}
          livenessProbe:
            httpGet:
              path: {{ .Values.apps.{{APP_NAME}}.healthCheck.path }}
              port: http
            initialDelaySeconds: {{ .Values.apps.{{APP_NAME}}.healthCheck.initialDelaySeconds }}
            periodSeconds: {{ .Values.apps.{{APP_NAME}}.healthCheck.periodSeconds }}
          readinessProbe:
            httpGet:
              path: {{ .Values.apps.{{APP_NAME}}.healthCheck.path }}
              port: http
            initialDelaySeconds: {{ .Values.apps.{{APP_NAME}}.healthCheck.initialDelaySeconds }}
            periodSeconds: {{ .Values.apps.{{APP_NAME}}.healthCheck.periodSeconds }}
          {{- end }}
          {{- if .Values.apps.{{APP_NAME}}.config.env }}
          env:
            {{- range $key, $value := .Values.apps.{{APP_NAME}}.config.env }}
            - name: {{ $key }}
              value: {{ $value | quote }}
            {{- end }}
          {{- end }}
          resources:
            {{- toYaml .Values.apps.{{APP_NAME}}.resources | nindent 12 }}
      {{- with .Values.sharedResources.nodeSelector }}
      nodeSelector:
        {{- toYaml . | nindent 8 }}
      {{- end }}
      {{- with .Values.sharedResources.affinity }}
      affinity:
        {{- toYaml . | nindent 8 }}
      {{- end }}
      {{- with .Values.sharedResources.tolerations }}
      tolerations:
        {{- toYaml . | nindent 8 }}
      {{- end }}
{{- end }}
{{/APPS}}