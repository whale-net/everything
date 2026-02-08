# ManManV2 Host Manager - Local Development Setup

The host manager runs on **bare metal** (not in Kubernetes) because it needs direct access to the Docker daemon to orchestrate game server containers.

## Prerequisites

1. **Control plane running**: Start the ManManV2 Tiltfile first
   ```bash
   cd manman-v2
   tilt up
   ```

2. **Docker installed**: Host manager uses Docker SDK
   ```bash
   docker --version
   ```

3. **RabbitMQ accessible**: Either from Tilt (localhost:5672) or custom

## Architecture

```
┌─────────────────────────────────────────┐
│  Control Plane (K8s via Tilt)           │
│  - PostgreSQL (localhost:5432)          │
│  - RabbitMQ (localhost:5672)            │
│  - API (localhost:50051)                │
│  - Event Processor                      │
└──────────────┬──────────────────────────┘
               │ RabbitMQ (commands/status)
               │
┌──────────────▼──────────────────────────┐
│  Host Manager (Bare Metal - YOU RUN)    │
│  - Listens to RabbitMQ commands         │
│  - Manages Docker containers directly   │
│  - Stdin forwarding via attach          │
│  - Publishes session status             │
└──────────────┬──────────────────────────┘
               │ Docker SDK (attach/create/stop)
               │
┌──────────────▼──────────────────────────┐
│  Docker Daemon (localhost)              │
│  - Game server containers               │
└─────────────────────────────────────────┘
```

## Building the Test Game Server Image

For testing the host manager with a simple game server:

```bash
# Build from testdata directory
docker build -t manmanv2-test-game-server \
  -f manman/testdata/Dockerfile \
  manman/testdata/

# Verify image
docker images | grep manmanv2-test-game-server
```

This creates an Alpine-based container with a bash script that simulates a game server for integration testing.

## Running the Host Manager

### Option 1: Run with Bazel

```bash
# From repo root
bazel run //manman/host:host -- \
  --server-id=host-local-dev-1 \
  --rabbitmq-url=amqp://rabbit:password@localhost:5672/manmanv2-dev \
  --docker-socket=/var/run/docker.sock
```

### Option 2: Build and run binary directly

```bash
# Build the binary
bazel build //manman/host:host

# Find the binary location
ls -la bazel-bin/manman/host/host_/host

# Run it
./bazel-bin/manman/host/host_/host \
  --server-id=host-local-dev-1 \
  --rabbitmq-url=amqp://rabbit:password@localhost:5672/manmanv2-dev \
  --docker-socket=/var/run/docker.sock
```

### Option 3: Use environment variables

```bash
# Set environment variables
export SERVER_ID=host-local-dev-1
export RABBITMQ_URL=amqp://rabbit:password@localhost:5672/manmanv2-dev
export DOCKER_SOCKET=/var/run/docker.sock

# Run the binary
bazel run //manman/host:host
```

## Configuration

The host manager accepts these configuration options:

| Environment Variable | Default | Description |
|---------------------|---------|-------------|
| `API_ADDRESS` | `localhost:50051` | API server address for self-registration |
| `API_USE_TLS` | *(auto-detect)* | Force TLS enable (`true`/`false`); auto-detects from address if omitted |
| `API_TLS_SKIP_VERIFY` | `false` | Skip certificate verification (INSECURE - dev only) |
| `API_CA_CERT_PATH` | *(system CA)* | Path to custom CA certificate file |
| `API_TLS_SERVER_NAME` | *(from address)* | Server name for certificate verification (SNI) |
| `SERVER_NAME` | *(auto-generated)* | Override server name (default: `hostname-environment`) |
| `ENVIRONMENT` | *(none)* | Environment label for server grouping (e.g., `dev`, `prod`) |
| `RABBITMQ_URL` | *(required)* | RabbitMQ connection URL with vhost |
| `DOCKER_SOCKET` | `/var/run/docker.sock` | Path to Docker socket |

### TLS Configuration

The host manager supports TLS connections to the API server for production deployments. TLS is automatically enabled when the API address contains `:443` or starts with `https://`.

**Auto-detection examples:**
- `API_ADDRESS=localhost:50051` → Plaintext (local development)
- `API_ADDRESS=dev-api.manmanv2.whalenet.dev:443` → TLS enabled
- `API_ADDRESS=https://api.example.com` → TLS enabled

**Usage scenarios:**

1. **Local development (plaintext):**
   ```bash
   export API_ADDRESS=localhost:50051
   bazel run //manman/host:host-manager
   ```

2. **Production with TLS (auto-detected):**
   ```bash
   export API_ADDRESS=dev-api.manmanv2.whalenet.dev:443
   bazel run //manman/host:host-manager
   ```

3. **Development with TLS but skip verification (INSECURE):**
   ```bash
   export API_ADDRESS=dev-api.manmanv2.whalenet.dev:443
   export API_TLS_SKIP_VERIFY=true
   bazel run //manman/host:host-manager
   ```

4. **Kubernetes internal service with external certificate:**
   ```bash
   # Connect to k8s service but verify against external domain cert
   export API_ADDRESS=manmanv2-api.namespace.svc.cluster.local:50051
   export API_USE_TLS=true
   export API_TLS_SERVER_NAME=dev-api.manmanv2.whalenet.dev
   bazel run //manman/host:host-manager
   ```

5. **Custom CA certificate:**
   ```bash
   export API_ADDRESS=api.example.com:443
   export API_CA_CERT_PATH=/etc/ssl/certs/custom-ca.crt
   bazel run //manman/host:host-manager
   ```

**Security warnings:**
- **NEVER** use `API_TLS_SKIP_VERIFY=true` in production environments
- This setting disables certificate verification and is vulnerable to man-in-the-middle attacks
- Only use for local development with self-signed certificates

### RabbitMQ URL Format

The RabbitMQ URL must include the vhost for environment isolation:

```
amqp://[user]:[password]@[host]:[port]/[vhost]

Example (local dev):
amqp://rabbit:password@localhost:5672/manmanv2-dev

Example (custom):
amqp://myuser:mypass@rabbitmq.example.com:5672/manmanv2-staging
```

## Verifying the Host is Running

### Check RabbitMQ Messages

1. Open RabbitMQ Management UI: http://localhost:15672
2. Login: `rabbit` / `password`
3. Go to "Exchanges" → "manman"
4. You should see messages being published:
   - `status.host.online` (on startup)
   - `health.heartbeat` (every 30 seconds)

### Check the API

```bash
# List registered servers (requires grpcurl)
grpcurl -plaintext localhost:50051 manman.ManManAPI/ListServers

# Check server health
grpcurl -plaintext \
  -d '{"server_id": "host-local-dev-1"}' \
  localhost:50051 \
  manman.ManManAPI/GetServer
```

## Testing with a Session

Once the host is running, test the full flow:

```bash
# 1. Create a game config (via API)
grpcurl -plaintext \
  -d '{
    "name": "test-game",
    "image": "manmanv2-test-game-server:latest",
    "description": "Test game for development"
  }' \
  localhost:50051 \
  manman.ManManAPI/CreateGame

# 2. Create a server game config
grpcurl -plaintext \
  -d '{
    "server_id": "host-local-dev-1",
    "game_id": 1,
    "name": "test-deployment"
  }' \
  localhost:50051 \
  manman.ManManAPI/CreateServerGameConfig

# 3. Start a session
grpcurl -plaintext \
  -d '{
    "server_game_config_id": 1
  }' \
  localhost:50051 \
  manman.ManManAPI/StartSession

# 4. Check session status
grpcurl -plaintext \
  -d '{"id": 1}' \
  localhost:50051 \
  manman.ManManAPI/GetSession

# 5. Check Docker containers
docker ps | grep game-

# You should see one game container per session:
# - game-<server_id>-<sgc_id>

# 6. Stop the session
grpcurl -plaintext \
  -d '{"session_id": 1}' \
  localhost:50051 \
  manman.ManManAPI/StopSession
```

## Troubleshooting

### Host manager can't connect to RabbitMQ

**Problem**: `Failed to connect to RabbitMQ`

**Solution**: Verify RabbitMQ is accessible:
```bash
# Check RabbitMQ is running (from Tilt)
kubectl get pods -n manmanv2-local-dev | grep rabbitmq

# Verify port forward
netstat -an | grep 5672

# Test connection
telnet localhost 5672
```

### Docker permission denied

**Problem**: `permission denied while trying to connect to Docker daemon`

**Solution**: Add your user to the docker group:
```bash
sudo usermod -aG docker $USER
# Log out and back in
```

### Orphaned containers after crashes

**Problem**: Containers left running after host crash

**Solution**: Host manager automatically recovers orphans on startup. To manually clean up:
```bash
# Find manmanv2 containers
docker ps -a | grep manmanv2

# Stop and remove
docker stop $(docker ps -a -q --filter "label=manmanv2.managed=true")
docker rm $(docker ps -a -q --filter "label=manmanv2.managed=true")
```

## Advanced: Custom Docker Network

By default, the host manager creates isolated Docker networks for each session (`session-<id>`). To customize:

```go
// In host/session/manager.go
networkName := fmt.Sprintf("custom-net-%s", sessionID)
```

## Advanced: Multiple Host Managers

To test multi-host scenarios:

```bash
# Terminal 1 - Host A
export SERVER_ID=host-a
bazel run //manman/host:host

# Terminal 2 - Host B
export SERVER_ID=host-b
bazel run //manman/host:host
```

Each host will:
- Register independently in the database
- Listen for commands on their own RabbitMQ routing key
- Manage separate sets of sessions
- Publish independent status updates

## Related Documentation

- [ManManV2 Architecture](../manman/manman-v2.md)
- [Event Processor](../manman/PHASE_6_COMPLETE.md)
- [Integration Tests](../manman/processor/integration_test.go)
