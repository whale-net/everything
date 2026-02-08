# Containerized Host Manager Deployment

This document describes how to deploy the ManManV2 host manager in a Docker container, which is the recommended approach for production deployments.

## Overview

The host manager can run in two modes:

1. **Bare Metal Mode** (development): Run directly on the host with `bazel run //manman/host:host`
2. **Containerized Mode** (production): Run in a Docker container with proper volume mounts

## Why Containerize?

Running the host manager in a container provides:
- Consistent runtime environment
- Easy deployment and updates
- Isolation from host system dependencies
- Better resource management

## Requirements

### Volume Mounts

The containerized host manager requires two critical volume mounts:

```yaml
volumes:
  - /var/run/docker.sock:/var/run/docker.sock  # Docker API access
  - /home/user/manmanv2/data:/data             # GSC data persistence

environment:
  - DATA_DIR=/data                             # Path inside this container
  - HOST_DATA_DIR=/home/user/manmanv2/data     # Path on the host machine
```

#### Docker Socket (`/var/run/docker.sock`)
Allows the host manager to control Docker on the host machine to create game server containers.

#### Data Directory (`./data:/data`)
**Critical for proper operation.** This mount allows the host manager to:
1. Create GSC directories (`/data/gsc-{env}-{sgc_id}`) that persist game server data
2. Bind mount these directories into game containers
3. Enable data persistence across container restarts AND sessions for the same GSC

**Without this mount**, session starts will fail with:
```
Error: invalid mount config for type "bind": bind source path does not exist: /data/gsc-prod-1
```

## Docker Compose Configuration

### Minimal Configuration

```yaml
services:
  host-manager:
    image: ghcr.io/whale-net/manmanv2-host-manager:latest
    container_name: manmanv2-host-manager
    restart: always
    network_mode: host
    volumes:
      - /var/run/docker.sock:/var/run/docker.sock
      - ./data:/data
    environment:
      SERVER_NAME: ${SERVER_NAME}
      RABBITMQ_URL: ${RABBITMQ_URL}
      API_ADDRESS: ${API_ADDRESS:-localhost:50051}
      ENVIRONMENT: ${ENVIRONMENT:-production}
```

### Environment Variables

| Variable | Required | Default | Description |
|----------|----------|---------|-------------|
| `SERVER_NAME` | Yes | - | Unique name for this host manager instance |
| `RABBITMQ_URL` | Yes | - | RabbitMQ connection URL |
| `API_ADDRESS` | No | `localhost:50051` | Control plane API address |
| `ENVIRONMENT` | No | `production` | Deployment environment (dev/staging/production) |
| `DOCKER_SOCKET` | No | `/var/run/docker.sock` | Docker socket path |

## GSC Data Directory Structure

When sessions are started, the host manager creates directories in the mounted `/data` volume:

```
./data/
├── gsc-prod-1/      # GSC 1 game data in production
├── gsc-dev-1/       # GSC 1 game data in development
└── gsc-prod-n/      # GSC N game data in production
```

Each GSC directory is bind-mounted into its game container at `/data/game`, allowing:
- Game servers to persist world data, configs, and save files across sessions
- Backups to be created from GSC data
- Restoration of game state for a specific GSC

## Testing Containerized Deployment

A comprehensive test script validates the containerized deployment:

```bash
cd manman-v2

# Build host manager image
bazel run //manman/host:host-manager_image_load

# Ensure control plane is running
tilt up

# Run containerized deployment test
./scripts/test-containerized-flow.sh
```

This test validates:
- Host manager starts successfully in a container
- Session data directories are created correctly
- Game containers start with proper bind mounts
- Full session lifecycle (start → running → stop)

## Troubleshooting

### Issue: "bind source path does not exist"

**Symptom:**
```
Error: failed to create game container: Error response from daemon:
invalid mount config for type "bind": bind source path does not exist: /data/gsc-1
```

**Solution:**
Ensure the data volume is mounted in your docker-compose.yml:
```yaml
volumes:
  - ./data:/data  # This line is required!
```

### Issue: Permission denied on /data

**Symptom:**
```
Error: failed to create GSC data directory: permission denied
```

**Solution:**
Ensure the data directory has appropriate permissions:
```bash
mkdir -p ./data
chmod 755 ./data
```

### Issue: Game container can't write to /data/game

**Symptom:**
Game server logs show permission errors when writing to `/data/game`

**Solution:**
The GSC directory may have restrictive permissions. The host manager creates directories with `0755` by default, which should work for most game servers.

If your game server runs as a specific UID, you may need to adjust permissions:
```bash
# Find the GSC directory
ls -la ./data/

# Adjust permissions for the specific GSC
chmod 777 ./data/gsc-{sgc_id}
```

## Deployment Checklist

Before deploying to production:

- [ ] Data directory is mounted (`./data:/data`)
- [ ] Docker socket is mounted (`/var/run/docker.sock:/var/run/docker.sock`)
- [ ] Network mode is set to `host`
- [ ] Environment variables are configured
- [ ] Server name is unique across all host managers
- [ ] RabbitMQ URL is correct and accessible
- [ ] Control plane API is reachable
- [ ] Host manager image is up to date
- [ ] Test script passes: `./scripts/test-containerized-flow.sh`

## Security Considerations

### Docker Socket Access

The host manager requires access to the Docker socket, which gives it full control over Docker on the host. This is a privileged operation. Ensure:

- The host manager container is from a trusted source
- The host system is properly secured
- Only authorized users can modify the host manager configuration

### Network Mode: Host

The host manager uses `network_mode: host` to:
- Access the host's Docker daemon
- Allow game containers to bind to host ports
- Communicate with RabbitMQ and the control plane

This means the host manager can access any network port on the host. Ensure proper firewall rules are in place.

## Migration from Bare Metal to Containerized

If you're currently running the host manager with `bazel run //manman/host:host`:

1. **Stop the bare metal host manager**
   ```bash
   # Press Ctrl+C to stop the running process
   ```

2. **Create data directory**
   ```bash
   mkdir -p ./data
   ```

3. **Configure docker-compose.yml** (as shown above)

4. **Start containerized host manager**
   ```bash
   docker-compose up -d host-manager
   ```

5. **Verify registration**
   ```bash
   docker-compose logs -f host-manager
   # Look for: "Successfully registered with control plane"
   ```

6. **Test with a session**
   ```bash
   # Use the test script or start a session via the API
   ./scripts/test-containerized-flow.sh
   ```

## Performance Considerations

The containerized host manager has minimal overhead compared to bare metal mode:

- **Docker socket communication**: Negligible latency
- **Volume mount overhead**: Minimal (native bind mounts)
- **Network performance**: No overhead with `network_mode: host`

For production workloads, containerized deployment is recommended as it provides better operational benefits with negligible performance impact.
