{{- if .Values.sharedResources.serviceAccount.create -}}
apiVersion: v1
kind: ServiceAccount
metadata:
  name: {{ include "{{COMPOSITE_NAME}}.serviceAccountName" . }}
  labels:
    {{- include "{{COMPOSITE_NAME}}.labels" . | nindent 4 }}
  {{- with .Values.sharedResources.serviceAccount.annotations }}
  annotations:
    {{- toYaml . | nindent 4 }}
  {{- end }}
{{- end }}