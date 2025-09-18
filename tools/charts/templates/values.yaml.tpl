# Default values for {{APP_NAME}}
# This is a YAML-formatted file.

replicaCount: 1

image:
  repository: {{IMAGE_REPO}}
  tag: "{{APP_VERSION}}"
  pullPolicy: IfNotPresent

imagePullSecrets: []
nameOverride: ""
fullnameOverride: ""

serviceAccount:
  # Specifies whether a service account should be created
  create: true
  # Annotations to add to the service account
  annotations: {}
  # The name of the service account to use.
  # If not set and create is true, a name is generated using the fullname template
  name: ""

podAnnotations: {}

podSecurityContext: {}
  # fsGroup: 2000

securityContext: {}
  # capabilities:
  #   drop:
  #   - ALL
  # readOnlyRootFilesystem: true
  # runAsNonRoot: true
  # runAsUser: 1000

service:
  type: ClusterIP
  port: 80
  targetPort: 8000

ingress:
  enabled: false
  className: ""
  annotations: {}
    # kubernetes.io/ingress.class: nginx
    # kubernetes.io/tls-acme: "true"
  hosts:
    - host: {{APP_NAME}}.local
      paths:
        - path: /
          pathType: Prefix
  tls: []
  #  - secretName: {{APP_NAME}}-tls
  #    hosts:
  #      - {{APP_NAME}}.local

resources: {}
  # We usually recommend not to specify default resources and to leave this as a conscious
  # choice for the user. This also increases chances charts run on environments with little
  # resources, such as Minikube. If you do want to specify resources, uncomment the following
  # lines, adjust them as necessary, and remove the curly braces after 'resources:'.
  # limits:
  #   cpu: 100m
  #   memory: 128Mi
  # requests:
  #   cpu: 100m
  #   memory: 128Mi

autoscaling:
  enabled: false
  minReplicas: 1
  maxReplicas: 100
  targetCPUUtilizationPercentage: 80
  # targetMemoryUtilizationPercentage: 80

nodeSelector: {}

tolerations: []

affinity: {}

# Application-specific configuration
app:
  # Environment variables for the application
  env: {}
    # Example:
    # LOG_LEVEL: info
    # DEBUG: "false"
  
  # Port the application listens on
  port: 8000
  
  # Health check configuration
  healthCheck:
    enabled: true
    path: /docs  # FastAPI automatically provides /docs endpoint
    initialDelaySeconds: 30
    periodSeconds: 10