# Host Manager Self-Registration

## Overview

The ManManV2 host manager now supports **self-registration**, allowing it to autonomously register with the control plane without requiring a pre-configured `SERVER_ID`. This simplifies deployment and enables dynamic scaling of compute resources.

## Features

- **Stable server identification**: Reuses same server record across restarts
- **Environment-based organization**: Group servers by deployment environment (dev/prod/staging)
- **Minimal security**: Registration protected by shared secret key
- **Multiple managers per host**: Run dev and prod on same physical server
- **Automatic capability reporting**: Host detects and reports Docker resources during registration
- **Configuration preservation**: Server associations and configs survive restarts

## Configuration

### API Server

Add the registration key to your API server environment:

```bash
REGISTRATION_KEY=your-secret-key-here
```

If `REGISTRATION_KEY` is not set, the API will allow any registration (insecure, for development only).

### Host Manager

#### Recommended Configuration (with Environment)

```bash
# Required
API_ADDRESS=api.example.com:50051
REGISTRATION_KEY=your-secret-key-here
RABBITMQ_URL=amqp://user:pass@rabbitmq:5672/vhost
DOCKER_SOCKET=/var/run/docker.sock

# Recommended: Set environment for stable naming
ENVIRONMENT=production

# Optional: Override server name explicitly
# SERVER_NAME=gameserver-nyc-01
```

#### Multiple Managers on Same Host

Perfect for running dev and prod on one physical server:

```bash
# Dev manager
ENVIRONMENT=dev
API_ADDRESS=localhost:50051
REGISTRATION_KEY=secret
RABBITMQ_URL=amqp://user:pass@localhost:5672/manmanv2-dev

# Prod manager (same host)
ENVIRONMENT=production
API_ADDRESS=localhost:50051
REGISTRATION_KEY=secret
RABBITMQ_URL=amqp://user:pass@localhost:5672/manmanv2-prod
```

Each gets its own server record: `hostname-dev` and `hostname-production`.

## How It Works

### Self-Registration Flow

1. **Host Startup**: Host manager starts
2. **Name Generation**: Creates stable name based on:
   - If `SERVER_NAME` set: Use that value
   - Else if `ENVIRONMENT` set: Use `hostname-environment` (e.g., `myserver-production`)
   - Else: Use `hostname-{uuid}` (⚠️ creates new server each restart!)
3. **Capability Detection**: Queries Docker for system resources
4. **API Registration**: Calls `RegisterServer` RPC with:
   - Generated/configured name
   - System capabilities (CPU, memory, Docker version)
   - Registration key for authentication
   - Environment tag (optional)
5. **Server Lookup**: API checks if server with this name already exists
6. **Reuse or Create**:
   - If found: Returns existing `server_id` (preserves configurations!)
   - If not found: Creates new server record
7. **Heartbeat**: Host sends periodic heartbeats using assigned ID

### Sequence Diagram

```
Host Manager                    API Server              Database
    |                               |                       |
    |-- Generate name               |                       |
    |   (hostname-environment)      |                       |
    |                               |                       |
    |-- Get Docker info             |                       |
    |                               |                       |
    |-- RegisterServer ------------>|                       |
    |   (name, caps, key, env)      |                       |
    |                               |-- Verify key          |
    |                               |                       |
    |                               |-- GetByName --------->|
    |                               |<-- Found (ID=42) -----|
    |                               |   OR                  |
    |                               |<-- Not Found ---------|
    |                               |-- Create if needed -->|
    |                               |                       |
    |                               |-- Update status ----->|
    |                               |   (online, last_seen) |
    |                               |                       |
    |<-- server_id=42 --------------|                       |
    |                               |                       |
    |-- Start operations            |                       |
    |   (using ID 42)               |                       |
    |                               |                       |
    |-- Heartbeat(42) ------------->|                       |
    |                               |-- Update status ----->|
    |<-- Acknowledged --------------|                       |
    |                               |                       |
    [RESTART]                       |                       |
    |                               |                       |
    |-- RegisterServer ------------>|                       |
    |   (same name!)                |                       |
    |                               |-- GetByName --------->|
    |                               |<-- Found (ID=42) -----|
    |                               |   (reuses same!)      |
    |<-- server_id=42 --------------|                       |
    |   (configs preserved!)        |                       |
```

## Security Considerations

### Registration Key

The registration key provides **minimal security** through a shared secret:

- **Purpose**: Prevents unauthorized hosts from joining the cluster
- **Strength**: Simple string comparison, not cryptographic
- **Deployment**: Must be distributed to all legitimate hosts
- **Rotation**: Requires restarting API and hosts

### Best Practices

1. **Use a strong key**: Generate with `openssl rand -hex 32`
2. **Keep it secret**: Don't commit to version control
3. **Use environment variables**: Never hardcode in source
4. **Rotate regularly**: Change key and redeploy periodically
5. **Network security**: Use TLS for gRPC in production
6. **Firewall rules**: Restrict API access to trusted networks

### Limitations

This is **minimal security**, suitable for:
- Internal networks behind firewalls
- Development/testing environments
- Trusted datacenter deployments

For production environments with higher security requirements, consider adding:
- TLS mutual authentication (mTLS)
- Token-based authentication with expiration
- Integration with identity providers (OAuth, LDAP, etc.)
- Network policies restricting pod-to-pod communication

## API Changes

### RegisterServer RPC

**Before:**
```protobuf
message RegisterServerRequest {
  string name = 1;
  ServerCapabilities capabilities = 2;
}
```

**After:**
```protobuf
message RegisterServerRequest {
  string name = 1;
  ServerCapabilities capabilities = 2;
  string registration_key = 3;  // New field
}
```

### Error Responses

| Error | Code | Description |
|-------|------|-------------|
| Invalid registration key | `PERMISSION_DENIED` | Key doesn't match configured value |
| Missing name | `INVALID_ARGUMENT` | Name field is required |
| Database error | `INTERNAL` | Failed to create or update server |

## Deployment Examples

### Docker Compose

```yaml
services:
  manmanv2-api:
    image: ghcr.io/whale-net/manmanv2-api:latest
    environment:
      REGISTRATION_KEY: ${REGISTRATION_KEY}
      # ... other config

  manmanv2-host:
    image: ghcr.io/whale-net/manmanv2-host:latest
    environment:
      API_ADDRESS: manmanv2-api:50051
      REGISTRATION_KEY: ${REGISTRATION_KEY}
      RABBITMQ_URL: amqp://rabbit:password@rabbitmq:5672/manmanv2
      # SERVER_ID not set - uses self-registration
    volumes:
      - /var/run/docker.sock:/var/run/docker.sock
```

### Kubernetes

```yaml
apiVersion: v1
kind: Secret
metadata:
  name: manmanv2-registration
stringData:
  registration-key: "your-secret-key-here"

---

apiVersion: apps/v1
kind: DaemonSet
metadata:
  name: manmanv2-host
spec:
  selector:
    matchLabels:
      app: manmanv2-host
  template:
    metadata:
      labels:
        app: manmanv2-host
    spec:
      hostNetwork: true
      containers:
      - name: host
        image: ghcr.io/whale-net/manmanv2-host:latest
        env:
        - name: API_ADDRESS
          value: "manmanv2-api.manman.svc.cluster.local:50051"
        - name: REGISTRATION_KEY
          valueFrom:
            secretKeyRef:
              name: manmanv2-registration
              key: registration-key
        - name: RABBITMQ_URL
          value: "amqp://rabbit:password@rabbitmq:5672/manmanv2"
        volumeMounts:
        - name: docker-sock
          mountPath: /var/run/docker.sock
      volumes:
      - name: docker-sock
        hostPath:
          path: /var/run/docker.sock
```

### Bare Metal (systemd)

```ini
# /etc/systemd/system/manmanv2-host.service
[Unit]
Description=ManManV2 Host Manager
After=docker.service
Requires=docker.service

[Service]
Type=simple
ExecStart=/usr/local/bin/manmanv2-host
Environment="API_ADDRESS=api.manman.example.com:50051"
Environment="REGISTRATION_KEY=your-secret-key-here"
Environment="RABBITMQ_URL=amqp://user:pass@rabbitmq.example.com:5672/manmanv2"
Environment="DOCKER_SOCKET=/var/run/docker.sock"
Restart=always
RestartSec=10

[Install]
WantedBy=multi-user.target
```

## Troubleshooting

### Registration Fails with "invalid registration key"

**Cause**: Key mismatch between host and API

**Solution**:
1. Check `REGISTRATION_KEY` on both API and host
2. Ensure no extra whitespace or newlines
3. Verify environment variable is being loaded

```bash
# On host
echo $REGISTRATION_KEY

# On API pod
kubectl exec -it manmanv2-api-xxx -- printenv REGISTRATION_KEY
```

### Host keeps re-registering with new IDs

**Cause**: Host isn't persisting server_id or restarts too often

**Solution**: This is expected behavior for self-registration. Each restart generates a new identity. If persistence is needed:
1. Use legacy `SERVER_ID` mode instead
2. Or mount a volume to persist state (future feature)

### "Failed to connect to API" error

**Cause**: Network connectivity or wrong `API_ADDRESS`

**Solution**:
1. Verify API is running: `kubectl get pods -n manman`
2. Test connectivity: `telnet api.manman.svc.cluster.local 50051`
3. Check DNS resolution
4. Verify firewall rules

### Docker capability detection fails

**Cause**: Docker socket not accessible

**Solution**:
1. Verify socket path: `ls -la /var/run/docker.sock`
2. Check permissions: Host process needs read/write access
3. If using Docker Desktop: Set `DOCKER_SOCKET=/var/run/docker.sock.raw`

## Migration Guide

### From Legacy Mode to Self-Registration

1. **Backup your database**: Self-registration creates new server records
2. **Update configuration**: Add `API_ADDRESS` and `REGISTRATION_KEY`, remove `SERVER_ID`
3. **Redeploy hosts**: New registrations will create new entries
4. **Clean up old servers**: Delete old server records from database

```sql
-- List inactive servers (not seen in 24 hours)
SELECT server_id, name, status, last_seen
FROM servers
WHERE last_seen < NOW() - INTERVAL '24 hours';

-- Delete old servers (be careful!)
DELETE FROM servers
WHERE server_id IN (1, 2, 3);  -- Replace with actual IDs
```

### From Self-Registration to Legacy Mode

1. **Note assigned server IDs**: Check database or logs
2. **Update configuration**: Set `SERVER_ID` to the assigned value
3. **Remove**: `API_ADDRESS` and `REGISTRATION_KEY` no longer needed
4. **Redeploy**: Host will use pre-configured ID

## Future Enhancements

Potential improvements to consider:

1. **ID Persistence**: Store server_id in local file to survive restarts
2. **Certificate-based auth**: Replace shared key with mTLS
3. **Registration approval**: Admin must approve new hosts before activation
4. **Metadata**: Allow hosts to report custom labels/tags during registration
5. **Deregistration**: Explicit API to remove host on graceful shutdown
6. **Re-registration detection**: Detect if same physical host re-registers with new ID

## Related Documentation

- [ManManV2 Architecture](./architecture.md)
- [Configuration Guide](./configuration.md)
- [API Reference](./api-reference.md)
- [Deployment Guide](./deployment.md)
