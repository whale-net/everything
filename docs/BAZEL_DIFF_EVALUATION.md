# Bazel-Diff Evaluation for Change Detection

## Executive Summary

This document evaluates whether [@Tinder/bazel-diff](https://github.com/Tinder/bazel-diff) could replace the current custom change detection system in the Everything monorepo.

**Recommendation: ✅ YES - bazel-diff is a suitable replacement**

bazel-diff is purpose-built for exactly this use case and offers significant advantages over the current custom implementation:

1. **More reliable** - Uses Bazel's own query system to compute accurate diffs
2. **Battle-tested** - Widely used in production by Tinder and many other organizations
3. **Maintained** - Active open-source project with regular updates
4. **Simpler** - Eliminates custom code and complex query logic
5. **Accurate** - Handles transitive dependencies correctly using Bazel hashing

## Current Implementation Analysis

### What We Have Now

The current change detection system (`tools/release_helper/changes.py`) implements a custom solution that:

1. **Gets changed files** using `git diff` between two commits
2. **Filters non-build files** (docs, workflows, etc.)
3. **Converts file paths to Bazel labels** (e.g., `libs/python/utils.py` → `//libs/python:utils.py`)
4. **Validates labels** using `bazel query` to filter out deleted files
5. **Finds reverse dependencies** using `rdeps(//..., <changed_labels>)`
6. **Identifies affected apps** by intersecting with `app_metadata` targets

### Code Complexity

The current implementation is ~230 lines of Python with complex logic:

```python
def detect_changed_apps(base_commit: Optional[str] = None) -> List[Dict[str, str]]:
    """Detect which apps have changed compared to a base commit.
    
    Uses Bazel query to find app binaries that depend on changed source files.
    """
    # 1. Get changed files from git
    changed_files = _get_changed_files(base_commit)
    
    # 2. Filter out non-build files
    relevant_files = [f for f in changed_files if not _should_ignore_file(f)]
    
    # 3. Convert to Bazel labels with special handling for BUILD files
    file_labels = []
    changed_packages = set()
    for f in relevant_files:
        if f.endswith('.bzl'):
            continue
        if f.endswith(('BUILD', 'BUILD.bazel')):
            # BUILD file changes affect entire package
            changed_packages.add(f"//{package}")
            continue
        file_labels.append(f"//{package}:{filename}")
    
    # 4. Validate labels (filters deleted files)
    valid_labels = []
    try:
        result = run_bazel(["query", labels_expr, "--output=label"])
        valid_labels = result.stdout.strip().split('\n')
    except subprocess.CalledProcessError:
        # Fall back to individual validation
        for label in file_labels:
            # ... individual validation logic
    
    # 5. Find all affected targets
    result = run_bazel(["query", f"rdeps(//..., {labels_expr})"])
    all_affected_targets = set(result.stdout.strip().split('\n'))
    
    # 6. Find affected app_metadata targets
    all_metadata_targets = run_bazel(["query", "kind('app_metadata', //...)"])
    result = run_bazel(["query", f"rdeps({metadata_expr}, {affected_expr})"])
    
    # 7. Match to app list
    affected_apps = [app for app in all_apps if app['bazel_target'] in all_affected_metadata]
    return affected_apps
```

### Known Issues

The README documents several limitations:

> **Known Limitations:**
> - Bazel query dependency analysis may not catch all transitive dependencies accurately
> - File-based detection uses directory prefix matching which can be overly broad
> - Infrastructure changes (tools/, .github/, MODULE.bazel) trigger all apps to rebuild as a safety measure
> - If no specific apps are detected as changed but files were modified, all apps are rebuilt conservatively

These limitations exist because:
1. **Query complexity** - Multiple `bazel query` invocations with complex expressions
2. **File-to-label mapping** - Custom logic to convert git paths to Bazel labels
3. **Edge cases** - Deleted files, .bzl files, BUILD files all need special handling
4. **Conservative fallbacks** - When detection fails, rebuild everything

## What is @Tinder/bazel-diff?

bazel-diff is a tool that:

> Performs Bazel Target Diffing between two revisions in Git, allowing for Test Target Selection and Selective Building

### How It Works

1. **Generate hashes** for all targets in the workspace at two different commits
2. **Compare the hashes** to determine which targets changed
3. **Output** the changed targets in various formats (JSON, text, etc.)

The key insight: Instead of manually tracking file changes and computing dependencies, bazel-diff uses Bazel's own action graph and hashing to detect changes. This is **exactly** what Bazel uses internally for incremental builds.

### Key Features

- ✅ **Accurate dependency tracking** - Uses Bazel's action graph
- ✅ **Handles transitive dependencies** - Automatically included in hash computation
- ✅ **No manual label conversion** - Works at the Bazel target level
- ✅ **Battle-tested** - Used in production by major companies
- ✅ **Multiple output formats** - JSON, text, query expressions
- ✅ **Configurable** - Can filter by target patterns, kinds, etc.

## How bazel-diff Would Replace Current System

### Architecture Comparison

**Current System:**
```
Git Diff → Filter Files → Convert to Labels → Validate → Query rdeps → Find Apps
  ↓          ↓              ↓                    ↓          ↓            ↓
Custom    Custom         Custom              Bazel      Bazel       Custom
Python    Python         Python              Query      Query       Python
```

**With bazel-diff:**
```
bazel-diff → Filter by kind → Find Apps
     ↓            ↓              ↓
  Tool        Bazel Query     Custom Python
```

### Implementation Plan

#### 1. Add bazel-diff to the Repository

bazel-diff can be added as:
- **Option A**: Bazel module dependency (if available in BCR)
- **Option B**: Pre-built binary downloaded via `http_archive` in MODULE.bazel
- **Option C**: Built from source using Bazel

#### 2. Update CI Workflow

Current workflow in `.github/workflows/ci.yml`:
```yaml
- name: Plan Docker builds using release tool
  run: |
    BASE_COMMIT="${{ github.event.pull_request.base.sha }}"
    bazel run //tools:release -- plan \
      --event-type "$EVENT_TYPE" \
      --base-commit="$BASE_COMMIT" \
      --format github
```

With bazel-diff:
```yaml
- name: Generate bazel-diff hashes
  run: |
    # Generate starting hash at base commit
    git checkout ${{ github.event.pull_request.base.sha }}
    bazel run @bazel_diff//:bazel-diff -- generate-hashes -o /tmp/start.json
    
    # Generate ending hash at current commit  
    git checkout ${{ github.sha }}
    bazel run @bazel_diff//:bazel-diff -- generate-hashes -o /tmp/end.json
    
- name: Plan Docker builds using bazel-diff
  run: |
    # Get changed targets
    bazel run @bazel_diff//:bazel-diff -- \
      get-changed-targets \
      --starting-hashes /tmp/start.json \
      --ending-hashes /tmp/end.json \
      --output /tmp/changed-targets.txt
    
    # Use release tool to plan builds based on changed targets
    bazel run //tools:release -- plan-from-targets \
      --targets-file /tmp/changed-targets.txt \
      --format github
```

#### 3. Simplify Release Helper

Replace `tools/release_helper/changes.py` (~230 lines) with a simpler version (~50 lines):

```python
def detect_changed_apps_with_bazel_diff(
    changed_targets_file: str
) -> List[Dict[str, str]]:
    """Detect which apps have changed using bazel-diff output.
    
    Args:
        changed_targets_file: Path to file with changed targets from bazel-diff
    
    Returns:
        List of app dictionaries with bazel_target, name, and domain
    """
    all_apps = list_all_apps()
    
    # Read changed targets from bazel-diff
    with open(changed_targets_file) as f:
        changed_targets = set(line.strip() for line in f if line.strip())
    
    if not changed_targets:
        print("No targets changed", file=sys.stderr)
        return []
    
    # Get all app_metadata targets
    result = run_bazel([
        "query",
        "kind('app_metadata', //...)",
        "--output=label"
    ])
    all_metadata_targets = set(result.stdout.strip().split('\n'))
    
    # For each metadata target, check if it's in the changed set or depends on changed targets
    affected_apps = []
    for app in all_apps:
        metadata_target = app['bazel_target']
        
        # Check direct change or dependency change
        if metadata_target in changed_targets:
            affected_apps.append(app)
            continue
            
        # Check if metadata depends on any changed target
        try:
            result = run_bazel([
                "query",
                f"somepath({metadata_target}, {' + '.join(changed_targets)})"
            ])
            if result.stdout.strip():
                affected_apps.append(app)
        except subprocess.CalledProcessError:
            # No path found, not affected
            pass
    
    return affected_apps
```

**Simplification benefits:**
- ❌ Remove `_get_changed_files()` - bazel-diff handles this
- ❌ Remove `_should_ignore_file()` - bazel-diff only tracks build targets
- ❌ Remove file-to-label conversion logic - bazel-diff outputs targets
- ❌ Remove label validation logic - bazel-diff only outputs valid targets
- ❌ Remove complex rdeps queries - bazel-diff computes dependencies
- ✅ Keep app_metadata filtering - still needed to identify apps

## Advantages of bazel-diff

### 1. Accuracy

**Current system:** Manual dependency tracking with multiple query invocations
**bazel-diff:** Uses Bazel's action graph and content hashing

bazel-diff will catch changes that the current system might miss:
- Changes in external dependencies (MODULE.bazel updates)
- Changes in .bzl files that affect build logic
- Transitive dependency changes (A depends on B depends on C)

### 2. Maintainability

**Current system:** ~230 lines of custom Python with complex edge case handling
**bazel-diff:** ~50 lines to integrate + maintained external tool

Less custom code = fewer bugs, easier maintenance.

### 3. Performance

**Current system:** Multiple `bazel query` invocations with complex expressions
**bazel-diff:** Two hash generations (parallelizable) + one diff

bazel-diff can cache hash files and reuse them across builds.

### 4. Reliability

**Current system:** Custom implementation with known limitations
**bazel-diff:** Battle-tested tool used by Tinder, Google, and others

The current system has conservative fallbacks ("if unsure, rebuild everything"). bazel-diff is precise.

### 5. Standard Tool

bazel-diff is becoming the de facto standard for this use case in the Bazel community. Using it means:
- Community support and documentation
- Regular updates and bug fixes
- Integration with other Bazel tools
- Best practices from experienced users

## Potential Concerns & Solutions

### Concern 1: Learning Curve

**Response:** bazel-diff is simpler than the current system. The API is straightforward:
```bash
# Generate hashes
bazel-diff generate-hashes -o hashes.json

# Get changed targets
bazel-diff get-changed-targets --starting-hashes start.json --ending-hashes end.json
```

### Concern 2: CI Integration

**Response:** bazel-diff is designed for CI and has excellent GitHub Actions integration:
- Multiple hash generation can run in parallel
- Hash files can be cached as artifacts
- Output formats are CI-friendly

### Concern 3: Custom Filtering

**Response:** bazel-diff supports filtering:
```bash
# Only check specific target patterns
bazel-diff get-changed-targets \
  --target-patterns "//demo/..." "//manman/..."
  
# Filter by target kind
bazel-diff get-changed-targets \
  --target-kinds "app_metadata" "py_binary"
```

### Concern 4: Additional Dependency

**Response:** We're already using many Bazel modules. Adding one more well-maintained tool is acceptable, especially given the benefits.

### Concern 5: Migration Effort

**Response:** Migration can be incremental:
1. Add bazel-diff to MODULE.bazel
2. Implement parallel detection (keep current system as fallback)
3. Test in CI with comparison between systems
4. Switch to bazel-diff once validated
5. Remove old system

## Implementation Roadmap

### Phase 1: Proof of Concept (1-2 days)

1. Add bazel-diff to MODULE.bazel
2. Create a test script that:
   - Generates hashes for two commits
   - Compares with current system output
   - Documents differences
3. Run on recent PRs to validate accuracy

### Phase 2: Integration (2-3 days)

1. Update `tools/release_helper/changes.py` to support bazel-diff
2. Add CLI flag to use bazel-diff (opt-in)
3. Test locally with various scenarios
4. Update documentation

### Phase 3: CI Migration (1-2 days)

1. Update `.github/workflows/ci.yml` to use bazel-diff
2. Keep current system as fallback
3. Run both systems in parallel for comparison
4. Monitor for discrepancies

### Phase 4: Cleanup (1 day)

1. Remove old change detection code
2. Update documentation
3. Remove fallback mechanisms
4. Celebrate reduced complexity! 🎉

**Total estimated effort: 5-8 days**

## Example Usage

### Local Development

```bash
# Compare current branch against main
git checkout main
bazel run @bazel_diff//:bazel-diff -- generate-hashes -o /tmp/main.json

git checkout feature-branch
bazel run @bazel_diff//:bazel-diff -- generate-hashes -o /tmp/feature.json

bazel run @bazel_diff//:bazel-diff -- get-changed-targets \
  --starting-hashes /tmp/main.json \
  --ending-hashes /tmp/feature.json \
  --output /tmp/changed.txt

# Use release tool with changed targets
bazel run //tools:release -- plan-from-targets \
  --targets-file /tmp/changed.txt
```

### CI Pipeline

```yaml
jobs:
  detect-changes:
    runs-on: ubuntu-latest
    outputs:
      changed-targets: ${{ steps.diff.outputs.changed }}
    steps:
      - name: Checkout base
        uses: actions/checkout@v4
        with:
          ref: ${{ github.event.pull_request.base.sha }}
      
      - name: Generate base hashes
        run: bazel run @bazel_diff//:bazel-diff -- generate-hashes -o /tmp/start.json
      
      - name: Checkout head
        uses: actions/checkout@v4
        with:
          ref: ${{ github.sha }}
      
      - name: Generate head hashes
        run: bazel run @bazel_diff//:bazel-diff -- generate-hashes -o /tmp/end.json
      
      - name: Calculate diff
        id: diff
        run: |
          bazel run @bazel_diff//:bazel-diff -- get-changed-targets \
            --starting-hashes /tmp/start.json \
            --ending-hashes /tmp/end.json \
            --output /tmp/changed.txt
          
          echo "changed=$(cat /tmp/changed.txt | jq -R -s -c 'split("\n")[:-1]')" >> $GITHUB_OUTPUT
  
  plan-release:
    needs: detect-changes
    runs-on: ubuntu-latest
    steps:
      - name: Plan builds
        run: |
          echo '${{ needs.detect-changes.outputs.changed-targets }}' > /tmp/changed.json
          bazel run //tools:release -- plan-from-targets \
            --targets-file /tmp/changed.json \
            --format github
```

## Comparison with Current System

| Aspect | Current System | bazel-diff |
|--------|---------------|------------|
| **Lines of Code** | ~230 lines | ~50 lines (integration) |
| **Accuracy** | Known limitations | Bazel-native accuracy |
| **Transitive Deps** | May miss some | Always correct |
| **Edge Cases** | Manual handling | Automatic |
| **Performance** | Multiple queries | Two hashes + diff |
| **Maintainability** | Custom code | External tool |
| **Community Support** | Internal only | Active community |
| **Documentation** | Limited | Extensive |
| **Testing** | Custom tests | Tool tested by community |
| **Reliability** | Conservative fallbacks | Precise |

## Conclusion

**Recommendation: Adopt bazel-diff**

The current change detection system has served its purpose, but it's time to move to a better solution. bazel-diff offers:

✅ **Improved accuracy** - Fewer false negatives and positives
✅ **Reduced complexity** - Less custom code to maintain
✅ **Better reliability** - Battle-tested by the community
✅ **Future-proof** - Standard tool with active development
✅ **Easier debugging** - Well-documented with community support

The migration effort is reasonable (5-8 days) and can be done incrementally with minimal risk. The long-term benefits far outweigh the short-term investment.

## Next Steps

1. **Review this evaluation** with the team
2. **Get approval** to proceed with migration
3. **Start Phase 1** - Proof of concept
4. **Iterate** based on findings
5. **Complete migration** incrementally

## References

- [bazel-diff GitHub Repository](https://github.com/Tinder/bazel-diff)
- [bazel-diff Documentation](https://github.com/Tinder/bazel-diff/blob/master/README.md)
- [Current Implementation](../tools/release_helper/changes.py)
- [CI Workflow](../.github/workflows/ci.yml)
- [Release Workflow](../.github/workflows/release.yml)
