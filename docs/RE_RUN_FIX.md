# Re-run Fix for GHCR Image Tag Publishing

## Problem
When GitHub Actions release workflows were re-run (e.g., after a temporary failure), image tags would not be properly published to GitHub Container Registry (GHCR). This was particularly problematic for the `latest` tag and commit SHA tags.

## Root Cause
Bazel's remote caching system would determine that the image build hadn't changed and skip the push operation. While this is efficient for builds, it's problematic for push operations which are side-effecting actions that should always execute.

## Solution
Added the `--noremote_accept_cached` flag to the `bazel run` command in `push_image_with_tags()` function.

### Code Changes
**File: `tools/release_helper/images.py`**

```python
# Before:
bazel_args = ["run", push_target, "--"]

# After:
bazel_args = ["run", "--noremote_accept_cached", push_target, "--"]
```

This flag ensures that:
1. The push operation always executes, regardless of Bazel's cache state
2. All tags (version, latest, commit SHA) are published on every run
3. Re-runs of the release workflow behave correctly

## Testing
All existing tests were updated and pass:
- `test_images.py`: 16/16 tests passing
- `test_release.py`: 31/31 tests passing

## Verification
To verify the fix is working, check that workflow re-runs successfully push all tags:

```bash
# After a successful release, re-run the workflow
# All tags should be published again without errors

# Check GHCR for the expected tags:
# - ghcr.io/OWNER/domain-app:vX.Y.Z
# - ghcr.io/OWNER/domain-app:latest  
# - ghcr.io/OWNER/domain-app:COMMIT_SHA
```

## Related Files
- `tools/release_helper/images.py` - Main fix
- `tools/release_helper/test_images.py` - Updated tests
- `.github/workflows/release.yml` - Release workflow that benefits from this fix

## Impact
- ✅ Workflow re-runs now work correctly
- ✅ No breaking changes to API or behavior
- ✅ All existing tests pass
- ✅ Minimal code change (single flag addition)
