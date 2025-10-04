# Ingress Troubleshooting Guide

This guide helps you troubleshoot common ingress issues, particularly with nginx ingress controllers.

---

## 413 Request Entity Too Large

### Problem

When sending large requests (file uploads, large JSON payloads, etc.) to your API through the ingress, nginx returns:

```html
<html>
  <head><title>413 Request Entity Too Large</title></head>
  <body>
  <center><h1>413 Request Entity Too Large</h1></center>
  <hr><center>nginx</center>
  </body>
</html>
```

### Root Cause

By default, nginx ingress controllers limit request body size to **1MB**. Requests larger than this limit are rejected with a 413 error.

### Solution

Add the `nginx.ingress.kubernetes.io/proxy-body-size` annotation to your ingress configuration to increase the limit.

#### Option 1: Global Ingress Configuration (Recommended)

Configure the annotation globally in your `values.yaml` or via Helm set flags. This applies to all ingress resources generated from the chart.

**Example `values.yaml`**:
```yaml
ingress:
  enabled: true
  className: nginx
  annotations:
    nginx.ingress.kubernetes.io/proxy-body-size: "50m"  # Allow up to 50MB
    nginx.ingress.kubernetes.io/proxy-read-timeout: "300"  # Optional: 5 min timeout
    nginx.ingress.kubernetes.io/proxy-send-timeout: "300"  # Optional: 5 min timeout
```

**Via Helm Install/Upgrade**:
```bash
helm install my-app ./chart \
  --set ingress.enabled=true \
  --set ingress.className=nginx \
  --set ingress.annotations."nginx\.ingress\.kubernetes\.io/proxy-body-size"=50m
```

**Via Bazel Helm Composer**:
```bash
# When using the composer tool to generate charts
bazel run //tools/helm:composer -- \
  --chart-name my-app \
  --environment production \
  --ingress-enabled \
  --ingress-class nginx \
  --ingress-annotation "nginx.ingress.kubernetes.io/proxy-body-size=50m"
```

#### Option 2: Per-App Ingress Configuration

For apps that need different limits, configure at the app level (requires template support):

**Note**: The current `ingress.yaml.tmpl` applies global annotations to all ingress resources. Per-app annotations would require template modification.

#### Option 3: Manual Ingress Override

If using a manual ingress manifest, add the annotation directly:

```yaml
apiVersion: networking.k8s.io/v1
kind: Ingress
metadata:
  name: my-app-ingress
  annotations:
    nginx.ingress.kubernetes.io/proxy-body-size: "50m"
spec:
  ingressClassName: nginx
  rules:
    - host: api.example.com
      http:
        paths:
          - path: /
            pathType: Prefix
            backend:
              service:
                name: my-app-service
                port:
                  number: 8000
```

### Size Guidelines

Choose an appropriate size limit based on your use case:

| Use Case | Recommended Limit |
|----------|-------------------|
| Small JSON APIs | `10m` (10 MB) |
| File uploads (images) | `50m` (50 MB) |
| Large file uploads (videos, archives) | `100m` - `500m` |
| Unlimited uploads | `0` (no limit - **not recommended**) |

⚠️ **Security Warning**: Setting very large limits can expose your cluster to DoS attacks. Always set reasonable limits based on your actual needs.

---

## Other Common Nginx Ingress Annotations

### Timeout Configuration

For long-running requests (uploads, downloads, long-polling):

```yaml
annotations:
  nginx.ingress.kubernetes.io/proxy-read-timeout: "600"  # 10 minutes
  nginx.ingress.kubernetes.io/proxy-send-timeout: "600"  # 10 minutes
  nginx.ingress.kubernetes.io/proxy-connect-timeout: "600"  # 10 minutes
```

### SSL/TLS Configuration

```yaml
annotations:
  nginx.ingress.kubernetes.io/force-ssl-redirect: "true"  # Force HTTPS
  nginx.ingress.kubernetes.io/ssl-protocols: "TLSv1.2 TLSv1.3"  # TLS versions
  cert-manager.io/cluster-issuer: "letsencrypt-prod"  # Auto TLS with cert-manager
```

### Rate Limiting

```yaml
annotations:
  nginx.ingress.kubernetes.io/limit-rps: "10"  # 10 requests per second per IP
  nginx.ingress.kubernetes.io/limit-rpm: "100"  # 100 requests per minute per IP
```

### CORS Configuration

```yaml
annotations:
  nginx.ingress.kubernetes.io/enable-cors: "true"
  nginx.ingress.kubernetes.io/cors-allow-origin: "https://example.com"
  nginx.ingress.kubernetes.io/cors-allow-methods: "GET, POST, PUT, DELETE, OPTIONS"
```

### Connection and Buffer Limits

```yaml
annotations:
  nginx.ingress.kubernetes.io/proxy-body-size: "50m"  # Request body size
  nginx.ingress.kubernetes.io/proxy-buffer-size: "8k"  # Response buffer
  nginx.ingress.kubernetes.io/client-body-buffer-size: "8k"  # Request buffer
  nginx.ingress.kubernetes.io/proxy-buffering: "on"  # Enable buffering
```

---

## Verifying Your Configuration

### 1. Check Ingress Annotations

```bash
kubectl get ingress <ingress-name> -n <namespace> -o yaml
```

Look for the annotations section:
```yaml
metadata:
  annotations:
    nginx.ingress.kubernetes.io/proxy-body-size: 50m
```

### 2. Check Nginx Ingress Controller Logs

```bash
# Find the ingress controller pod
kubectl get pods -n ingress-nginx

# View logs
kubectl logs -n ingress-nginx <ingress-controller-pod-name>
```

Look for errors like:
- `client intended to send too large body`
- `upstream sent too big header`

### 3. Test with curl

```bash
# Test with a large payload
dd if=/dev/zero of=test-file bs=1M count=10  # Create 10MB file
curl -X POST -F "file=@test-file" https://your-api.example.com/upload

# Should succeed with proper annotation, fail with 413 without it
```

---

## Debugging Steps

If you're still getting 413 errors after adding the annotation:

1. **Verify annotation is applied**:
   ```bash
   kubectl describe ingress <ingress-name> -n <namespace>
   ```

2. **Check if ingress controller is nginx**:
   ```bash
   kubectl get ingressclass
   ```

3. **Restart ingress controller** (annotations are usually picked up automatically, but restart if needed):
   ```bash
   kubectl rollout restart deployment -n ingress-nginx nginx-ingress-controller
   ```

4. **Check for conflicting ConfigMap settings**:
   ```bash
   kubectl get configmap -n ingress-nginx nginx-configuration -o yaml
   ```

5. **Test from inside the cluster** to rule out external proxy issues:
   ```bash
   kubectl run -it --rm debug --image=curlimages/curl --restart=Never -- \
     curl -X POST -d @large-file.json http://<service-name>.<namespace>.svc.cluster.local:<port>/endpoint
   ```

---

## References

- [Nginx Ingress Annotations Documentation](https://kubernetes.github.io/ingress-nginx/user-guide/nginx-configuration/annotations/)
- [Nginx Ingress Configuration Examples](https://kubernetes.github.io/ingress-nginx/examples/)
- [Helm Chart Template Documentation](./TEMPLATES.md)
- [App Types and Ingress Configuration](./APP_TYPES.md)

---

## Quick Fix Summary

**For the 413 error**, add this to your ingress configuration:

```yaml
ingress:
  enabled: true
  className: nginx
  annotations:
    nginx.ingress.kubernetes.io/proxy-body-size: "50m"
```

**Yes, this is on your ingress** - the 413 error comes from the nginx ingress controller, not from your application. The annotation must be added to the Ingress resource to fix it.
