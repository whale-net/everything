#!/usr/bin/env bash
set -euo pipefail

# Load an itzg/minecraft-server GameConfig with defaults for local testing.
# Requires: grpcurl, python3
#
# Usage: ./scripts/load-minecraft-config.sh [OPTIONS]
#
# Options:
#   --grpc-url=HOST:PORT      GRPC API endpoint (default: localhost:50052)
#   --api-endpoint=HOST:PORT  Alias for --grpc-url
#   --game-name=NAME          Game name (default: Minecraft)
#   --config-name=NAME        Config name (default: Vanilla)
#   --image=IMAGE             Docker image (default: itzg/minecraft-server:latest)
#   --tls                     Use TLS for GRPC connection (auto-detected for port 443)
#   --insecure                Use insecure TLS (skip certificate verification)
#   --help                    Show this help message

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "${SCRIPT_DIR}/../.." && pwd)"

# Default values (can be overridden by env vars or CLI args)
CONTROL_API_ADDR="${CONTROL_API_ADDR:-localhost:50052}"
GAME_NAME="${GAME_NAME:-Minecraft}"
GAME_CONFIG_NAME="${GAME_CONFIG_NAME:-Vanilla}"
IMAGE="${IMAGE:-itzg/minecraft-server:latest}"
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
    -proto "${REPO_ROOT}/manmanv2/protos/api.proto" \
    -proto "${REPO_ROOT}/manmanv2/protos/messages.proto" \
    -d "${data}" \
    "${addr}" "${method}"
}

echo ""
echo "════════════════════════════════════════════════════"
echo "  Loading Minecraft Configuration"
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

find_game_id() {
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
    name=(g.get("name") or "").strip().lower()
    if name=="minecraft":
        found=(g.get("game_id") or g.get("gameId") or "")
        break
next_token=(data.get("next_page_token") or data.get("nextPageToken") or "")
print(f"{found}|{next_token}")' <<< "${resp}")"
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

bad_game_id="$(find_game_id_by_name "__GAME_NAME__")"
if [[ -n "${bad_game_id}" ]]; then
  echo "Deleting bad game '__GAME_NAME__' (id=${bad_game_id})..."
  grpc_call "${CONTROL_API_ADDR}" "manman.v1.ManManAPI/DeleteGame" "{\"game_id\": ${bad_game_id}}" >/dev/null
fi

game_id="$(find_game_id)"

if [[ -z "${game_id}" ]]; then
  echo "Creating game '${GAME_NAME}'..."
  create_game_payload="$(cat <<EOF
{
  "name": "${GAME_NAME}",
  "steam_app_id": "",
  "metadata": {
    "genre": "Sandbox",
    "publisher": "Mojang",
    "tags": ["minecraft", "vanilla"]
  }
}
EOF
)"
  create_game_json="$(grpc_call "${CONTROL_API_ADDR}" "manman.v1.ManManAPI/CreateGame" "${create_game_payload}" || true)"
  game_id="$(python3 -c 'import json,sys; data=json.loads(sys.stdin.read() or "{}"); game=data.get("game", {}); print(game.get("game_id") or game.get("gameId") or "")' <<< "${create_game_json}")"
fi

if [[ -z "${game_id}" ]]; then
  echo "Game already exists or create failed; re-listing..."
  game_id="$(find_game_id)"
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
    name=(c.get("name") or "").strip().lower()
    if name=="vanilla":
        found=(c.get("config_id") or c.get("configId") or "")
        break
next_token=(data.get("next_page_token") or data.get("nextPageToken") or "")
print(f"{found}|{next_token}")' <<< "${resp}")"
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
    "EULA": "TRUE"
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

echo "Ensuring Minecraft volume strategy exists..."
create_strategy_payload="$(cat <<EOF
{
  "game_id": ${game_id},
  "name": "data",
  "description": "Persistent game data volume mounted to /data in container",
  "strategy_type": "volume",
  "target_path": "/data",
  "base_template": "data"
}
EOF
)"
strategy_result="$(grpc_call "${CONTROL_API_ADDR}" "manman.v1.ManManAPI/CreateConfigurationStrategy" "${create_strategy_payload}" 2>&1 || true)"
if echo "${strategy_result}" | grep -q "duplicate key\|already exists"; then
  echo "  Volume strategy 'data' already exists (skipped)"
elif echo "${strategy_result}" | grep -q "strategy"; then
  echo "  ✔ Created volume strategy 'data'"
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
      "container_port": 25565,
      "host_port": 25565,
      "protocol": "TCP"
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
      "container_port": 25565,
      "host_port": 25565,
      "protocol": "TCP"
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
echo "Ensuring server.properties configuration strategy exists..."

# First, check if strategy already exists
existing_strategies="$(grpc_call "${CONTROL_API_ADDR}" "manman.v1.ManManAPI/ListConfigurationStrategies" "{\"game_id\": ${game_id}}" 2>&1 || true)"
existing_id="$(python3 -c 'import json,sys; data=json.loads(sys.stdin.read() or "{}"); strategies=data.get("strategies", []);
for s in strategies:
    if s.get("name") == "server.properties":
        print(s.get("strategyId") or "")
        break' <<< "${existing_strategies}" 2>/dev/null || true)"

if [[ -n "${existing_id}" && "${existing_id}" != "null" ]]; then
  echo "  Found existing server.properties strategy (ID: ${existing_id})"
  strategy_id="${existing_id}"
else
  # Create new strategy with empty base_template (merge mode)
  server_props_strategy_payload="$(cat <<EOF
{
  "game_id": ${game_id},
  "name": "server.properties",
  "description": "Minecraft server configuration file (merge mode - patches existing file)",
  "strategy_type": "file_properties",
  "target_path": "/data/server.properties",
  "apply_order": 2
}
EOF
)"

  strategy_resp="$(grpc_call "${CONTROL_API_ADDR}" "manman.v1.ManManAPI/CreateConfigurationStrategy" "${server_props_strategy_payload}" 2>&1 || true)"

  if echo "${strategy_resp}" | grep -qi "duplicate key\|already exists"; then
    echo "  Strategy already exists, fetching ID..."
    existing_strategies="$(grpc_call "${CONTROL_API_ADDR}" "manman.v1.ManManAPI/ListConfigurationStrategies" "{\"game_id\": ${game_id}}" 2>&1 || true)"
    strategy_id="$(python3 -c 'import json,sys; data=json.loads(sys.stdin.read() or "{}"); strategies=data.get("strategies", []);
for s in strategies:
    if s.get("name") == "server.properties":
        print(s.get("strategyId") or "")
        break' <<< "${existing_strategies}" 2>/dev/null || true)"
  else
    strategy_id="$(python3 -c 'import json,sys; data=json.loads(sys.stdin.read() or "{}"); strategy=data.get("strategy", {}); print(strategy.get("strategyId") or "")' <<< "${strategy_resp}" 2>/dev/null || true)"
  fi

  if [[ -n "${strategy_id}" && "${strategy_id}" != "null" ]]; then
    echo "  ✔ Server.properties strategy ready (ID: ${strategy_id})"
  else
    echo "  ⚠️  Could not create or find server.properties strategy (skipping patches)"
    strategy_id=""
  fi
fi

if [[ -n "${strategy_id}" && "${strategy_id}" != "null" ]]; then
  echo ""
  echo "Creating configuration patches..."

  # Create patch at game_config level with base Minecraft settings
  patch_content="online-mode=true
max-players=20
difficulty=normal
pvp=true
motd=ManManV2 Minecraft Server"

  create_patch_payload="$(cat <<EOF
{
  "strategy_id": ${strategy_id},
  "patch_level": "game_config",
  "entity_id": ${config_id},
  "patch_content": "${patch_content}",
  "patch_format": "properties"
}
EOF
)"

  patch_resp="$(grpc_call "${CONTROL_API_ADDR}" "manman.v1.ManManAPI/CreateConfigurationPatch" "${create_patch_payload}" 2>&1 || true)"

  if echo "${patch_resp}" | grep -q "patch"; then
    echo "  ✔ Created game_config patch with base Minecraft settings"
  elif echo "${patch_resp}" | grep -qi "duplicate\|already exists"; then
    echo "  Game_config patch already exists (skipped)"
  else
    echo "  ⚠️  Could not create game_config patch"
  fi

  # Create patch at server_game_config level to override MOTD (only if SGC exists)
  if [[ -n "${sgc_id}" && "${sgc_id}" != "null" ]]; then
    sgc_patch_content="motd=ManManV2 Dev Server - SGC Override"

    create_sgc_patch_payload="$(cat <<EOF
{
  "strategy_id": ${strategy_id},
  "patch_level": "server_game_config",
  "entity_id": ${sgc_id},
  "patch_content": "${sgc_patch_content}",
  "patch_format": "properties"
}
EOF
)"

    sgc_patch_resp="$(grpc_call "${CONTROL_API_ADDR}" "manman.v1.ManManAPI/CreateConfigurationPatch" "${create_sgc_patch_payload}" 2>&1 || true)"

    if echo "${sgc_patch_resp}" | grep -q "patch"; then
      echo "  ✔ Created server_game_config patch to override MOTD"
    elif echo "${sgc_patch_resp}" | grep -qi "duplicate\|already exists"; then
      echo "  Server_game_config patch already exists (skipped)"
    else
      echo "  ⚠️  Could not create server_game_config patch"
    fi
  else
    echo "  ⚠️  No SGC ID available, skipping SGC patch"
  fi
else
  echo "  ⚠️  No strategy ID available, skipping all patches"
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
  echo "Configuration cascade:"
  echo "  Game: Defines strategies (volume, server.properties)"
  echo "  GameConfig: Sets base values (online-mode, max-players, motd)"
  echo "  ServerGameConfig #${sgc_id}: Overrides motd (if patches created)"
else
  echo "  SGC ID:    (not created - may already exist or port conflict)"
fi
echo ""
echo "⚠️  Note: The itzg/minecraft-server image regenerates server.properties on startup,"
echo "   which overwrites file patches. Consider using environment variable strategy instead."
