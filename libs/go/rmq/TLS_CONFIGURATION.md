# RabbitMQ TLS/SSL Configuration

## Overview

The rmq library supports TLS/SSL connections to RabbitMQ when using `amqps://` URLs. TLS configuration is automatically loaded from environment variables.

## Environment Variables

### `RABBITMQ_SSL_VERIFY`

Controls SSL certificate verification.

- **Default**: `true` (verify certificates)
- **Values**: `true` | `false`
- **Security**: Setting to `false` is **insecure** and should only be used for development/testing

```bash
# Development: Disable certificate verification (INSECURE - dev only!)
export RABBITMQ_SSL_VERIFY=false

# Production: Verify certificates (default, recommended)
export RABBITMQ_SSL_VERIFY=true
# or simply omit the variable
```

### `RABBITMQ_CA_CERT_PATH`

Path to a custom CA certificate file for verifying the RabbitMQ server's certificate.

- **Default**: (empty) - uses system CA certificates
- **Format**: PEM-encoded certificate file
- **Use case**: Self-signed certificates or private CA

```bash
# Use custom CA certificate
export RABBITMQ_CA_CERT_PATH=/etc/ssl/certs/rabbitmq-ca.crt
```

## Usage

### Basic Connection (Non-TLS)

```go
import "github.com/whale-net/everything/libs/go/rmq"

// Connect without TLS
conn, err := rmq.NewConnectionFromURL("amqp://user:pass@localhost:5672/")
```

### TLS Connection with Auto-Configuration

```go
import "github.com/whale-net/everything/libs/go/rmq"

// Connect with TLS - configuration loaded from environment variables
conn, err := rmq.NewConnectionFromURL("amqps://user:pass@rabbitmq.example.com:5671/")
```

### TLS Connection with Explicit Configuration

```go
import "github.com/whale-net/everything/libs/go/rmq"

tlsConfig := &rmq.TLSConfig{
	Enabled:            true,
	InsecureSkipVerify: false, // Verify certificates (secure)
	CACertPath:         "/path/to/ca.crt",
}

conn, err := rmq.NewConnectionWithTLS("amqps://user:pass@rabbitmq.example.com:5671/", tlsConfig)
```

## Kubernetes Deployment

### Option 1: Mount CA Certificate from ConfigMap/Secret

```yaml
apiVersion: v1
kind: ConfigMap
metadata:
  name: rabbitmq-ca-cert
data:
  ca.crt: |
    -----BEGIN CERTIFICATE-----
    MIIDXTCCAkWgAwIBAgIJAKZ...
    -----END CERTIFICATE-----
---
apiVersion: apps/v1
kind: Deployment
metadata:
  name: manmanv2-api
spec:
  template:
    spec:
      containers:
      - name: api
        env:
        - name: RABBITMQ_URL
          value: "amqps://user:pass@rabbitmq:5671/vhost"
        - name: RABBITMQ_CA_CERT_PATH
          value: /etc/rabbitmq/ca.crt
        volumeMounts:
        - name: rabbitmq-ca
          mountPath: /etc/rabbitmq
          readOnly: true
      volumes:
      - name: rabbitmq-ca
        configMap:
          name: rabbitmq-ca-cert
```

### Option 2: Use System CA Certificates

If RabbitMQ uses a certificate signed by a trusted CA:

```yaml
apiVersion: apps/v1
kind: Deployment
metadata:
  name: manmanv2-api
spec:
  template:
    spec:
      containers:
      - name: api
        env:
        - name: RABBITMQ_URL
          value: "amqps://user:pass@rabbitmq.example.com:5671/vhost"
        # No additional configuration needed - uses system CA bundle
```

### Option 3: Development/Testing (Insecure)

**⚠️ WARNING**: Only for development environments!

```yaml
apiVersion: apps/v1
kind: Deployment
metadata:
  name: manmanv2-api-dev
spec:
  template:
    spec:
      containers:
      - name: api
        env:
        - name: RABBITMQ_URL
          value: "amqps://user:pass@rabbitmq-dev:5671/vhost"
        - name: RABBITMQ_SSL_VERIFY
          value: "false"  # INSECURE - dev only!
```

## Migration from amqp:// to amqps://

1. **Update connection URL** from `amqp://` to `amqps://` (port typically changes from 5672 to 5671)
2. **Choose security approach**:
   - Production: Mount CA certificate and set `RABBITMQ_CA_CERT_PATH`
   - Testing: Use `RABBITMQ_SSL_VERIFY=false` (insecure)
3. **Test connection** before deploying to production

## Security Best Practices

1. ✅ **Always verify certificates in production** (`RABBITMQ_SSL_VERIFY=true` or omit)
2. ✅ **Mount CA certificates** for self-signed or private CAs
3. ✅ **Use Kubernetes Secrets** for sensitive certificates
4. ❌ **Never disable verification in production** (`RABBITMQ_SSL_VERIFY=false`)
5. ✅ **Rotate certificates** regularly following your security policy

## Troubleshooting

### Error: "x509: certificate signed by unknown authority"

**Cause**: The RabbitMQ server's certificate is signed by a CA that's not in the system trust store.

**Solution**: Provide the CA certificate via `RABBITMQ_CA_CERT_PATH`.

### Error: "x509: certificate is valid for X, not Y"

**Cause**: Hostname in the connection URL doesn't match the certificate's CN or SAN.

**Solutions**:
1. Use the correct hostname in the connection URL
2. Get a certificate with the correct hostname
3. For development only: Use `RABBITMQ_SSL_VERIFY=false` (insecure)

### Error: "remote error: tls: bad certificate"

**Cause**: Client certificate authentication is required but not provided.

**Solution**: This library currently doesn't support client certificates. File an issue if needed.

## Implementation Details

The library automatically detects `amqps://` URLs and applies TLS configuration:

1. Checks URL scheme (`amqps://` triggers TLS)
2. Loads configuration from environment variables
3. Builds `tls.Config` with specified options
4. Uses `amqp.DialTLS()` for secure connection

## Related Issues

- Fixes: #328 - RabbitMQ SSL certificate verification failures
