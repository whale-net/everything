#!/usr/bin/env bash
set -euo pipefail

# Load a joedwards32/cs2 GameConfig with defaults for local testing.
# Requires: grpcurl, python3
#
# Usage: ./scripts/load-cs2-config.sh [OPTIONS]
#
# Options:
#   --grpc-url=HOST:PORT      GRPC API endpoint (default: localhost:50052)
#   --api-endpoint=HOST:PORT  Alias for --grpc-url
#   --game-name=NAME          Game name (default: Counter-Strike 2)
#   --config-name=NAME        Config name (default: Competitive)
#   --image=IMAGE             Docker image (default: joedwards32/cs2:latest)
#   --tls                     Use TLS for GRPC connection (auto-detected for port 443)
#   --insecure                Use insecure TLS (skip certificate verification)
#   --help                    Show this help message

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "${SCRIPT_DIR}/../.." && pwd)"

# Default values (can be overridden by env vars or CLI args)
CONTROL_API_ADDR="${CONTROL_API_ADDR:-localhost:50052}"
GAME_NAME="${GAME_NAME:-Counter-Strike 2}"
GAME_CONFIG_NAME="${GAME_CONFIG_NAME:-Competitive}"
IMAGE="${IMAGE:-joedwards32/cs2:latest}"
USE_TLS="${USE_TLS:-auto}"
INSECURE_TLS="${INSECURE_TLS:-false}"

# Parse arguments
for arg in "$@"; do
  case $arg in
    --grpc-url=*|--api-endpoint=*)
      CONTROL_API_ADDR="${arg#*=}"
      shift
      ;;
    --game-name=*)
      GAME_NAME="${arg#*=}"
      shift
      ;;
    --config-name=*)
      GAME_CONFIG_NAME="${arg#*=}"
      shift
      ;;
    --image=*)
      IMAGE="${arg#*=}"
      shift
      ;;
    --tls)
      USE_TLS="true"
      shift
      ;;
    --insecure)
      INSECURE_TLS="true"
      shift
      ;;
    --help)
      head -n 17 "$0" | tail -n +4 | sed 's/^# //' | sed 's/^#$//'
      exit 0
      ;;
    *)
      echo "Unknown option: $arg"
      echo "Run with --help for usage information"
      exit 1
      ;;
  esac
done

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

  grpcurl ${tls_flags} \
    -import-path "${REPO_ROOT}" \
    -proto "${REPO_ROOT}/manman/protos/api.proto" \
    -proto "${REPO_ROOT}/manman/protos/messages.proto" \
    -d "${data}" \
    "${addr}" "${method}"
}

echo ""
echo "════════════════════════════════════════════════════"
echo "  Loading Counter-Strike 2 Configuration"
echo "════════════════════════════════════════════════════"
echo "GRPC API:  ${CONTROL_API_ADDR}"
echo "TLS:       ${USE_TLS}"
echo "Game:      ${GAME_NAME}"
echo "Config:    ${GAME_CONFIG_NAME}"
echo "Image:     ${IMAGE}"
echo ""

# Check for grpcurl
if ! command -v grpcurl &> /dev/null; then
  echo "Error: grpcurl is not installed"
  echo "Install with: brew install grpcurl (macOS) or go install github.com/fullstorydev/grpcurl/cmd/grpcurl@latest"
  exit 1
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

if ! grpcurl ${TLS_FLAGS} "${CONTROL_API_ADDR}" list manman.v1.ManManAPI &> /dev/null; then
  echo "✗ Cannot connect to API at ${CONTROL_API_ADDR}"
  echo "Make sure the control plane is running and accessible"
  if [[ "${USE_TLS}" == "false" ]]; then
    echo "Hint: If the endpoint uses TLS, try adding --tls flag"
  fi
  exit 1
fi
echo "✓ API is reachable"
echo ""

find_game_id_by_name() {
  local target_name="${1}"
  local page_token=""
  while true; do
    local payload
    if [[ -n "${page_token}" ]]; then
      payload="$(printf '{"page_size":100,"page_token":"%s"}' "${page_token}")"
    else
      payload='{"page_size":100}'
    fi
    local resp
    resp="$(grpc_call "${CONTROL_API_ADDR}" "manman.v1.ManManAPI/ListGames" "${payload}")"
    local result
    result="$(python3 -c 'import json,sys; data=json.loads(sys.stdin.read() or "{}"); games=data.get("games", []);
found="";
for g in games:
    name=(g.get("name") or "").strip()
    if name==sys.argv[1]:
        found=(g.get("game_id") or g.get("gameId") or "")
        break
next_token=(data.get("next_page_token") or data.get("nextPageToken") or "")
print(f"{found}|{next_token}")' "${target_name}" <<< "${resp}")"
    local found="${result%%|*}"
    local next="${result#*|}"
    if [[ -n "${found}" ]]; then
      echo "${found}"
      return 0
    fi
    if [[ -z "${next}" ]]; then
      echo ""
      return 0
    fi
    page_token="${next}"
  done
}

game_id="$(find_game_id_by_name "${GAME_NAME}")"

if [[ -z "${game_id}" ]]; then
  echo "Creating game '${GAME_NAME}'..."
  create_game_payload="$(cat <<EOF
{
  "name": "${GAME_NAME}",
  "steam_app_id": "730",
  "metadata": {
    "genre": "FPS",
    "publisher": "Valve",
    "tags": ["cs2", "counter-strike", "competitive"]
  }
}
EOF
)"
  create_game_json="$(grpc_call "${CONTROL_API_ADDR}" "manman.v1.ManManAPI/CreateGame" "${create_game_payload}" || true)"
  game_id="$(python3 -c 'import json,sys; data=json.loads(sys.stdin.read() or "{}"); game=data.get("game", {}); print(game.get("game_id") or game.get("gameId") or "")' <<< "${create_game_json}")"
fi

if [[ -z "${game_id}" ]]; then
  echo "Game already exists or create failed; re-listing..."
  game_id="$(find_game_id_by_name "${GAME_NAME}")"
  if [[ -z "${game_id}" ]]; then
    echo "Failed to resolve game_id"
    exit 1
  fi
fi

find_config_id() {
  local page_token=""
  while true; do
    local payload
    if [[ -n "${page_token}" ]]; then
      payload="$(printf '{"game_id":%s,"page_size":100,"page_token":"%s"}' "${game_id}" "${page_token}")"
    else
      payload="$(printf '{"game_id":%s,"page_size":100}' "${game_id}")"
    fi
    local resp
    resp="$(grpc_call "${CONTROL_API_ADDR}" "manman.v1.ManManAPI/ListGameConfigs" "${payload}")"
    local result
    result="$(python3 -c 'import json,sys; data=json.loads(sys.stdin.read() or "{}"); configs=data.get("configs", []);
found="";
for c in configs:
    name=(c.get("name") or "").strip()
    if name==sys.argv[1]:
        found=(c.get("config_id") or c.get("configId") or "")
        break
next_token=(data.get("next_page_token") or data.get("nextPageToken") or "")
print(f"{found}|{next_token}")' "${GAME_CONFIG_NAME}" <<< "${resp}")"
    local found="${result%%|*}"
    local next="${result#*|}"
    if [[ -n "${found}" ]]; then
      echo "${found}"
      return 0
    fi
    if [[ -z "${next}" ]]; then
      echo ""
      return 0
    fi
    page_token="${next}"
  done
}

config_id="$(find_config_id)"

if [[ -z "${config_id}" ]]; then
  echo "Creating game config '${GAME_CONFIG_NAME}' for game_id=${game_id}..."
  create_config_payload="$(cat <<EOF
{
  "game_id": ${game_id},
  "name": "${GAME_CONFIG_NAME}",
  "image": "${IMAGE}",
  "args_template": "",
  "env_template": {
    "SRCDS_TOKEN": "YOUR_SRCDS_TOKEN_HERE",
    "CS2_SERVERNAME": "ManManV2 CS2 Server",
    "CS2_IP": "0.0.0.0",
    "CS2_PORT": "27015",
    "CS2_RCONPW": "changeme",
    "CS2_MAXPLAYERS": "10",
    "CS2_GAMEALIAS": "competitive",
    "CS2_STARTMAP": "de_inferno",
    "CS2_MAPGROUP": "mg_active",
    "CS2_BOT_DIFFICULTY": "1",
    "CS2_BOT_QUOTA": "0",
    "CS2_LOG": "on",
    "TV_ENABLE": "0",
    "STEAMAPPVALIDATE": "0"
  },
  "files": [],
  "parameters": [],
  "entrypoint": [],
  "command": []
}
EOF
)"
  create_config_json="$(grpc_call "${CONTROL_API_ADDR}" "manman.v1.ManManAPI/CreateGameConfig" "${create_config_payload}" || true)"
  config_id="$(python3 -c 'import json,sys; data=json.loads(sys.stdin.read() or "{}"); config=data.get("config", {}); print(config.get("config_id") or config.get("configId") or "")' <<< "${create_config_json}")"
fi

echo "Ensuring CS2 volume strategy exists..."
create_strategy_payload="$(cat <<EOF
{
  "game_id": ${game_id},
  "name": "cs2-data",
  "description": "Persistent CS2 game data volume mounted to /home/steam/cs2-dedicated in container",
  "strategy_type": "volume",
  "target_path": "/home/steam/cs2-dedicated",
  "base_template": "cs2-data"
}
EOF
)"
strategy_result="$(grpc_call "${CONTROL_API_ADDR}" "manman.v1.ManManAPI/CreateConfigurationStrategy" "${create_strategy_payload}" 2>&1 || true)"
if echo "${strategy_result}" | grep -q "duplicate key\|already exists"; then
  echo "  Volume strategy 'cs2-data' already exists (skipped)"
elif echo "${strategy_result}" | grep -q "strategy"; then
  echo "  ✔ Created volume strategy 'cs2-data'"
else
  echo "  Warning: Unexpected response from volume strategy creation"
fi

if [[ -z "${config_id}" ]]; then
  echo "Config already exists or create failed; re-listing..."
  config_id="$(find_config_id)"
  if [[ -z "${config_id}" ]]; then
    echo "Failed to resolve config_id"
    exit 1
  fi
fi

echo "✔ Game ID: ${game_id}"
echo "✔ Config ID: ${config_id}"

# Deploy or update ServerGameConfig with port bindings
echo "Checking if config is deployed to default server..."
find_sgc_id() {
  local resp
  resp="$(grpc_call "${CONTROL_API_ADDR}" "manman.v1.ManManAPI/ListServerGameConfigs" '{"page_size":100}')"
  local result
  result="$(python3 -c 'import json,sys; data=json.loads(sys.stdin.read() or "{}"); configs=data.get("configs", []);
found="";
for c in configs:
    cid=str(c.get("gameConfigId") or c.get("game_config_id") or "")
    if cid==sys.argv[1]:
        found=str(c.get("serverGameConfigId") or c.get("sgc_id") or "")
        break
print(found)' "${config_id}" <<< "${resp}")"
  echo "${result}"
}

sgc_id="$(find_sgc_id)"

if [[ -z "${sgc_id}" ]]; then
  echo "Deploying config to default server with port bindings..."
  deploy_payload="$(cat <<EOF
{
  "server_id": 1,
  "game_config_id": ${config_id},
  "port_bindings": [
    {
      "container_port": 27015,
      "host_port": 27015,
      "protocol": "TCP"
    },
    {
      "container_port": 27015,
      "host_port": 27015,
      "protocol": "UDP"
    },
    {
      "container_port": 27020,
      "host_port": 27020,
      "protocol": "UDP"
    }
  ],
  "parameters": {}
}
EOF
)"
  deploy_json="$(grpc_call "${CONTROL_API_ADDR}" "manman.v1.ManManAPI/DeployGameConfig" "${deploy_payload}" 2>&1 || true)"

  if echo "${deploy_json}" | grep -qi "error\|failed"; then
    echo "  ⚠️  Deploy failed (may already exist or port conflict)"
    echo "  Checking if SGC was created..."
    sgc_id="$(find_sgc_id)"
    if [[ -n "${sgc_id}" ]]; then
      echo "  ✔ Found existing SGC ID: ${sgc_id}"
    else
      echo "  ✗ Could not find or create SGC"
      echo "  Error details: $(echo "${deploy_json}" | head -5)"
    fi
  else
    sgc_id="$(python3 -c 'import json,sys; data=json.loads(sys.stdin.read() or "{}"); config=data.get("config", {}); print(config.get("serverGameConfigId") or config.get("sgc_id") or "")' <<< "${deploy_json}")"
    echo "  ✔ Deployed to server as SGC ID: ${sgc_id}"
  fi
else
  echo "Config already deployed as SGC ID: ${sgc_id}"
  echo "Updating port bindings..."
  update_payload="$(cat <<EOF
{
  "server_game_config_id": ${sgc_id},
  "port_bindings": [
    {
      "container_port": 27015,
      "host_port": 27015,
      "protocol": "TCP"
    },
    {
      "container_port": 27015,
      "host_port": 27015,
      "protocol": "UDP"
    },
    {
      "container_port": 27020,
      "host_port": 27020,
      "protocol": "UDP"
    }
  ],
  "update_paths": ["port_bindings"]
}
EOF
)"
  update_result="$(grpc_call "${CONTROL_API_ADDR}" "manman.v1.ManManAPI/UpdateServerGameConfig" "${update_payload}" 2>&1 || true)"
  if echo "${update_result}" | grep -qi "error"; then
    echo "  ⚠️  Update failed (SGC may be unchanged)"
  else
    echo "  ✔ Port bindings updated"
  fi
fi

echo ""
echo "✔ Setup complete!"
echo ""
echo "Summary:"
echo "  Game ID:   ${game_id}"
echo "  Config ID: ${config_id}"
if [[ -n "${sgc_id}" && "${sgc_id}" != "null" ]]; then
  echo "  SGC ID:    ${sgc_id}"
  echo ""
  echo "Port Bindings:"
  echo "  27015/TCP - Game server port"
  echo "  27015/UDP - Game server port"
  echo "  27020/UDP - SourceTV port (if enabled)"
else
  echo "  SGC ID:    (not created - may already exist or port conflict)"
fi
echo ""
echo "⚠️  Important: Set the SRCDS_TOKEN environment variable to your Steam Game Server Token"
echo "   Get one at: https://steamcommunity.com/dev/managegameservers"
echo ""
echo "Next steps:"
echo "  1. Update the SRCDS_TOKEN in the game config environment variables"
echo "  2. Adjust CS2_SERVERNAME, CS2_RCONPW, and other settings as needed"
echo "  3. Run ./scripts/seed_cs2_actions.sh to load game actions"
echo ""
