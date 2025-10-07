# Image Tagging Optimization

## Overview

The release process has been optimized to avoid rebuilding container images when releasing the same commit multiple times with different version tags. This significantly reduces build times and resource usage.

## How It Works

### Before Optimization

Previously, every release would:
1. Build the container image from scratch
2. Push the image with all tags (commit, version, latest)

This meant that if you released the same commit as `v1.0.0`, then later as `v1.0.1`, both releases would rebuild the exact same image.

### After Optimization

Now, the release process:
1. **Checks** if an image with the commit SHA tag already exists in the registry
2. **If exists**: Re-tags the existing image with the new version tags (instant operation)
3. **If not exists**: Builds and pushes the image as before (fallback)

### Benefits

- **Faster releases**: Re-tagging is nearly instant vs. rebuilding which can take minutes
- **Reduced resource usage**: No need to rebuild the same image multiple times
- **Same reliability**: Falls back to rebuilding if re-tagging fails for any reason

## Implementation Details

### Functions Added

#### `check_image_exists_in_registry(image_ref: str) -> bool`
Location: `tools/release_helper/validation.py`

Checks if a specific image tag exists in the container registry using `docker manifest inspect`.

#### `tag_existing_image(source_tag: str, target_tags: List[str]) -> None`
Location: `tools/release_helper/images.py`

Re-tags an existing image with additional tags using `docker buildx imagetools create`. Falls back to `pull + tag + push` if buildx is not available.

### Functions Modified

#### `tag_and_push_image()`
Location: `tools/release_helper/release.py`

Now checks for existing commit-tagged images before building. If found, re-tags instead of rebuilding.

#### `release_multiarch_image()`
Location: `tools/release_helper/images.py`

Applied the same optimization for multi-architecture releases (used in CI/CD workflows).

## Usage Examples

### Scenario 1: First Release of a Commit

```bash
# First release - image doesn't exist yet
bazel run //tools:release -- release-multiarch hello_python --version v1.0.0 --commit abc123def
# Output: No existing image found for commit abc123d, will build
# → Builds and pushes image with tags: v1.0.0, latest, abc123def
```

### Scenario 2: Re-releasing the Same Commit

```bash
# Later, release the same commit with a different version
bazel run //tools:release -- release-multiarch hello_python --version v1.0.1 --commit abc123def
# Output: ✅ Found existing image for commit abc123d: ghcr.io/owner/demo-hello_python:abc123def
# Output: Optimizing: Re-tagging existing image instead of rebuilding
# → Re-tags existing image with v1.0.1 and latest (takes seconds instead of minutes)
```

### Scenario 3: Fallback on Failure

```bash
# If re-tagging fails for any reason
bazel run //tools:release -- release-multiarch hello_python --version v1.0.2 --commit abc123def
# Output: Failed to tag existing image, falling back to rebuild
# → Builds and pushes image normally
```

## CI/CD Integration

The optimization is automatically used in GitHub Actions workflows. When you release an app, the workflow:

1. Passes the commit SHA via `--commit ${{ github.sha }}`
2. The release helper checks for existing images automatically
3. Re-tags if possible, rebuilds if necessary

No changes to workflow files are needed - the optimization is transparent.

## Requirements

### For Optimal Performance (Re-tagging)
- Docker with `buildx` support (available in modern Docker versions)
- Access to the container registry to check for existing images

### For Fallback (Rebuilding)
- Standard Docker installation
- Bazel build environment

## Technical Notes

### Image Uniqueness by Commit SHA

The optimization relies on commit SHA tags being unique and immutable:
- Each commit SHA corresponds to exactly one build of the image
- If an image with tag `abc123def` exists, it's guaranteed to be the same build
- Re-tagging just creates new manifest references to the same image layers

### Multi-Architecture Support

The optimization works with multi-architecture images:
- The commit tag points to an OCI image index
- The index contains all platform variants (amd64, arm64)
- Re-tagging the index automatically applies to all architectures

### Registry Requirements

The optimization uses `docker manifest inspect` which:
- Works with Docker Hub, GitHub Container Registry (GHCR), and most OCI-compliant registries
- Doesn't download the image (only checks metadata)
- Requires read access to the registry

## Troubleshooting

### "No existing image found" when it should exist

**Possible causes:**
1. Different commit SHA was used in previous releases
2. Registry credentials not properly configured
3. Image was deleted or unpublished

**Solution:** The process will fall back to rebuilding automatically.

### "Failed to tag existing image"

**Possible causes:**
1. Docker buildx not installed or not enabled
2. Network issues accessing the registry
3. Insufficient permissions to create new tags

**Solution:** The process will fall back to rebuilding automatically.

### Re-tagging takes longer than expected

If `docker buildx imagetools` is not available, the fallback method is used:
1. Pull the source image (can be large for multi-arch images)
2. Tag locally
3. Push each new tag

This is still faster than rebuilding but not as fast as using buildx.

## Future Enhancements

Potential improvements for future iterations:

1. **Smart cache invalidation**: Detect when dependencies change and skip optimization
2. **Batch re-tagging**: When releasing multiple apps with the same commit, optimize all at once
3. **Registry-native operations**: Use registry API directly instead of Docker CLI

## References

- [Docker Buildx Imagetools Documentation](https://docs.docker.com/engine/reference/commandline/buildx_imagetools/)
- [OCI Image Spec](https://github.com/opencontainers/image-spec)
- [Docker Manifest Inspect](https://docs.docker.com/engine/reference/commandline/manifest_inspect/)
