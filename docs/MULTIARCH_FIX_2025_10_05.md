# Multi-Architecture Image Fix - October 5, 2025

## Problem
The OCI image manifests published to GHCR were only containing AMD64 images, even though the build system was configured to build both AMD64 and ARM64 images. For example:
- https://github.com/whale-net/everything/pkgs/container/manman-status_api was missing ARM64 images

## Root Cause
The `oci_image_index` rule in `tools/container_image.bzl` was not specifying which platform to build each image for. This caused both `_amd64` and `_arm64` image targets to be built for the default platform (x86_64), resulting in an OCI manifest that only contained one architecture.

When Bazel built the image index, it would:
1. Build `status_api_image_amd64` → used default platform (x86_64)
2. Build `status_api_image_arm64` → used default platform (x86_64)
3. Create an index pointing to both → but both were actually x86_64 images!

The resulting manifest would have the correct structure but both platform entries would reference the same x86_64 image.

## Solution
Added the `platforms` attribute to the `oci_image_index` rule in `tools/container_image.bzl`:

```starlark
oci_image_index(
    name = name,
    images = [
        ":" + name + "_amd64",
        ":" + name + "_arm64",
    ],
    platforms = [
        "//tools:linux_x86_64",
        "//tools:linux_arm64",
    ],
    tags = ["manual"],
)
```

The `platforms` attribute is a list that corresponds one-to-one with the `images` list. Each platform label tells Bazel which platform constraint to use when building the corresponding image:
- First image (`_amd64`) is built with `--platforms=//tools:linux_x86_64`
- Second image (`_arm64`) is built with `--platforms=//tools:linux_arm64`

## Verification
To verify the fix works, check the published manifest on GHCR:

```bash
# Pull the manifest and inspect it
docker manifest inspect ghcr.io/whale-net/manman-status_api:v0.0.11

# Should show BOTH platforms:
# {
#   "manifests": [
#     {
#       "platform": {
#         "architecture": "amd64",
#         "os": "linux"
#       }
#     },
#     {
#       "platform": {
#         "architecture": "arm64",
#         "os": "linux"
#       }
#     }
#   ]
# }
```

Alternatively, try pulling on different architectures:
```bash
# On AMD64 machine
docker pull --platform linux/amd64 ghcr.io/whale-net/manman-status_api:v0.0.11

# On ARM64 machine  
docker pull --platform linux/arm64 ghcr.io/whale-net/manman-status_api:v0.0.11
```

Both should work without errors.

## Impact
- ✅ All future releases will include both AMD64 and ARM64 images
- ✅ Users can pull images on any supported architecture
- ✅ Kubernetes deployments on ARM64 nodes will work correctly
- ✅ No changes needed to existing `release_app` configurations

## Files Changed
- `tools/container_image.bzl` - Added `platforms` attribute to `oci_image_index`

## References
- GitHub Issue: Investigation of missing ARM64 images
- Workflow Run: https://github.com/whale-net/everything/actions/runs/18258565500
- rules_oci documentation: https://github.com/bazel-contrib/rules_oci
