# Multi-app composite chart values template
# This chart can deploy multiple applications together

# Global settings for all apps
global:
  imageRegistry: {{GLOBAL_REGISTRY}}
  imagePullSecrets: []
  storageClass: ""

# Configuration for each application in this composite chart
apps:
{{#APPS}}
  {{APP_NAME}}:
    enabled: true
    replicaCount: 1
    
    image:
      repository: {{IMAGE_REPO}}
      tag: "{{APP_VERSION}}"
      pullPolicy: IfNotPresent
    
    service:
      type: ClusterIP
      port: 80
      targetPort: {{SERVICE_PORT}}
    
    # App-specific configuration
    config:
      port: {{SERVICE_PORT}}
      # Environment variables for this app
      env: {}
        # Example:
        # LOG_LEVEL: info
        # DEBUG: "false"
    
    # Health check configuration
    healthCheck:
      enabled: true
      path: {{HEALTH_PATH}}
      initialDelaySeconds: 30
      periodSeconds: 10
    
    # Resources for this specific app
    resources: {}
      # limits:
      #   cpu: 100m
      #   memory: 128Mi
      # requests:
      #   cpu: 100m
      #   memory: 128Mi
    
    # Autoscaling for this app
    autoscaling:
      enabled: false
      minReplicas: 1
      maxReplicas: 10
      targetCPUUtilizationPercentage: 80

{{/APPS}}

# Shared ingress configuration for all apps
ingress:
  enabled: false
  className: ""
  annotations: {}
    # kubernetes.io/ingress.class: nginx
    # kubernetes.io/tls-acme: "true"
  hosts:
    - host: {{COMPOSITE_NAME}}.local
      paths:
{{#APPS}}
        - path: /{{APP_NAME}}
          pathType: Prefix
          serviceName: {{APP_NAME}}
{{/APPS}}
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