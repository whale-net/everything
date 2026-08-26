#!/usr/bin/env bash
# push-config.sh — Push a named scenario config to a leaflab device via gRPC.
#
# This script uses the OIDC device authorization grant flow to authenticate
# and the published service descriptor set instead of server reflection.
#
# Usage:
#   ./push-config.sh <device_id> <scenario>
#   ./push-config.sh <device_id> --list
#
# Environment:
#   LEAFLAB_API_HOST               gRPC host:port  (default: localhost:50051)
#   LEAFLAB_API_OIDC_ISSUER        OIDC issuer URL (default: http://localhost:8080/auth/realms/whale-net)
#   LEAFLAB_API_OIDC_CLIENT_ID     OIDC client ID (default: leaflab-device)
#   LEAFLAB_API_DESCRIPTOR_URL     Descriptor set download URL (default: computed from HOST)
#   LEAFLAB_API_DESCRIPTOR_PATH    Local descriptor set path (optional, overrides download)
#
# Examples:
#   ./push-config.sh leaflab-ccdba79f5fac single-light
#   ./push-config.sh leaflab-ccdba79f5fac mux-light-temp
#   LEAFLAB_API_HOST=10.0.0.5:50051 ./push-config.sh leaflab-abc123 light-temp
#
# Scenarios are JSON files in ./scenarios/.  Add a new file there to define
# additional hardware setups without touching this script.
#
# Dependencies: grpcurl, jq, curl

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
SCENARIOS_DIR="$SCRIPT_DIR/scenarios"
HOST="${LEAFLAB_API_HOST:-localhost:50051}"
OIDC_ISSUER="${LEAFLAB_API_OIDC_ISSUER:-http://localhost:8080/auth/realms/whale-net}"
OIDC_CLIENT_ID="${LEAFLAB_API_OIDC_CLIENT_ID:-leaflab-device}"
DESCRIPTOR_PATH="${LEAFLAB_API_DESCRIPTOR_PATH:-}"

# Device auth cache directory
CACHE_DIR="${XDG_CACHE_HOME:-.cache}/grpcauth"
TOKEN_CACHE="$CACHE_DIR/device_grant.json"

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

# Load cached token if it exists and is valid
load_cached_token() {
    if [[ ! -f "$TOKEN_CACHE" ]]; then
        return 1
    fi

    # Verify restrictive permissions (0600 = rw-------)
    local perms
    perms=$(stat -c %a "$TOKEN_CACHE" 2>/dev/null || echo "")
    if [[ "$perms" != "600" ]]; then
        echo "Warning: token cache has overly permissive mode $perms, re-authenticating" >&2
        rm -f "$TOKEN_CACHE"
        return 1
    fi

    # Check if refresh token is expired
    local expires_at
    expires_at=$(jq -r '.expires_at // 0' "$TOKEN_CACHE" 2>/dev/null || echo "0")
    if [[ "$expires_at" -gt 0 ]] && (( $(date +%s) > expires_at )); then
        echo "Warning: cached token expired, re-authenticating" >&2
        rm -f "$TOKEN_CACHE"
        return 1
    fi

    return 0
}

# Save token to cache with secure permissions
save_token() {
    local access_token="$1"
    local refresh_token="$2"
    local expires_in="$3"

    mkdir -p "$CACHE_DIR"
    local expires_at=$(( $(date +%s) + expires_in ))

    local tmpfile="$TOKEN_CACHE.tmp"
    jq -n \
        --arg access "$access_token" \
        --arg refresh "$refresh_token" \
        --arg expires "$expires_at" \
        '{access_token: $access, refresh_token: $refresh, expires_at: ($expires | tonumber)}' \
        > "$tmpfile"
    chmod 600 "$tmpfile"
    mv "$tmpfile" "$TOKEN_CACHE"
}

# Get access token, using cached token if available and refreshing if needed
get_access_token() {
    # Try to load from cache
    if load_cached_token; then
        local access_token
        local refresh_token
        access_token=$(jq -r '.access_token' "$TOKEN_CACHE")
        refresh_token=$(jq -r '.refresh_token' "$TOKEN_CACHE")

        # Try to refresh the token
        if refresh_token_internal "$refresh_token"; then
            return 0
        fi
    fi

    # Cache doesn't exist or refresh failed; perform device flow
    device_authorization_grant
}

# Refresh access token using refresh token
refresh_token_internal() {
    local refresh_token="$1"
    local token_endpoint="$OIDC_ISSUER/protocol/openid-connect/token"

    local response
    response=$(curl -s -X POST "$token_endpoint" \
        -d "grant_type=refresh_token" \
        -d "refresh_token=$refresh_token" \
        -d "client_id=$OIDC_CLIENT_ID" \
        -H "Content-Type: application/x-www-form-urlencoded")

    local error
    error=$(echo "$response" | jq -r '.error // ""')

    if [[ -n "$error" ]]; then
        case "$error" in
            invalid_grant)
                echo "Error: refresh token invalid or expired — user must re-authenticate" >&2
                rm -f "$TOKEN_CACHE"
                return 1
                ;;
            *)
                echo "Error: token refresh failed ($error): $(echo "$response" | jq -r '.error_description // ""')" >&2
                return 1
                ;;
        esac
    fi

    local access_token
    local new_refresh_token
    local expires_in
    access_token=$(echo "$response" | jq -r '.access_token // ""')
    new_refresh_token=$(echo "$response" | jq -r '.refresh_token // ""')
    expires_in=$(echo "$response" | jq -r '.expires_in // 3600')

    if [[ -z "$access_token" ]]; then
        echo "Error: no access token in refresh response" >&2
        return 1
    fi

    # Use new refresh token if provided, otherwise keep old one
    if [[ -z "$new_refresh_token" ]]; then
        new_refresh_token=$(jq -r '.refresh_token' "$TOKEN_CACHE")
    fi

    save_token "$access_token" "$new_refresh_token" "$expires_in"
    return 0
}

# Perform the device authorization grant flow
device_authorization_grant() {
    local device_endpoint="$OIDC_ISSUER/protocol/openid-connect/auth/device"
    local token_endpoint="$OIDC_ISSUER/protocol/openid-connect/token"

    # Step 1: Request device code
    echo "Initiating device authorization flow..." >&2
    local device_response
    device_response=$(curl -s -X POST "$device_endpoint" \
        -d "client_id=$OIDC_CLIENT_ID" \
        -d "scope=openid profile email" \
        -H "Content-Type: application/x-www-form-urlencoded")

    local device_code
    local user_code
    local verification_uri
    local expires_in
    local interval
    device_code=$(echo "$device_response" | jq -r '.device_code // ""')
    user_code=$(echo "$device_response" | jq -r '.user_code // ""')
    verification_uri=$(echo "$device_response" | jq -r '.verification_uri_complete // ""')
    expires_in=$(echo "$device_response" | jq -r '.expires_in // 600')
    interval=$(echo "$device_response" | jq -r '.interval // 5')

    if [[ -z "$device_code" ]]; then
        echo "Error: failed to get device code" >&2
        echo "Response: $device_response" >&2
        return 1
    fi

    # Step 2: Display user code and verification URI
    echo ""
    echo "Please authorize this application by visiting:"
    echo ""
    echo "  $verification_uri"
    echo ""
    echo "Enter code: $user_code"
    echo ""
    read -p "Press Enter when authorized (or Ctrl+C to cancel)..."
    echo ""

    # Step 3: Poll for authorization with backoff
    local poll_interval=$interval
    local slow_down_interval=$poll_interval
    local expiry_time=$(( $(date +%s) + expires_in ))

    while true; do
        if (( $(date +%s) > expiry_time )); then
            echo "Error: device code expired, user took too long to authorize" >&2
            return 1
        fi

        # Poll token endpoint
        local poll_response
        poll_response=$(curl -s -X POST "$token_endpoint" \
            -d "grant_type=urn:ietf:params:oauth:grant-type:device_code" \
            -d "device_code=$device_code" \
            -d "client_id=$OIDC_CLIENT_ID" \
            -H "Content-Type: application/x-www-form-urlencoded")

        local error
        error=$(echo "$poll_response" | jq -r '.error // ""')

        if [[ -z "$error" ]]; then
            # Success: we got a token
            local access_token
            local refresh_token
            local token_expires_in
            access_token=$(echo "$poll_response" | jq -r '.access_token // ""')
            refresh_token=$(echo "$poll_response" | jq -r '.refresh_token // ""')
            token_expires_in=$(echo "$poll_response" | jq -r '.expires_in // 3600')

            if [[ -z "$access_token" ]]; then
                echo "Error: no access token in polling response" >&2
                return 1
            fi

            save_token "$access_token" "$refresh_token" "$token_expires_in"
            echo "Authorization successful!" >&2
            return 0

        elif [[ "$error" == "authorization_pending" ]]; then
            # User hasn't authorized yet; continue polling
            sleep "$slow_down_interval"

        elif [[ "$error" == "slow_down" ]]; then
            # Server is rate-limiting; increase polling interval
            slow_down_interval=$(( slow_down_interval + poll_interval ))
            sleep "$slow_down_interval"

        elif [[ "$error" == "expired_token" ]]; then
            echo "Error: device code expired, please try again" >&2
            return 1

        elif [[ "$error" == "access_denied" ]]; then
            echo "Error: user denied authorization" >&2
            return 1

        else
            local error_desc
            error_desc=$(echo "$poll_response" | jq -r '.error_description // ""')
            echo "Error: device flow error ($error): $error_desc" >&2
            return 1
        fi
    done
}

# Get or acquire a bearer token for authentication
get_bearer_token() {
    if ! get_access_token; then
        return 1
    fi
    jq -r '.access_token' "$TOKEN_CACHE"
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
require_cmd curl

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

# ── obtain and validate descriptor set ────────────────────────────────────────

if [[ -n "$DESCRIPTOR_PATH" ]] && [[ -f "$DESCRIPTOR_PATH" ]]; then
    echo "Using descriptor set: $DESCRIPTOR_PATH"
else
    echo "Error: descriptor set not found" >&2
    echo "Set LEAFLAB_API_DESCRIPTOR_PATH to the path of the LeafLab API descriptor set" >&2
    echo "Or set LEAFLAB_API_DESCRIPTOR_URL to download it" >&2
    exit 1
fi

# ── authenticate ──────────────────────────────────────────────────────────────

BEARER_TOKEN=$(get_bearer_token) || exit 1

# ── build request and push ────────────────────────────────────────────────────

REQUEST="$(jq -n \
    --arg device_id "$DEVICE_ID" \
    --slurpfile s "$SCENARIO_FILE" \
    '{deviceId: $device_id, sensors: $s[0].sensors}')"

RESPONSE="$(grpcurl -plaintext \
    -H "authorization: Bearer $BEARER_TOKEN" \
    -protoset "$DESCRIPTOR_PATH" \
    -d "$REQUEST" \
    "$HOST" \
    leaflab.api.v1.LeafLabAPI/PushDeviceConfig)" || {
    exit_code=$?
    if echo "$RESPONSE" | grep -q "Unauthenticated"; then
        echo "Error: authentication failed — credential not approved or expired" >&2
    elif echo "$RESPONSE" | grep -q "PermissionDenied"; then
        echo "Error: authorization failed — insufficient permissions" >&2
    fi
    exit "$exit_code"
}

VERSION="$(echo "$RESPONSE" | jq -r '.version // "unknown"')"
echo "Pushed — assigned version $VERSION"
echo ""
echo "Watch the device ACK:"
echo "  mosquitto_sub -h localhost -p 1883 -u rabbit -P password \\"
echo "    -t 'leaflab/$DEVICE_ID/config/ack' -v"
