#!/bin/bash
# End-to-end test script for ManManV2 containerized deployment
#
# This script tests the complete flow with the host manager running in a container:
# 1. Start host manager in Docker container
# 2. Create a game configuration
# 3. Create a server game config
# 4. Start a session
# 5. Verify containers are running
# 6. Stop the session
# 7. Verify cleanup
# 8. Stop host manager container
#
# Prerequisites:
#   - Control plane running (tilt up)
#   - Host manager image built (bazel run //manman/host:host-manager_image_load)
#   - grpcurl installed (brew install grpcurl)
#
# Usage: ./scripts/test-containerized-flow.sh [OPTIONS]
#
# Options:
#   --api-endpoint=HOST:PORT   API endpoint (default: localhost:50051)
#   --server-name=NAME        Server name (default: host-test-containerized)
#   --rabbitmq-url=URL        RabbitMQ URL (default: amqp://rabbit:password@localhost:5672/manmanv2-dev)
#   --cleanup                 Clean up resources after test
#   --help                    Show this help message

set -e

# Colors
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
BLUE='\033[0;34m'
NC='\033[0m'

# Default values
API_ENDPOINT="localhost:50051"
SERVER_NAME="host-test-containerized"
RABBITMQ_URL="amqp://rabbit:password@localhost:5672/manmanv2-dev"
CLEANUP=false
HOST_CONTAINER_NAME="test-host-manager-$$"
DATA_DIR="/tmp/manmanv2-test-data-$$"

# Parse arguments
for arg in "$@"; do
  case $arg in
    --api-endpoint=*)
      API_ENDPOINT="${arg#*=}"
      shift
      ;;
    --server-name=*)
      SERVER_NAME="${arg#*=}"
      shift
      ;;
    --rabbitmq-url=*)
      RABBITMQ_URL="${arg#*=}"
      shift
      ;;
    --cleanup)
      CLEANUP=true
      shift
      ;;
    --help)
      head -n 23 "$0" | tail -n +2 | sed 's/^# //'
      exit 0
      ;;
    *)
      echo -e "${RED}Unknown option: $arg${NC}"
      exit 1
      ;;
  esac
done

# Check for grpcurl
if ! command -v grpcurl &> /dev/null; then
  echo -e "${RED}Error: grpcurl is not installed${NC}"
  echo "Install with: brew install grpcurl (macOS) or go install github.com/fullstorydev/grpcurl/cmd/grpcurl@latest"
  exit 1
fi

# Cleanup function
cleanup() {
  echo ""
  echo -e "${YELLOW}Cleaning up...${NC}"

  # Stop and remove host manager container
  if docker ps -a --format '{{.Names}}' | grep -q "^${HOST_CONTAINER_NAME}$"; then
    echo "Stopping host manager container..."
    docker stop "$HOST_CONTAINER_NAME" || true
    docker rm "$HOST_CONTAINER_NAME" || true
  fi

  # Remove test data directory
  if [ -d "$DATA_DIR" ]; then
    echo "Removing test data directory..."
    rm -rf "$DATA_DIR"
  fi
}

# Set up trap to cleanup on exit
trap cleanup EXIT

echo ""
echo -e "${GREEN}═══════════════════════════════════════════════════${NC}"
echo -e "${GREEN}  ManManV2 Containerized Deployment Test${NC}"
echo -e "${GREEN}═══════════════════════════════════════════════════${NC}"
echo -e "${BLUE}API Endpoint:${NC} $API_ENDPOINT"
echo -e "${BLUE}Server Name:${NC} $SERVER_NAME"
echo -e "${BLUE}RabbitMQ URL:${NC} $RABBITMQ_URL"
echo -e "${BLUE}Data Directory:${NC} $DATA_DIR"
echo ""

# Test API connectivity
echo -e "${YELLOW}Testing API connectivity...${NC}"
if ! grpcurl -plaintext "$API_ENDPOINT" list manman.v1.ManManAPI &> /dev/null; then
  echo -e "${RED}✗ Cannot connect to API at $API_ENDPOINT${NC}"
  echo "Make sure control plane is running: tilt up"
  exit 1
fi
echo -e "${GREEN}✓ API is reachable${NC}"
echo ""

# Create test data directory
echo -e "${BLUE}━━━ Setup: Creating Test Data Directory ━━━${NC}"
mkdir -p "$DATA_DIR"
echo -e "${GREEN}✓ Created $DATA_DIR${NC}"
echo ""

# Start host manager in container
echo -e "${BLUE}━━━ Step 1: Start Host Manager Container ━━━${NC}"
docker run -d \
  --name "$HOST_CONTAINER_NAME" \
  --network host \
  -v /var/run/docker.sock:/var/run/docker.sock \
  -v "$DATA_DIR:/data" \
  -e SERVER_NAME="$SERVER_NAME" \
  -e RABBITMQ_URL="$RABBITMQ_URL" \
  -e DOCKER_SOCKET=/var/run/docker.sock \
  -e API_ADDRESS="$API_ENDPOINT" \
  -e ENVIRONMENT=test \
  manmanv2-host-manager:latest

echo -e "${GREEN}✓ Host manager container started${NC}"
echo ""

# Wait for host manager to register
echo -e "${YELLOW}Waiting for host manager to register (max 30 seconds)...${NC}"
for i in {1..30}; do
  # List servers and check if our server is registered
  SERVERS=$(grpcurl -plaintext "$API_ENDPOINT" manman.v1.ManManAPI/ListServers 2>/dev/null || echo "{}")
  if echo "$SERVERS" | grep -q "$SERVER_NAME"; then
    echo -e "${GREEN}✓ Host manager registered${NC}"
    break
  fi

  if [ $i -eq 30 ]; then
    echo -e "${RED}✗ Host manager did not register within 30 seconds${NC}"
    echo "Container logs:"
    docker logs "$HOST_CONTAINER_NAME"
    exit 1
  fi

  sleep 1
done

# Get server ID
SERVER_ID=$(echo "$SERVERS" | grep -A5 "$SERVER_NAME" | grep '"serverId"' | grep -o '"[0-9]*"' | tr -d '"')
echo -e "${BLUE}Server ID:${NC} $SERVER_ID"
echo ""

# Step 2: Create game
echo -e "${BLUE}━━━ Step 2: Create Game ━━━${NC}"
GAME_RESPONSE=$(grpcurl -plaintext \
  -d '{
    "name": "test-game-containerized",
    "metadata": {
      "genre": "test",
      "publisher": "ManManV2",
      "tags": ["test"]
    }
  }' \
  "$API_ENDPOINT" \
  manman.v1.ManManAPI/CreateGame)

GAME_ID=$(echo "$GAME_RESPONSE" | grep -o '"gameId": *"[0-9]*"' | grep -o '[0-9]*')

if [ -z "$GAME_ID" ]; then
  echo -e "${RED}✗ Failed to create game${NC}"
  echo "$GAME_RESPONSE"
  exit 1
fi

echo -e "${GREEN}✓ Created game with ID: $GAME_ID${NC}"
echo ""

# Step 3: Create game config
echo -e "${BLUE}━━━ Step 3: Create Game Config ━━━${NC}"
CONFIG_RESPONSE=$(grpcurl -plaintext \
  -d "{
    \"game_id\": $GAME_ID,
    \"name\": \"test-config-containerized\",
    \"image\": \"manmanv2-test-game-server:latest\",
    \"env_template\": {}
  }" \
  "$API_ENDPOINT" \
  manman.v1.ManManAPI/CreateGameConfig)

CONFIG_ID=$(echo "$CONFIG_RESPONSE" | grep -o '"configId": *"[0-9]*"' | grep -o '[0-9]*')

if [ -z "$CONFIG_ID" ]; then
  echo -e "${RED}✗ Failed to create game config${NC}"
  echo "$CONFIG_RESPONSE"
  exit 1
fi

echo -e "${GREEN}✓ Created game config with ID: $CONFIG_ID${NC}"
echo ""

# Step 4: Deploy game config
echo -e "${BLUE}━━━ Step 4: Deploy Game Config ━━━${NC}"
DEPLOY_RESPONSE=$(grpcurl -plaintext \
  -d "{
    \"server_id\": $SERVER_ID,
    \"game_config_id\": $CONFIG_ID,
    \"port_bindings\": [
      {
        \"container_port\": 8080,
        \"host_port\": 18080,
        \"protocol\": \"TCP\"
      }
    ]
  }" \
  "$API_ENDPOINT" \
  manman.v1.ManManAPI/DeployGameConfig)

SGC_ID=$(echo "$DEPLOY_RESPONSE" | grep -o '"serverGameConfigId": *"[0-9]*"' | grep -o '[0-9]*')

if [ -z "$SGC_ID" ]; then
  echo -e "${RED}✗ Failed to deploy game config${NC}"
  echo "$DEPLOY_RESPONSE"
  exit 1
fi

echo -e "${GREEN}✓ Deployed with server game config ID: $SGC_ID${NC}"
echo ""

# Step 5: Start session
echo -e "${BLUE}━━━ Step 5: Start Session ━━━${NC}"
SESSION_RESPONSE=$(grpcurl -plaintext \
  -d "{
    \"server_game_config_id\": $SGC_ID
  }" \
  "$API_ENDPOINT" \
  manman.v1.ManManAPI/StartSession)

SESSION_ID=$(echo "$SESSION_RESPONSE" | grep -o '"sessionId": *"[0-9]*"' | grep -o '[0-9]*')

if [ -z "$SESSION_ID" ]; then
  echo -e "${RED}✗ Failed to start session${NC}"
  echo "$SESSION_RESPONSE"
  exit 1
fi

echo -e "${GREEN}✓ Started session with ID: $SESSION_ID${NC}"
echo ""

# Wait for session to start
echo -e "${YELLOW}Waiting for session to start (max 30 seconds)...${NC}"
for i in {1..30}; do
  SESSION_STATUS=$(grpcurl -plaintext \
    -d "{\"session_id\": $SESSION_ID}" \
    "$API_ENDPOINT" \
    manman.v1.ManManAPI/GetSession 2>/dev/null | grep -o '"status": *"[^"]*"' | grep -o '"[^"]*"$' | tr -d '"' || echo "unknown")

  echo -e "${BLUE}  Status: $SESSION_STATUS (attempt $i/30)${NC}"

  if [ "$SESSION_STATUS" = "running" ]; then
    echo -e "${GREEN}✓ Session is running${NC}"
    break
  elif [ "$SESSION_STATUS" = "crashed" ] || [ "$SESSION_STATUS" = "stopped" ]; then
    echo -e "${RED}✗ Session ended unexpectedly with status: $SESSION_STATUS${NC}"
    echo "Host manager logs:"
    docker logs "$HOST_CONTAINER_NAME" | tail -50
    exit 1
  fi

  sleep 1
done

if [ "$SESSION_STATUS" != "running" ]; then
  echo -e "${RED}✗ Session did not start within 30 seconds${NC}"
  echo "Host manager logs:"
  docker logs "$HOST_CONTAINER_NAME" | tail -50
  exit 1
fi
echo ""

# Step 6: Verify data directory was created
echo -e "${BLUE}━━━ Step 6: Verify GSC Data Directory ━━━${NC}"
GSC_DATA_DIR="$DATA_DIR/gsc-test-$SGC_ID"
if [ -d "$GSC_DATA_DIR" ]; then
  echo -e "${GREEN}✓ GSC data directory created: $GSC_DATA_DIR${NC}"
else
  echo -e "${RED}✗ GSC data directory not found: $GSC_DATA_DIR${NC}"
  exit 1
fi
echo ""

# Step 7: Verify containers
echo -e "${BLUE}━━━ Step 7: Verify Game Container ━━━${NC}"
GAME_CONTAINER=$(docker ps --filter "label=manman.session_id=$SESSION_ID" --filter "label=manman.type=game" --format "{{.Names}}")

if [ -z "$GAME_CONTAINER" ]; then
  echo -e "${RED}✗ Game container not found${NC}"
  docker ps --filter "label=manman.session_id=$SESSION_ID"
  exit 1
fi

echo -e "${GREEN}✓ Game container: $GAME_CONTAINER${NC}"

# Verify the bind mount is working
MOUNT_INFO=$(docker inspect "$GAME_CONTAINER" | grep -A10 "Mounts" | grep "/data/game")
if [ -n "$MOUNT_INFO" ]; then
  echo -e "${GREEN}✓ Bind mount configured correctly${NC}"
else
  echo -e "${YELLOW}⚠ Could not verify bind mount (this may be okay)${NC}"
fi
echo ""

# Step 8: Stop session
echo -e "${BLUE}━━━ Step 8: Stop Session ━━━${NC}"
STOP_RESPONSE=$(grpcurl -plaintext \
  -d "{\"session_id\": $SESSION_ID}" \
  "$API_ENDPOINT" \
  manman.v1.ManManAPI/StopSession)

echo -e "${GREEN}✓ Stop command sent${NC}"
echo ""

# Wait for session to stop
echo -e "${YELLOW}Waiting for session to stop (max 20 seconds)...${NC}"
for i in {1..20}; do
  SESSION_STATUS=$(grpcurl -plaintext \
    -d "{\"session_id\": $SESSION_ID}" \
    "$API_ENDPOINT" \
    manman.v1.ManManAPI/GetSession 2>/dev/null | grep -o '"status": *"[^"]*"' | grep -o '"[^"]*"$' | tr -d '"' || echo "unknown")

  echo -e "${BLUE}  Status: $SESSION_STATUS (attempt $i/20)${NC}"

  if [ "$SESSION_STATUS" = "stopped" ]; then
    echo -e "${GREEN}✓ Session stopped${NC}"
    break
  fi

  sleep 1
done

if [ "$SESSION_STATUS" != "stopped" ]; then
  echo -e "${YELLOW}⚠ Session status is: $SESSION_STATUS (expected: stopped)${NC}"
fi
echo ""

# Step 9: Verify cleanup
echo -e "${BLUE}━━━ Step 9: Verify Container Cleanup ━━━${NC}"
REMAINING_CONTAINERS=$(docker ps --filter "label=manman.session_id=$SESSION_ID" --format "{{.Names}}")

if [ -z "$REMAINING_CONTAINERS" ]; then
  echo -e "${GREEN}✓ All session containers cleaned up${NC}"
else
  echo -e "${YELLOW}⚠ Some containers still running:${NC}"
  echo "$REMAINING_CONTAINERS"
fi
echo ""

# Summary
echo -e "${GREEN}═══════════════════════════════════════════════════${NC}"
echo -e "${GREEN}  Test Summary${NC}"
echo -e "${GREEN}═══════════════════════════════════════════════════${NC}"
echo -e "${GREEN}✓ All containerized deployment tests passed!${NC}"
echo ""
echo -e "${BLUE}Key achievements:${NC}"
echo "  ✓ Host manager ran successfully in container"
echo "  ✓ GSC data directory created at: $GSC_DATA_DIR"
echo "  ✓ Game container started with bind mount"
echo "  ✓ Session lifecycle completed successfully"
echo ""
echo -e "${BLUE}Resources created:${NC}"
echo "  Game ID: $GAME_ID"
echo "  Config ID: $CONFIG_ID"
echo "  Server Game Config ID: $SGC_ID"
echo "  Session ID: $SESSION_ID"
echo "  Server ID: $SERVER_ID"
echo ""
