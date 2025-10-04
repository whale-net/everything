# Helm Template Development Guide

This document explains how Helm templates work in the chart composition system and how to extend or customize them.

## Template Overview

The helm chart system uses Go templates to generate Kubernetes manifests. Templates are located in `tools/helm/templates/`.

### Available Templates

| Template | Purpose | Generated For |
|----------|---------|---------------|
| `deployment.yaml.tmpl` | Kubernetes Deployment | external-api, internal-api, worker |
| `service.yaml.tmpl` | Kubernetes Service | external-api, internal-api |
| `ingress.yaml.tmpl` | Kubernetes Ingress | external-api (1:1 per app) |
| `job.yaml.tmpl` | Kubernetes Job with Helm hooks | job |
| `configmap.yaml.tmpl` | Environment variables | All app types |
| `pdb.yaml.tmpl` | PodDisruptionBudget | external-api, internal-api, worker |

---

## Go Template Syntax Basics

### Variables

```yaml
# Access top-level values
{{ .Values.global.environment }}

# Access app-specific values
{{ $app.replicas }}

# Define local variables
{{- $appName := "api_server" }}
{{- $port := $app.port }}
```

### Conditionals

```yaml
# If statement
{{- if .Values.global.ingress.enabled }}
  ingress: enabled
{{- end }}

# If-else
{{- if eq $app.type "external-api" }}
  type: external-api
{{- else }}
  type: other
{{- end }}

# Check for field existence
{{- if $app.livenessProbe }}
  livenessProbe: {{ toYaml $app.livenessProbe | nindent 4 }}
{{- end }}
```

### Loops

```yaml
# Iterate over map
{{- range $appName, $app := .Values.apps }}
  app: {{ $appName }}
{{- end }}

# Iterate over array
{{- range .Values.global.ingress.tls }}
  - secretName: {{ .secretName }}
{{- end }}
```

### Whitespace Control

- `{{-` : Trim whitespace before
- `-}}` : Trim whitespace after

```yaml
# Without whitespace control
{{ .Values.name }}

# With whitespace control (cleaner output)
{{- .Values.name }}
```

---

## Template Structure

### 1. deployment.yaml.tmpl

Generates Deployments for external-api, internal-api, and worker apps.

**Key sections**:

```yaml
{{- range $appName, $app := .Values.apps }}
{{- if and $app.enabled (or (eq $app.type "external-api") (eq $app.type "internal-api") (eq $app.type "worker")) }}
---
apiVersion: apps/v1
kind: Deployment
metadata:
  name: {{ $appName }}-{{ $.Values.global.environment }}
  annotations:
    argocd.argoproj.io/sync-wave: "0"  # Default application wave
spec:
  replicas: {{ $app.replicas | default 1 }}
  selector:
    matchLabels:
      app: {{ $appName }}
  template:
    spec:
      containers:
      - name: {{ $appName }}
        image: {{ $app.image }}
        {{- if or (eq $app.type "external-api") (eq $app.type "internal-api") }}
        ports:
        - containerPort: {{ $app.port }}
        {{- end }}
```

**Conditional logic**:
1. Only generate for enabled apps
2. Only generate for Deployment-based types (not jobs)
3. Only include ports for API types
4. Include resources if defined

### 2. service.yaml.tmpl

Generates Services for external-api and internal-api apps.

**Key sections**:

```yaml
{{- range $appName, $app := .Values.apps }}
{{- if and $app.enabled (or (eq $app.type "external-api") (eq $app.type "internal-api")) }}
---
apiVersion: v1
kind: Service
metadata:
  name: {{ $appName }}-{{ $.Values.global.environment }}
  annotations:
    argocd.argoproj.io/sync-wave: "0"
spec:
  type: ClusterIP
  selector:
    app: {{ $appName }}
  ports:
  - port: {{ $app.port }}
    targetPort: {{ $app.port }}
    protocol: TCP
```

**Conditional logic**:
- Only generate for API types (external-api, internal-api)
- Always uses ClusterIP type
- Port must be defined

### 3. ingress.yaml.tmpl

Generates 1:1 Ingress resources for external-api apps.

**Key sections**:

```yaml
{{- if .Values.global.ingress.enabled }}
{{- range $appName, $app := .Values.apps }}
{{- if and $app.enabled (eq $app.type "external-api") }}
---
apiVersion: networking.k8s.io/v1
kind: Ingress
metadata:
  name: {{ $appName }}-{{ $.Values.global.environment }}-ingress
  annotations:
    argocd.argoproj.io/sync-wave: "0"
    {{- range $key, $value := $.Values.global.ingress.annotations }}
    {{ $key }}: {{ $value | quote }}
    {{- end }}
spec:
  {{- if $.Values.global.ingress.className }}
  ingressClassName: {{ $.Values.global.ingress.className }}
  {{- end }}
  rules:
  - host: {{ $appName }}-{{ $.Values.global.environment }}.local
    http:
      paths:
      - path: /
        pathType: Prefix
        backend:
          service:
            name: {{ $appName }}-{{ $.Values.global.environment }}
            port:
              number: {{ $app.port }}
```

**Conditional logic**:
- Only generate if `global.ingress.enabled: true`
- Only generate for external-api apps
- Each external-api gets its own Ingress (1:1 pattern)
- Host pattern: `{appName}-{environment}.local`
- Include TLS config if defined

**Pattern**: This implements the 1:1 app:ingress mapping where each external-api gets a dedicated Ingress resource.

**Common Ingress Annotations**:

Configure ingress behavior through the `global.ingress.annotations` section:

```yaml
# values.yaml
global:
  ingress:
    enabled: true
    className: nginx
    annotations:
      # Increase request body size limit (fixes 413 errors)
      nginx.ingress.kubernetes.io/proxy-body-size: "50m"
      # Timeout configuration
      nginx.ingress.kubernetes.io/proxy-read-timeout: "300"
      nginx.ingress.kubernetes.io/proxy-send-timeout: "300"
      # SSL configuration
      nginx.ingress.kubernetes.io/force-ssl-redirect: "true"
      cert-manager.io/cluster-issuer: "letsencrypt-prod"
```

📖 **See [INGRESS_TROUBLESHOOTING.md](./INGRESS_TROUBLESHOOTING.md) for common issues like 413 errors and detailed configuration examples.**

### 4. job.yaml.tmpl

Generates Jobs for job-type apps with Helm hooks.

**Key sections**:

```yaml
{{- range $appName, $app := .Values.apps }}
{{- if and $app.enabled (eq $app.type "job") }}
---
apiVersion: batch/v1
kind: Job
metadata:
  name: {{ $appName }}-{{ $.Values.global.environment }}
  annotations:
    helm.sh/hook: pre-install,pre-upgrade
    helm.sh/hook-weight: "0"
    helm.sh/hook-delete-policy: before-hook-creation
    argocd.argoproj.io/sync-wave: "-1"  # Run before applications
spec:
  backoffLimit: {{ $app.backoffLimit | default 3 }}
  ttlSecondsAfterFinished: {{ $app.ttlSecondsAfterFinished | default 86400 }}
  template:
    spec:
      restartPolicy: {{ $app.restartPolicy | default "Never" }}
      containers:
      - name: {{ $appName }}
        image: {{ $app.image }}
```

**Helm hooks**:
- `pre-install`: Runs before initial install
- `pre-upgrade`: Runs before upgrade
- `hook-delete-policy: before-hook-creation`: Cleans up old jobs

**ArgoCD sync-wave**: `-1` ensures jobs run before applications (wave `0`).

### 5. configmap.yaml.tmpl

Generates ConfigMaps for all app types.

**Key sections**:

```yaml
{{- range $appName, $app := .Values.apps }}
{{- if $app.enabled }}
---
apiVersion: v1
kind: ConfigMap
metadata:
  name: {{ $appName }}-{{ $.Values.global.environment }}-config
data:
  APP_NAME: {{ $appName | quote }}
  ENVIRONMENT: {{ $.Values.global.environment | quote }}
  {{- if $app.env }}
  {{- range $key, $value := $app.env }}
  {{ $key }}: {{ $value | quote }}
  {{- end }}
  {{- end }}
```

**Usage in Deployment/Job**:

```yaml
containers:
- name: {{ $appName }}
  envFrom:
  - configMapRef:
      name: {{ $appName }}-{{ $.Values.global.environment }}-config
```

### 6. pdb.yaml.tmpl

Generates PodDisruptionBudgets for Deployment-based apps.

**Key sections**:

```yaml
{{- if .Values.global.pdb.enabled }}
{{- range $appName, $app := .Values.apps }}
{{- if and $app.enabled (or (eq $app.type "external-api") (eq $app.type "internal-api") (eq $app.type "worker")) }}
---
apiVersion: policy/v1
kind: PodDisruptionBudget
metadata:
  name: {{ $appName }}-{{ $.Values.global.environment }}-pdb
  annotations:
    argocd.argoproj.io/sync-wave: "0"
spec:
  minAvailable: {{ $.Values.global.pdb.minAvailable | default 1 }}
  selector:
    matchLabels:
      app: {{ $appName }}
```

**Conditional logic**:
- Only generate if `global.pdb.enabled: true`
- Only for Deployment-based apps (not jobs)
- Default `minAvailable: 1`

---

## Template Functions

### Built-in Functions

```yaml
# String operations
{{ .Values.name | quote }}              # Add quotes
{{ .Values.name | upper }}              # Uppercase
{{ .Values.name | lower }}              # Lowercase
{{ .Values.name | replace "-" "_" }}    # Replace characters

# Default values
{{ $app.replicas | default 1 }}         # Default to 1 if not set

# Type conversion
{{ .Values.port | toString }}           # Convert to string
{{ .Values.enabled | toString }}        # Boolean to string

# YAML manipulation
{{ toYaml $app.resources | nindent 4 }} # Convert to YAML with indentation
{{ toJson $app.labels }}                # Convert to JSON

# Conditionals
{{ if eq $app.type "external-api" }}    # Equality check
{{ if ne $app.type "job" }}             # Not equal
{{ if and $a $b }}                      # Logical AND
{{ if or $a $b }}                       # Logical OR
```

### Common Patterns

#### Pattern 1: Type-Based Conditionals

```yaml
{{- if or (eq $app.type "external-api") (eq $app.type "internal-api") }}
  # Only for API types
{{- end }}
```

#### Pattern 2: Default Values with Override

```yaml
replicas: {{ $app.replicas | default 1 }}
resources:
  requests:
    memory: {{ $app.resources.requests.memory | default "128Mi" | quote }}
```

#### Pattern 3: Optional Sections

```yaml
{{- if $app.livenessProbe }}
livenessProbe: {{ toYaml $app.livenessProbe | nindent 2 }}
{{- end }}
```

#### Pattern 4: Iterating with Context

```yaml
{{- range $appName, $app := .Values.apps }}
  # Use $ to access root context
  environment: {{ $.Values.global.environment }}
  # Use local variables
  app: {{ $appName }}
{{- end }}
```

---

## Adding New Templates

### Step 1: Create Template File

Create new template in `tools/helm/templates/`:

```bash
touch tools/helm/templates/networkpolicy.yaml.tmpl
```

### Step 2: Define Template Logic

```yaml
{{- range $appName, $app := .Values.apps }}
{{- if and $app.enabled (eq $app.type "external-api") }}
---
apiVersion: networking.k8s.io/v1
kind: NetworkPolicy
metadata:
  name: {{ $appName }}-{{ $.Values.global.environment }}-netpol
  annotations:
    argocd.argoproj.io/sync-wave: "0"
spec:
  podSelector:
    matchLabels:
      app: {{ $appName }}
  policyTypes:
  - Ingress
  - Egress
  ingress:
  - from:
    - namespaceSelector:
        matchLabels:
          name: {{ $.Values.global.environment }}
    ports:
    - protocol: TCP
      port: {{ $app.port }}
{{- end }}
{{- end }}
```

### Step 3: Update Composer

Update `tools/helm/composer.go` to include new template:

```go
// Add to templateFiles array
var templateFiles = []string{
    "deployment.yaml.tmpl",
    "service.yaml.tmpl",
    "ingress.yaml.tmpl",
    "job.yaml.tmpl",
    "configmap.yaml.tmpl",
    "pdb.yaml.tmpl",
    "networkpolicy.yaml.tmpl",  // NEW
}
```

### Step 4: Test

```bash
# Rebuild composer
bazel build //tools/helm:composer

# Test chart generation
bazel build //demo/hello_python:hello_python_chart

# Verify new resource
helm template test bazel-bin/demo/hello_python/hello_python_chart/ | grep NetworkPolicy
```

---

## Customizing Existing Templates

### Option 1: Modify Template Files

Edit template files directly in `tools/helm/templates/`.

**Example**: Add custom labels to Deployments:

```yaml
# deployment.yaml.tmpl
metadata:
  name: {{ $appName }}-{{ $.Values.global.environment }}
  labels:
    app: {{ $appName }}
    environment: {{ $.Values.global.environment }}
    version: {{ $app.version | default "latest" }}  # NEW
```

### Option 2: Override via Values

Use values to customize without changing templates:

```yaml
# values.yaml
apps:
  api_server:
    annotations:
      custom.io/annotation: "value"
```

```yaml
# deployment.yaml.tmpl
metadata:
  annotations:
    argocd.argoproj.io/sync-wave: "0"
    {{- if $app.annotations }}
    {{- range $key, $value := $app.annotations }}
    {{ $key }}: {{ $value | quote }}
    {{- end }}
    {{- end }}
```

---

## ArgoCD Sync-Wave Annotations

### Sync-Wave Strategy

Sync-waves control resource deployment order in ArgoCD:

| Wave | Resources | Purpose |
|------|-----------|---------|
| `-1` | Jobs | Migrations, setup tasks (run first) |
| `0` | Deployments, Services, Ingress, PDB | Applications (run after jobs) |

**Implementation in templates**:

```yaml
# job.yaml.tmpl
metadata:
  annotations:
    argocd.argoproj.io/sync-wave: "-1"  # Wave -1

# deployment.yaml.tmpl, service.yaml.tmpl, ingress.yaml.tmpl, pdb.yaml.tmpl
metadata:
  annotations:
    argocd.argoproj.io/sync-wave: "0"   # Wave 0
```

### Customizing Sync-Waves

Override via values:

```yaml
apps:
  db_migration:
    type: job
    syncWave: "-5"  # Run even earlier
  
  api_server:
    type: external-api
    syncWave: "1"   # Run after standard apps
```

Update template:

```yaml
metadata:
  annotations:
    argocd.argoproj.io/sync-wave: {{ $app.syncWave | default "0" | quote }}
```

---

## Debugging Templates

### 1. Use helm template

```bash
# Generate all YAML
helm template test ./chart/

# Generate with specific values
helm template test ./chart/ --values custom-values.yaml

# Output to file for review
helm template test ./chart/ > /tmp/output.yaml
```

### 2. Debug Mode

```bash
# Show template comments and debug info
helm template test ./chart/ --debug
```

### 3. Check Specific Resource

```bash
# Show only Deployments
helm template test ./chart/ | grep -A 50 "kind: Deployment"

# Count resources
helm template test ./chart/ | grep "kind:" | sort | uniq -c
```

### 4. Validate YAML

```bash
# Validate with kubectl
helm template test ./chart/ | kubectl apply --dry-run=client -f -

# Check for YAML syntax errors
helm template test ./chart/ | yamllint -
```

### 5. Test Conditionals

```bash
# Enable ingress
helm template test ./chart/ --set global.ingress.enabled=true | grep "kind: Ingress"

# Disable ingress
helm template test ./chart/ --set global.ingress.enabled=false | grep "kind: Ingress"
# Should return nothing
```

---

## Best Practices

### 1. Whitespace Control

Always use `{{-` and `-}}` for cleaner output:

```yaml
# Bad (extra blank lines)
{{ if .Values.enabled }}
  enabled: true
{{ end }}

# Good (clean output)
{{- if .Values.enabled }}
  enabled: true
{{- end }}
```

### 2. Quoting

Quote string values to avoid YAML parsing issues:

```yaml
# Bad
env: {{ .Values.env }}

# Good
env: {{ .Values.env | quote }}
```

### 3. Default Values

Always provide defaults for optional values:

```yaml
replicas: {{ $app.replicas | default 1 }}
```

### 4. Type Checking

Check types before accessing fields:

```yaml
{{- if and $app.resources $app.resources.limits }}
resources:
  limits: {{ toYaml $app.resources.limits | nindent 4 }}
{{- end }}
```

### 5. Comments

Add comments for complex logic:

```yaml
{{- /* Only generate Ingress for external-api apps when ingress is enabled */ -}}
{{- if .Values.global.ingress.enabled }}
{{- range $appName, $app := .Values.apps }}
{{- if and $app.enabled (eq $app.type "external-api") }}
  # ... Ingress spec
{{- end }}
{{- end }}
{{- end }}
```

### 6. Consistency

Use consistent naming patterns:

```yaml
# Deployment name
{{ $appName }}-{{ $.Values.global.environment }}

# Service name
{{ $appName }}-{{ $.Values.global.environment }}

# Ingress name
{{ $appName }}-{{ $.Values.global.environment }}-ingress
```

---

## Testing Strategy

### Unit Tests

Test individual templates:

```bash
# Test deployment template
helm template test ./chart/ \
  --set apps.test.enabled=true \
  --set apps.test.type=external-api \
  --set apps.test.image=nginx \
  --set apps.test.port=80 | \
  grep -A 30 "kind: Deployment"
```

### Integration Tests

Test full chart:

```bash
# Generate full chart
helm template test ./chart/ > /tmp/full-chart.yaml

# Validate all resources
kubectl apply --dry-run=client -f /tmp/full-chart.yaml

# Check resource counts
grep "kind:" /tmp/full-chart.yaml | sort | uniq -c
```

### Linting

```bash
# Lint chart
helm lint ./chart/

# Expected: 0 failures
```

---

## Troubleshooting

### Ingress Issues

#### 413 Request Entity Too Large

**Problem**: Nginx returns 413 error when uploading files or sending large requests.

**Solution**: Add `nginx.ingress.kubernetes.io/proxy-body-size` annotation:

```yaml
global:
  ingress:
    enabled: true
    annotations:
      nginx.ingress.kubernetes.io/proxy-body-size: "50m"
```

📖 **See [INGRESS_TROUBLESHOOTING.md](./INGRESS_TROUBLESHOOTING.md) for detailed solutions and debugging steps.**

#### Ingress Not Created

**Problem**: No ingress resources are generated.

**Checklist**:
1. Is `ingress.enabled: true` in values?
2. Is the app type `external-api`?
3. Is the app enabled?

```bash
# Verify ingress is generated
helm template test ./chart/ --set ingress.enabled=true | grep "kind: Ingress"
```

#### Ingress Created But Not Working

**Problem**: Ingress exists but traffic doesn't reach the app.

**Checklist**:
1. Check ingress class matches controller: `kubectl get ingressclass`
2. Verify service exists: `kubectl get svc`
3. Check ingress status: `kubectl describe ingress <name>`
4. View ingress controller logs: `kubectl logs -n ingress-nginx <controller-pod>`

### Template Rendering Issues

#### Missing Required Values

**Problem**: Template fails with "nil pointer" or "undefined variable" errors.

**Solution**: Check required values are set:

```bash
# See what values are used
helm template test ./chart/ --debug

# Provide missing values
helm template test ./chart/ --set apps.myapp.port=8000
```

#### YAML Syntax Errors

**Problem**: Generated YAML is invalid.

**Solution**: Validate output:

```bash
# Check for syntax errors
helm template test ./chart/ | kubectl apply --dry-run=client -f -

# Use yamllint for detailed errors
helm template test ./chart/ | yamllint -
```

#### Unexpected Resource Count

**Problem**: Wrong number of resources generated.

**Solution**: Check app configuration:

```bash
# Count resources by type
helm template test ./chart/ | grep "kind:" | sort | uniq -c

# Verify app is enabled
helm template test ./chart/ | grep "name: myapp"
```

### ArgoCD Sync Issues

#### Resources Deploy Out of Order

**Problem**: Apps deploy before migrations complete.

**Solution**: Check sync-wave annotations:

```bash
# View sync-waves
helm template test ./chart/ | grep -B 5 "sync-wave"

# Jobs should be wave -1, apps should be wave 0
```

#### Sync Fails with "Resource Already Exists"

**Problem**: ArgoCD can't apply resources.

**Solution**: 
1. Ensure metadata (name, namespace, labels) is consistent
2. Check for duplicate resources
3. Use `--force` to recreate resources if needed

---

## See Also

- [README.md](README.md) - Quick start and common patterns
- [APP_TYPES.md](APP_TYPES.md) - App type reference
- [INGRESS_TROUBLESHOOTING.md](INGRESS_TROUBLESHOOTING.md) - Detailed ingress configuration and troubleshooting
- [MIGRATION.md](MIGRATION.md) - Migration guide
- [IMPLEMENTATION_PLAN.md](IMPLEMENTATION_PLAN.md) - Full implementation details
- [Helm Template Documentation](https://helm.sh/docs/chart_template_guide/)
