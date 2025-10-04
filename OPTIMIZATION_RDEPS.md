# Reverse Dependency Query Optimization

## Summary

Optimized the change discovery system to reduce Bazel query time by scoping `rdeps` queries to only metadata targets instead of the entire repository.

## Changes Made

### 1. Optimized `detect_changed_apps` in `tools/release_helper/changes.py`

**Before:**
```python
# Query rdeps over the entire repository
rdeps(//..., changed_files)
# Then filter to app_metadata targets
rdeps(app_metadata_targets, all_affected_targets)
```

**After:**
```python
# Query app_metadata targets first
app_metadata_targets = kind('app_metadata', //...)
# Query rdeps only within metadata target scope
rdeps(app_metadata_targets, changed_files)
```

**Performance Impact:**
- **Before**: Two Bazel queries, first one scans entire repository
- **After**: Two Bazel queries, but first is scoped to metadata targets only
- **Speedup**: Significantly faster because `rdeps` only traverses the dependency graph of metadata targets, not all targets in the monorepo

### 2. Added `detect_changed_helm_charts` function

New function in `tools/release_helper/changes.py` that uses the same optimized approach for Helm chart change detection:

```python
def detect_changed_helm_charts(base_commit: Optional[str] = None) -> List[Dict[str, str]]
```

Features:
- Detects changed Helm charts using optimized rdeps query
- Scopes query to `helm_chart_metadata` targets only
- Reuses existing helper functions for file filtering and label validation
- Consistent with `detect_changed_apps` implementation

### 3. Updated `plan_helm_release` CLI command

Enhanced `tools/release_helper/cli.py` to support change detection:

```bash
# New parameter
--base-commit <commit>
```

Usage:
```bash
# Detect changed charts since a commit
bazel run //tools:release -- plan-helm-release --base-commit=main

# Detect changed charts in specific domain
bazel run //tools:release -- plan-helm-release --charts=manman --base-commit=HEAD~5

# Static mode (original behavior)
bazel run //tools:release -- plan-helm-release --charts=all
```

## Technical Details

### Why This Optimization Works

The key insight is that we only care about changes that affect release metadata targets (`app_metadata` or `helm_chart_metadata`), not all targets in the repository.

**Before:**
1. `rdeps(//..., changed_files)` → Returns ALL targets that depend on changed files (could be thousands)
2. `rdeps(app_metadata, all_affected)` → Filters down to just metadata targets

**After:**
1. `kind('app_metadata', //...)` → Get ~10-50 metadata targets (fast)
2. `rdeps(app_metadata, changed_files)` → Only traverse metadata dependency graphs (fast)

### Query Complexity Comparison

| Approach | Scope | Targets Analyzed | Time Complexity |
|----------|-------|------------------|-----------------|
| Old | `rdeps(//..., files)` | All targets in repo | O(all_targets) |
| New | `rdeps(metadata, files)` | Only metadata deps | O(metadata_deps) |

In a monorepo with 1000+ targets but only 20 apps:
- **Old**: Analyzes all 1000+ targets
- **New**: Analyzes only ~20 metadata targets and their dependencies

### Correctness Guarantees

The optimization is **semantically equivalent** because:

1. App metadata targets already depend on all necessary build targets (binaries, tests, etc.)
2. If a change affects an app, it must be in that app's dependency graph
3. Therefore, `rdeps(metadata, files)` will find the same affected apps as the two-step approach

## Testing

### Test Coverage

1. **Unit tests** - Existing tests in `test_changes_git.py` validate file detection
2. **Integration tests** - Can test with real commits:

```bash
# Test app change detection
bazel run //tools:release -- changes --base-commit=HEAD~1

# Test helm chart detection  
bazel run //tools:release -- plan-helm-release --base-commit=HEAD~1
```

### Manual Validation Scenarios

Test the optimization by modifying different files and verifying correct detection:

**Scenario 1: App source file change**
```bash
# Modify an app file
echo "# test" >> demo/hello_python/main.py
git add demo/hello_python/main.py
git commit -m "test change"

# Should detect hello_python
bazel run //tools:release -- changes --base-commit=HEAD~1
```

**Scenario 2: Shared library change**
```bash
# Modify a shared library
echo "# test" >> libs/python/utils.py
git add libs/python/utils.py
git commit -m "test lib change"

# Should detect all apps using that library
bazel run //tools:release -- changes --base-commit=HEAD~1
```

**Scenario 3: Helm chart change**
```bash
# Modify a chart BUILD file
echo "# test" >> demo/BUILD.bazel
git add demo/BUILD.bazel
git commit -m "test chart change"

# Should detect demo charts
bazel run //tools:release -- plan-helm-release --base-commit=HEAD~1
```

## Future Work

### Test Plan Detection (Not Yet Implemented)

The issue mentions supporting "test plan (not yet implemented, but not release_app based)". This would require:

1. Create a `test_metadata` rule in Bazel (similar to `app_metadata`)
2. Add `detect_changed_tests` function using same optimization pattern:

```python
def detect_changed_tests(base_commit: Optional[str] = None) -> List[Dict[str, str]]:
    # Get test metadata targets
    test_metadata = kind('test_metadata', //...)
    # Optimized rdeps query
    rdeps(test_metadata, changed_files)
```

3. Add CLI command:
```bash
bazel run //tools:release -- plan-test --base-commit=main
```

### Docker Plan Integration

Current `plan_release` already uses `detect_changed_apps`, so Docker image building benefits from this optimization automatically.

## References

- Issue: whale-net/everything#113 (comment about optimization)
- PR #132: Previous improvement to change detection
- Bazel query docs: https://bazel.build/query/language#rdeps
