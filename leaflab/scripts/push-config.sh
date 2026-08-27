#!/usr/bin/env bash
# push-config.sh — Push a named scenario config to a leaflab device via gRPC.
#
# Usage:
#   ./push-config.sh <device_id> <scenario>
#   ./push-config.sh <device_id> --list
#
# Environment:
#   LEAFLAB_API_HOST             gRPC host:port  (default: localhost:50051)
#   LEAFLAB_API_OIDC_ISSUER      Keycloak realm URL (required — passed
#                                 through to the authtoken credential helper)
#   LEAFLAB_DEVICE_FLOW_CLIENT_ID  Public Keycloak client id with the device
#                                   authorization grant enabled (required) —
#                                   see libs/go/grpcauth/KEYCLOAK.md
#   LEAFLAB_DESCRIPTOR_SET        Path to a pre-built FileDescriptorSet;
#                                 skips the `bazel build` below when set
#                                 (mainly for tests)
#   LEAFLAB_AUTHTOKEN_BIN         Path to a pre-built authtoken binary;
#                                 skips the `bazel build` below when set
#
# Examples:
#   ./push-config.sh leaflab-ccdba79f5fac single-light
#   ./push-config.sh leaflab-ccdba79f5fac mux-light-temp
#   LEAFLAB_API_HOST=10.0.0.5:50051 ./push-config.sh leaflab-abc123 light-temp
#
# Scenarios are JSON files in ./scenarios/.  Add a new file there to define
# additional hardware setups without touching this script.
#
# Authentication (FR81): this script obtains a bearer credential via the
# OIDC device authorization grant (RFC 8628) and resolves the service
# contract from the published descriptor set Bazel artifact — never from
# server reflection, which FR11.1 turns off outside development. The first
# time you use this against a given realm/client, run the one-time
# interactive login:
#
#   bazel run //leaflab/scripts/authtoken:authtoken -- login
#
# After that, this script refreshes the cached credential silently. See
# leaflab/README.md "Pushing device config (push-config.sh)" for the full
# migration notes.
#
# Dependencies: bazel, grpcurl, jq

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
SCENARIOS_DIR="$SCRIPT_DIR/scenarios"
REPO_ROOT="$(cd "$SCRIPT_DIR/../.." && pwd)"
HOST="${LEAFLAB_API_HOST:-localhost:50051}"

# ── helpers ───────────────────────────────────────────────────────────────────

list_scenarios() {
    echo "Available scenarios:"
    for f in "$SCENARIOS_DIR"/*.json; do
        name="$(basename "$f" .json)"
        desc="$(jq -r '.description // "(no description)"' "$f")"
        printf "  %-28s %s\n" "$name" "$desc"
    done
}

usage() {
    echo "Usage: $(basename "$0") <device_id> <scenario>"
    echo "       $(basename "$0") <device_id> --list"
    echo ""
    list_scenarios
    exit 1
}

require_cmd() {
    command -v "$1" >/dev/null 2>&1 || { echo "Error: '$1' not found in PATH" >&2; exit 1; }
}

# resolve_descriptor_set prints the path to a self-contained FileDescriptorSet
# built from //leaflab/api/proto:leaflabapi_descriptor_set (FR81 contract
# half, NFR11) so grpcurl can resolve leaflab.api.v1.LeafLabAPI without
# server reflection, which FR11.1 turns off outside development.
resolve_descriptor_set() {
    if [[ -n "${LEAFLAB_DESCRIPTOR_SET:-}" ]]; then
        echo "$LEAFLAB_DESCRIPTOR_SET"
        return 0
    fi
    require_cmd bazel
    (cd "$REPO_ROOT" && bazel build //leaflab/api:leaflab_api_descriptor_set >&2)
    echo "$REPO_ROOT/bazel-bin/leaflab/api/proto/leaflabapi_descriptor_set.pb"
}

# resolve_authtoken_bin prints the path to the authtoken helper binary
# (leaflab/scripts/authtoken) that wraps libs/go/grpcauth's OIDC device
# authorization grant (FR81 credential half).
resolve_authtoken_bin() {
    if [[ -n "${LEAFLAB_AUTHTOKEN_BIN:-}" ]]; then
        echo "$LEAFLAB_AUTHTOKEN_BIN"
        return 0
    fi
    require_cmd bazel
    (cd "$REPO_ROOT" && bazel build //leaflab/scripts/authtoken:authtoken >&2)
    echo "$REPO_ROOT/bazel-bin/leaflab/scripts/authtoken/authtoken_/authtoken"
}

# obtain_access_token runs the authtoken helper non-interactively: it loads
# and (if needed) silently refreshes a cached device-flow credential, but
# never launches the interactive approval prompt itself. When no cached
# credential exists it fails with authtoken's own actionable message
# (surfaced on stderr) instead of hanging — the one-time interactive login
# is a separate, explicit step (`authtoken login`), never implicit here.
obtain_access_token() {
    local authtoken_bin
    authtoken_bin="$(resolve_authtoken_bin)"

    local token
    if ! token="$("$authtoken_bin")"; then
        echo "" >&2
        echo "Run this once to authenticate interactively, then re-run this script:" >&2
        echo "  bazel run //leaflab/scripts/authtoken:authtoken -- login" >&2
        exit 1
    fi
    echo "$token"
}

# ── arg parsing ───────────────────────────────────────────────────────────────

[[ $# -lt 2 ]] && usage

DEVICE_ID="$1"
SCENARIO="$2"

if [[ "$SCENARIO" == "--list" ]]; then
    list_scenarios
    exit 0
fi

require_cmd grpcurl
require_cmd jq

# ── load scenario ─────────────────────────────────────────────────────────────

SCENARIO_FILE="$SCENARIOS_DIR/$SCENARIO.json"
if [[ ! -f "$SCENARIO_FILE" ]]; then
    echo "Error: unknown scenario '$SCENARIO'" >&2
    echo "" >&2
    list_scenarios >&2
    exit 1
fi

DESC="$(jq -r '.description // ""' "$SCENARIO_FILE")"
SENSOR_COUNT="$(jq '.sensors | length' "$SCENARIO_FILE")"

echo "Device:   $DEVICE_ID"
echo "Scenario: $SCENARIO — $DESC"
echo "Sensors:  $SENSOR_COUNT entries"
echo "API:      $HOST"
echo ""

# ── credential + contract (FR81) ────────────────────────────────────────────

ACCESS_TOKEN="$(obtain_access_token)"
DESCRIPTOR_SET="$(resolve_descriptor_set)"

# ── build request and push ────────────────────────────────────────────────────

REQUEST="$(jq -n \
    --arg device_id "$DEVICE_ID" \
    --slurpfile s "$SCENARIO_FILE" \
    '{deviceId: $device_id, sensors: $s[0].sensors}')"

RESPONSE="$(grpcurl -plaintext \
    -protoset "$DESCRIPTOR_SET" \
    -H "authorization: Bearer $ACCESS_TOKEN" \
    -d "$REQUEST" \
    "$HOST" \
    leaflab.api.v1.LeafLabAPI/PushDeviceConfig)"

VERSION="$(echo "$RESPONSE" | jq -r '.version // "unknown"')"
echo "Pushed — assigned version $VERSION"
echo ""
echo "Watch the device ACK:"
echo "  mosquitto_sub -h localhost -p 1883 -u rabbit -P password \\"
echo "    -t 'leaflab/$DEVICE_ID/config/ack' -v"
