# Testing Guide: Rdeps Optimization

This guide explains how to test and verify the rdeps optimization works correctly.

## Quick Start

### 1. Run the Validation Demo
```bash
python3 validate_optimization.py
```
This shows a side-by-side comparison of the old vs new approach.

### 2. Run Unit Tests (when Bazel is available)
```bash
# Test helm chart detection
bazel test //tools/release_helper:test_detect_helm_charts

# Test optimization pattern
bazel test //tools/release_helper:test_rdeps_optimization

# Test all release helper tests
bazel test //tools/release_helper:all
```

## Manual Testing Scenarios

### Scenario 1: App Source Change
Test that changing an app's source file correctly detects that app.

```bash
# Create a test commit
echo "# test change" >> demo/hello_python/main.py
git add demo/hello_python/main.py
git commit -m "test: change app source"

# Detect changes (when Bazel is working)
bazel run //tools:release -- changes --base-commit=HEAD~1

# Expected output:
#   Changed files: demo/hello_python/main.py
#   Analyzing 1 changed files using Bazel query...
#   hello_python: affected by changes
```

### Scenario 2: Shared Library Change
Test that changing a shared library affects all dependent apps.

```bash
# Create a test commit
echo "# test change" >> libs/python/utils.py
git add libs/python/utils.py
git commit -m "test: change shared lib"

# Detect changes
bazel run //tools:release -- changes --base-commit=HEAD~1

# Expected output:
#   Changed files: libs/python/utils.py
#   Analyzing 1 changed files using Bazel query...
#   hello_python: affected by changes
#   hello_fastapi: affected by changes
#   (all apps using this library)
```

### Scenario 3: Helm Chart Detection
Test that helm chart changes are correctly detected.

```bash
# Create a test commit
echo "# test change" >> demo/BUILD.bazel
git add demo/BUILD.bazel
git commit -m "test: change chart BUILD"

# Detect changed charts
bazel run //tools:release -- plan-helm-release --base-commit=HEAD~1

# Expected output:
#   Changed files: demo/BUILD.bazel
#   Analyzing 1 changed files using Bazel query...
#   fastapi-chart: affected by changes
#   multi-app-chart: affected by changes
```

### Scenario 4: No Changes
Test that no changes are detected when files haven't changed.

```bash
# Try to detect changes with no actual changes
bazel run //tools:release -- changes --base-commit=HEAD

# Expected output:
#   No files changed, no apps need to be built
```

### Scenario 5: Ignored Files
Test that changes to documentation/workflows don't trigger builds.

```bash
# Change a documentation file
echo "# test" >> README.md
git add README.md
git commit -m "docs: update readme"

# Detect changes
bazel run //tools:release -- changes --base-commit=HEAD~1

# Expected output:
#   Changed files: README.md
#   Filtered out 1 non-build files (workflows, docs, etc.)
#   All changed files are non-build artifacts. No apps need to be built.
```

## Verification Checklist

### Correctness Verification
- [ ] App changes are correctly detected
- [ ] Shared library changes affect all dependent apps
- [ ] Helm chart changes are correctly detected
- [ ] Documentation changes don't trigger builds
- [ ] BUILD file changes affect entire package
- [ ] Deleted files don't cause errors

### Performance Verification
You can verify the optimization by checking Bazel query output:

```bash
# Set verbose logging
export BAZEL_VERBOSE=1

# Run change detection and observe queries
bazel run //tools:release -- changes --base-commit=HEAD~1 2>&1 | grep "rdeps"

# You should see:
# - ONE query with "rdeps" that does NOT use "rdeps(//..., ...)"
# - The query should scope to metadata targets
```

### Integration Testing

#### CI Workflow Simulation
```bash
# Simulate PR workflow
BASE_COMMIT="origin/main"

# Plan Docker release
bazel run //tools:release -- plan \
  --event-type=pull_request \
  --base-commit=$BASE_COMMIT \
  --format=github

# Plan Helm release
bazel run //tools:release -- plan-helm-release \
  --base-commit=$BASE_COMMIT \
  --format=github
```

#### Multi-Domain Testing
```bash
# Test with demo domain changes
echo "# test" >> demo/hello_python/main.py
git add . && git commit -m "test: demo change"
bazel run //tools:release -- changes --base-commit=HEAD~1

# Test with manman domain changes
echo "# test" >> manman/src/host/main.py
git add . && git commit -m "test: manman change"
bazel run //tools:release -- changes --base-commit=HEAD~1
```

## Troubleshooting

### If tests fail with "No module named pytest"
```bash
# Install pytest
pip install pytest

# Or use bazel to run tests
bazel test //tools/release_helper:test_detect_helm_charts
```

### If Bazel queries fail
```bash
# Check Bazel is installed
bazel version

# Try a simple query
bazel query "//..."

# Check network connectivity (BCR access)
curl -I https://bcr.bazel.build/
```

### If change detection returns unexpected results
```bash
# Check git diff
git diff --name-only HEAD~1

# Verify files are not in .gitignore
git check-ignore -v <file>

# Test file filtering
python3 -c "
from tools.release_helper.changes import _should_ignore_file
print(_should_ignore_file('demo/main.py'))  # Should be False
print(_should_ignore_file('README.md'))     # Should be True
"
```

## Expected Test Results

### Unit Tests
When running the test suite, you should see:
```
//tools/release_helper:test_detect_helm_charts          PASSED
//tools/release_helper:test_rdeps_optimization          PASSED
//tools/release_helper:test_changes_git                 PASSED
```

### Integration Tests
When testing manually with real commits:
- Changes to app source files should detect only that app
- Changes to shared libraries should detect multiple apps
- Changes to BUILD files should detect the entire package
- Changes to docs/workflows should not trigger builds

## Performance Comparison

To see the performance improvement:

```bash
# Old approach (hypothetical - this isn't actually used anymore)
# Would take 2-5 seconds on a large repo:
# bazel query "rdeps(//..., //demo/main.py)"  # Scans all 1000+ targets

# New approach (what we use now)
# Takes 100-500ms on a large repo:
# bazel query "rdeps(<metadata_set>, //demo/main.py)"  # Scans only ~20 targets
```

## Success Criteria

✅ All unit tests pass
✅ Manual scenarios work as expected
✅ No false positives (unchanged apps not detected)
✅ No false negatives (changed apps are detected)
✅ Performance is noticeably faster on large changesets
✅ Backward compatibility maintained (existing workflows work)

## Next Steps

After validation:
1. Clean up any test commits: `git reset --hard HEAD~N`
2. Run full test suite: `bazel test //...`
3. Deploy to CI and monitor performance improvements
