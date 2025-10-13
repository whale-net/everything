# GHCR Tag Publishing Bug Fix - Verification Guide

## Issue Summary
The release pipeline was tagging the wrong artifact when multiple apps shared the same name across different domains. This violated the `domain-app` naming invariant and could cause significant data corruption.

## Root Cause
- `find_app_bazel_target()` only matched by app `name` and returned the FIRST match
- Multiple helper functions had similar bugs
- The workflow passed short app names instead of full `domain-app` names

## Fix Applied
All code paths now use the `validate_apps()` function which:
- Supports full format: `domain-app` (e.g., "demo-hello_python")
- Supports path format: `domain/app` (e.g., "demo/hello_python")  
- Supports short format: `app` (e.g., "hello_python") - only if unambiguous
- Raises clear error if name is ambiguous

## Verification Steps

### 1. Unit Tests
Run the existing test suite to verify the fix:

```bash
# Run all release helper tests
bazel test //tools/release_helper:all

# Run specific test for find_app_bazel_target
bazel test //tools/release_helper:test_release --test_filter="*find_app_bazel_target*"

# Run validation tests
bazel test //tools/release_helper:test_validation
```

### 2. CLI Testing
Test the CLI commands with different naming formats:

```bash
# Test with full domain-app format (recommended)
bazel run //tools:release -- build demo-hello_python

# Test with path format
bazel run //tools:release -- build demo/hello_python

# Test with short format (should work if unambiguous)
bazel run //tools:release -- build hello_python

# Test that ambiguous names are rejected (if you have collisions)
# This should fail with a clear error message
bazel run //tools:release -- build hello_python  # If hello_python exists in multiple domains
```

### 3. Integration Testing
Test the full release workflow in a safe environment:

```bash
# Plan a release with specific apps (dry run)
bazel run //tools:release -- plan \
  --event-type workflow_dispatch \
  --apps demo-hello_python \
  --version v99.99.99 \
  --format json

# Test multiarch release (dry run)
bazel run //tools:release -- release-multiarch demo-hello_python \
  --version v99.99.99 \
  --dry-run
```

### 4. GitHub Actions Workflow Testing
Test the workflow with manual dispatch:

1. Go to Actions tab in GitHub
2. Select "Release" workflow
3. Click "Run workflow"
4. Configure:
   - apps: `demo-hello_python,demo-hello_go`
   - version: `v99.99.99`
   - dry_run: `true` ✓ (important for testing!)
5. Monitor the workflow logs to verify:
   - Correct apps are selected
   - Full domain-app names are used
   - No ambiguity errors occur

### 5. Validation Checklist
- [ ] Unit tests pass for find_app_bazel_target
- [ ] Ambiguous name test properly rejects duplicates
- [ ] Full domain-app format works in CLI
- [ ] Path format (domain/app) works in CLI
- [ ] Short format works for unambiguous names
- [ ] Workflow uses full domain-app names
- [ ] Release notes generation uses full names
- [ ] OpenAPI builds use full names
- [ ] GitHub release creation uses full names

## Expected Behavior

### Before Fix
```bash
# If demo/hello_python and api/hello_python both exist:
$ bazel run //tools:release -- build hello_python
# Would silently select the FIRST match (wrong!)
# No error, no warning, just wrong artifact
```

### After Fix
```bash
# Same scenario - now properly handles ambiguity:
$ bazel run //tools:release -- build hello_python
Error: Invalid apps: hello_python (ambiguous, could be: demo-hello_python, api-hello_python).

# Use full format to disambiguate:
$ bazel run //tools:release -- build demo-hello_python
✓ Building demo-hello_python...

# Or use path format:
$ bazel run //tools:release -- build demo/hello_python
✓ Building demo-hello_python...
```

## Rollout Strategy

1. **Immediate**: This fix should be merged immediately as it prevents data corruption
2. **Communication**: Notify all developers about the new naming requirement
3. **Documentation**: Update release documentation to emphasize using full names
4. **Monitoring**: Watch the first few releases after deployment for any issues

## Prevention

To prevent similar bugs in the future:

1. **Always use `validate_apps()`**: Never manually iterate and match by name
2. **Use full names in workflows**: Always pass `$DOMAIN-$APP` not just `$APP`
3. **Test with collisions**: Add test cases with apps that have same names in different domains
4. **Code review**: Look for patterns like `app['name'] == ...` in reviews

## Related Files
- `tools/release_helper/release.py` - Core fix
- `tools/release_helper/validation.py` - validate_apps() implementation
- `.github/workflows/release.yml` - Workflow updates
- `tools/release_helper/github_release.py` - GitHub release fixes
- `tools/release_helper/release_notes.py` - Release notes fixes
- `tools/release_helper/cli.py` - CLI fixes

## Contact
For questions or issues with this fix, contact the infrastructure team or open an issue.
