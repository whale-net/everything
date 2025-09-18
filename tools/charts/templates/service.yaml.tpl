apiVersion: v1
kind: Service
metadata:
  name: {{ include "{{APP_NAME}}.fullname" . }}
  labels:
    {{- include "{{APP_NAME}}.labels" . | nindent 4 }}
spec:
  type: {{ .Values.service.type }}
  ports:
    - port: {{ .Values.service.port }}
      targetPort: {{ .Values.service.targetPort }}
      protocol: TCP
      name: http
  selector:
    {{- include "{{APP_NAME}}.selectorLabels" . | nindent 4 }}