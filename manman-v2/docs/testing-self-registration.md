# Testing Self-Registration

Quick guide for testing the new self-registration feature.

## Local Testing with Tilt

### 1. Update Environment Configuration

Edit `manman-v2/.env`:

```bash
# Add registration key (or use .env.example as template)
REGISTRATION_KEY=test-key-123

# For host manager (if running locally outside Tilt)
API_ADDRESS=localhost:30051  # Note: Kubernetes NodePort
```

### 2. Start Control Plane

```bash
cd manman-v2
tilt up
```

Wait for services to be ready:
- PostgreSQL: Running
- RabbitMQ: Running
- API Server: Running

### 3. Test Self-Registration (Option A: Local Binary)

Build and run host manager locally:

```bash
# Build
bazel build //manman/host:host

# Run with self-registration
API_ADDRESS=localhost:30051 \
REGISTRATION_KEY=test-key-123 \
RABBITMQ_URL=amqp://rabbit:password@localhost:30672/manmanv2-dev \
DOCKER_SOCKET=/var/run/docker.sock \
bazel-bin/manman/host/host_/host
```

Expected output:
```
Starting ManManV2 Host Manager (self-registration mode)
Registered as server 'myhost-a1b2c3d4'
Successfully registered with control plane (server_id=1)
Connecting to Docker...
Connecting to RabbitMQ...
...
ManManV2 Host Manager is running. Press Ctrl+C to stop.
```

### 4. Test Self-Registration (Option B: Docker)

```bash
docker run --rm \
  -e API_ADDRESS=host.docker.internal:30051 \
  -e REGISTRATION_KEY=test-key-123 \
  -e RABBITMQ_URL=amqp://rabbit:password@host.docker.internal:30672/manmanv2-dev \
  -v /var/run/docker.sock:/var/run/docker.sock \
  ghcr.io/whale-net/manmanv2-host:latest
```

### 5. Verify Registration

Check the database to see registered servers:

```bash
# Connect to PostgreSQL
kubectl exec -it -n manman-dev $(kubectl get pods -n manman-dev -l app=postgres -o jsonpath='{.items[0].metadata.name}') -- \
  psql -U postgres -d manman

# Query servers
SELECT server_id, name, status, last_seen FROM servers;
```

Expected result:
```
 server_id |        name         | status | last_seen
-----------+---------------------+--------+---------------------------
         1 | myhost-a1b2c3d4     | online | 2025-02-07 12:34:56.789
```

### 6. Test Invalid Registration Key

Try registering with wrong key:

```bash
REGISTRATION_KEY=wrong-key \
API_ADDRESS=localhost:30051 \
RABBITMQ_URL=amqp://rabbit:password@localhost:30672/manmanv2-dev \
bazel-bin/manman/host/host_/host
```

Expected error:
```
Fatal error: failed to self-register: registration failed: rpc error: code = PermissionDenied desc = invalid registration key
```

## Integration Testing

### Test Heartbeat After Registration

1. Start host manager (self-registration)
2. Watch logs for heartbeat messages (every 5 seconds)
3. Check database for updated `last_seen` timestamps

```sql
SELECT server_id, name, status, last_seen FROM servers;
-- last_seen should update every few seconds
```

### Test Session Lifecycle

1. Register host via self-registration
2. Create a test game config and server game config
3. Start a session targeting the auto-registered server
4. Verify session starts successfully

```bash
# Use grpcurl or API client
grpcurl -plaintext localhost:30051 manman.v1.ManManAPI/ListServers
grpcurl -plaintext localhost:30051 manman.v1.ManManAPI/StartSession
```

### Test Multiple Hosts

Start multiple host managers to verify unique name generation:

```bash
# Terminal 1
bazel-bin/manman/host/host_/host

# Terminal 2 (different host, or same host with different name)
bazel-bin/manman/host/host_/host

# Check database
SELECT server_id, name FROM servers;
-- Should see 2 different servers with unique names
```

## Cleanup

### Remove Test Servers

```sql
-- Delete test servers
DELETE FROM servers WHERE name LIKE '%test%';

-- Or reset entire database
TRUNCATE servers CASCADE;
```

### Stop Services

```bash
tilt down
```

## Common Issues

### "Failed to connect to API"
- Check API is accessible: `curl -v telnet://localhost:30051`
- Verify Kubernetes port-forward: `kubectl port-forward -n manman-dev svc/manmanv2-api 30051:50051`

### "Failed to connect to Docker"
- Check Docker is running: `docker ps`
- Verify socket permissions: `ls -la /var/run/docker.sock`

### "Registration failed: context deadline exceeded"
- API server may be slow to start
- Increase gRPC timeout in code (currently default)
- Check API logs: `kubectl logs -n manman-dev deployment/manmanv2-api`

## Manual Testing Checklist

- [ ] Self-registration with valid key succeeds
- [ ] Self-registration with invalid key fails
- [ ] Unique server names generated for each host
- [ ] Server appears in database with "online" status
- [ ] Heartbeat updates `last_seen` timestamp
- [ ] Server capabilities recorded correctly
- [ ] Sessions can be started on auto-registered hosts
- [ ] Host can re-register after restart (creates new ID)
- [ ] Legacy mode (with SERVER_ID) still works
- [ ] Multiple hosts can register simultaneously

## Automated Testing

TODO: Add integration tests using:
- `go test` with testcontainers for PostgreSQL/RabbitMQ
- Mock gRPC server for API testing
- Docker-in-Docker for host manager testing
