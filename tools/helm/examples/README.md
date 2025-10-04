# Helm Chart Examples

This directory contains example values files showing common configuration patterns for the Bazel-generated Helm charts.

## Available Examples

### values-with-nginx-annotations.yaml

**Purpose**: Production-ready configuration for external-api apps with nginx ingress controller.

**Key Features**:
- Fixes 413 Request Entity Too Large errors (`proxy-body-size: 50m`)
- Timeout configuration for long-running requests
- SSL/TLS enforcement
- Rate limiting (commented examples)
- CORS configuration (commented examples)
- cert-manager integration for automatic TLS certificates
- Resource limits and health checks

**Usage**:
```bash
helm install my-api ./chart -f tools/helm/examples/values-with-nginx-annotations.yaml
```

**When to use**: 
- APIs that handle file uploads
- APIs with large JSON payloads
- Production deployments with SSL/TLS
- Apps needing custom timeout configurations

## Common Configuration Patterns

### Fixing 413 Request Entity Too Large

The most common ingress issue. Add this annotation:

```yaml
ingress:
  annotations:
    nginx.ingress.kubernetes.io/proxy-body-size: "50m"
```

See [INGRESS_TROUBLESHOOTING.md](../INGRESS_TROUBLESHOOTING.md) for details.

### SSL/TLS with cert-manager

Automatically provision TLS certificates:

```yaml
ingress:
  annotations:
    cert-manager.io/cluster-issuer: "letsencrypt-prod"
    nginx.ingress.kubernetes.io/force-ssl-redirect: "true"
  tls:
    - secretName: my-tls-cert
      hosts:
        - api.example.com
```

### Timeout Configuration

For long-running requests (uploads, downloads, websockets):

```yaml
ingress:
  annotations:
    nginx.ingress.kubernetes.io/proxy-read-timeout: "600"
    nginx.ingress.kubernetes.io/proxy-send-timeout: "600"
```

### Rate Limiting

Protect your API from abuse:

```yaml
ingress:
  annotations:
    nginx.ingress.kubernetes.io/limit-rps: "10"  # 10 requests/sec per IP
```

## Testing Examples

### Dry Run

Test configuration without deploying:

```bash
helm install my-api ./chart \
  -f tools/helm/examples/values-with-nginx-annotations.yaml \
  --dry-run --debug
```

### Template Preview

See generated Kubernetes manifests:

```bash
helm template my-api ./chart \
  -f tools/helm/examples/values-with-nginx-annotations.yaml
```

### Validate

Check for issues:

```bash
helm lint ./chart -f tools/helm/examples/values-with-nginx-annotations.yaml
```

## Customizing for Your Needs

1. **Copy an example**: `cp values-with-nginx-annotations.yaml my-values.yaml`
2. **Edit**: Update image, replicas, hosts, etc.
3. **Deploy**: `helm install my-release ./chart -f my-values.yaml`

## Documentation

- [INGRESS_TROUBLESHOOTING.md](../INGRESS_TROUBLESHOOTING.md) - Detailed ingress configuration guide
- [TEMPLATES.md](../TEMPLATES.md) - Template development
- [README.md](../README.md) - Main Helm chart documentation
- [APP_TYPES.md](../APP_TYPES.md) - Application type reference
