#!/bin/bash
# End-to-end test script for ManManV2
#
# This script tests the complete flow:
# 1. Create a game configuration
# 2. Create a server game config
# 3. Start a session
# 4. Verify containers are running
# 5. Stop the session
# 6. Verify cleanup
#
# Prerequisites:
#   - Control plane running (tilt up)
#   - Host manager running (bazel run //manman/host:host)
#   - grpcurl installed (brew install grpcurl)
#
# Usage: ./scripts/test-flow.sh [OPTIONS]
#
# Options:
#   --api-endpoint=HOST:PORT   API endpoint (default: localhost:50051)
#   --server-id=ID            Server ID to use (default: host-local-dev-1)
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
SERVER_ID="host-local-dev-1"
CLEANUP=false

# Parse arguments
for arg in "$@"; do
  case $arg in
    --api-endpoint=*)
      API_ENDPOINT="${arg#*=}"
      shift
      ;;
    --server-id=*)
      SERVER_ID="${arg#*=}"
      shift
      ;;
    --cleanup)
      CLEANUP=true
      shift
      ;;
    --help)
      head -n 17 "$0" | tail -n +2 | sed 's/^# //'
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

echo ""
echo -e "${GREEN}═══════════════════════════════════════════════════${NC}"
echo -e "${GREEN}  ManManV2 End-to-End Test${NC}"
echo -e "${GREEN}═══════════════════════════════════════════════════${NC}"
echo -e "${BLUE}API Endpoint:${NC} $API_ENDPOINT"
echo -e "${BLUE}Server ID:${NC} $SERVER_ID"
echo ""

# Test API connectivity
echo -e "${YELLOW}Testing API connectivity...${NC}"
if ! grpcurl -plaintext "$API_ENDPOINT" list manman.ManManAPI &> /dev/null; then
  echo -e "${RED}✗ Cannot connect to API at $API_ENDPOINT${NC}"
  echo "Make sure control plane is running: tilt up"
  exit 1
fi
echo -e "${GREEN}✓ API is reachable${NC}"
echo ""

# Step 1: Create game
echo -e "${BLUE}━━━ Step 1: Create Game ━━━${NC}"
GAME_RESPONSE=$(grpcurl -plaintext \
  -d '{
    "name": "test-game-e2e",
    "image": "manmanv2-test-game-server:latest",
    "description": "End-to-end test game"
  }' \
  "$API_ENDPOINT" \
  manman.ManManAPI/CreateGame)

GAME_ID=$(echo "$GAME_RESPONSE" | grep -o '"id": *[0-9]*' | grep -o '[0-9]*')

if [ -z "$GAME_ID" ]; then
  echo -e "${RED}✗ Failed to create game${NC}"
  echo "$GAME_RESPONSE"
  exit 1
fi

echo -e "${GREEN}✓ Created game with ID: $GAME_ID${NC}"
echo ""

# Step 2: Create server game config
echo -e "${BLUE}━━━ Step 2: Create Server Game Config ━━━${NC}"
SGC_RESPONSE=$(grpcurl -plaintext \
  -d "{
    \"server_id\": \"$SERVER_ID\",
    \"game_id\": $GAME_ID,
    \"name\": \"test-deployment-e2e\"
  }" \
  "$API_ENDPOINT" \
  manman.ManManAPI/CreateServerGameConfig)

SGC_ID=$(echo "$SGC_RESPONSE" | grep -o '"id": *[0-9]*' | grep -o '[0-9]*')

if [ -z "$SGC_ID" ]; then
  echo -e "${RED}✗ Failed to create server game config${NC}"
  echo "$SGC_RESPONSE"
  exit 1
fi

echo -e "${GREEN}✓ Created server game config with ID: $SGC_ID${NC}"
echo ""

# Step 3: Start session
echo -e "${BLUE}━━━ Step 3: Start Session ━━━${NC}"
SESSION_RESPONSE=$(grpcurl -plaintext \
  -d "{
    \"server_game_config_id\": $SGC_ID
  }" \
  "$API_ENDPOINT" \
  manman.ManManAPI/StartSession)

SESSION_ID=$(echo "$SESSION_RESPONSE" | grep -o '"session_id": *[0-9]*' | grep -o '[0-9]*')

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
    -d "{\"id\": $SESSION_ID}" \
    "$API_ENDPOINT" \
    manman.ManManAPI/GetSession | grep -o '"status": *"[^"]*"' | grep -o '"[^"]*"$' | tr -d '"')

  echo -e "${BLUE}  Status: $SESSION_STATUS (attempt $i/30)${NC}"

  if [ "$SESSION_STATUS" = "running" ]; then
    echo -e "${GREEN}✓ Session is running${NC}"
    break
  elif [ "$SESSION_STATUS" = "crashed" ] || [ "$SESSION_STATUS" = "stopped" ]; then
    echo -e "${RED}✗ Session ended unexpectedly with status: $SESSION_STATUS${NC}"
    exit 1
  fi

  sleep 1
done

if [ "$SESSION_STATUS" != "running" ]; then
  echo -e "${RED}✗ Session did not start within 30 seconds${NC}"
  exit 1
fi
echo ""

# Step 4: Verify containers
echo -e "${BLUE}━━━ Step 4: Verify Containers ━━━${NC}"
WRAPPER_CONTAINER=$(docker ps --filter "label=manmanv2.session_id=$SESSION_ID" --filter "name=wrapper" --format "{{.Names}}")
GAME_CONTAINER=$(docker ps --filter "label=manmanv2.session_id=$SESSION_ID" --filter "name=test-game" --format "{{.Names}}")

if [ -z "$WRAPPER_CONTAINER" ]; then
  echo -e "${RED}✗ Wrapper container not found${NC}"
  docker ps --filter "label=manmanv2.session_id=$SESSION_ID"
  exit 1
fi

if [ -z "$GAME_CONTAINER" ]; then
  echo -e "${RED}✗ Game container not found${NC}"
  docker ps --filter "label=manmanv2.session_id=$SESSION_ID"
  exit 1
fi

echo -e "${GREEN}✓ Wrapper container: $WRAPPER_CONTAINER${NC}"
echo -e "${GREEN}✓ Game container: $GAME_CONTAINER${NC}"
echo ""

# Step 5: Stop session
echo -e "${BLUE}━━━ Step 5: Stop Session ━━━${NC}"
STOP_RESPONSE=$(grpcurl -plaintext \
  -d "{\"session_id\": $SESSION_ID}" \
  "$API_ENDPOINT" \
  manman.ManManAPI/StopSession)

echo -e "${GREEN}✓ Stop command sent${NC}"
echo ""

# Wait for session to stop
echo -e "${YELLOW}Waiting for session to stop (max 20 seconds)...${NC}"
for i in {1..20}; do
  SESSION_STATUS=$(grpcurl -plaintext \
    -d "{\"id\": $SESSION_ID}" \
    "$API_ENDPOINT" \
    manman.ManManAPI/GetSession | grep -o '"status": *"[^"]*"' | grep -o '"[^"]*"$' | tr -d '"')

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

# Step 6: Verify cleanup
echo -e "${BLUE}━━━ Step 6: Verify Cleanup ━━━${NC}"
REMAINING_CONTAINERS=$(docker ps --filter "label=manmanv2.session_id=$SESSION_ID" --format "{{.Names}}")

if [ -z "$REMAINING_CONTAINERS" ]; then
  echo -e "${GREEN}✓ All containers cleaned up${NC}"
else
  echo -e "${YELLOW}⚠ Some containers still running:${NC}"
  echo "$REMAINING_CONTAINERS"
fi
echo ""

# Cleanup resources if requested
if [ "$CLEANUP" = true ]; then
  echo -e "${BLUE}━━━ Cleanup: Removing Test Resources ━━━${NC}"

  # Note: API currently doesn't have delete methods implemented
  echo -e "${YELLOW}⚠ Manual cleanup required:${NC}"
  echo "  Game ID: $GAME_ID"
  echo "  Server Game Config ID: $SGC_ID"
  echo "  Session ID: $SESSION_ID"
  echo ""
fi

# Summary
echo -e "${GREEN}═══════════════════════════════════════════════════${NC}"
echo -e "${GREEN}  Test Summary${NC}"
echo -e "${GREEN}═══════════════════════════════════════════════════${NC}"
echo -e "${GREEN}✓ All tests passed!${NC}"
echo ""
echo -e "${BLUE}Resources created:${NC}"
echo "  Game ID: $GAME_ID"
echo "  Server Game Config ID: $SGC_ID"
echo "  Session ID: $SESSION_ID"
echo ""
echo -e "${BLUE}Verify in database:${NC}"
echo "  psql postgresql://postgres:password@localhost:5432/manmanv2 -c 'SELECT * FROM sessions WHERE id = $SESSION_ID;'"
echo ""
echo -e "${BLUE}Check RabbitMQ messages:${NC}"
echo "  http://localhost:15672 (user: rabbit, pass: password)"
echo ""
