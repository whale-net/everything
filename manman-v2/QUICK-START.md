# ManManV2 Quick Start Guide

Get ManManV2 running in under 5 minutes.

## Prerequisites

- Docker Desktop with Kubernetes enabled
- Bazel installed
- `grpcurl` for testing (optional but recommended)

```bash
# macOS
brew install grpcurl

# Linux
go install github.com/fullstorydev/grpcurl/cmd/grpcurl@latest
```

## Step 1: Build Images (2 minutes)

```bash
cd manman-v2
./scripts/build-images.sh
```

This builds:
- `manmanv2-api` - Control plane API
- `manmanv2-processor` - Event processor
- `manmanv2-wrapper` - Wrapper sidecar
- `manmanv2-test-game-server` - Test game server

## Step 2: Start Control Plane (1 minute)

```bash
tilt up
```

Press `space` to open Tilt UI in your browser.

Wait for all services to be green:
- ✓ postgres-dev
- ✓ rabbitmq-dev
- ✓ otelcollector-dev
- ✓ manmanv2-api
- ✓ manmanv2-processor

## Step 3: Run Host Manager (30 seconds)

In a new terminal:

```bash
# From repo root
export SERVER_ID=host-local-dev-1
export RABBITMQ_URL=amqp://rabbit:password@localhost:5672/manmanv2-dev
bazel run //manman/host:host
```

You should see:
```
Host manager starting...
Connected to RabbitMQ
Published status.host.online
Listening for commands...
```

## Step 4: Test the System (1 minute)

Run the end-to-end test:

```bash
./scripts/test-flow.sh
```

This will:
1. Create a game configuration
2. Create a server game config
3. Start a session
4. Verify containers are running
5. Stop the session
6. Verify cleanup

If all tests pass, you'll see:
```
✓ All tests passed!
```

## Step 5: Explore

### View running containers

```bash
docker ps | grep manmanv2
```

### Check the database

```bash
psql postgresql://postgres:password@localhost:5432/manmanv2 -c "\dt"
```

### Browse RabbitMQ messages

Open http://localhost:15672 (login: `rabbit` / `password`)

Navigate to: Exchanges → manman → Bindings

### Query the API

```bash
# List all servers
grpcurl -plaintext localhost:50051 manman.ManManAPI/ListServers

# List all sessions
grpcurl -plaintext localhost:50051 manman.ManManAPI/ListSessions

# Get API methods
grpcurl -plaintext localhost:50051 list manman.ManManAPI
```

## Common Issues

### "No Kubernetes cluster selected"

Enable Kubernetes in Docker Desktop settings.

### "Cannot connect to API"

Wait for Tilt to show all services as green, then try again.

### "Wrapper image not found"

Run `./scripts/build-images.sh` again.

### "Host manager can't connect to RabbitMQ"

Check RabbitMQ is running:
```bash
kubectl get pods -n manmanv2-local-dev | grep rabbitmq
```

## Next Steps

- Read [README.md](./README.md) for detailed architecture
- Read [README-HOST.md](./README-HOST.md) for host manager details
- Check [../manman/manman-v2.md](../manman/manman-v2.md) for design docs

## Shutting Down

```bash
# Stop control plane
tilt down

# Stop host manager
# Press Ctrl+C in the terminal where it's running

# Clean up Docker containers (if needed)
docker stop $(docker ps -q --filter "label=manmanv2.managed=true")
docker rm $(docker ps -aq --filter "label=manmanv2.managed=true")
```
