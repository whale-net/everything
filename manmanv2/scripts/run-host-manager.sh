#!/bin/bash
set -e

# Configuration
IMAGE_NAME="manmanv2-host-manager:latest"
CONTAINER_NAME="local-host-manager"
SGC_HOST_DATA_PATH="$(pwd)/tmp/manman-data"

# Parse arguments
DETACH_MODE=""
if [[ "$1" == "-d" ]] || [[ "$1" == "--detach" ]]; then
    DETACH_MODE="-d"
else
    # Interactive mode by default
    DETACH_MODE="-it"
fi

# Ensure host data directory exists
mkdir -p "$SGC_HOST_DATA_PATH"
chmod 777 "$SGC_HOST_DATA_PATH"

# Cleanup previous container if it exists
if docker ps -a --format '{{.Names}}' | grep -q "^${CONTAINER_NAME}$"; then
    echo "Stopping existing container..."
    docker stop "$CONTAINER_NAME" >/dev/null 2>&1 || true
    docker rm "$CONTAINER_NAME" >/dev/null 2>&1 || true
fi

# Build image
echo "Building host manager image..."
bazel run //manmanv2/host:host-manager_image_load

# Run container
# We use host.docker.internal to access services running on the host (Tilt/K8s port-forwards)
# This works reliably across Docker Desktop and Linux/WSL2 with --add-host
echo "Starting host manager..."
if [[ "$DETACH_MODE" == "-d" ]]; then
    docker run -d \
      --add-host=host.docker.internal:host-gateway \
      --name "$CONTAINER_NAME" \
      -e SERVER_NAME="local-test-host" \
      -e ENVIRONMENT="dev" \
      -e RABBITMQ_URL="amqp://rabbit:password@host.docker.internal:5672/manmanv2-dev" \
      -e API_ADDRESS="host.docker.internal:50052" \
      -e API_USE_TLS="false" \
      -e DOCKER_SOCKET="/var/run/docker.sock" \
      -e HOST_DATA_DIR="$SGC_HOST_DATA_PATH" \
      -v /var/run/docker.sock:/var/run/docker.sock \
      -v "$SGC_HOST_DATA_PATH:/var/lib/manman/sessions" \
      "$IMAGE_NAME"

    echo "Host manager started in background!"
    echo "To follow logs: docker logs -f $CONTAINER_NAME"
    echo "To stop: docker stop $CONTAINER_NAME"
else
    # Foreground mode
    docker run -it \
      --add-host=host.docker.internal:host-gateway \
      --name "$CONTAINER_NAME" \
      -e SERVER_NAME="local-test-host" \
      -e ENVIRONMENT="dev" \
      -e RABBITMQ_URL="amqp://rabbit:password@host.docker.internal:5672/manmanv2-dev" \
      -e API_ADDRESS="host.docker.internal:50052" \
      -e API_USE_TLS="false" \
      -e DOCKER_SOCKET="/var/run/docker.sock" \
      -e HOST_DATA_DIR="$SGC_HOST_DATA_PATH" \
      -v /var/run/docker.sock:/var/run/docker.sock \
      -v "$SGC_HOST_DATA_PATH:/var/lib/manman/sessions" \
      "$IMAGE_NAME"
fi
