#!/usr/bin/env bash
set -euo pipefail

# Load a wolveix/satisfactory-server GameConfig for manmanv2.
# Requires: grpcurl, python3
#
# Usage: ./scripts/load-satisfactory-config.sh [OPTIONS]
#
# Options:
#   --grpc-url=HOST:PORT      GRPC API endpoint (default: localhost:50052)
#   --api-endpoint=HOST:PORT  Alias for --grpc-url
#   --game-name=NAME          Game name (default: Satisfactory)
#   --config-name=NAME        Config name (default: Default)
#   --image=IMAGE             Docker image (default: wolveix/satisfactory-server:latest)
#   --tls                     Use TLS for GRPC connection (auto-detected for port 443)
#   --insecure                Use insecure TLS (skip certificate verification)
#   --help                    Show this help message

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=common.sh
source "${SCRIPT_DIR}/common.sh"

# Default values (can be overridden by env vars or CLI args)
CONTROL_API_ADDR="${CONTROL_API_ADDR:-localhost:50052}"
GAME_NAME="${GAME_NAME:-Satisfactory}"
GAME_CONFIG_NAME="${GAME_CONFIG_NAME:-Default}"
IMAGE="${IMAGE:-wolveix/satisfactory-server:latest}"
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

resolve_tls

echo ""
echo "════════════════════════════════════════════════════"
echo "  Loading Satisfactory Configuration"
echo "════════════════════════════════════════════════════"
echo "GRPC API:  ${CONTROL_API_ADDR}"
echo "TLS:       ${USE_TLS}"
echo "Game:      ${GAME_NAME}"
echo "Config:    ${GAME_CONFIG_NAME}"
echo "Image:     ${IMAGE}"
echo ""

require_grpcurl
setup_auth
test_api_connectivity

game_id="$(find_game_id_by_name "${GAME_NAME}")"

if [[ -z "${game_id}" ]]; then
  echo "Creating game '${GAME_NAME}'..."
  create_game_payload="$(cat <<EOF
{
  "name": "${GAME_NAME}",
  "steam_app_id": "526870",
  "metadata": {
    "genre": "Factory Building",
    "publisher": "Coffee Stain Studios",
    "tags": ["satisfactory", "factory", "building", "multiplayer"]
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

config_id="$(find_config_id_by_name "${game_id}" "${GAME_CONFIG_NAME}")"

if [[ -z "${config_id}" ]]; then
  echo "Creating game config '${GAME_CONFIG_NAME}' for game_id=${game_id}..."
  create_config_payload="$(cat <<EOF
{
  "game_id": ${game_id},
  "name": "${GAME_CONFIG_NAME}",
  "image": "${IMAGE}",
  "args_template": "",
  "env_template": {
    "AUTOSAVENUM": "5",
    "MAXPLAYERS": "4",
    "MAXTICKRATE": "30",
    "PGID": "1000",
    "PUID": "1000",
    "SERVERGAMEPORT": "7777",
    "SERVERMESSAGINGPORT": "8888",
    "SKIPUPDATE": "false",
    "STEAMBETA": "false",
    "TIMEOUT": "30"
  },
  "entrypoint": [],
  "command": []
}
EOF
)"
  create_config_json="$(grpc_call "${CONTROL_API_ADDR}" "manman.v1.ManManAPI/CreateGameConfig" "${create_config_payload}" || true)"
  config_id="$(python3 -c 'import json,sys; data=json.loads(sys.stdin.read() or "{}"); config=data.get("config", {}); print(config.get("config_id") or config.get("configId") or "")' <<< "${create_config_json}")"
fi

if [[ -z "${config_id}" ]]; then
  echo "Config already exists or create failed; re-listing..."
  config_id="$(find_config_id_by_name "${game_id}" "${GAME_CONFIG_NAME}")"
  if [[ -z "${config_id}" ]]; then
    echo "Failed to resolve config_id"
    exit 1
  fi
fi

echo "Ensuring Satisfactory config volume exists for config..."
create_volume_payload="$(cat <<EOF
{
  "config_id": ${config_id},
  "name": "satisfactory-config",
  "description": "Persistent Satisfactory saves and config mounted to /config in container",
  "container_path": "/config",
  "host_subpath": "satisfactory-config",
  "read_only": false
}
EOF
)"
volume_result="$(grpc_call "${CONTROL_API_ADDR}" "manman.v1.ManManAPI/CreateGameConfigVolume" "${create_volume_payload}" 2>&1 || true)"
if echo "${volume_result}" | grep -q "duplicate key\|already exists"; then
  echo "  Volume 'satisfactory-config' already exists for this config (skipped)"
elif echo "${volume_result}" | grep -q "volume"; then
  echo "  ✔ Created volume 'satisfactory-config' for GameConfig"
else
  echo "  Warning: Unexpected response from volume creation"
fi

echo "✔ Game ID: ${game_id}"
echo "✔ Config ID: ${config_id}"

echo "Checking if config is deployed to default server..."
sgc_id="$(find_sgc_id "${config_id}")"

if [[ -z "${sgc_id}" ]]; then
  echo "Deploying config to default server with port bindings..."
  deploy_payload="$(cat <<EOF
{
  "server_id": 1,
  "game_config_id": ${config_id},
  "port_bindings": [
    {
      "container_port": 7777,
      "host_port": 38225,
      "protocol": "TCP"
    },
    {
      "container_port": 7777,
      "host_port": 38225,
      "protocol": "UDP"
    },
    {
      "container_port": 8888,
      "host_port": 38226,
      "protocol": "TCP"
    }
  ]
}
EOF
)"
  deploy_json="$(grpc_call "${CONTROL_API_ADDR}" "manman.v1.ManManAPI/DeployGameConfig" "${deploy_payload}" 2>&1 || true)"

  if echo "${deploy_json}" | grep -qi "error\|failed"; then
    echo "  ⚠️  Deploy failed (may already exist or port conflict)"
    echo "  Checking if SGC was created..."
    sgc_id="$(find_sgc_id "${config_id}")"
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
      "container_port": 7777,
      "host_port": 38225,
      "protocol": "TCP"
    },
    {
      "container_port": 7777,
      "host_port": 38225,
      "protocol": "UDP"
    },
    {
      "container_port": 8888,
      "host_port": 38226,
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
echo "✔ Setup complete!"
echo ""
echo "Summary:"
echo "  Game ID:   ${game_id}"
echo "  Config ID: ${config_id}"
if [[ -n "${sgc_id}" && "${sgc_id}" != "null" ]]; then
  echo "  SGC ID:    ${sgc_id}"
  echo ""
  echo "Port Bindings:"
  echo "  7777/TCP (container) → 38225/TCP (host) — Satisfactory game port"
  echo "  7777/UDP (container) → 38225/UDP (host) — Satisfactory game port"
  echo "  8888/TCP (container) → 38226/TCP (host) — Satisfactory messaging port"
else
  echo "  SGC ID:    (not created - may already exist or port conflict)"
fi
echo ""
echo "Next steps:"
echo "  1. Set MAXPLAYERS env var to increase the player limit (default: 4)"
echo "  2. The server uses ~8GB RAM; ensure the host has sufficient memory"
echo "  3. Run ./scripts/seed_satisfactory_actions.sh to load game actions"
echo ""
