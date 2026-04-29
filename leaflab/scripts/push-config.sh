#!/usr/bin/env bash
# push-config.sh — Push a named scenario config to a leaflab device via gRPC.
#
# Usage:
#   ./push-config.sh <device_id> <scenario>
#   ./push-config.sh <device_id> --list
#
# Environment:
#   LEAFLAB_API_HOST  gRPC host:port  (default: localhost:50051)
#
# Examples:
#   ./push-config.sh leaflab-ccdba79f5fac single-light
#   ./push-config.sh leaflab-ccdba79f5fac mux-light-temp
#   LEAFLAB_API_HOST=10.0.0.5:50051 ./push-config.sh leaflab-abc123 light-temp
#
# Scenarios are JSON files in ./scenarios/.  Add a new file there to define
# additional hardware setups without touching this script.
#
# Dependencies: grpcurl, jq

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
SCENARIOS_DIR="$SCRIPT_DIR/scenarios"
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

# ── build request and push ────────────────────────────────────────────────────

REQUEST="$(jq -n \
    --arg device_id "$DEVICE_ID" \
    --slurpfile s "$SCENARIO_FILE" \
    '{deviceId: $device_id, sensors: $s[0].sensors}')"

RESPONSE="$(grpcurl -plaintext \
    -d "$REQUEST" \
    "$HOST" \
    leaflab.api.v1.LeafLabAPI/PushDeviceConfig)"

VERSION="$(echo "$RESPONSE" | jq -r '.version // "unknown"')"
echo "Pushed — assigned version $VERSION"
echo ""
echo "Watch the device ACK:"
echo "  mosquitto_sub -h localhost -p 1883 -u rabbit -P password \\"
echo "    -t 'leaflab/$DEVICE_ID/config/ack' -v"
