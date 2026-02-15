#!/usr/bin/env bash
# Integration test: compare kraken (Go) output against release_helper (Python)
# for the subcommands used in CI. Both tools must produce identical structured
# output when given the same inputs.
#
# Usage:
#   bazel test //tools/kraken:integration_test --test_output=streamed
#   # or directly:
#   ./tools/kraken/integration_test.sh

set -euo pipefail

PASS=0
FAIL=0
ERRORS=""

# --- helpers ----------------------------------------------------------------

green()  { printf '\033[32m%s\033[0m\n' "$*"; }
red()    { printf '\033[31m%s\033[0m\n' "$*"; }
yellow() { printf '\033[33m%s\033[0m\n' "$*"; }

assert_eq() {
  local label="$1" expected="$2" actual="$3"
  if [[ "$expected" == "$actual" ]]; then
    green "  PASS: $label"
    PASS=$((PASS + 1))
  else
    red "  FAIL: $label"
    echo "    expected: $(echo "$expected" | head -3)"
    echo "    actual:   $(echo "$actual" | head -3)"
    FAIL=$((FAIL + 1))
    ERRORS="${ERRORS}\n  - ${label}"
  fi
}

assert_exit_code() {
  local label="$1" expected="$2" actual="$3"
  if [[ "$expected" == "$actual" ]]; then
    green "  PASS: $label (exit $actual)"
    PASS=$((PASS + 1))
  else
    red "  FAIL: $label (expected exit $expected, got $actual)"
    FAIL=$((FAIL + 1))
    ERRORS="${ERRORS}\n  - ${label}"
  fi
}

assert_contains() {
  local label="$1" haystack="$2" needle="$3"
  if echo "$haystack" | grep -qF "$needle"; then
    green "  PASS: $label"
    PASS=$((PASS + 1))
  else
    red "  FAIL: $label — output does not contain '$needle'"
    echo "    output: $(echo "$haystack" | head -3)"
    FAIL=$((FAIL + 1))
    ERRORS="${ERRORS}\n  - ${label}"
  fi
}

# --- locate binaries --------------------------------------------------------

# When run via bazel test, BUILD_WORKSPACE_DIRECTORY is set.
# When run directly, find workspace root.
if [[ -n "${BUILD_WORKSPACE_DIRECTORY:-}" ]]; then
  WORKSPACE="$BUILD_WORKSPACE_DIRECTORY"
else
  WORKSPACE="$(cd "$(dirname "$0")/../.." && pwd)"
fi

cd "$WORKSPACE"

echo "=== Building both tools ==="
bazel build //tools:release //tools:kraken 2>/dev/null

RELEASE_HELPER="$(bazel info bazel-bin 2>/dev/null)/tools/release_helper/release_helper"
KRAKEN="$(bazel info bazel-bin 2>/dev/null)/tools/kraken/cmd/kraken/kraken_/kraken"

if [[ ! -x "$RELEASE_HELPER" ]]; then
  red "release_helper binary not found at $RELEASE_HELPER"
  exit 1
fi
if [[ ! -x "$KRAKEN" ]]; then
  red "kraken binary not found at $KRAKEN"
  exit 1
fi

echo "release_helper: $RELEASE_HELPER"
echo "kraken:         $KRAKEN"
echo ""

# ============================================================================
# TEST 1: list — app discovery produces same set of apps
# ============================================================================
echo "=== Test 1: list — app discovery ==="

PY_LIST=$("$RELEASE_HELPER" list --format json 2>/dev/null)
GO_LIST=$("$KRAKEN" list 2>/dev/null)

# Extract app names from Python JSON output
PY_NAMES=$(echo "$PY_LIST" | python3 -c "
import json, sys
apps = json.load(sys.stdin)
for a in apps:
    print(a['name'])
" | sort)

# Extract app names from Go tabular output (skip header + separator)
GO_NAMES=$(echo "$GO_LIST" | tail -n +3 | grep -v '^-' | grep -v '^Total' | awk '{print $1}' | grep -v '^$' | sort)

assert_eq "app names match" "$PY_NAMES" "$GO_NAMES"

# Count comparison
PY_COUNT=$(echo "$PY_NAMES" | wc -l | tr -d ' ')
GO_COUNT=$(echo "$GO_NAMES" | wc -l | tr -d ' ')
assert_eq "app count matches ($PY_COUNT)" "$PY_COUNT" "$GO_COUNT"

# ============================================================================
# TEST 2: plan — demo domain, specific version, JSON output
# ============================================================================
echo ""
echo "=== Test 2: plan — demo domain, v1.0.0 ==="

PY_PLAN=$("$RELEASE_HELPER" plan \
  --event-type workflow_dispatch \
  --apps demo \
  --version v1.0.0 \
  --format json \
  --include-demo 2>/dev/null)

GO_PLAN=$("$KRAKEN" plan \
  --event-type workflow_dispatch \
  --apps demo \
  --version v1.0.0 \
  --json \
  --include-demo 2>/dev/null)

# Compare matrix.include app names
PY_MATRIX_APPS=$(echo "$PY_PLAN" | python3 -c "
import json, sys
d = json.load(sys.stdin)
for item in d['matrix']['include']:
    print(item['app'])
" | sort)

GO_MATRIX_APPS=$(echo "$GO_PLAN" | python3 -c "
import json, sys
d = json.load(sys.stdin)
for item in d['matrix']['include']:
    print(item['app'])
" | sort)

assert_eq "plan matrix apps match" "$PY_MATRIX_APPS" "$GO_MATRIX_APPS"

# Compare matrix.include domains
PY_MATRIX_DOMAINS=$(echo "$PY_PLAN" | python3 -c "
import json, sys
d = json.load(sys.stdin)
for item in d['matrix']['include']:
    print(item['domain'])
" | sort)

GO_MATRIX_DOMAINS=$(echo "$GO_PLAN" | python3 -c "
import json, sys
d = json.load(sys.stdin)
for item in d['matrix']['include']:
    print(item['domain'])
" | sort)

assert_eq "plan matrix domains match" "$PY_MATRIX_DOMAINS" "$GO_MATRIX_DOMAINS"

# Compare matrix.include bazel_targets
PY_MATRIX_TARGETS=$(echo "$PY_PLAN" | python3 -c "
import json, sys
d = json.load(sys.stdin)
for item in d['matrix']['include']:
    print(item['bazel_target'])
" | sort)

GO_MATRIX_TARGETS=$(echo "$GO_PLAN" | python3 -c "
import json, sys
d = json.load(sys.stdin)
for item in d['matrix']['include']:
    print(item['bazel_target'])
" | sort)

assert_eq "plan matrix bazel_targets match" "$PY_MATRIX_TARGETS" "$GO_MATRIX_TARGETS"

# Compare matrix.include versions
PY_MATRIX_VERSIONS=$(echo "$PY_PLAN" | python3 -c "
import json, sys
d = json.load(sys.stdin)
for item in d['matrix']['include']:
    print(item['version'])
" | sort)

GO_MATRIX_VERSIONS=$(echo "$GO_PLAN" | python3 -c "
import json, sys
d = json.load(sys.stdin)
for item in d['matrix']['include']:
    print(item['version'])
" | sort)

assert_eq "plan matrix versions match" "$PY_MATRIX_VERSIONS" "$GO_MATRIX_VERSIONS"

# Compare top-level apps list
PY_APPS_LIST=$(echo "$PY_PLAN" | python3 -c "
import json, sys
d = json.load(sys.stdin)
for a in sorted(d['apps']):
    print(a)
")

GO_APPS_LIST=$(echo "$GO_PLAN" | python3 -c "
import json, sys
d = json.load(sys.stdin)
for a in sorted(d['apps']):
    print(a)
")

assert_eq "plan apps list match" "$PY_APPS_LIST" "$GO_APPS_LIST"

# Compare versions map
PY_VERSIONS_MAP=$(echo "$PY_PLAN" | python3 -c "
import json, sys
d = json.load(sys.stdin)
for k in sorted(d['versions'].keys()):
    print(f'{k}={d[\"versions\"][k]}')
")

GO_VERSIONS_MAP=$(echo "$GO_PLAN" | python3 -c "
import json, sys
d = json.load(sys.stdin)
for k in sorted(d['versions'].keys()):
    print(f'{k}={d[\"versions\"][k]}')
")

assert_eq "plan versions map match" "$PY_VERSIONS_MAP" "$GO_VERSIONS_MAP"

# ============================================================================
# TEST 3: plan — single app by full name
# ============================================================================
echo ""
echo "=== Test 3: plan — single app (demo-hello-python) ==="

PY_SINGLE=$("$RELEASE_HELPER" plan \
  --event-type workflow_dispatch \
  --apps demo-hello-python \
  --version v2.0.0 \
  --format json 2>/dev/null)

GO_SINGLE=$("$KRAKEN" plan \
  --event-type workflow_dispatch \
  --apps demo-hello-python \
  --version v2.0.0 \
  --json 2>/dev/null)

PY_SINGLE_APP=$(echo "$PY_SINGLE" | python3 -c "
import json, sys
d = json.load(sys.stdin)
print(d['matrix']['include'][0]['app'])
")

GO_SINGLE_APP=$(echo "$GO_SINGLE" | python3 -c "
import json, sys
d = json.load(sys.stdin)
print(d['matrix']['include'][0]['app'])
")

assert_eq "single app name match" "$PY_SINGLE_APP" "$GO_SINGLE_APP"

PY_SINGLE_VER=$(echo "$PY_SINGLE" | python3 -c "
import json, sys
d = json.load(sys.stdin)
print(d['matrix']['include'][0]['version'])
")

GO_SINGLE_VER=$(echo "$GO_SINGLE" | python3 -c "
import json, sys
d = json.load(sys.stdin)
print(d['matrix']['include'][0]['version'])
")

assert_eq "single app version match" "$PY_SINGLE_VER" "$GO_SINGLE_VER"

# ============================================================================
# TEST 4: plan — 'all' without demo excluded
# ============================================================================
echo ""
echo "=== Test 4: plan — all (demo excluded) ==="

PY_ALL=$("$RELEASE_HELPER" plan \
  --event-type workflow_dispatch \
  --apps all \
  --version v1.0.0 \
  --format json 2>/dev/null)

GO_ALL=$("$KRAKEN" plan \
  --event-type workflow_dispatch \
  --apps all \
  --version v1.0.0 \
  --json 2>/dev/null)

PY_ALL_APPS=$(echo "$PY_ALL" | python3 -c "
import json, sys
d = json.load(sys.stdin)
for a in sorted(d['apps']):
    print(a)
")

GO_ALL_APPS=$(echo "$GO_ALL" | python3 -c "
import json, sys
d = json.load(sys.stdin)
for a in sorted(d['apps']):
    print(a)
")

assert_eq "plan all (no demo) apps match" "$PY_ALL_APPS" "$GO_ALL_APPS"

# Verify demo is excluded
if echo "$GO_ALL_APPS" | grep -q "^demo-"; then
  red "  FAIL: demo apps should be excluded from 'all' without --include-demo"
  FAIL=$((FAIL + 1))
  ERRORS="${ERRORS}\n  - demo excluded from all"
else
  green "  PASS: demo apps excluded from 'all'"
  PASS=$((PASS + 1))
fi

# ============================================================================
# TEST 5: plan — error: invalid version format
# ============================================================================
echo ""
echo "=== Test 5: plan — error cases ==="

# Invalid version (no v prefix)
PY_EXIT=0
"$RELEASE_HELPER" plan \
  --event-type workflow_dispatch \
  --apps demo-hello-python \
  --version 1.0.0 \
  --format json 2>/dev/null >/dev/null || PY_EXIT=$?

GO_EXIT=0
"$KRAKEN" plan \
  --event-type workflow_dispatch \
  --apps demo-hello-python \
  --version 1.0.0 \
  --json 2>/dev/null >/dev/null || GO_EXIT=$?

# Both should fail (non-zero exit)
if [[ "$PY_EXIT" -ne 0 && "$GO_EXIT" -ne 0 ]]; then
  green "  PASS: both reject invalid version '1.0.0'"
  PASS=$((PASS + 1))
else
  red "  FAIL: invalid version handling (py=$PY_EXIT, go=$GO_EXIT)"
  FAIL=$((FAIL + 1))
  ERRORS="${ERRORS}\n  - invalid version rejection"
fi

# Missing apps for workflow_dispatch
PY_EXIT=0
"$RELEASE_HELPER" plan \
  --event-type workflow_dispatch \
  --version v1.0.0 \
  --format json 2>/dev/null >/dev/null || PY_EXIT=$?

GO_EXIT=0
"$KRAKEN" plan \
  --event-type workflow_dispatch \
  --version v1.0.0 \
  --json 2>/dev/null >/dev/null || GO_EXIT=$?

if [[ "$PY_EXIT" -ne 0 && "$GO_EXIT" -ne 0 ]]; then
  green "  PASS: both reject missing apps for workflow_dispatch"
  PASS=$((PASS + 1))
else
  red "  FAIL: missing apps handling (py=$PY_EXIT, go=$GO_EXIT)"
  FAIL=$((FAIL + 1))
  ERRORS="${ERRORS}\n  - missing apps rejection"
fi

# ============================================================================
# TEST 6: release-multiarch --dry-run output structure
# ============================================================================
echo ""
echo "=== Test 6: release-multiarch --dry-run ==="

PY_DRY=$("$RELEASE_HELPER" release-multiarch demo-hello-python \
  --version v1.0.0 --dry-run 2>/dev/null)

GO_DRY=$("$KRAKEN" release-multiarch demo-hello-python \
  --version v1.0.0 --dry-run 2>/dev/null)

# Both should mention the app and version
assert_contains "py dry-run mentions version" "$PY_DRY" "v1.0.0"
assert_contains "go dry-run mentions version" "$GO_DRY" "v1.0.0"
assert_contains "py dry-run mentions app" "$PY_DRY" "hello-python"
assert_contains "go dry-run mentions app" "$GO_DRY" "hello-python"
assert_contains "py dry-run mentions DRY RUN" "$PY_DRY" "DRY RUN"
assert_contains "go dry-run mentions DRY RUN" "$GO_DRY" "DRY RUN"

# ============================================================================
# TEST 7: plan — multiple specific apps
# ============================================================================
echo ""
echo "=== Test 7: plan — multiple specific apps ==="

PY_MULTI=$("$RELEASE_HELPER" plan \
  --event-type workflow_dispatch \
  --apps "demo-hello-python,demo-hello-go" \
  --version v3.0.0 \
  --format json 2>/dev/null)

GO_MULTI=$("$KRAKEN" plan \
  --event-type workflow_dispatch \
  --apps "demo-hello-python,demo-hello-go" \
  --version v3.0.0 \
  --json 2>/dev/null)

PY_MULTI_APPS=$(echo "$PY_MULTI" | python3 -c "
import json, sys
d = json.load(sys.stdin)
for a in sorted(d['apps']):
    print(a)
")

GO_MULTI_APPS=$(echo "$GO_MULTI" | python3 -c "
import json, sys
d = json.load(sys.stdin)
for a in sorted(d['apps']):
    print(a)
")

assert_eq "multi-app plan apps match" "$PY_MULTI_APPS" "$GO_MULTI_APPS"

PY_MULTI_COUNT=$(echo "$PY_MULTI" | python3 -c "
import json, sys
d = json.load(sys.stdin)
print(len(d['matrix']['include']))
")

GO_MULTI_COUNT=$(echo "$GO_MULTI" | python3 -c "
import json, sys
d = json.load(sys.stdin)
print(len(d['matrix']['include']))
")

assert_eq "multi-app plan count match ($PY_MULTI_COUNT)" "$PY_MULTI_COUNT" "$GO_MULTI_COUNT"

# ============================================================================
# SUMMARY
# ============================================================================
echo ""
echo "================================================================"
echo "Integration Test Results: $PASS passed, $FAIL failed"
echo "================================================================"

if [[ "$FAIL" -gt 0 ]]; then
  red "Failures:"
  echo -e "$ERRORS"
  exit 1
fi

green "All integration tests passed."
