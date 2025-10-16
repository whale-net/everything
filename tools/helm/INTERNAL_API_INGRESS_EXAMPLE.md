# Internal API Ingress Exposure Example

This example demonstrates how to expose an internal-api app via Ingress for debugging purposes.

## Default Behavior (No Ingress)

By default, `internal-api` apps do NOT generate an Ingress resource. They are only accessible within the cluster via the Service.

```yaml
# values.yaml (default behavior)
apps:
  my-internal-api:
    type: internal-api
    port: 8080
    exposeIngress: false  # This field is included for internal-api apps
```

**Note**: The `exposeIngress` field only appears in values.yaml for `internal-api` app types. For `external-api`, `worker`, and `job` types, this field is not included.

This generates:
- ✅ Deployment
- ✅ Service
- ❌ Ingress (not generated)

## Exposing Internal API for Debugging

To expose an internal-api externally (for debugging, testing, or temporary access):

### Option 1: Override in values.yaml

```yaml
# custom-values.yaml
apps:
  my-internal-api:
    exposeIngress: true  # Enable Ingress
    ingress:
      host: my-internal-api-debug.example.com
      tlsSecretName: my-internal-api-tls
```

Deploy with:
```bash
helm install my-app ./chart/ --values custom-values.yaml
```

### Option 2: Helm command line

```bash
helm install my-app ./chart/ \
  --set apps.my-internal-api.exposeIngress=true
```

### Option 3: Helm upgrade existing deployment

```bash
helm upgrade my-app ./chart/ \
  --set apps.my-internal-api.exposeIngress=true \
  --reuse-values
```

## Generated Resources

When `exposeIngress: true`:
- ✅ Deployment
- ✅ Service
- ✅ Ingress (newly generated)

The Ingress will use the same pattern as external-api apps:
- **Default host**: `{appName}-{environment}.local` (e.g., `my-internal-api-dev.local`)
- **Custom host**: Set via `ingress.host` configuration (see examples above)
- **Path**: `/` (Prefix)
- **Service**: `{appName}-{environment}-service`

## Use Cases

1. **Development/Testing**: Temporarily expose internal API to test from outside cluster
2. **Debugging**: Access internal API directly during troubleshooting
3. **Integration Testing**: Expose internal API for external test runners
4. **Staging Environments**: Enable external access in non-production environments

## Security Considerations

⚠️ **Important**: `exposeIngress` should typically be `false` in production environments.

When exposing internal APIs:
- Use proper authentication/authorization
- Restrict access via Ingress annotations (IP whitelisting, etc.)
- Use TLS/HTTPS for encrypted communication
- Consider network policies for additional security

## Example: Temporary Debug Access

```bash
# Enable debug access with default host pattern
helm upgrade my-app ./chart/ \
  --set apps.my-internal-api.exposeIngress=true

# Test the API using the default host pattern
curl http://my-internal-api-dev.local/health

# Or configure a custom host
helm upgrade my-app ./chart/ \
  --set apps.my-internal-api.exposeIngress=true \
  --set apps.my-internal-api.ingress.host=debug.example.com

# Test with custom host
curl https://debug.example.com/health

# Disable debug access
helm upgrade my-app ./chart/ \
  --set apps.my-internal-api.exposeIngress=false
```

## Comparison with external-api

| Feature | external-api | internal-api (default) | internal-api (exposeIngress=true) |
|---------|-------------|------------------------|-----------------------------------|
| Deployment | ✅ | ✅ | ✅ |
| Service | ✅ | ✅ | ✅ |
| Ingress | ✅ Always | ❌ Never | ✅ Optional |
| Use Case | Public APIs | Cluster-only services | Debug/testing internal services |
