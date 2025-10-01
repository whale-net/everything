# Manual Kubernetes Manifests in Helm Charts

**Feature Status**: ✅ Production Ready | **Version 1.0** | **Updated**: September 30, 2025

Add custom Kubernetes resources (ConfigMaps, Secrets, NetworkPolicies, etc.) to your Helm charts alongside auto-generated app resources.

---

## Quick Start

### 1. Create Your Manifest Files

Create standard Kubernetes YAML files:

```yaml
# configmap.yaml
apiVersion: v1
kind: ConfigMap
metadata:
  name: my-app-config
  namespace: default
  labels:
    app: my-app
data:
  DATABASE_URL: "postgresql://localhost:5432/mydb"
  CACHE_ENABLED: "true"
```

### 2. Define a Filegroup

In your `BUILD.bazel`:

```starlark
filegroup(
    name = "k8s_manifests",
    srcs = [
        "configmap.yaml",
        "networkpolicy.yaml",
        "secret.yaml",
    ],
    tags = ["k8s_manifests"],
    visibility = ["//visibility:public"],
)
```

### 3. Include in Helm Chart

```starlark
load("//tools/helm:helm.bzl", "helm_chart")

helm_chart(
    name = "my_chart",
    apps = ["//path/to:app_metadata"],
    manual_manifests = [":k8s_manifests"],  # ← Add your manifests
    chart_name = "my-app",
    namespace = "production",
)
```

### 4. Build and Deploy

```bash
# Build the chart
bazel build //path/to:my_chart

# Verify manifests are included
ls bazel-bin/path/to/my-app_chart/my-app/templates/

# Deploy
helm install my-release bazel-bin/path/to/my-app_chart/my-app
```

---

## How It Works

### Automatic Processing

The helm chart generator automatically:

1. **Copies** your manifest files into the chart's `templates/` directory
2. **Injects** Helm template directives for configuration
3. **Wraps** manifests with `.Values.manifests.enabled` flag
4. **Prefixes** filenames with `manifest-##-` to avoid conflicts

### Template Injection

Your manifests are processed to support Helm values:

**Original manifest:**
```yaml
apiVersion: v1
kind: ConfigMap
metadata:
  name: my-config
  namespace: default
  labels:
    app: my-app
data:
  KEY: "value"
```

**Processed template:**
```yaml
{{- if .Values.manifests.enabled | default true }}
apiVersion: v1
kind: ConfigMap
metadata:
  name: my-config
  namespace: {{ .Values.global.namespace }}
  labels:
    environment: {{ .Values.global.environment }}
    app: my-app
data:
  KEY: "value"
{{- end }}
```

### What Gets Templated

| Field | Original | Templated |
|-------|----------|-----------|
| `metadata.namespace` | `default` or any value | `{{ .Values.global.namespace }}` |
| `metadata.labels.environment` | (added if labels exist) | `{{ .Values.global.environment }}` |
| Entire manifest | (always rendered) | Wrapped in `{{- if .Values.manifests.enabled }}` |

---

## Common Use Cases

### ConfigMaps for Application Config

```yaml
# config/app-config.yaml
apiVersion: v1
kind: ConfigMap
metadata:
  name: app-config
  namespace: default
data:
  DATABASE_URL: "postgresql://db:5432/app"
  REDIS_URL: "redis://cache:6379"
  LOG_LEVEL: "info"
```

```starlark
# BUILD.bazel
filegroup(
    name = "config_manifests",
    srcs = ["config/app-config.yaml"],
    tags = ["k8s_manifests"],
)

helm_chart(
    name = "app_chart",
    apps = [":app_metadata"],
    manual_manifests = [":config_manifests"],
    chart_name = "my-app",
    namespace = "production",
)
```

### NetworkPolicies for Security

```yaml
# security/network-policy.yaml
apiVersion: networking.k8s.io/v1
kind: NetworkPolicy
metadata:
  name: app-netpol
  namespace: default
spec:
  podSelector:
    matchLabels:
      app: my-app-production
  policyTypes:
    - Ingress
    - Egress
  ingress:
    - from:
        - namespaceSelector:
            matchLabels:
              name: production
      ports:
        - protocol: TCP
          port: 8000
  egress:
    - to:
        - namespaceSelector: {}
      ports:
        - protocol: TCP
          port: 5432  # Database
```

### Secrets (Placeholder Pattern)

```yaml
# secrets/db-secret.yaml
apiVersion: v1
kind: Secret
metadata:
  name: db-credentials
  namespace: default
type: Opaque
stringData:
  username: "REPLACE_ME"
  password: "REPLACE_ME"
```

**Note**: Don't commit real secrets! Use placeholder values and override at deploy time:

```bash
helm install my-app ./chart \
  --set-file secrets.dbPassword=./real-password.txt
```

Or use external secret management (Sealed Secrets, External Secrets Operator, etc.)

### PersistentVolumeClaims

```yaml
# storage/data-pvc.yaml
apiVersion: v1
kind: PersistentVolumeClaim
metadata:
  name: app-data
  namespace: default
spec:
  accessModes:
    - ReadWriteOnce
  resources:
    requests:
      storage: 10Gi
  storageClassName: fast-ssd
```

### ServiceAccounts and RBAC

```yaml
# rbac/serviceaccount.yaml
apiVersion: v1
kind: ServiceAccount
metadata:
  name: app-sa
  namespace: default
---
apiVersion: rbac.authorization.k8s.io/v1
kind: Role
metadata:
  name: app-role
  namespace: default
rules:
  - apiGroups: [""]
    resources: ["configmaps"]
    verbs: ["get", "list"]
---
apiVersion: rbac.authorization.k8s.io/v1
kind: RoleBinding
metadata:
  name: app-rolebinding
  namespace: default
subjects:
  - kind: ServiceAccount
    name: app-sa
roleRef:
  kind: Role
  name: app-role
  apiGroup: rbac.authorization.k8s.io
```

---

## Advanced Patterns

### Multiple Manifest Groups

Organize manifests by category:

```starlark
# BUILD.bazel
filegroup(
    name = "config_manifests",
    srcs = glob(["config/*.yaml"]),
    tags = ["k8s_manifests"],
)

filegroup(
    name = "security_manifests",
    srcs = glob(["security/*.yaml"]),
    tags = ["k8s_manifests"],
)

filegroup(
    name = "storage_manifests",
    srcs = glob(["storage/*.yaml"]),
    tags = ["k8s_manifests"],
)

helm_chart(
    name = "full_stack_chart",
    apps = [":app_metadata"],
    manual_manifests = [
        ":config_manifests",
        ":security_manifests",
        ":storage_manifests",
    ],
    chart_name = "full-stack",
    namespace = "production",
)
```

### Conditional Manifests

Control which manifests are rendered:

```yaml
# In your manifest (add this manually if needed)
{{- if .Values.manifests.networkPolicy.enabled }}
apiVersion: networking.k8s.io/v1
kind: NetworkPolicy
...
{{- end }}
```

Deploy with control:

```bash
# Enable network policy
helm install app ./chart --set manifests.networkPolicy.enabled=true

# Disable all manual manifests
helm install app ./chart --set manifests.enabled=false
```

### Environment-Specific Values

Override manifest data per environment:

```yaml
# configmap.yaml
apiVersion: v1
kind: ConfigMap
metadata:
  name: app-config
  namespace: default
data:
  LOG_LEVEL: "{{ .Values.manifests.logLevel | default \"info\" }}"
  MAX_CONNECTIONS: "{{ .Values.manifests.maxConnections | default \"100\" }}"
```

Deploy with overrides:

```bash
# Production
helm install app ./chart \
  --set manifests.logLevel=warn \
  --set manifests.maxConnections=500

# Development
helm install app ./chart \
  --set manifests.logLevel=debug \
  --set manifests.maxConnections=10
```

---

## Values Configuration

### Default Values

All charts with manual manifests include:

```yaml
manifests:
  enabled: true  # Toggle all manual manifests
```

### Custom Configuration

Add custom values for your manifests:

```yaml
# values.yaml
manifests:
  enabled: true
  networkPolicy:
    enabled: true
  configMap:
    databaseUrl: "postgresql://prod-db:5432/app"
    cacheUrl: "redis://prod-cache:6379"
```

Reference in manifests:

```yaml
data:
  DATABASE_URL: "{{ .Values.manifests.configMap.databaseUrl }}"
  CACHE_URL: "{{ .Values.manifests.configMap.cacheUrl }}"
```

---

## File Naming and Organization

### Filename Conventions

Manifests are renamed in the chart to avoid conflicts:

| Your File | In Chart Templates |
|-----------|-------------------|
| `configmap.yaml` | `manifest-00-configmap.yaml` |
| `secret.yaml` | `manifest-01-secret.yaml` |
| `networkpolicy.yaml` | `manifest-02-networkpolicy.yaml` |

### Recommended Structure

```
my_app/
├── BUILD.bazel
├── main.py
├── config/
│   ├── app-config.yaml
│   └── feature-flags.yaml
├── security/
│   ├── network-policy.yaml
│   └── pod-security-policy.yaml
└── storage/
    └── data-pvc.yaml
```

---

## Validation

### Lint the Chart

```bash
helm lint bazel-bin/path/to/chart_name_chart/chart_name
```

### Preview Resources

```bash
# See all resources including manifests
helm template test ./chart

# See only your manifests
helm template test ./chart | grep -A 20 "kind: ConfigMap"
```

### Dry Run

```bash
helm install --dry-run --debug test ./chart
```

---

## Limitations and Considerations

### What Gets Templated

**✅ Automatically templated:**
- `metadata.namespace` → `.Values.global.namespace`
- `metadata.labels.environment` → `.Values.global.environment` (if labels exist)
- Entire manifest wrapped in `.Values.manifests.enabled` check

**❌ NOT automatically templated:**
- Resource-specific fields (you must add `{{ }}` manually)
- Complex nested structures
- Multi-document YAML files (supported but processed as one unit)

### Manual Templating

For advanced control, add Helm syntax directly in your YAML:

```yaml
apiVersion: v1
kind: ConfigMap
metadata:
  name: {{ .Values.manifests.configMapName | default "app-config" }}
  namespace: default
data:
  {{- range $key, $value := .Values.manifests.configData }}
  {{ $key }}: {{ $value | quote }}
  {{- end }}
```

### Security Best Practices

1. **Don't commit secrets**: Use placeholders and external secret management
2. **Review generated templates**: Check what gets templated before deploying
3. **Validate RBAC**: Ensure ServiceAccounts have minimal permissions
4. **Test namespace isolation**: Verify resources deploy to correct namespace

---

## Examples

### Example 1: Basic ConfigMap

```starlark
# demo/hello_fastapi/BUILD.bazel
filegroup(
    name = "k8s_manifests",
    srcs = ["configmap.yaml"],
    tags = ["k8s_manifests"],
)

helm_chart(
    name = "fastapi_chart",
    apps = [":hello_fastapi_metadata"],
    manual_manifests = [":k8s_manifests"],
    chart_name = "hello-fastapi",
    namespace = "demo",
)
```

```bash
bazel build //demo/hello_fastapi:fastapi_chart
helm template test bazel-bin/demo/hello_fastapi_chart/hello-fastapi
```

### Example 2: Full Security Setup

```yaml
# security/manifests.yaml
apiVersion: v1
kind: ServiceAccount
metadata:
  name: app-sa
  namespace: default
---
apiVersion: networking.k8s.io/v1
kind: NetworkPolicy
metadata:
  name: app-netpol
  namespace: default
spec:
  podSelector:
    matchLabels:
      app: my-app
  policyTypes: [Ingress, Egress]
  ingress:
    - from:
        - podSelector: {}
  egress:
    - to:
        - namespaceSelector: {}
```

```starlark
filegroup(
    name = "security_manifests",
    srcs = ["security/manifests.yaml"],
)

helm_chart(
    name = "secure_app_chart",
    apps = [":app"],
    manual_manifests = [":security_manifests"],
    chart_name = "secure-app",
    namespace = "production",
)
```

---

## Troubleshooting

### Manifest Not Appearing in Chart

**Check:**
1. File is listed in `srcs` of filegroup
2. Filegroup is referenced in `manual_manifests`
3. File has `.yaml` or `.yml` extension
4. Build succeeded without errors

### Template Syntax Errors

**Check:**
1. Your YAML is valid (use `yamllint`)
2. Helm template syntax is correct (test with `helm template`)
3. Namespace field is present (required for templating)

### Values Not Substituting

**Check:**
1. You're using the correct syntax: `{{ .Values.manifests.key }}`
2. Values are defined in values.yaml or passed via `--set`
3. Default values are provided: `{{ .Values.manifests.key | default "value" }}`

---

## Integration with Existing Charts

### Migrating Manual Resources

If you have existing Kubernetes YAML files:

1. Move them to your app directory
2. Create a filegroup
3. Add to `manual_manifests` in helm_chart
4. Remove from manual deployment process

**Before:**
```bash
kubectl apply -f configmap.yaml
helm install app ./chart
```

**After:**
```bash
bazel build //app:chart
helm install app bazel-bin/app/chart_name_chart/chart_name
```

### Combining with Auto-Generated Resources

Manual manifests work alongside auto-generated Deployment, Service, Ingress, etc.:

```
Chart includes:
✅ Deployment (from app_type)
✅ Service (from app_type)
✅ Ingress (from app_type)
✅ ConfigMap (from manual_manifests)
✅ NetworkPolicy (from manual_manifests)
✅ PVC (from manual_manifests)
```

---

## See Also

- **[README.md](README.md)** - Helm chart system overview
- **[APP_TYPES.md](APP_TYPES.md)** - Application type reference
- **[TEMPLATES.md](TEMPLATES.md)** - Template development guide
- **[MIGRATION.md](MIGRATION.md)** - Migration from manual charts

---

## FAQ

**Q: Can I include any Kubernetes resource?**  
A: Yes! ConfigMaps, Secrets, NetworkPolicies, PVCs, ServiceAccounts, RBAC, etc. Any valid Kubernetes YAML.

**Q: Are multi-document YAML files supported?**  
A: Yes, files with multiple resources (separated by `---`) are supported.

**Q: How do I disable a manifest at deploy time?**  
A: Set `manifests.enabled: false` or add custom flags in your manifest.

**Q: Can I use Helm template functions in my YAML?**  
A: Yes! Add Helm syntax directly in your YAML files for advanced control.

**Q: What about secrets?**  
A: Use placeholders in your YAML and override at deploy time, or integrate with external secret management (Sealed Secrets, etc.).

**Q: Do manifests respect the chart's namespace?**  
A: Yes! The `namespace` field is automatically templated to use `.Values.global.namespace`.

**Q: Can I organize manifests in subdirectories?**  
A: Yes! Use `glob(["config/**/*.yaml"])` in your filegroup srcs.

**Q: How do I version control these manifests?**  
A: Commit them to your repo like any other source file. They're part of your app definition.

---

**Ready to enhance your charts?** Check out the example in `demo/hello_fastapi` or start adding your own manifests!
