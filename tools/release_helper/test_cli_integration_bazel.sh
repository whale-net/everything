#!/bin/bash
# Integration tests for the release helper CLI that require a live Bazel environment.
# Tests list-apps, plan, and changes commands which internally run Bazel queries.
#
# These tests are tagged "manual" and must be run explicitly:
#   bazel test //tools/release_helper:test_cli_integration_bazel
#
# The script invokes `bazel run //tools:release` so that Bazel can resolve the
# workspace and its targets.

set -euo pipefail

# Resolve the workspace root: prefer BUILD_WORKSPACE_DIRECTORY (set by bazel run),
# then walk up from the script location looking for MODULE.bazel.
find_workspace_root() {
    if [ -n "${BUILD_WORKSPACE_DIRECTORY:-}" ]; then
        echo "$BUILD_WORKSPACE_DIRECTORY"
        return
    fi
    local dir
    dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
    while [ "$dir" != "/" ]; do
        if [ -f "$dir/MODULE.bazel" ] || [ -f "$dir/WORKSPACE" ]; then
            echo "$dir"
            return
        fi
        dir="$(dirname "$dir")"
    done
    echo "ERROR: Cannot find workspace root (no MODULE.bazel found)" >&2
    exit 1
}

WORKSPACE_ROOT=$(find_workspace_root)
BAZEL_BIN="${BAZEL_BIN:-bazel}"

# Optional JDK override — matches the repo-wide convention.
JAVA_BASE="${SERVER_JAVABASE:-/usr/lib/jvm/temurin-21-jdk-amd64}"
SERVER_JAVABASE_ARGS=()
if [ -d "$JAVA_BASE" ]; then
    SERVER_JAVABASE_ARGS=("--server_javabase=$JAVA_BASE")
fi

# Run `bazel run //tools:release -- <args>` from the workspace root.
# On failure, print the combined output so test failures are diagnosable.
release() {
    local output
    if output=$(
        cd "$WORKSPACE_ROOT" && \
        "$BAZEL_BIN" "${SERVER_JAVABASE_ARGS[@]}" run //tools:release --noshow_progress -- "$@" 2>&1
    ); then
        echo "$output"
    else
        local rc=$?
        echo "$output" >&2
        return $rc
    fi
}

# ─── helpers ──────────────────────────────────────────────────────────────────

PASS=0
FAIL=0

pass() { echo "PASS: $1"; PASS=$((PASS + 1)); }
fail() { echo "FAIL: $1"; FAIL=$((FAIL + 1)); }

# assert_exit <expected_code> <description> <release_subcommand> [args...]
assert_exit() {
    local expected=$1 description=$2
    shift 2
    local actual=0
    release "$@" >/dev/null 2>&1 || actual=$?
    if [ "$actual" -eq "$expected" ]; then
        pass "$description (exit $actual)"
    else
        fail "$description (expected exit $expected, got $actual)"
    fi
}

# capture <release_subcommand> [args...]
# Run the release tool and return combined stdout+stderr; always succeeds in shell.
capture() {
    release "$@" 2>&1 || true
}

# ─── tests ────────────────────────────────────────────────────────────────────

echo "=== Release Helper Bazel Integration Tests ==="
echo ""
echo "Workspace: $WORKSPACE_ROOT"
echo ""

# ── list-apps ─────────────────────────────────────────────────────────────────

assert_exit 0 "list-apps exits 0" \
    list-apps --format json

output=$(capture list-apps --format json)
if echo "$output" | python3 -c "import sys, json; data=json.load(sys.stdin); assert isinstance(data, list)" 2>/dev/null; then
    pass "list-apps --format json outputs valid JSON array"
else
    fail "list-apps --format json outputs valid JSON array (got: $output)"
fi

if echo "$output" | python3 -c "import sys, json; data=json.load(sys.stdin); assert len(data) > 0, 'empty'" 2>/dev/null; then
    pass "list-apps returns at least one app"
else
    fail "list-apps returns at least one app (got: $output)"
fi

if echo "$output" | python3 -c "
import sys, json
data = json.load(sys.stdin)
first = data[0]
assert 'name' in first, 'missing name'
assert 'domain' in first, 'missing domain'
" 2>/dev/null; then
    pass "list-apps entries have 'name' and 'domain' fields"
else
    fail "list-apps entries have 'name' and 'domain' fields (got: $output)"
fi

# ── list (alias) ──────────────────────────────────────────────────────────────

assert_exit 0 "list (alias) exits 0" \
    list --format json

output_alias=$(capture list --format json)
if [ "$output" = "$output_alias" ]; then
    pass "list is identical to list-apps"
else
    fail "list is identical to list-apps (outputs differ)"
fi

# ── plan: workflow_dispatch ───────────────────────────────────────────────────

plan_output=$(capture plan --event-type workflow_dispatch --apps all --version v1.0.0 --format json)

if echo "$plan_output" | python3 -c "import sys, json; json.load(sys.stdin)" 2>/dev/null; then
    pass "plan --format json outputs valid JSON"
else
    fail "plan --format json outputs valid JSON (got: $plan_output)"
fi

if echo "$plan_output" | python3 -c "
import sys, json
data = json.load(sys.stdin)
assert 'matrix' in data, 'missing matrix key'
assert 'apps' in data, 'missing apps key'
assert 'include' in data['matrix'], 'missing matrix.include'
" 2>/dev/null; then
    pass "plan JSON has required keys (matrix, apps)"
else
    fail "plan JSON has required keys (matrix, apps) (got: $plan_output)"
fi

assert_exit 0 "plan --format github exits 0" \
    plan --event-type workflow_dispatch --apps all --version v1.0.0 --format github

github_output=$(capture plan --event-type workflow_dispatch --apps all --version v1.0.0 --format github)
if echo "$github_output" | grep -q "^matrix="; then
    pass "plan --format github outputs 'matrix=...'"
else
    fail "plan --format github outputs 'matrix=...' (got: $github_output)"
fi

# ── plan: pull_request (change detection, no apps required) ──────────────────

assert_exit 0 "plan accepts pull_request event without explicit apps" \
    plan --event-type pull_request --format json

# ── plan: specific app ───────────────────────────────────────────────────────

# Pick the first known app from list-apps and confirm plan handles it.
first_app=$(capture list-apps --format json | python3 -c "
import sys, json
data = json.load(sys.stdin)
if data:
    print(data[0]['domain'] + '-' + data[0]['name'])
")

if [ -n "$first_app" ]; then
    assert_exit 0 "plan with specific app '$first_app' exits 0" \
        plan --event-type workflow_dispatch --apps "$first_app" --version v1.0.0 --format json

    specific_output=$(capture plan \
        --event-type workflow_dispatch \
        --apps "$first_app" \
        --version v1.0.0 \
        --format json)
    if echo "$specific_output" | python3 -c "
import sys, json
data = json.load(sys.stdin)
apps = data.get('apps', [])
assert len(apps) >= 1, 'expected at least one app'
" 2>/dev/null; then
        pass "plan for '$first_app' includes at least one app in result"
    else
        fail "plan for '$first_app' includes at least one app in result (got: $specific_output)"
    fi
else
    echo "SKIP: no apps found in list-apps, skipping specific-app plan test"
fi

# ─── summary ──────────────────────────────────────────────────────────────────

echo ""
echo "=== Results: $PASS passed, $FAIL failed ==="

if [ "$FAIL" -gt 0 ]; then
    exit 1
fi
