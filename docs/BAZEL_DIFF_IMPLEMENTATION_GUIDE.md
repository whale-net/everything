# bazel-diff Implementation Guide

This guide provides step-by-step instructions for implementing bazel-diff in the Everything monorepo.

## Prerequisites

- Bazel 8.3.1+ installed
- GitHub Actions access for CI updates
- Familiarity with Bazel query language

## Phase 1: Add bazel-diff Dependency

### Option A: Using Bazel Module (Preferred)

Check if bazel-diff is available in the Bazel Central Registry:

```bash
# Search BCR
curl -s "https://bcr.bazel.build/modules/bazel-diff" | jq .
```

If available, add to `MODULE.bazel`:

```starlark
# Add bazel-diff dependency
bazel_dep(name = "bazel_diff", version = "x.y.z")
```

### Option B: Using http_archive

If not in BCR, download pre-built binary:

```starlark
# In MODULE.bazel
http_archive = use_repo_rule("@bazel_tools//tools/build_defs/repo:http.bzl", "http_archive")

http_archive(
    name = "bazel_diff",
    urls = ["https://github.com/Tinder/bazel-diff/releases/download/7.3.3/bazel-diff-7.3.3-all.jar"],
    sha256 = "...",  # Add SHA256 from releases page
)
```

Create wrapper script in `tools/bazel_diff.sh`:

```bash
#!/bin/bash
# Wrapper for bazel-diff JAR

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
BAZEL_DIFF_JAR="${SCRIPT_DIR}/../external/bazel_diff/file/bazel-diff-7.3.3-all.jar"

java -jar "$BAZEL_DIFF_JAR" "$@"
```

Add BUILD.bazel target:

```starlark
# In tools/BUILD.bazel
sh_binary(
    name = "bazel_diff",
    srcs = ["bazel_diff.sh"],
    data = ["@bazel_diff//file"],
    visibility = ["//visibility:public"],
)
```

### Verify Installation

```bash
# Test bazel-diff is accessible
bazel run //tools:bazel_diff -- --help
```

## Phase 2: Create Integration Helper

Create `tools/release_helper/bazel_diff.py`:

```python
"""Integration with bazel-diff for change detection."""

import json
import os
import subprocess
import sys
import tempfile
from pathlib import Path
from typing import Dict, List, Optional, Set

from tools.release_helper.core import run_bazel
from tools.release_helper.metadata import list_all_apps


def generate_hashes(output_file: str) -> None:
    """Generate bazel-diff hashes for current workspace state.
    
    Args:
        output_file: Path to write hashes JSON
    """
    print(f"Generating bazel-diff hashes to {output_file}...", file=sys.stderr)
    
    result = run_bazel([
        "run",
        "//tools:bazel_diff",
        "--",
        "generate-hashes",
        "-o", output_file
    ])
    
    if not os.path.exists(output_file):
        raise RuntimeError(f"Hash generation failed: {output_file} not created")
    
    print(f"✓ Generated hashes: {output_file}", file=sys.stderr)


def get_changed_targets(
    starting_hashes: str,
    ending_hashes: str,
    output_file: Optional[str] = None
) -> Set[str]:
    """Get changed targets between two hash files.
    
    Args:
        starting_hashes: Path to starting hashes JSON
        ending_hashes: Path to ending hashes JSON
        output_file: Optional path to write changed targets
    
    Returns:
        Set of changed target labels
    """
    if not os.path.exists(starting_hashes):
        raise FileNotFoundError(f"Starting hashes not found: {starting_hashes}")
    if not os.path.exists(ending_hashes):
        raise FileNotFoundError(f"Ending hashes not found: {ending_hashes}")
    
    # Create temp file if output not specified
    if output_file is None:
        fd, output_file = tempfile.mkstemp(suffix=".txt", prefix="changed-targets-")
        os.close(fd)
    
    print(f"Computing changed targets...", file=sys.stderr)
    
    result = run_bazel([
        "run",
        "//tools:bazel_diff",
        "--",
        "get-changed-targets",
        "-sh", starting_hashes,
        "-fh", ending_hashes,
        "-o", output_file
    ])
    
    # Read changed targets
    with open(output_file, 'r') as f:
        changed_targets = {line.strip() for line in f if line.strip()}
    
    print(f"✓ Found {len(changed_targets)} changed targets", file=sys.stderr)
    
    return changed_targets


def detect_changed_apps_with_bazel_diff(
    starting_hashes: str,
    ending_hashes: str
) -> List[Dict[str, str]]:
    """Detect which apps have changed using bazel-diff.
    
    Args:
        starting_hashes: Path to starting hashes JSON
        ending_hashes: Path to ending hashes JSON
    
    Returns:
        List of app dictionaries with bazel_target, name, and domain
    """
    all_apps = list_all_apps()
    
    # Get changed targets from bazel-diff
    changed_targets = get_changed_targets(starting_hashes, ending_hashes)
    
    if not changed_targets:
        print("No targets changed", file=sys.stderr)
        return []
    
    # Get all app_metadata targets
    result = run_bazel([
        "query",
        "kind('app_metadata', //...)",
        "--output=label"
    ])
    all_metadata_targets = set(result.stdout.strip().split('\n')) if result.stdout.strip() else set()
    
    if not all_metadata_targets:
        print("No app_metadata targets found", file=sys.stderr)
        return []
    
    # Find affected apps
    affected_apps = []
    
    for app in all_apps:
        metadata_target = app['bazel_target']
        
        # Check if metadata target itself changed
        if metadata_target in changed_targets:
            affected_apps.append(app)
            print(f"  {app['name']}: metadata target changed", file=sys.stderr)
            continue
        
        # Check if any dependency of the metadata changed
        # Build a query to check if there's a path from metadata to any changed target
        if changed_targets:
            try:
                # Use somepath to check if metadata depends on any changed target
                changed_expr = " + ".join(f'"{t}"' for t in changed_targets)
                result = run_bazel([
                    "query",
                    f"somepath({metadata_target}, {changed_expr})",
                    "--output=label"
                ])
                
                if result.stdout.strip():
                    affected_apps.append(app)
                    print(f"  {app['name']}: depends on changed targets", file=sys.stderr)
            except subprocess.CalledProcessError:
                # No path found, app not affected
                pass
    
    if not affected_apps:
        print("No apps affected by changed targets", file=sys.stderr)
    
    return affected_apps


def generate_hashes_for_commit(commit: str, output_file: str) -> None:
    """Generate hashes for a specific git commit.
    
    Args:
        commit: Git commit SHA or ref
        output_file: Path to write hashes
    """
    # Store current state
    try:
        current_ref = subprocess.run(
            ["git", "rev-parse", "HEAD"],
            capture_output=True,
            text=True,
            check=True
        ).stdout.strip()
    except subprocess.CalledProcessError as e:
        raise RuntimeError(f"Failed to get current commit: {e}")
    
    try:
        # Checkout target commit
        print(f"Checking out {commit}...", file=sys.stderr)
        subprocess.run(
            ["git", "checkout", commit],
            capture_output=True,
            check=True
        )
        
        # Generate hashes
        generate_hashes(output_file)
        
    finally:
        # Restore original state
        print(f"Restoring {current_ref}...", file=sys.stderr)
        subprocess.run(
            ["git", "checkout", current_ref],
            capture_output=True,
            check=True
        )
```

## Phase 3: Add CLI Commands

Update `tools/release_helper/cli.py`:

```python
from tools.release_helper.bazel_diff import (
    detect_changed_apps_with_bazel_diff,
    generate_hashes,
    generate_hashes_for_commit,
    get_changed_targets,
)

@app.command("generate-hashes")
def generate_hashes_cmd(
    output: Annotated[str, typer.Option(help="Output file for hashes")] = "bazel-diff-hashes.json",
    commit: Annotated[Optional[str], typer.Option(help="Generate hashes for specific commit")] = None,
):
    """Generate bazel-diff hashes for current or specified commit."""
    if commit:
        generate_hashes_for_commit(commit, output)
    else:
        generate_hashes(output)
    print(f"Hashes written to: {output}")


@app.command("changed-targets")
def changed_targets_cmd(
    starting_hashes: Annotated[str, typer.Argument(help="Starting hashes file")],
    ending_hashes: Annotated[str, typer.Argument(help="Ending hashes file")],
    output: Annotated[Optional[str], typer.Option(help="Output file for changed targets")] = None,
):
    """Get changed targets using bazel-diff."""
    changed = get_changed_targets(starting_hashes, ending_hashes, output)
    
    if changed:
        print(f"Changed targets ({len(changed)}):")
        for target in sorted(changed):
            print(f"  {target}")
    else:
        print("No targets changed")


@app.command("changes-bazel-diff")
def changes_bazel_diff(
    starting_hashes: Annotated[str, typer.Argument(help="Starting hashes file")],
    ending_hashes: Annotated[str, typer.Argument(help="Ending hashes file")],
):
    """Detect changed apps using bazel-diff."""
    changed_apps = detect_changed_apps_with_bazel_diff(starting_hashes, ending_hashes)
    
    if changed_apps:
        print(f"Changed apps ({len(changed_apps)}):")
        for app in changed_apps:
            print(f"  {app['name']}: {app['bazel_target']}")
    else:
        print("No apps changed")
```

## Phase 4: Test Locally

```bash
# Generate hashes for current state
bazel run //tools:release -- generate-hashes --output /tmp/current.json

# Make some changes to a file
echo "# Test change" >> demo/hello_python/main.py

# Generate hashes for changed state
bazel run //tools:release -- generate-hashes --output /tmp/changed.json

# Get changed targets
bazel run //tools:release -- changed-targets /tmp/current.json /tmp/changed.json

# Detect changed apps
bazel run //tools:release -- changes-bazel-diff /tmp/current.json /tmp/changed.json

# Compare with current system
bazel run //tools:release -- changes --base-commit HEAD~1
```

## Phase 5: Update CI Workflow

### Update `.github/workflows/ci.yml`

Replace the change detection logic in the `plan-docker` job:

```yaml
plan-docker:
  name: Plan Docker Builds
  runs-on: ubuntu-latest
  needs: [test, test-container-arch]
  outputs:
    matrix: ${{ steps.plan.outputs.matrix }}
    apps: ${{ steps.plan.outputs.apps }}
  steps:
  - name: Checkout code (base)
    uses: actions/checkout@v4
    with:
      ref: ${{ github.event.pull_request.base.sha || github.event.before }}
      fetch-depth: 0
      path: base
      
  - name: Setup Build Environment (base)
    uses: ./base/.github/actions/setup-build-env
    with:
      cache-suffix: 'plan-base'
      
  - name: Generate base hashes
    working-directory: base
    run: |
      bazel run //tools:release -- generate-hashes --output /tmp/base-hashes.json
      
  - name: Checkout code (head)
    uses: actions/checkout@v4
    with:
      fetch-depth: 0
      path: head
      
  - name: Setup Build Environment (head)
    uses: ./head/.github/actions/setup-build-env
    with:
      cache-suffix: 'plan-head'
      
  - name: Generate head hashes
    working-directory: head
    run: |
      bazel run //tools:release -- generate-hashes --output /tmp/head-hashes.json
      
  - name: Detect changes and plan builds
    id: plan
    working-directory: head
    env:
      GITHUB_REPOSITORY_OWNER: ${{ github.repository_owner }}
    run: |
      # Get changed targets
      bazel run //tools:release -- changed-targets \
        /tmp/base-hashes.json \
        /tmp/head-hashes.json \
        --output /tmp/changed-targets.txt
      
      # Detect changed apps
      bazel run //tools:release -- changes-bazel-diff \
        /tmp/base-hashes.json \
        /tmp/head-hashes.json > /tmp/changed-apps.txt
      
      # Use existing plan command with detected apps
      if [ -s /tmp/changed-apps.txt ]; then
        # Extract app names
        CHANGED_APPS=$(cat /tmp/changed-apps.txt | grep '  ' | awk '{print $1}' | tr '\n' ',' | sed 's/,$//')
        
        if [ -n "$CHANGED_APPS" ]; then
          echo "Changed apps detected: $CHANGED_APPS"
          PLAN_OUTPUT=$(bazel run //tools:release -- plan \
            --event-type "${{ github.event_name }}" \
            --apps "$CHANGED_APPS" \
            --format github)
        else
          echo "No apps changed"
          PLAN_OUTPUT="matrix={\"include\":[]}"
        fi
      else
        echo "No apps changed"
        PLAN_OUTPUT="matrix={\"include\":[]}"
      fi
      
      # Parse and set outputs
      echo "$PLAN_OUTPUT" | while IFS= read -r line; do
        if [[ "$line" == matrix=* ]]; then
          echo "${line}" >> $GITHUB_OUTPUT
        elif [[ "$line" == apps=* ]]; then
          echo "${line}" >> $GITHUB_OUTPUT
        fi
      done
```

## Phase 6: Testing & Validation

### Create Test Script

Create `tools/test_bazel_diff.sh`:

```bash
#!/bin/bash
# Test script to validate bazel-diff integration

set -euo pipefail

echo "=== Testing bazel-diff Integration ==="
echo ""

# Test 1: Generate hashes
echo "Test 1: Generate hashes for current state"
bazel run //tools:release -- generate-hashes --output /tmp/test-current.json
if [ -f /tmp/test-current.json ]; then
    echo "✓ Hash generation successful"
    echo "  Hash file size: $(stat -f%z /tmp/test-current.json 2>/dev/null || stat -c%s /tmp/test-current.json) bytes"
else
    echo "✗ Hash generation failed"
    exit 1
fi
echo ""

# Test 2: Make a change and detect it
echo "Test 2: Detect changes"
echo "# Test comment" >> demo/hello_python/main.py

bazel run //tools:release -- generate-hashes --output /tmp/test-changed.json

CHANGED_COUNT=$(bazel run //tools:release -- changed-targets \
    /tmp/test-current.json /tmp/test-changed.json 2>&1 | grep -c "//demo/hello_python" || true)

if [ "$CHANGED_COUNT" -gt 0 ]; then
    echo "✓ Change detection successful"
    echo "  Detected changes in demo/hello_python"
else
    echo "✗ Change detection failed"
    git checkout demo/hello_python/main.py
    exit 1
fi

# Revert test change
git checkout demo/hello_python/main.py
echo ""

# Test 3: Compare with current system
echo "Test 3: Compare with current system"
echo "Testing against HEAD~1..."

# Current system
bazel run //tools:release -- changes --base-commit HEAD~1 > /tmp/current-system.txt 2>&1

# bazel-diff system
bazel run //tools:release -- generate-hashes --commit HEAD~1 --output /tmp/base.json
bazel run //tools:release -- generate-hashes --output /tmp/head.json
bazel run //tools:release -- changes-bazel-diff /tmp/base.json /tmp/head.json > /tmp/bazel-diff-system.txt 2>&1

echo "Current system detected:"
cat /tmp/current-system.txt | grep "Changed apps" || echo "  (no changes)"

echo ""
echo "bazel-diff detected:"
cat /tmp/bazel-diff-system.txt | head -5

echo ""
echo "=== All tests passed ==="
```

Run tests:

```bash
chmod +x tools/test_bazel_diff.sh
./tools/test_bazel_diff.sh
```

## Phase 7: Parallel Deployment

Run both systems in parallel for validation:

1. **Keep old system** for comparison
2. **Add bazel-diff** as new path
3. **Log both results** in CI
4. **Compare outputs** to ensure accuracy
5. **Switch to bazel-diff** once validated
6. **Remove old system**

## Phase 8: Cleanup

Once bazel-diff is validated:

1. Remove `tools/release_helper/changes.py`
2. Remove `tools/release_helper/test_changes_git.py`
3. Update documentation
4. Update README to remove "Known Limitations"

## Troubleshooting

### Issue: Hash generation fails

**Check:**
- Bazel cache is clean: `bazel clean`
- All dependencies are resolved: `bazel sync`
- Java is installed (for JAR-based bazel-diff)

### Issue: Too many targets changed

**Check:**
- Generated hashes at correct commits
- Working directory is clean: `git status`
- External dependencies haven't changed

### Issue: Missing changes

**Check:**
- Hash generation includes all workspace targets
- Not filtering too aggressively
- Transitive dependencies are included

## Performance Tips

1. **Cache hash files** in CI for reuse
2. **Generate hashes in parallel** for base and head
3. **Use target patterns** to limit scope if needed
4. **Store hashes as artifacts** for debugging

## References

- [bazel-diff GitHub](https://github.com/Tinder/bazel-diff)
- [Current Implementation](../tools/release_helper/changes.py)
- [CI Workflow](../.github/workflows/ci.yml)
