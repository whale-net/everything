#!/usr/bin/env bash
# Mints a Keycloak access token via client_credentials (service-account
# grant) and prints it to stdout -- for the manual-bearer-token recipe
# in ../README.md "Connecting Claude Code to `mcp`" (CI/scripting/SA use;
# a human at a browser should prefer FR9's OAuth2 sign-in instead, which
# needs no token at all). Same grant/shape as manmanv2/scripts/common.sh's
# get_access_token, standalone here since whagent-net has no equivalent
# common.sh yet.
#
# Required env: WHAGENT_KC_TOKEN_URL, WHAGENT_KC_CLIENT_ID, WHAGENT_KC_CLIENT_SECRET
# (a confidential Keycloak client with "Service accounts roles" enabled --
# see libs/go/grpcauth/KEYCLOAK.md step 4 -- holding whatever realm role
# api's FR9's required_role check expects for the agent you're calling).
#
# Usage: export WHAGENT_DEV_ACCESS_TOKEN="$(whagent_net/scripts/kc-token.sh)"
set -euo pipefail

: "${WHAGENT_KC_TOKEN_URL:?WHAGENT_KC_TOKEN_URL is required, e.g. https://auth.whalenet.dev/realms/whalenet/protocol/openid-connect/token}"
: "${WHAGENT_KC_CLIENT_ID:?WHAGENT_KC_CLIENT_ID is required}"
: "${WHAGENT_KC_CLIENT_SECRET:?WHAGENT_KC_CLIENT_SECRET is required}"

token_resp=$(curl -sf -X POST "${WHAGENT_KC_TOKEN_URL}" \
  -H "Content-Type: application/x-www-form-urlencoded" \
  -d "grant_type=client_credentials" \
  -d "client_id=${WHAGENT_KC_CLIENT_ID}" \
  -d "client_secret=${WHAGENT_KC_CLIENT_SECRET}")

access_token=$(python3 -c 'import json,sys; print(json.loads(sys.stdin.read() or "{}").get("access_token", ""))' <<< "${token_resp}")

if [[ -z "${access_token}" ]]; then
  echo "Error: token endpoint did not return an access_token" >&2
  echo "${token_resp}" >&2
  exit 1
fi

echo "${access_token}"
