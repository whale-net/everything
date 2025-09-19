# Multi-app composite chart values template
# This chart can deploy multiple applications together

# Global settings for all apps
global:
  imageRegistry: {{GLOBAL_REGISTRY}}
  imagePullSecrets: []
  storageClass: ""

# Configuration for each application in this composite chart
# Note: This is a simplified template - actual implementation would generate
# per-app sections dynamically based on the apps list
apps:
  # App configurations will be generated based on the apps parameter
  # Each app gets a section like:
  # app_name:
  #   enabled: true
  #   image:
  #     repository: image_repo
  #     tag: app_version
  #   service:
  #     port: 80
  #     targetPort: service_port
  
# Shared ingress configuration for all apps
ingress:
  enabled: false
  className: ""
  annotations: {}
    # kubernetes.io/ingress.class: nginx
    # kubernetes.io/tls-acme: "true"
  hosts:
    - host: {{COMPOSITE_NAME}}.local
      paths: []
        # Paths will be generated based on apps
  tls: []

# Shared resources
sharedResources:
  serviceAccount:
    create: true
    annotations: {}
    name: ""
  
  podSecurityContext: {}
  securityContext: {}
  nodeSelector: {}
  tolerations: []
  affinity: {}

# Shared configuration that applies to all apps
shared:
  # Database configuration that all apps might use
  database:
    enabled: false
    # host: postgres.default.svc.cluster.local
    # port: 5432
    # name: mydb
  
  # Redis configuration that all apps might use  
  redis:
    enabled: false
    # host: redis.default.svc.cluster.local
    # port: 6379