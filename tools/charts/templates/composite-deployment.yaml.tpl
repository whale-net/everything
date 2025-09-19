# Multi-app deployment template
# This is a simplified template - in a complete implementation,
# this would be generated dynamically for each app in the composite chart

# Example deployment structure - actual deployments would be generated
# based on the apps parameter in the release_composite_helm_chart macro

# Note: This template serves as documentation of the intended structure.
# The actual composite chart implementation would generate separate deployment
# YAML sections for each app specified in the apps list.

# Each app would get a deployment like:
#
# ---
# apiVersion: apps/v1
# kind: Deployment
# metadata:
#   name: {{ include "COMPOSITE_NAME.fullname" . }}-APP_NAME
#   labels:
#     app.kubernetes.io/name: APP_NAME
#     app.kubernetes.io/instance: {{ .Release.Name }}
# spec:
#   replicas: {{ .Values.apps.APP_NAME.replicaCount }}
#   selector:
#     matchLabels:
#       app.kubernetes.io/name: APP_NAME
#       app.kubernetes.io/instance: {{ .Release.Name }}
#   template:
#     metadata:
#       labels:
#         app.kubernetes.io/name: APP_NAME
#         app.kubernetes.io/instance: {{ .Release.Name }}
#     spec:
#       containers:
#         - name: APP_NAME
#           image: "IMAGE_REPO:APP_VERSION"
#           ports:
#             - containerPort: SERVICE_PORT

# This template needs to be enhanced to generate actual deployments