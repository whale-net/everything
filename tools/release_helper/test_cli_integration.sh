#!/bin/bash
# Integration tests for the release helper CLI.
# Tests CLI argument validation and output formatting without requiring Bazel.
# These tests invoke the release_helper binary directly and check exit codes and output.

set -euo pipefail

# Locate the release_helper binary from Bazel runfiles.
RUNFILES_DIR="${RUNFILES_DIR:-$0.runfiles}"
RELEASE_HELPER="${RUNFILES_DIR}/_main/tools/release_helper/release_helper"
if [ ! -f "$RELEASE_HELPER" ]; then
    echo "ERROR: Cannot find release_helper binary: $RELEASE_HELPER" >&2
    exit 1
fi

# ─── helpers ──────────────────────────────────────────────────────────────────

PASS=0
FAIL=0

pass() { echo "PASS: $1"; PASS=$((PASS + 1)); }
fail() { echo "FAIL: $1"; FAIL=$((FAIL + 1)); }

# assert_exit <expected_code> <description> <command> [args...]
# Runs <command> [args...], checks its exit code equals <expected_code>.
assert_exit() {
    local expected=$1 description=$2
    shift 2
    local actual=0
    "$@" >/dev/null 2>&1 || actual=$?
    if [ "$actual" -eq "$expected" ]; then
        pass "$description (exit $actual)"
    else
        fail "$description (expected exit $expected, got $actual)"
    fi
}

# assert_output_contains <pattern> <description> <command> [args...]
# Runs <command> [args...], checks that combined stdout+stderr contains <pattern>.
assert_output_contains() {
    local pattern=$1 description=$2
    shift 2
    local output
    output=$("$@" 2>&1) || true
    if echo "$output" | grep -qF -- "$pattern"; then
        pass "$description"
    else
        fail "$description (expected '$pattern' in output, got: $output)"
    fi
}

# assert_exit_with_output <expected_code> <pattern> <description> <command> [args...]
# Runs <command> [args...], checks exit code AND that output contains <pattern>.
assert_exit_with_output() {
    local expected=$1 pattern=$2 description=$3
    shift 3
    local actual=0
    local output
    output=$("$@" 2>&1) || actual=$?
    local ok=1
    if [ "$actual" -ne "$expected" ]; then
        fail "$description (expected exit $expected, got $actual)"
        ok=0
    fi
    if ! echo "$output" | grep -qF -- "$pattern"; then
        fail "$description (expected '$pattern' in output, got: $output)"
        ok=0
    fi
    [ "$ok" -eq 1 ] && pass "$description"
}

# ─── tests ────────────────────────────────────────────────────────────────────

echo "=== Release Helper CLI Integration Tests ==="
echo ""

# ── help ──────────────────────────────────────────────────────────────────────

assert_exit 0 "help flag exits 0" \
    "$RELEASE_HELPER" --help

assert_output_contains "Release helper" "help shows tool description" \
    "$RELEASE_HELPER" --help

assert_output_contains "list-apps" "help lists list-apps command" \
    "$RELEASE_HELPER" --help

assert_output_contains "plan" "help lists plan command" \
    "$RELEASE_HELPER" --help

assert_output_contains "summary" "help lists summary command" \
    "$RELEASE_HELPER" --help

# ── plan: event-type validation ───────────────────────────────────────────────

assert_exit 1 "plan rejects invalid event-type" \
    "$RELEASE_HELPER" plan --event-type invalid-event

assert_exit_with_output 1 "event-type must be one of" \
    "plan prints error for invalid event-type" \
    "$RELEASE_HELPER" plan --event-type invalid-event

# ── plan: format validation ───────────────────────────────────────────────────
# Note: format validation fires before any Bazel call, so no Bazel is needed.

assert_exit 1 "plan rejects invalid --format" \
    "$RELEASE_HELPER" plan --event-type workflow_dispatch --apps all --version v1.0.0 --format invalid

assert_exit_with_output 1 "format must be one of: json, github" \
    "plan prints error for invalid format" \
    "$RELEASE_HELPER" plan --event-type workflow_dispatch --apps all --version v1.0.0 --format invalid

# ── plan: mutually exclusive version options ──────────────────────────────────

assert_exit 1 "--version and --increment-minor are mutually exclusive" \
    "$RELEASE_HELPER" plan --event-type workflow_dispatch --version v1.0.0 --increment-minor

assert_exit 1 "--version and --increment-patch are mutually exclusive" \
    "$RELEASE_HELPER" plan --event-type workflow_dispatch --version v1.0.0 --increment-patch

assert_exit 1 "--increment-minor and --increment-patch are mutually exclusive" \
    "$RELEASE_HELPER" plan --event-type workflow_dispatch --increment-minor --increment-patch

assert_exit_with_output 1 "mutually exclusive" \
    "plan prints mutually exclusive error for version + increment-minor" \
    "$RELEASE_HELPER" plan --event-type workflow_dispatch --version v1.0.0 --increment-minor

# ── summary: event-type validation ───────────────────────────────────────────

assert_exit 1 "summary rejects invalid event-type" \
    "$RELEASE_HELPER" summary \
        --matrix '{}' --version v1.0.0 --event-type invalid-event

assert_exit_with_output 1 "event-type must be one of: workflow_dispatch, tag_push" \
    "summary prints error for invalid event-type" \
    "$RELEASE_HELPER" summary \
        --matrix '{}' --version v1.0.0 --event-type invalid-event

# ── summary: empty matrix ─────────────────────────────────────────────────────

assert_exit 0 "summary exits 0 for empty matrix" \
    "$RELEASE_HELPER" summary \
        --matrix '{}' --version v1.0.0 --event-type workflow_dispatch

assert_output_contains "No apps detected for release" \
    "summary reports no apps for empty matrix" \
    "$RELEASE_HELPER" summary \
        --matrix '{}' --version v1.0.0 --event-type workflow_dispatch

assert_exit 0 "summary exits 0 for empty matrix with tag_push" \
    "$RELEASE_HELPER" summary \
        --matrix '{}' --version v1.0.0 --event-type tag_push

# ── summary: non-empty matrix with dry-run (no Bazel call) ───────────────────

MATRIX='{"include":[{"app":"hello_python","version":"v1.0.0"}]}'

assert_exit 0 "summary exits 0 for non-empty matrix with --dry-run" \
    "$RELEASE_HELPER" summary \
        --matrix "$MATRIX" --version v1.0.0 --event-type workflow_dispatch --dry-run

assert_output_contains "Release completed" \
    "summary shows Release completed for non-empty matrix" \
    "$RELEASE_HELPER" summary \
        --matrix "$MATRIX" --version v1.0.0 --event-type workflow_dispatch --dry-run

assert_output_contains "hello_python" \
    "summary includes app name from matrix" \
    "$RELEASE_HELPER" summary \
        --matrix "$MATRIX" --version v1.0.0 --event-type workflow_dispatch --dry-run

assert_output_contains "Dry run mode - no images were published" \
    "summary indicates dry run mode" \
    "$RELEASE_HELPER" summary \
        --matrix "$MATRIX" --version v1.0.0 --event-type workflow_dispatch --dry-run

# ── plan-helm-release: format validation ──────────────────────────────────────

assert_exit 1 "plan-helm-release rejects invalid --format" \
    "$RELEASE_HELPER" plan-helm-release --format invalid

assert_exit_with_output 1 "format must be one of: json, github" \
    "plan-helm-release prints error for invalid format" \
    "$RELEASE_HELPER" plan-helm-release --format invalid

# ── build-helm-chart: bump validation ────────────────────────────────────────

assert_exit 1 "build-helm-chart rejects invalid --bump" \
    "$RELEASE_HELPER" build-helm-chart mychart --bump invalid

assert_output_contains "--bump must be one of: major, minor, patch" \
    "build-helm-chart prints bump validation error" \
    "$RELEASE_HELPER" build-helm-chart mychart --bump invalid

# ─── summary ──────────────────────────────────────────────────────────────────

echo ""
echo "=== Results: $PASS passed, $FAIL failed ==="

if [ "$FAIL" -gt 0 ]; then
    exit 1
fi
