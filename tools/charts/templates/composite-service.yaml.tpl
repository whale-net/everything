{{/* Generate services for each enabled app */}}
{{#APPS}}
{{- if .Values.apps.{{APP_NAME}}.enabled }}
---
apiVersion: v1
kind: Service
metadata:
  name: {{ include "{{COMPOSITE_NAME}}.fullname" . }}-{{APP_NAME}}
  labels:
    {{- include "{{COMPOSITE_NAME}}.appLabels" (dict "appName" "{{APP_NAME}}" "Chart" .Chart "Release" .Release "Values" .Values) | nindent 4 }}
spec:
  type: {{ .Values.apps.{{APP_NAME}}.service.type }}
  ports:
    - port: {{ .Values.apps.{{APP_NAME}}.service.port }}
      targetPort: {{ .Values.apps.{{APP_NAME}}.service.targetPort }}
      protocol: TCP
      name: http
  selector:
    {{- include "{{COMPOSITE_NAME}}.appSelectorLabels" (dict "appName" "{{APP_NAME}}" "Release" .Release) | nindent 4 }}
{{- end }}
{{/APPS}}