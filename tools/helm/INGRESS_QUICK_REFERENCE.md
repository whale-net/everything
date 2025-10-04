# Ingress Quick Reference

Quick solutions for common nginx ingress issues.

## 413 Request Entity Too Large

**TL;DR**: Add this to fix 413 errors:

```yaml
ingress:
  annotations:
    nginx.ingress.kubernetes.io/proxy-body-size: "50m"
```

**Deploy with helm**:
```bash
helm install my-api ./chart \
  --set 'ingress.annotations.nginx\.ingress\.kubernetes\.io/proxy-body-size=50m'
```

**Yes, this is on your ingress** - the error comes from the nginx ingress controller, not your application.

---

## Common Annotations

| Annotation | Purpose | Example Value |
|------------|---------|---------------|
| `nginx.ingress.kubernetes.io/proxy-body-size` | Max request size | `"50m"`, `"100m"`, `"0"` (unlimited) |
| `nginx.ingress.kubernetes.io/proxy-read-timeout` | Read timeout | `"300"` (5 minutes) |
| `nginx.ingress.kubernetes.io/proxy-send-timeout` | Send timeout | `"300"` (5 minutes) |
| `nginx.ingress.kubernetes.io/force-ssl-redirect` | Force HTTPS | `"true"` |
| `nginx.ingress.kubernetes.io/limit-rps` | Rate limit | `"10"` (requests/sec) |
| `cert-manager.io/cluster-issuer` | Auto TLS cert | `"letsencrypt-prod"` |

---

## Size Guidelines

| Use Case | Recommended `proxy-body-size` |
|----------|-------------------------------|
| Small JSON APIs | `10m` |
| Image uploads | `50m` |
| Video/large file uploads | `100m` - `500m` |
| No limit (⚠️ not recommended) | `0` |

---

## Complete Example

```yaml
ingress:
  enabled: true
  className: nginx
  annotations:
    # Fix 413 errors
    nginx.ingress.kubernetes.io/proxy-body-size: "50m"
    # Timeouts
    nginx.ingress.kubernetes.io/proxy-read-timeout: "300"
    nginx.ingress.kubernetes.io/proxy-send-timeout: "300"
    # SSL
    nginx.ingress.kubernetes.io/force-ssl-redirect: "true"
    cert-manager.io/cluster-issuer: "letsencrypt-prod"
  tls:
    - secretName: api-tls
      hosts:
        - api.example.com
```

---

## Debugging

**Check if annotation is applied**:
```bash
kubectl get ingress <name> -o yaml | grep proxy-body-size
```

**View ingress controller logs**:
```bash
kubectl logs -n ingress-nginx <controller-pod>
```

**Test with curl**:
```bash
# Create test file
dd if=/dev/zero of=test.dat bs=1M count=10

# Upload
curl -X POST -F "file=@test.dat" https://api.example.com/upload
```

---

## More Info

- **[INGRESS_TROUBLESHOOTING.md](./INGRESS_TROUBLESHOOTING.md)** - Complete troubleshooting guide
- **[examples/](./examples/)** - Example values files
- **[TEMPLATES.md](./TEMPLATES.md)** - Template documentation
