# Change Detection System using Bazel rdeps

## Overview

This document explains the refactored change detection system that uses Bazel's `rdeps` query to precisely identify affected targets based on file changes.

## Problem Statement

The original approach of running `bazel test //...` for every CI run was slow because:
1. **Analysis phase overhead**: Bazel must parse ALL BUILD files and construct the entire dependency graph (~30-60 seconds)
2. **No targeting**: Even with caching, we still paid the full analysis cost for unchanged code

## Solution: rdeps-Based Change Detection

We implemented a system using Bazel's `rdeps` (reverse dependencies) query function to find only the targets affected by changed files.

### Architecture

```
┌─────────────────────────────────────────────────────────────┐
│ Core Module: tools/release_helper/change_detection.py      │
│                                                             │
│ detect_affected_targets(                                   │
│     base_commit: str,                                      │
│     target_kind: Optional[str],  # e.g., "test", "app"   │
│     universe: str = "//..."                               │
│ ) -> List[str]                                            │
│                                                            │
│ 1. Get changed files from git                            │
│ 2. Find Bazel targets containing those files             │
│ 3. Use rdeps(universe, targets) to find affected targets │
│ 4. Filter by target kind if specified                    │
│ 5. Return list of affected targets                       │
└─────────────────────────────────────────────────────────────┘
         │                                   │
         ▼                                   ▼
┌──────────────────┐              ┌──────────────────┐
│ changes.py       │              │ cli.py           │
│                  │              │                  │
│ detect_changed   │              │ plan-tests       │
│_apps()           │              │ command          │
│                  │              │                  │
│ Filters for      │              │ Filters for      │
│ app_metadata     │              │ test targets     │
│ targets          │              │                  │
└──────────────────┘              └──────────────────┘
```

### Key Components

#### 1. Core Change Detection (`change_detection.py`)

The `detect_affected_targets()` function is the single source of truth:

```python
def detect_affected_targets(
    base_commit: Optional[str] = None,
    target_kind: Optional[str] = None,
    universe: str = "//...",
) -> List[str]:
    """
    Uses Bazel rdeps to find all targets affected by changes.
    
    Algorithm:
    1. Get changed files from git
    2. For each file, find which Bazel targets it belongs to
    3. Use rdeps(universe, changed_targets) to find reverse dependencies
    4. Filter by target kind if needed
    """
```

**File-to-Target Mapping:**
- BUILD/bzl files → All targets in package recursively
- Source files → Try direct rdeps query, fall back to package-level
- Non-Bazel files (.github/, docs/, .md, .sh) → Skipped

**Bazel Query Used:**
```bash
bazel query 'rdeps(//..., set(//changed:target1 //changed:target2))'
```

This finds all targets in `//...` that transitively depend on the changed targets.

#### 2. App-Specific Detection (`changes.py`)

Wraps the core module to filter for apps:

```python
def detect_changed_apps(base_commit: str) -> List[Dict[str, str]]:
    # Get all affected targets
    affected = detect_affected_targets(base_commit)
    
    # Filter for apps whose metadata or binary targets are affected
    changed_apps = [app for app in all_apps 
                    if app_is_affected(app, affected)]
    return changed_apps
```

#### 3. Test Planning CLI (`cli.py`)

New `plan-tests` command for CI:

```bash
# Text output (one target per line)
bazel run //tools:release -- plan-tests --base-commit=origin/main

# GitHub Actions format
bazel run //tools:release -- plan-tests --base-commit=origin/main --format=github

# JSON format
bazel run //tools:release -- plan-tests --base-commit=origin/main --format=json
```

**GitHub Actions Output:**
```
test_targets=//path/to:test1 //path/to:test2
needs_testing=true
test_count=2
```

### CI Integration

The CI workflow now has two phases:

#### Phase 1: Plan Tests (Fast - ~10-30 seconds)
```yaml
plan-tests:
  steps:
    - run: |
        bazel run //tools:release -- plan-tests \
          --base-commit=$BASE_COMMIT \
          --format github
```

This only runs Bazel query (analysis), no execution.

#### Phase 2: Run Tests (Targeted)
```yaml
test:
  needs: plan-tests
  if: needs.plan-tests.outputs.needs_testing == 'true'
  steps:
    - run: |
        TEST_TARGETS="${{ needs.plan-tests.outputs.test_targets }}"
        bazel test $TEST_TARGETS
```

This runs only the affected tests.

## Performance Benefits

### Before (Naive Approach)
```
bazel test //...
├── Analysis: 30-60 seconds (parse ALL BUILD files)
├── Execution: 0-5 seconds (cached tests skip)
└── Total: 30-65 seconds EVERY TIME
```

### After (rdeps-Based)
```
# Plan phase
bazel run //tools:release -- plan-tests
├── Analysis: 10-30 seconds (only for release tool)
├── Query: 5-15 seconds (rdeps computation)
└── Total: 15-45 seconds

# Test phase (example: 10 affected tests out of 100)
bazel test <10 affected targets>
├── Analysis: 5-15 seconds (only affected packages)
├── Execution: 2-10 seconds (run affected tests)
└── Total: 7-25 seconds

# Grand Total: 22-70 seconds
# vs. 30-65 seconds for bazel test //...

# BUT: When only 1 file changes (common case):
# - Old way: 30-65 seconds
# - New way: ~25 seconds (plan) + ~10 seconds (test) = 35 seconds
#   AND we only test what's needed!
```

**Real Benefit**: For small changes (1-2 files), we test 5-10 targets instead of 100+.

## Usage Examples

### Find Affected Tests Locally
```bash
# Since last commit
bazel run //tools:release -- plan-tests

# Since specific commit
bazel run //tools:release -- plan-tests --base-commit=abc123

# Since origin/main
bazel run //tools:release -- plan-tests --base-commit=origin/main
```

### Find Affected Apps
```bash
# Old command (still works, now uses rdeps)
bazel run //tools:release -- changes --base-commit=origin/main
```

### In CI (GitHub Actions)
```yaml
- name: Plan tests
  id: plan
  run: |
    bazel run //tools:release -- plan-tests \
      --base-commit=${{ github.event.pull_request.base.sha }} \
      --format github

- name: Run tests
  if: steps.plan.outputs.needs_testing == 'true'
  run: |
    bazel test ${{ steps.plan.outputs.test_targets }}
```

## Technical Details

### Why rdeps?

`rdeps(universe, targets)` finds all targets in `universe` that transitively depend on `targets`. This is exactly what we need:

```
If file X changed → find targets containing X → find what depends on those
```

### Handling Edge Cases

1. **Non-Bazel files** (`.github/`, `docs/`, `.md`): Skipped entirely
2. **Build files**: Affect entire package recursively
3. **Root files**: Affect root package only
4. **No changes**: Returns empty list (CI skips testing)
5. **All files changed**: Falls back to testing everything

### Integration with Existing System

The refactoring maintains backward compatibility:
- `detect_changed_apps()` still works the same
- Existing CLI commands unchanged
- New `plan-tests` command added
- CI workflow enhanced, not replaced

## Future Improvements

1. **Caching query results**: Store rdeps results per commit
2. **Parallel queries**: Query multiple files in parallel
3. **Smart universe limiting**: Only query relevant parts of the tree
4. **Test sharding**: Split affected tests across multiple runners

## References

- [Bazel Query Guide](https://bazel.build/query/guide)
- [rdeps function docs](https://bazel.build/query/language#rdeps)
- Original discussion: GitHub Copilot conversation on CI test performance
