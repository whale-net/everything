# ManMan Helm Chart - Current Limitations & Solutions

**Date**: September 30, 2025  
**Status**: Identifying gaps for production readiness

---

## 🚨 Critical Issues Identified

You've correctly identified several significant gaps in the current helm tool implementation. Let me address each one:

---

## 1. ❌ Ingress Configuration Per App

### Current Problem

**What you get now:**
```yaml
# values.yaml - GLOBAL ingress config only
ingress:
  enabled: true
  className: ""
  annotations: {}
  tls: []
```

**What it generates:**
- Generic host: `{app_name}-{env}.local` (hardcoded)
- No per-app customization
- No way to set custom hosts, paths, or TLS per service
- All external-api apps get same ingress config

### What You Need

```yaml
# Per-app ingress configuration
apps:
  experience_api:
    type: external-api
    port: 8000
    ingress:
      host: "api.mycompany.com"
      paths:
        - path: /experience
          pathType: Prefix
      tls:
        secretName: experience-api-tls
        hosts:
          - api.mycompany.com
      annotations:
        cert-manager.io/cluster-issuer: letsencrypt-prod
        nginx.ingress.kubernetes.io/rate-limit: "100"
  
  worker_dal_api:
    type: external-api
    port: 8000
    ingress:
      host: "worker-api.mycompany.com"
      paths:
        - path: /
          pathType: Prefix
      tls:
        secretName: worker-dal-api-tls
```

### Status

**NOT IMPLEMENTED** - The helm tool currently only supports:
- Global ingress enabled/disabled flag
- Global TLS config (applies to all)
- Generic host pattern (`{app}-{env}.local`)

**Workaround:**
Override at deploy time with custom values file, but this defeats the purpose of build-time generation.

---

## 2. ❌ Replicas Hardcoded to 2 (or 1)

### Current Problem

**Hardcoded in `composer.go` lines 374-376:**
```go
replicas := 1
if appType == ExternalAPI || appType == InternalAPI {
    replicas = 2  // HARDCODED!
}
```

**Result:**
- External APIs and Internal APIs: **2 replicas** (cannot override at build time)
- Workers: **1 replica** (cannot override at build time)
- Jobs: **1 replica** (N/A for jobs)

### What You Need

```starlark
# In manman/BUILD.bazel
release_app(
    name = "experience_api",
    binary_target = "//manman/src/host:experience_api",
    app_type = "external-api",
    replicas = 1,  # ← Should be configurable here!
)
```

Or at minimum, default to 1 for dev environments and allow override via values.

### Status

**NOT IMPLEMENTED** - No way to configure replicas at:
- Build time (in BUILD.bazel)
- Chart generation time

**Workaround:**
Override at deploy time:
```bash
helm install manman-dev ./chart \
  --set apps.experience_api.replicas=1 \
  --set apps.status_api.replicas=1 \
  --set apps.worker_dal_api.replicas=1 \
  --set apps.status_processor.replicas=1
```

But this is manual and error-prone.

---

## 3. ❓ Manifests Element

### Current Situation

```yaml
# values.yaml
manifests:
  enabled: true
```

### What It's For

This is for **manual Kubernetes manifests** like ConfigMaps, Secrets, NetworkPolicies that you add via:

```starlark
helm_chart(
    name = "manman_chart",
    apps = [...],
    manual_manifests = [":k8s_manifests"],  # ← Optional ConfigMaps, etc.
)
```

See: `tools/helm/K8S_MANIFESTS.md` for details.

### Your Case

**You don't have any manual manifests**, so this section is useless clutter.

### Status

**UNNECESSARY** - Should be omitted if no `manual_manifests` are specified in the `helm_chart` rule.

**Current behavior:** Always included even if empty (bad design).

Keep as-is 

---

## 4. ❌ Health Checks Not Optional

### Current Problem

**Hardcoded in `composer.go` lines 394-403:**
```go
// Add health check for APIs
if appType == ExternalAPI || appType == InternalAPI {
    config.HealthCheck = &HealthCheckConfig{
        Path:                "/health",
        InitialDelaySeconds: 10,
        PeriodSeconds:       10,
        TimeoutSeconds:      5,
        SuccessThreshold:    1,
        FailureThreshold:    3,
    }
}
```

**Result:**
- ALL external-api and internal-api apps get health checks
- No way to disable them
- No way to customize the path per app
- If your app doesn't have `/health` endpoint → deployment fails

### What You Need

**Option 1: Make health checks optional**
```starlark
release_app(
    name = "experience_api",
    app_type = "external-api",
    health_check = {
        "enabled": True,
        "path": "/experience/health",
    },
)
```

**Option 2: Override in values**
```yaml
apps:
  experience_api:
    healthCheck:
      enabled: false  # Disable if not ready
```

**Option 3: Custom path**
```yaml
apps:
  experience_api:
    healthCheck:
      path: /experience/health  # Custom path
```

### Status

**NOT IMPLEMENTED** - Health checks are mandatory for all APIs with:
- Fixed path: `/health`
- No disable option
- No per-app customization at build time

**Workaround:**
Override in deploy-time values (but again, defeats the point).

Go with option 1

---

## 5. ❌ Environment Variables / Secrets

### The Biggest Gap

**Current Situation:**
```yaml
# values.yaml - NO environment variables!
apps:
  experience_api:
    type: external-api
    image: ghcr.io/manman-experience_api
    port: 8000
    # ← WHERE ARE DATABASE_URL, RABBITMQ credentials, etc.??
```

**Your manual chart had:**
```yaml
env:
  rabbitmq:
    host: <rabbitmq_host>
    user: <rabbitmq_user>
    password: <rabbitmq_password>
  db:
    url: <postgresql+psycopg2://...>
  app_env: dev
  otel:
    logging_enabled: true
```

### What You Need

**Per-app environment variables:**
```yaml
apps:
  experience_api:
    env:
      - name: MANMAN_POSTGRES_URL
        value: "postgresql://..."
      - name: MANMAN_RABBITMQ_HOST
        value: "rabbitmq.manman"
      - name: MANMAN_RABBITMQ_PORT
        value: "5672"
      - name: MANMAN_RABBITMQ_USER
        value: "manman"
      - name: MANMAN_RABBITMQ_PASSWORD
        valueFrom:
          secretKeyRef:
            name: rabbitmq-credentials
            key: password
      - name: APP_ENV
        value: "dev"
      - name: OTEL_EXPORTER_OTLP_LOGS_ENDPOINT
        value: "http://otel-collector:4317"
  
  migration:
    env:
      - name: MANMAN_POSTGRES_URL
        valueFrom:
          secretKeyRef:
            name: postgres-credentials
            key: connection-string
```

### Current Support

**PARTIAL** - The template supports `env` in values.yaml:

```yaml
# You CAN do this now:
apps:
  experience_api:
    env:
      MANMAN_POSTGRES_URL: "postgresql://..."
      APP_ENV: "dev"
```

But it's **plain text only** - no support for:
- `valueFrom.secretKeyRef` (Kubernetes secrets)
- `valueFrom.configMapKeyRef` (ConfigMaps)
- `valueFrom.fieldRef` (Downward API)
- Environment variable templating

### Status

**PARTIALLY IMPLEMENTED** - You can set env vars as plain text in values.yaml, but:
- ❌ No secret references
- ❌ No ConfigMap references
- ❌ Not generated from BUILD.bazel (manual override only)
- ❌ Insecure (plain text in values.yaml)

### Recommended Solution

**Create a separate Secret/ConfigMap:**

```yaml
# manman-secrets.yaml (apply separately)
apiVersion: v1
kind: Secret
metadata:
  name: manman-db-credentials
  namespace: manman
type: Opaque
data:
  postgres-url: <base64-encoded>
  
---
apiVersion: v1
kind: Secret
metadata:
  name: manman-rabbitmq-credentials
  namespace: manman
type: Opaque
data:
  host: <base64-encoded>
  username: <base64-encoded>
  password: <base64-encoded>
```

**Reference in values.yaml:**

```yaml
# custom-values.yaml
apps:
  experience_api:
    envFrom:
      - secretRef:
          name: manman-db-credentials
      - secretRef:
          name: manman-rabbitmq-credentials
      - configMapRef:
          name: manman-config
```

**But wait...** `envFrom` is **NOT SUPPORTED** in the current templates! 

You'd need to modify `tools/helm/templates/deployment.yaml.tmpl` to add:
```gotmpl
{{- if $app.envFrom }}
envFrom:
  {{- range $app.envFrom }}
  {{- if .secretRef }}
  - secretRef:
      name: {{ .secretRef.name }}
  {{- end }}
  {{- if .configMapRef }}
  - configMapRef:
      name: {{ .configMapRef.name }}
  {{- end }}
  {{- end }}
{{- end }}
```

---

## 🔧 Summary of Gaps

| Feature | Status | Workaround | Priority |
|---------|--------|-----------|----------|
| **Per-app Ingress Config** | ❌ Not implemented | Deploy-time override | 🔴 Critical |
| **Configurable Replicas** | ❌ Hardcoded to 2/1 | Deploy-time override | 🟡 High |
| **Unnecessary `manifests`** | ⚠️ Always included | Ignore it | 🟢 Low |
| **Optional Health Checks** | ❌ Always enabled | Deploy-time override | 🟡 High |
| **Secret References** | ❌ Not supported | External Secrets | 🔴 Critical |
| **ConfigMap References** | ❌ Not supported | External ConfigMap | 🔴 Critical |
| **`envFrom` Support** | ❌ Not supported | Manual template edit | 🔴 Critical |

---

## 🎯 Recommended Actions

### Short Term (Deploy Now)

1. **Create external Secret manifests:**
   ```bash
   kubectl create secret generic manman-db-credentials \
     --from-literal=MANMAN_POSTGRES_URL='postgresql://...' \
     -n manman
   
   kubectl create secret generic manman-rabbitmq-credentials \
     --from-literal=MANMAN_RABBITMQ_HOST='rabbitmq' \
     --from-literal=MANMAN_RABBITMQ_USER='user' \
     --from-literal=MANMAN_RABBITMQ_PASSWORD='pass' \
     -n manman
   ```

2. **Create custom values with plain-text env (for dev only):**
   ```yaml
   # manman-dev-values.yaml
   apps:
     experience_api:
       replicas: 1
       env:
         MANMAN_POSTGRES_URL: "postgresql://dev-db/manman"
         MANMAN_RABBITMQ_HOST: "rabbitmq.dev"
         MANMAN_RABBITMQ_PORT: "5672"
         MANMAN_RABBITMQ_USER: "dev-user"
         MANMAN_RABBITMQ_PASSWORD: "dev-pass"
         APP_ENV: "dev"
     status_api:
       replicas: 1
       env:
         MANMAN_POSTGRES_URL: "postgresql://dev-db/manman"
         # ... same env vars
     # ... repeat for all services
   ```

3. **Deploy with overrides:**
   ```bash
   helm install manman-dev \
     bazel-bin/manman/manman-host_chart/manman-host \
     -f manman-dev-values.yaml
   ```

### Medium Term (Fix the Tool)

File issues/PRs for:

1. **Add per-app ingress configuration**
   - `ingress.host` per app
   - `ingress.tls` per app
   - `ingress.annotations` per app

2. **Make replicas configurable in BUILD.bazel**
   ```starlark
   release_app(
       name = "experience_api",
       replicas = 1,  # Add this
   )
   ```

3. **Add `envFrom` support to deployment template**

4. **Make health checks optional**
   ```starlark
   release_app(
       name = "experience_api",
       health_check_enabled = True,
       health_check_path = "/health",
   )
   ```

5. **Remove `manifests:` section when empty**

### Long Term (Production Ready)

1. **Use external secret management:**
   - [External Secrets Operator](https://external-secrets.io/)
   - [Sealed Secrets](https://github.com/bitnami-labs/sealed-secrets)
   - Cloud provider secret managers (AWS Secrets Manager, etc.)

2. **Create environment-specific value files:**
   ```
   values/
   ├── dev.yaml
   ├── staging.yaml
   └── prod.yaml
   ```

3. **Separate chart generation from deployment:**
   - Build chart once with Bazel
   - Deploy to multiple environments with different values

---

## 💡 Why This Happened

The helm tool was designed for **simple demo apps** with minimal configuration:
- Single API with standard `/health` endpoint
- No secrets required
- Default replicas work fine
- Generic ingress sufficient

**Your manman application** is a **production microservices platform** with:
- Multiple APIs with complex routing
- Database and message queue credentials
- Custom health check paths
- Environment-specific configuration
- Security requirements (secrets, TLS)

**The gap:** Tool design vs. real-world requirements.

---

## 🚀 What to Do Next

### Option 1: Use Manual Chart (Recommended for Now)

Your manual chart in `__manual_backup_of_old_chart/` already handles:
- ✅ Environment variables properly
- ✅ Custom health check paths
- ✅ Configurable replicas
- ✅ Secret references (even if plain-text)
- ✅ Ingress customization

**Just keep using it** until the helm tool catches up.

### Option 2: Extend the Helm Tool

Contribute fixes to `tools/helm/composer.go` and templates to support:
- Per-app ingress config
- Secret references
- Configurable defaults
- Optional health checks

### Option 3: Hybrid Approach

Use generated chart as a **base** and **patch** it:
```bash
# Generate base chart
bazel build //manman:manman_chart

# Patch with custom values
helm install manman-dev \
  bazel-bin/manman/manman-host_chart/manman-host \
  -f manman-secrets.yaml \
  -f manman-dev-overrides.yaml
```

---

## 📝 Conclusion

The helm tool is **good for simple apps** but **not production-ready for complex microservices** like manman without significant enhancements.

**Your concerns are 100% valid.** The generated chart is missing critical features for production deployment.

**Recommended path forward:**
1. Document these gaps (this file)
2. File enhancement requests for the helm tool
3. Use manual chart or hybrid approach for now
4. Migrate fully when tool supports all requirements

---

## 📚 References

- Current helm tool: `tools/helm/`
- Composer code: `tools/helm/composer.go`
- Deployment template: `tools/helm/templates/deployment.yaml.tmpl`
- Manual chart (working): `manman/__manual_backup_of_old_chart/`
