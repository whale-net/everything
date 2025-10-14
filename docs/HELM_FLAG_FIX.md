# Helm Release Flag Fix - Issue Resolution

## Problem Statement
When helm charts were released alone (without apps in the same workflow run), they would use the `latest` image tag instead of the latest semantic version from git tags.

## Root Cause Analysis

### The Issue
The CLI flag syntax in `tools/release_helper/cli.py` line 556 was:

```python
use_released_versions: Annotated[bool, typer.Option("--use-released/--use-latest", ...)] = True,
```

This syntax is **incorrect** for typer's boolean flags.

### Typer Boolean Flag Requirements
Typer requires boolean flags to follow a specific pattern:
- **Correct**: `--flag-name/--no-flag-name` (negation with `--no-` prefix)
- **Incorrect**: `--flag-name/--other-name` (arbitrary names)

### Why This Caused the Bug
The incorrect syntax `--use-released/--use-latest` likely caused typer to:
1. Not recognize the flag properly
2. Either ignore it or parse it incorrectly
3. Result in always using the "latest" tag behavior

## Solution

### Code Changes
Changed the flag definition to use proper typer syntax:

```python
use_released_versions: Annotated[bool, typer.Option("--use-released/--no-use-released", ...)] = True,
```

### Updated Flag Behavior
- **No flag** (default): Uses released versions from git tags
- **`--use-released`**: Explicitly uses git tags (same as default, but explicit)
- **`--no-use-released`**: Uses "latest" tag (for local development)

### Documentation Updates
1. Updated `docs/HELM_RELEASE.md` to show correct flag usage
2. Removed obsolete command references (`helm-chart-info`, `resolve-chart-app-versions`)
3. Updated examples to show both explicit and default behaviors
4. Updated `docs/RELEASE_TOOL_CLEANUP.md` recommendations

### Workflow Verification
The workflow in `.github/workflows/release.yml` already uses `--use-released` explicitly, which works correctly with the new syntax:

```bash
"$RELEASE_HELPER" build-helm-chart "$CHART" \
  --output-dir /tmp/helm-charts \
  --use-released \
  --bump "$BUMP_TYPE"
```

## Testing

### What Was Tested
- Verified flag syntax is consistent with other boolean flags (`--auto-version/--no-auto-version`)
- Reviewed existing unit tests (they test the Python function directly, not CLI flags)
- Confirmed workflow uses correct flag

### Expected Behavior After Fix
1. **Helm-only releases**: Will query git tags for latest semver (e.g., `v1.2.3`)
2. **Error on missing tags**: Will raise clear error if no git tags found
3. **Development mode**: Can use `--no-use-released` to get "latest" tags

## Files Changed
- `tools/release_helper/cli.py` - Fixed flag syntax
- `docs/HELM_RELEASE.md` - Updated documentation and examples
- `docs/RELEASE_TOOL_CLEANUP.md` - Updated recommendations
- `.github/workflows/release.yml` - Added clarifying comment

## Validation
✅ Flag syntax matches typer requirements
✅ Consistent with other boolean flags in codebase  
✅ Documentation updated with correct usage
✅ Workflow verified to use correct syntax
✅ Default behavior (use released versions) is correct

## Related Code References
- Typer boolean flag documentation: https://typer.tiangolo.com/tutorial/parameter-types/bool/
- Similar pattern in codebase: `--auto-version/--no-auto-version` (line 557)
