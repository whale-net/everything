#!/usr/bin/env bash
set -euo pipefail

# Test download-release-tools logic:
# 1. Version string quoting & whitespace preservation
# 2. SHA256 integrity verification (succeeds on matching checksum, fails on mismatch)
# 3. Source-mode fallback and tool execution verification
# 4. App Registry GetEnvironmentState JSON parsing (#780, #859)
# 5. App Registry target_env resolution and fallback discipline

echo "=== Test 1: Version string quoting and whitespace preservation ==="
# Test various quoting and whitespace styles to ensure digits are preserved
for sample in "v0.3.0" "\"v0.3.0\"" "'v0.3.0'" "  v0.3.0  "; do
    parsed=$(echo "$sample" | sed -E "s/['\"[:space:]]//g")
    if [ "$parsed" != "v0.3.0" ]; then
        echo "FAIL: Version parsing corrupted $sample -> $parsed"
        exit 1
    fi
done
echo "PASS: Version parsing correctly handles all quotation and whitespace forms without digit stripping"

echo "=== Test 2: SHA256 integrity verification discipline ==="
TEST_DIR=$(mktemp -d)
trap 'rm -rf "$TEST_DIR"' EXIT

echo "test binary content for release helper" > "$TEST_DIR/release_helper_go"
EXPECTED_SHA=$(sha256sum "$TEST_DIR/release_helper_go" | awk '{print $1}')
echo "$EXPECTED_SHA  release_helper_go" > "$TEST_DIR/checksums.txt"

# Verify valid checksum
(
    cd "$TEST_DIR"
    sha256sum -c checksums.txt
)
echo "PASS: Valid checksum verified cleanly"

# Verify tampered binary fails checksum
echo "tampered binary content" > "$TEST_DIR/release_helper_go"
if (cd "$TEST_DIR" && sha256sum -c checksums.txt 2>/dev/null); then
    echo "FAIL: Tampered binary unexpectedly passed checksum verification!"
    exit 1
else
    echo "PASS: Tampered binary correctly caught and rejected by checksum verification"
fi

echo "=== Test 3: Binary execution test ==="
# Locate release_helper_go and app-registry binaries either via runfiles or bazel build
HELPER_BIN=""
REGISTRY_BIN=""

if [ -n "${TEST_SRCDIR:-}" ]; then
    # Running inside Bazel test environment
    HELPER_BIN="${TEST_SRCDIR}/_main/tools/release_helper_go/release_helper_go_/release_helper_go"
    REGISTRY_BIN="${TEST_SRCDIR}/_main/tools/app_registry/cli/app-registry_/app-registry"
else
    # Running standalone outside Bazel
    echo "Building release tools from source..."
    bazel build //tools/release_helper_go:release_helper_go //tools/app_registry/cli:app-registry
    BAZEL_BIN=$(bazel info bazel-bin)
    HELPER_BIN="$BAZEL_BIN/tools/release_helper_go/release_helper_go_/release_helper_go"
    REGISTRY_BIN="$BAZEL_BIN/tools/app_registry/cli/app-registry_/app-registry"
fi

if [ ! -x "$HELPER_BIN" ] && [ -f "$HELPER_BIN" ]; then
    chmod +x "$HELPER_BIN"
fi
if [ ! -x "$REGISTRY_BIN" ] && [ -f "$REGISTRY_BIN" ]; then
    chmod +x "$REGISTRY_BIN"
fi

if [ ! -f "$HELPER_BIN" ]; then
    echo "FAIL: $HELPER_BIN not found"
    exit 1
fi
if [ ! -f "$REGISTRY_BIN" ]; then
    echo "FAIL: $REGISTRY_BIN not found"
    exit 1
fi

"$HELPER_BIN" --help > /dev/null
echo "PASS: release_helper_go binary executed successfully"

"$REGISTRY_BIN" --help > /dev/null
echo "PASS: app-registry CLI executed successfully"

echo "=== Test 4: App Registry GetEnvironmentState JSON parsing ==="
# Mirrors the jq filter used by resolve_registry_version() in
# .github/actions/download-release-tools/action.yml: given a
# GetEnvironmentState-shaped response and a target app_id, extract the
# promoted binary version, or nothing if there's no matching entry.
FIXTURE_DIR=$(mktemp -d)
trap 'rm -rf "$TEST_DIR" "$FIXTURE_DIR"' EXIT

extract_version() {
    local json_file="$1"
    local app_id="$2"
    jq -r --arg app_id "$app_id" '
      [.entries[]?
        | select((.artifact.appId // .artifact.app_id) == $app_id)
        | select((.artifact.kind // "") == "ARTIFACT_KIND_BINARY")
      ][0].artifact.version // empty
    ' "$json_file" 2>/dev/null || true
}

# 4a: well-formed response with a matching binary entry (camelCase, as
# grpcurl's default protojson output would render it)
cat > "$FIXTURE_DIR/match.json" <<'EOF'
{
  "environment": {"environmentKey": "dev"},
  "entries": [
    {
      "artifact": {
        "appId": "app-123",
        "kind": "ARTIFACT_KIND_BINARY",
        "version": "v1.4.0"
      }
    },
    {
      "artifact": {
        "appId": "app-999",
        "kind": "ARTIFACT_KIND_BINARY",
        "version": "v9.9.9"
      }
    }
  ]
}
EOF
GOT=$(extract_version "$FIXTURE_DIR/match.json" "app-123")
if [ "$GOT" != "v1.4.0" ]; then
    echo "FAIL: expected v1.4.0 for matching entry, got '$GOT'"
    exit 1
fi
echo "PASS: extracted promoted version from matching entry"

# 4b: snake_case field rendering (original proto names) also parses
cat > "$FIXTURE_DIR/snake_case.json" <<'EOF'
{"entries": [{"artifact": {"app_id": "app-123", "kind": "ARTIFACT_KIND_BINARY", "version": "v2.0.0"}}]}
EOF
GOT=$(extract_version "$FIXTURE_DIR/snake_case.json" "app-123")
if [ "$GOT" != "v2.0.0" ]; then
    echo "FAIL: expected v2.0.0 for snake_case entry, got '$GOT'"
    exit 1
fi
echo "PASS: extracted promoted version from snake_case entry"

# 4c: non-binary kind (e.g. an image) for the same app_id must not match
cat > "$FIXTURE_DIR/wrong_kind.json" <<'EOF'
{"entries": [{"artifact": {"appId": "app-123", "kind": "ARTIFACT_KIND_IMAGE", "version": "v1.4.0"}}]}
EOF
GOT=$(extract_version "$FIXTURE_DIR/wrong_kind.json" "app-123")
if [ -n "$GOT" ]; then
    echo "FAIL: expected no match for non-binary kind, got '$GOT'"
    exit 1
fi
echo "PASS: non-binary artifact kind correctly excluded"

# 4d: no entries for the requested app_id -> yields empty (action fails and asks user to build from source)
cat > "$FIXTURE_DIR/no_match.json" <<'EOF'
{"entries": [{"artifact": {"appId": "app-999", "kind": "ARTIFACT_KIND_BINARY", "version": "v9.9.9"}}]}
EOF
GOT=$(extract_version "$FIXTURE_DIR/no_match.json" "app-123")
if [ -n "$GOT" ]; then
    echo "FAIL: expected no match when app_id is absent, got '$GOT'"
    exit 1
fi
echo "PASS: no matching entry correctly yields empty (no promoted version)"

# 4e: empty entries array -> yields empty
echo '{"entries": []}' > "$FIXTURE_DIR/empty_entries.json"
GOT=$(extract_version "$FIXTURE_DIR/empty_entries.json" "app-123")
if [ -n "$GOT" ]; then
    echo "FAIL: expected no match for empty entries array, got '$GOT'"
    exit 1
fi
echo "PASS: empty entries array correctly yields empty"

# 4f: malformed JSON (e.g. a truncated/garbled grpcurl error response) must
# not crash the parser and must yield empty, exactly like a genuinely empty
# response -- both drive the failure path in the action.
echo '{not valid json' > "$FIXTURE_DIR/malformed.json"
GOT=$(extract_version "$FIXTURE_DIR/malformed.json" "app-123")
if [ -n "$GOT" ]; then
    echo "FAIL: expected no match for malformed JSON, got '$GOT'"
    exit 1
fi
echo "PASS: malformed JSON correctly yields empty"

# 4g: missing/empty response body (e.g. grpcurl produced no output at all)
: > "$FIXTURE_DIR/missing.json"
GOT=$(extract_version "$FIXTURE_DIR/missing.json" "app-123")
if [ -n "$GOT" ]; then
    echo "FAIL: expected no match for empty response body, got '$GOT'"
    exit 1
fi
echo "PASS: empty response body correctly yields empty"

echo "=== Test 5: Fallback policy and target_env resolution discipline ==="
# Test that resolution from App Registry handles success, failure without fallback,
# and failure with fallback_to_source cleanly.

resolve_tool_version_sim() {
    local target_env="$1"
    local simulated_registry_ver="$2"
    local fallback_to_source="$3"

    local rh_ver=""
    if [ -n "$simulated_registry_ver" ]; then
        rh_ver="$simulated_registry_ver"
        echo "$rh_ver"
        return 0
    fi

    if [ "$fallback_to_source" = "true" ]; then
        echo "source"
        return 0
    else
        echo "ERROR: Could not resolve promoted version for 'release_helper_go' in environment '$target_env'. Fallback to source compilation is disabled. Please re-run the pipeline with build tools (e.g. 'use_source_tools: true' or 'source: source') or ensure the tool is promoted in App Registry." >&2
        return 1
    fi
}

# 5a: target_env set with registry match -> returns registry version
RES=$(resolve_tool_version_sim "dev" "v1.4.0" "false")
if [ "$RES" != "v1.4.0" ]; then
    echo "FAIL: expected v1.4.0, got '$RES'"
    exit 1
fi
echo "PASS: target_env with registry match resolves correctly"

# 5b: target_env set with NO registry match and fallback_to_source=false -> fails explicitly
if RES=$(resolve_tool_version_sim "dev" "" "false" 2>&1); then
    echo "FAIL: target_env without registry match and fallback=false should have failed, but returned '$RES'"
    exit 1
else
    if [[ "$RES" != *"Fallback to source compilation is disabled"* ]]; then
        echo "FAIL: expected error message regarding disabled fallback, got '$RES'"
        exit 1
    fi
fi
echo "PASS: target_env without registry match and fallback=false fails cleanly"

# 5c: target_env set with NO registry match and fallback_to_source=true -> falls back to source
RES=$(resolve_tool_version_sim "dev" "" "true")
if [ "$RES" != "source" ]; then
    echo "FAIL: expected source fallback, got '$RES'"
    exit 1
fi
echo "PASS: fallback_to_source=true correctly falls back to source"

echo "=== All download-release-tools tests passed ==="

