#!/usr/bin/env bash
set -euo pipefail

# Load an itzg/minecraft-server GameConfig with defaults for local testing.
# Requires: grpcurl, python3

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "${SCRIPT_DIR}/../.." && pwd)"

CONTROL_API_ADDR="${CONTROL_API_ADDR:-localhost:50052}"
GAME_NAME="Minecraft"
GAME_CONFIG_NAME="Vanilla"
IMAGE="itzg/minecraft-server:latest"

grpc_call() {
  local addr="$1"
  local method="$2"
  local data="$3"
  grpcurl -plaintext \
    -import-path "${REPO_ROOT}" \
    -proto "${REPO_ROOT}/manman/protos/api.proto" \
    -proto "${REPO_ROOT}/manman/protos/messages.proto" \
    -d "${data}" \
    "${addr}" "${method}"
}

echo "Using control API: ${CONTROL_API_ADDR}"
echo "Game: ${GAME_NAME}"
echo "Config: ${GAME_CONFIG_NAME}"
echo "Image: ${IMAGE}"

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
echo "Creating volume strategy..."
grpc_call "${CONTROL_API_ADDR}" "manman.v1.ManManAPI/CreateConfigurationStrategy" "${create_strategy_payload}" 2>&1 | grep -v "already exists" || true

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
  deploy_json="$(grpc_call "${CONTROL_API_ADDR}" "manman.v1.ManManAPI/DeployGameConfig" "${deploy_payload}")"
  sgc_id="$(python3 -c 'import json,sys; data=json.loads(sys.stdin.read() or "{}"); config=data.get("config", {}); print(config.get("serverGameConfigId") or config.get("sgc_id") or "")' <<< "${deploy_json}")"
  echo "✔ Deployed to server as SGC ID: ${sgc_id}"
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
  grpc_call "${CONTROL_API_ADDR}" "manman.v1.ManManAPI/UpdateServerGameConfig" "${update_payload}" >/dev/null
  echo "✔ Port bindings updated"
fi

echo ""
echo "✔ Setup complete! You can now start sessions."
