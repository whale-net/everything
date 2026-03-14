#!/usr/bin/env bash
set -euo pipefail

# Seed Minecraft action definitions using the API
# Requires: grpcurl, python3

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "${SCRIPT_DIR}/../.." && pwd)"

# Default values
CONTROL_API_ADDR="${CONTROL_API_ADDR:-localhost:50052}"
USE_TLS="${USE_TLS:-auto}"
INSECURE_TLS="${INSECURE_TLS:-false}"

# Auto-detect TLS based on port (443 = TLS)
if [[ "${USE_TLS}" == "auto" ]]; then
  if [[ "${CONTROL_API_ADDR}" =~ :443$ ]]; then
    USE_TLS="true"
  else
    USE_TLS="false"
  fi
fi

grpc_call() {
  local addr="$1"
  local method="$2"
  local data="$3"

  local tls_flags=""
  if [[ "${USE_TLS}" == "true" ]]; then
    if [[ "${INSECURE_TLS}" == "true" ]]; then
      tls_flags="-insecure"
    fi
  else
    tls_flags="-plaintext"
  fi

  local CMD=(grpcurl ${tls_flags})
  if [[ -n "${ACCESS_TOKEN:-}" ]]; then
    CMD+=("-H" "Authorization: Bearer ${ACCESS_TOKEN}")
  fi
  CMD+=(
    -import-path "${REPO_ROOT}"
    -proto "${REPO_ROOT}/manmanv2/protos/api.proto"
    -proto "${REPO_ROOT}/manmanv2/protos/messages.proto"
    -d "${data}"
    "${addr}" "${method}"
  )
  "${CMD[@]}"
}


create_action() {
  local data="$1"
  local output
  output="$(grpc_call "${CONTROL_API_ADDR}" "manman.v1.ManManAPI/CreateActionDefinition" "${data}" 2>&1)"
  local ec=$?
  if [[ $ec -eq 0 ]] && ! echo "$output" | grep -qi "error"; then
    echo "    ✓ Created"
  else
    echo "    ⚠ Failed/Already exists:"
    echo "$output" | sed 's/^/      /'
  fi
}

echo "════════════════════════════════════════════════════"
echo "  Seeding Minecraft Game Actions"

create_action() {
  local data="$1"
  local output
  output="$(grpc_call "${CONTROL_API_ADDR}" "manman.v1.ManManAPI/CreateActionDefinition" "${data}" 2>&1)"
  local ec=$?
  if [[ $ec -eq 0 ]] && ! echo "$output" | grep -qi "error"; then
    echo "    ✓ Created"
  else
    echo "    ⚠ Failed/Already exists:"
    echo "$output" | sed 's/^/      /'
  fi
}

echo "════════════════════════════════════════════════════"
echo "GRPC API:  ${CONTROL_API_ADDR}"
echo ""

# Check for grpcurl
if ! command -v grpcurl &> /dev/null; then
  echo "Error: grpcurl is not installed"
  echo "Install with: brew install grpcurl (macOS) or go install github.com/fullstorydev/grpcurl/cmd/grpcurl@latest"
  exit 1
fi

# Authentication setup
ACCESS_TOKEN=""
if [[ "${GRPC_AUTH_MODE:-none}" == "oidc" ]]; then
  echo "Getting OIDC token from Keycloak..."
  if [[ -z "${GRPC_AUTH_TOKEN_URL:-}" || -z "${GRPC_AUTH_CLIENT_ID:-}" || -z "${GRPC_AUTH_CLIENT_SECRET:-}" ]]; then
    echo "Error: GRPC_AUTH_MODE=oidc requires GRPC_AUTH_TOKEN_URL, GRPC_AUTH_CLIENT_ID, and GRPC_AUTH_CLIENT_SECRET"
    exit 1
  fi
  
  token_resp=$(curl -s -X POST "${GRPC_AUTH_TOKEN_URL}" \
    -H "Content-Type: application/x-www-form-urlencoded" \
    -d "grant_type=client_credentials" \
    -d "client_id=${GRPC_AUTH_CLIENT_ID}" \
    -d "client_secret=${GRPC_AUTH_CLIENT_SECRET}")
  
  ACCESS_TOKEN=$(python3 -c 'import json,sys; print(json.loads(sys.stdin.read() or "{}").get("access_token", ""))' <<< "${token_resp}")
  if [[ -z "${ACCESS_TOKEN}" ]]; then
    echo "Error: Failed to obtain access token."
    exit 1
  fi
  echo "✓ Token obtained successfully"
  echo ""
fi

# Test API connectivity
echo "Testing API connectivity..."
TLS_FLAGS=""
if [[ "${USE_TLS}" == "true" ]]; then
  if [[ "${INSECURE_TLS}" == "true" ]]; then
    TLS_FLAGS="-insecure"
  fi
else
  TLS_FLAGS="-plaintext"
fi

TEST_CMD=(grpcurl ${TLS_FLAGS})
if [[ -n "${ACCESS_TOKEN:-}" ]]; then
  TEST_CMD+=("-H" "Authorization: Bearer ${ACCESS_TOKEN}")
fi
TEST_CMD+=("${CONTROL_API_ADDR}" list manman.v1.ManManAPI)

if ! "${TEST_CMD[@]}" &> /dev/null; then
  echo "✗ Cannot connect to API at ${CONTROL_API_ADDR}"
  echo "Make sure the control plane is running and accessible"
  if [[ "${USE_TLS}" == "false" ]]; then
    echo "Hint: If the endpoint uses TLS, set USE_TLS=true"
  fi
  exit 1
fi
echo "✓ API is reachable"
echo ""

# Find Minecraft game ID
echo "Finding Minecraft game..."
games_resp="$(grpc_call "${CONTROL_API_ADDR}" "manman.v1.ManManAPI/ListGames" '{"page_size":100}')"
game_id="$(python3 -c 'import json,sys; data=json.loads(sys.stdin.read() or "{}"); games=data.get("games", []);
found="";
for g in games:
    name=(g.get("name") or "").strip()
    if name=="Minecraft":
        found=(g.get("gameId") or g.get("game_id") or "")
        break
print(found)' <<< "${games_resp}")"

if [[ -z "${game_id}" ]]; then
  echo "✗ Minecraft game not found"
  echo "Please create the Minecraft game first"
  exit 1
fi

echo "✓ Found Minecraft game (ID: ${game_id})"
echo ""

# Create actions
echo "Creating actions..."

# 1. Save World
echo "  Creating 'Save World' action..."
create_action "$(cat <<EOF
{
  "action": {
    "definition_level": "game",
    "entity_id": ${game_id},
    "name": "save_all",
    "label": "Save World",
    "description": "Save all chunks to disk",
    "command_template": "save-all",
    "display_order": 0,
    "group_name": "World Management",
    "button_style": "success",
    "icon": "fa-save",
    "enabled": true
  }
}
EOF
)"

# 2. Stop Server
echo "  Creating 'Stop Server' action..."
create_action "$(cat <<EOF
{
  "action": {
    "definition_level": "game",
    "entity_id": ${game_id},
    "name": "stop_server",
    "label": "Stop Server",
    "description": "Gracefully stop the Minecraft server",
    "command_template": "stop",
    "display_order": 1,
    "group_name": "Server Control",
    "button_style": "danger",
    "requires_confirmation": true,
    "confirmation_message": "This will stop the server and disconnect all players. Continue?",
    "enabled": true
  }
}
EOF
)"

# 3. Broadcast Preset Message (with select field and options)
echo "  Creating 'Broadcast Message' action..."
create_action "$(cat <<EOF
{
  "action": {
    "definition_level": "game",
    "entity_id": ${game_id},
    "name": "say_preset",
    "label": "Broadcast Message",
    "description": "Send a preset message to all players",
    "command_template": "say {{.message}}",
    "display_order": 2,
    "group_name": "Communication",
    "button_style": "info",
    "enabled": true
  },
  "input_fields": [
    {
      "name": "message",
      "label": "Select Message",
      "field_type": "select",
      "required": true,
      "display_order": 0,
      "help_text": "Choose a message to broadcast"
    }
  ],
  "input_options": [
    {
      "value": "Server will restart in 5 minutes!",
      "label": "Restart Warning (5 min)",
      "display_order": 0,
      "is_default": true
    },
    {
      "value": "Server will restart in 1 minute. Please find a safe place!",
      "label": "Restart Warning (1 min)",
      "display_order": 1
    },
    {
      "value": "Server restart complete. Welcome back!",
      "label": "Restart Complete",
      "display_order": 2
    },
    {
      "value": "Backup in progress. Minor lag expected.",
      "label": "Backup Notice",
      "display_order": 3
    },
    {
      "value": "Event starting at spawn in 10 minutes!",
      "label": "Event Announcement",
      "display_order": 4
    },
    {
      "value": "Please report any bugs or issues to the admin.",
      "label": "Bug Report Reminder",
      "display_order": 5
    }
  ]
}
EOF
)"

# 4. Custom Message (with text input)
echo "  Creating 'Custom Message' action..."
create_action "$(cat <<EOF
{
  "action": {
    "definition_level": "game",
    "entity_id": ${game_id},
    "name": "say_custom",
    "label": "Custom Message",
    "description": "Send a custom message to all players",
    "command_template": "say {{.custom_message}}",
    "display_order": 3,
    "group_name": "Communication",
    "button_style": "primary",
    "enabled": true
  },
  "input_fields": [
    {
      "name": "custom_message",
      "label": "Your Message",
      "field_type": "text",
      "required": true,
      "placeholder": "e.g., Welcome to the server!",
      "display_order": 0,
      "help_text": "Enter a message to broadcast to all players",
      "min_length": 1,
      "max_length": 256
    }
  ]
}
EOF
)"

echo ""
echo "✔ Minecraft actions seeded successfully!"
echo ""
echo "Summary:"
echo "  - Save World (simple button)"
echo "  - Stop Server (with confirmation)"
echo "  - Broadcast Message (select dropdown with 6 options)"
echo "  - Custom Message (text input)"
