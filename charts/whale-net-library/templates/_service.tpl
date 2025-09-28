{{/*
Service template for whale-net apps
Usage:
  {{- include "whale-net.app.service" (merge (dict "appName" "api") .Values.apps.api .) }}
*/}}
{{- define "whale-net.app.service" -}}
{{- if .service.enabled | default true }}
apiVersion: v1
kind: Service
metadata:
  name: {{ include "whale-net.app.fullname" . }}-service
  labels:
    {{- include "whale-net.app.labels" . | nindent 4 }}
  {{- with .service.annotations }}
  annotations:
    {{- toYaml . | nindent 4 }}
  {{- end }}
spec:
  type: {{ .service.type | default "ClusterIP" }}
  ports:
  - port: {{ .service.port | default 8000 }}
    targetPort: http
    protocol: TCP
    name: http
  {{- if eq (.service.type | default "ClusterIP") "NodePort" }}
  {{- if .service.nodePort }}
  - nodePort: {{ .service.nodePort }}
  {{- end }}
  {{- end }}
  selector:
    {{- include "whale-net.app.selectorLabels" . | nindent 4 }}
{{- end }}
{{- end }}