#!/usr/bin/env bash
# common.sh - Shared utilities for manmanv2 scripts.
# Source this file from a script that has already set SCRIPT_DIR.
# Provides: REPO_ROOT, resolve_tls, require_grpcurl, grpc_call,
#           setup_auth, test_api_connectivity,
#           find_game_id_by_name, find_config_id_by_name, find_sgc_id,
#           create_action

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"

resolve_tls() {
  if [[ "${USE_TLS}" == "auto" ]]; then
    if [[ "${CONTROL_API_ADDR}" =~ :443$ ]]; then
      USE_TLS="true"
    else
      USE_TLS="false"
    fi
  fi
}

require_grpcurl() {
  if ! command -v grpcurl &> /dev/null; then
    echo "Error: grpcurl is not installed"
    echo "Install with: brew install grpcurl (macOS) or go install github.com/fullstorydev/grpcurl/cmd/grpcurl@latest"
    exit 1
  fi
}

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

setup_auth() {
  if [[ "${GRPC_AUTH_MODE:-none}" == "token" ]]; then
    if [[ -z "${ACCESS_TOKEN:-}" ]]; then
      echo "Error: GRPC_AUTH_MODE=token requires ACCESS_TOKEN to be set"
      exit 1
    fi
    echo "✓ Using provided ACCESS_TOKEN"
    echo ""
    return
  fi
  ACCESS_TOKEN=""
  if [[ "${GRPC_AUTH_MODE:-none}" == "oidc" ]]; then
    echo "Getting OIDC token from Keycloak..."
    if [[ -z "${GRPC_AUTH_TOKEN_URL:-}" || -z "${GRPC_AUTH_CLIENT_ID:-}" || -z "${GRPC_AUTH_CLIENT_SECRET:-}" ]]; then
      echo "Error: GRPC_AUTH_MODE=oidc requires GRPC_AUTH_TOKEN_URL, GRPC_AUTH_CLIENT_ID, and GRPC_AUTH_CLIENT_SECRET"
      exit 1
    fi

    local token_resp
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
}

test_api_connectivity() {
  echo "Testing API connectivity..."

  local tls_flags=""
  if [[ "${USE_TLS}" == "true" ]]; then
    if [[ "${INSECURE_TLS}" == "true" ]]; then
      tls_flags="-insecure"
    fi
  else
    tls_flags="-plaintext"
  fi

  local TEST_CMD=(grpcurl ${tls_flags})
  if [[ -n "${ACCESS_TOKEN:-}" ]]; then
    TEST_CMD+=("-H" "Authorization: Bearer ${ACCESS_TOKEN}")
  fi
  TEST_CMD+=("${CONTROL_API_ADDR}" list manman.v1.ManManAPI)

  if ! "${TEST_CMD[@]}" &> /dev/null; then
    echo "✗ Cannot connect to API at ${CONTROL_API_ADDR}"
    echo "Make sure the control plane is running and accessible"
    if [[ "${USE_TLS}" == "false" ]]; then
      echo "Hint: If the endpoint uses TLS, try adding --tls flag"
    fi
    exit 1
  fi
  echo "✓ API is reachable"
  echo ""
}

# find_game_id_by_name <name>
# Paginates ListGames and prints the game_id whose name matches exactly.
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

# find_config_id_by_name <game_id> <config_name>
# Paginates ListGameConfigs and prints the config_id whose name matches exactly.
find_config_id_by_name() {
  local game_id="${1}"
  local config_name="${2}"
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
print(f"{found}|{next_token}")' "${config_name}" <<< "${resp}")"
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

# find_sgc_id <config_id>
# Returns the serverGameConfigId deployed for the given game_config_id.
find_sgc_id() {
  local config_id="${1}"
  local resp
  resp="$(grpc_call "${CONTROL_API_ADDR}" "manman.v1.ManManAPI/ListServerGameConfigs" '{"page_size":100}')"
  python3 -c 'import json,sys; data=json.loads(sys.stdin.read() or "{}"); configs=data.get("configs", []);
found="";
for c in configs:
    cid=str(c.get("gameConfigId") or c.get("game_config_id") or "")
    if cid==sys.argv[1]:
        found=str(c.get("serverGameConfigId") or c.get("sgc_id") or "")
        break
print(found)' "${config_id}" <<< "${resp}"
}

# create_action <json_payload>
# Calls CreateActionDefinition and prints success/failure.
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
