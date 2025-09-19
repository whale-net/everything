# Multi-app services template
# This is a simplified template - in a complete implementation,
# this would generate separate service YAML sections for each app

# Note: This template serves as documentation of the intended structure.
# The actual composite chart implementation would generate separate service
# YAML sections for each app specified in the apps list.

# Each app would get a service like:
#
# ---
# apiVersion: v1
# kind: Service
# metadata:
#   name: {{ include "COMPOSITE_NAME.fullname" . }}-APP_NAME
#   labels:
#     app.kubernetes.io/name: APP_NAME
#     app.kubernetes.io/instance: {{ .Release.Name }}
# spec:
#   type: {{ .Values.apps.APP_NAME.service.type }}
#   ports:
#     - port: {{ .Values.apps.APP_NAME.service.port }}
#       targetPort: {{ .Values.apps.APP_NAME.service.targetPort }}
#       protocol: TCP
#       name: http
#   selector:
#     app.kubernetes.io/name: APP_NAME
#     app.kubernetes.io/instance: {{ .Release.Name }}

# This template needs to be enhanced to generate actual services