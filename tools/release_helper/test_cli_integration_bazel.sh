#!/bin/bash
# Integration tests for the release helper CLI that require a live Bazel environment.
# These tests cover commands that internally run Bazel queries (list-apps, plan, etc.)
# and commands that interact with the repository (changes, release-notes).
#
# Tagged "manual" — must be run explicitly:
#   bazel test //tools/release_helper:test_cli_integration_bazel
#
# Coverage (mirrors CI/CD workflow usage):
#   list-apps, list (alias), plan, plan-openapi-builds, changes,
#   release-notes, list-helm-charts, plan-helm-release

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
# Used by: release.yml (create-combined step, release-notes-all step)

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
# Used in ci.yml (event-type pull_request/push) and release.yml (workflow_dispatch).

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
    pass "plan JSON has required keys (matrix, apps, matrix.include)"
else
    fail "plan JSON has required keys (matrix, apps, matrix.include) (got: $plan_output)"
fi

if echo "$plan_output" | python3 -c "
import sys, json
data = json.load(sys.stdin)
# Each include item must have app, domain, and version
items = data['matrix']['include']
assert len(items) > 0, 'no items'
first = items[0]
assert 'app' in first, 'missing app'
assert 'domain' in first, 'missing domain'
assert 'version' in first, 'missing version'
" 2>/dev/null; then
    pass "plan matrix items have required fields (app, domain, version)"
else
    fail "plan matrix items have required fields (got: $plan_output)"
fi

assert_exit 0 "plan --format github exits 0" \
    plan --event-type workflow_dispatch --apps all --version v1.0.0 --format github

github_output=$(capture plan --event-type workflow_dispatch --apps all --version v1.0.0 --format github)
if echo "$github_output" | grep -q "^matrix="; then
    pass "plan --format github outputs 'matrix=...' line"
else
    fail "plan --format github outputs 'matrix=...' line (got: $github_output)"
fi
if echo "$github_output" | grep -q "^apps="; then
    pass "plan --format github outputs 'apps=...' line"
else
    fail "plan --format github outputs 'apps=...' line (got: $github_output)"
fi

# ── plan: pull_request event (change detection) ───────────────────────────────
# Used in ci.yml to detect which apps changed on a PR/push.

assert_exit 0 "plan accepts pull_request event-type" \
    plan --event-type pull_request --format json

assert_exit 0 "plan accepts push event-type" \
    plan --event-type push --format json

assert_exit 0 "plan accepts fallback event-type" \
    plan --event-type fallback --format json

# ── plan: --include-demo flag ─────────────────────────────────────────────────
# Release.yml supports --include-demo to include demo domain apps.

assert_exit 0 "plan accepts --include-demo flag" \
    plan --event-type workflow_dispatch --apps all --version v1.0.0 --include-demo --format json

# ── plan: specific app ───────────────────────────────────────────────────────

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

# ── plan-openapi-builds ───────────────────────────────────────────────────────
# Used in release.yml to find apps with OpenAPI spec targets.
# Only apps with fastapi_app/openapi_spec_target configured will appear.

assert_exit 0 "plan-openapi-builds exits 0 with valid apps" \
    plan-openapi-builds --apps all --format json

openapi_output=$(capture plan-openapi-builds --apps all --format json)
if echo "$openapi_output" | python3 -c "import sys, json; data=json.load(sys.stdin); assert 'apps_with_specs' in data and 'count' in data" 2>/dev/null; then
    pass "plan-openapi-builds JSON output has expected fields (apps_with_specs, count)"
else
    fail "plan-openapi-builds JSON output has expected fields (got: $openapi_output)"
fi

assert_exit 0 "plan-openapi-builds --format github exits 0" \
    plan-openapi-builds --apps all --format github

github_openapi=$(capture plan-openapi-builds --apps all --format github)
if echo "$github_openapi" | grep -q "^matrix="; then
    pass "plan-openapi-builds --format github outputs 'matrix=...' line"
else
    fail "plan-openapi-builds --format github outputs 'matrix=...' line (got: $github_openapi)"
fi

# ── changes ───────────────────────────────────────────────────────────────────
# Used in ci.yml via plan --base-commit to detect changed apps.

assert_exit 0 "changes exits 0 with no base-commit" \
    changes

# ── list-helm-charts ──────────────────────────────────────────────────────────
# Used implicitly by plan-helm-release and build-helm-chart.

assert_exit 0 "list-helm-charts exits 0" \
    list-helm-charts

helm_output=$(capture list-helm-charts)
if echo "$helm_output" | grep -qE "\(domain:"; then
    pass "list-helm-charts output includes domain information"
else
    fail "list-helm-charts output includes domain information (got: $helm_output)"
fi

# ── plan-helm-release ────────────────────────────────────────────────────────
# Used in release.yml to build helm chart release matrix.

assert_exit 0 "plan-helm-release exits 0 (all charts, json)" \
    plan-helm-release --format json

helm_plan_output=$(capture plan-helm-release --format json)
if echo "$helm_plan_output" | python3 -c "import sys, json; data=json.load(sys.stdin); assert 'matrix' in data and 'charts' in data" 2>/dev/null; then
    pass "plan-helm-release JSON has required keys (matrix, charts)"
else
    fail "plan-helm-release JSON has required keys (got: $helm_plan_output)"
fi

assert_exit 0 "plan-helm-release --format github exits 0" \
    plan-helm-release --format github

github_helm=$(capture plan-helm-release --format github)
if echo "$github_helm" | grep -q "^matrix="; then
    pass "plan-helm-release --format github outputs 'matrix=...' line"
else
    fail "plan-helm-release --format github outputs 'matrix=...' line (got: $github_helm)"
fi
if echo "$github_helm" | grep -q "^charts="; then
    pass "plan-helm-release --format github outputs 'charts=...' line"
else
    fail "plan-helm-release --format github outputs 'charts=...' line (got: $github_helm)"
fi

# ── release-notes: with current-tag=HEAD ──────────────────────────────────────
# Used in release.yml to generate per-app markdown release notes.
# With --current-tag HEAD and no previous tags, the command may produce empty/minimal notes.

if [ -n "$first_app" ]; then
    assert_exit 0 "release-notes exits 0 for valid app with --current-tag HEAD (--format markdown)" \
        release-notes "$first_app" --current-tag HEAD --format markdown

    assert_exit 0 "release-notes exits 0 for valid app with --current-tag HEAD (--format json)" \
        release-notes "$first_app" --current-tag HEAD --format json
else
    echo "SKIP: no apps found, skipping release-notes tests"
fi

# ─── summary ──────────────────────────────────────────────────────────────────

echo ""
echo "=== Results: $PASS passed, $FAIL failed ==="

if [ "$FAIL" -gt 0 ]; then
    exit 1
fi
