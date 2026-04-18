#!/usr/bin/env bash
set -euo pipefail

# Load a ghcr.io/acemod/arma-reforger GameConfig for manmanv2.
# Requires: grpcurl, python3
#
# Usage: ./scripts/load-reforger-config.sh [OPTIONS]
#
# Options:
#   --grpc-url=HOST:PORT      GRPC API endpoint (default: localhost:50052)
#   --api-endpoint=HOST:PORT  Alias for --grpc-url
#   --game-name=NAME          Game name (default: Arma Reforger)
#   --config-name=NAME        Config name (default: Default)
#   --image=IMAGE             Docker image (default: ghcr.io/acemod/arma-reforger:latest)
#   --tls                     Use TLS for GRPC connection (auto-detected for port 443)
#   --insecure                Use insecure TLS (skip certificate verification)
#   --help                    Show this help message

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=common.sh
source "${SCRIPT_DIR}/common.sh"

# Default values (can be overridden by env vars or CLI args)
CONTROL_API_ADDR="${CONTROL_API_ADDR:-localhost:50052}"
GAME_NAME="${GAME_NAME:-Arma Reforger}"
GAME_CONFIG_NAME="${GAME_CONFIG_NAME:-Default}"
IMAGE="${IMAGE:-ghcr.io/acemod/arma-reforger:latest}"
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
echo "  Loading Arma Reforger Configuration"
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
  "steam_app_id": "1874900",
  "metadata": {
    "genre": "Military Simulation",
    "publisher": "Bohemia Interactive",
    "tags": ["arma", "milsim", "reforger"]
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
    "GAME_NAME": "ManManV2 Arma Reforger Server",
    "GAME_PASSWORD": "",
    "GAME_PASSWORD_ADMIN": "",
    "GAME_ADMINS": "",
    "GAME_MAX_PLAYERS": "32",
    "GAME_VISIBLE": "true",
    "GAME_SUPPORTED_PLATFORMS": "PLATFORM_PC,PLATFORM_XBL,PLATFORM_PSN",
    "GAME_SCENARIO_ID": "{ECC61978EDCC2B5A}Missions/23_Campaign.conf",
    "GAME_PROPS_BATTLEYE": "true",
    "GAME_PROPS_DISABLE_THIRD_PERSON": "false",
    "GAME_PROPS_FAST_VALIDATION": "true",
    "GAME_PROPS_SERVER_MAX_VIEW_DISTANCE": "2500",
    "GAME_PROPS_SERVER_MIN_GRASS_DISTANCE": "50",
    "GAME_PROPS_NETWORK_VIEW_DISTANCE": "1000",
    "GAME_MODS_IDS_LIST": "",
    "SERVER_PUBLIC_ADDRESS": "",
    "SERVER_BIND_PORT": "38201",
    "SERVER_PUBLIC_PORT": "38201",
    "SERVER_A2S_PORT": "17777",
    "RCON_PASSWORD": "",
    "RCON_PORT": "19999",
    "ARMA_MAX_FPS": "120",
    "SKIP_INSTALL": "false"
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

echo "Ensuring volumes exist for config..."

for vol_json in \
  '{"name":"reforger-profile","description":"Arma Reforger server profile and player data","container_path":"/home/profile","volume_type":"named","read_only":false}' \
  '{"name":"reforger-configs","description":"Arma Reforger server config files","container_path":"/reforger/Configs","volume_type":"named","read_only":false}' \
  '{"name":"reforger-workshop","description":"Arma Reforger downloaded workshop mods","container_path":"/reforger/workshop","volume_type":"named","read_only":false}'; do

  vol_name="$(python3 -c 'import json,sys; print(json.loads(sys.argv[1])["name"])' "${vol_json}")"
  payload="$(python3 -c "import json,sys; d=json.loads(sys.argv[1]); d['config_id']=${config_id}; d.setdefault('host_subpath',''); print(json.dumps(d))" "${vol_json}")"
  result="$(grpc_call "${CONTROL_API_ADDR}" "manman.v1.ManManAPI/CreateGameConfigVolume" "${payload}" 2>&1 || true)"
  if echo "${result}" | grep -q "duplicate key\|already exists"; then
    echo "  Volume '${vol_name}' already exists (skipped)"
  elif echo "${result}" | grep -q "volume"; then
    echo "  ✔ Created volume '${vol_name}'"
  else
    echo "  Warning: Unexpected response for volume '${vol_name}'"
  fi
done

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
      "container_port": 38201,
      "host_port": 38201,
      "protocol": "UDP"
    },
    {
      "container_port": 17777,
      "host_port": 17777,
      "protocol": "UDP"
    }
  ]
}
EOF
)"
  deploy_json="$(grpc_call "${CONTROL_API_ADDR}" "manman.v1.ManManAPI/DeployGameConfig" "${deploy_payload}" 2>&1 || true)"

  if echo "${deploy_json}" | grep -qi "error\|failed"; then
    echo "  ⚠️  Deploy failed (may already exist or port conflict)"
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
      "container_port": 38201,
      "host_port": 38201,
      "protocol": "UDP"
    },
    {
      "container_port": 17777,
      "host_port": 17777,
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
  echo "  38201/UDP — Arma Reforger game port (internal: 38201)"
  echo "  17777/UDP — Steam A2S query port"
fi
echo ""
echo "Next steps:"
echo "  1. Set SERVER_PUBLIC_ADDRESS to the server's public IP"
echo "  2. Set GAME_PASSWORD_ADMIN for admin access"
echo "  3. Add mod IDs to GAME_MODS_IDS_LIST (auto-downloaded on start)"
echo "  4. Set RCON_PASSWORD to enable RCON"
echo "  5. Run ./scripts/seed_reforger_actions.sh to load game actions"
echo ""
