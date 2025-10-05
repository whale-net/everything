"""Simplified OCI container image rules with multiplatform support.

SIMPLIFIED APPROACH:
- Single py_binary or go_binary target (no platform-specific variants needed)
- Build for different platforms using Bazel's --platforms flag
- Combine platform-specific images using oci_image_index
- Cross-compilation handled automatically by Bazel and rules_pycross

This is the idiomatic Bazel way to build multiplatform images.
"""

load("@rules_oci//oci:defs.bzl", "oci_image", "oci_image_index", "oci_load", "oci_push")
load("@rules_pkg//:pkg.bzl", "pkg_tar")

def _get_binary_name(binary):
    """Extract binary name from label."""
    if ":" in binary:
        return binary.split(":")[-1]
    return binary.split("/")[-1]

def container_image(
    name,
    binary,
    base = "@ubuntu_base",
    env = None,
    entrypoint = None,
    language = None,
    **kwargs):
    """Build a single-platform OCI container image.
    
    Simple, clean wrapper around oci_image that uses hermetic toolchains.
    The same binary target will be built for different platforms when invoked
    with different --platforms flags.
    
    Args:
        name: Image name
        binary: Binary target to containerize (single target, built for current platform)
        base: Base image (defaults to ubuntu:24.04)
        env: Environment variables dict
        entrypoint: Override entrypoint (auto-detected from language)
        language: Language of the binary ("python" or "go") - REQUIRED
        **kwargs: Additional oci_image arguments
    """
    if not language:
        fail("language parameter is required for container_image")
    
    # Create application layer with runfiles
    pkg_tar(
        name = name + "_layer",
        srcs = [binary],
        package_dir = "/app",
        include_runfiles = True,
        tags = ["manual"],
    )
    
    binary_name = _get_binary_name(binary)
    image_env = env or {}
    
    # Determine entrypoint based on language
    if not entrypoint:
        if language == "python":
            # Find hermetic Python interpreter in runfiles
            entrypoint = [
                "/bin/sh",
                "-c",
                'PYTHON=$(find /app -path "*/rules_python*/bin/python3" -type f 2>/dev/null | head -1) && exec "$PYTHON" "/app/$1"',
                "sh",
                binary_name,
            ]
        else:
            # Go binaries are self-contained executables
            entrypoint = ["/app/" + binary_name]
    
    oci_image(
        name = name,
        base = base,
        tars = [":" + name + "_layer"],
        entrypoint = entrypoint,
        workdir = "/app",
        env = image_env,
        tags = ["manual"],
        **kwargs
    )

def multiplatform_image(
    name,
    binary,
    base = "@ubuntu_base",
    registry = "ghcr.io",
    repository = None,
    image_name = None,
    language = None,
    **kwargs):
    """Build multiplatform OCI images using a single binary target.
    
    SIMPLIFIED APPROACH - The Idiomatic Bazel Way:
    Takes a single binary target and builds it for multiple platforms.
    
    NOTE: Platform-specific images must be built with explicit --platforms flag:
        bazel build //app:my_app_image_amd64 --platforms=//tools:linux_x86_64
        bazel build //app:my_app_image_arm64 --platforms=//tools:linux_arm64
    
    The release system handles this automatically. For local development,
    use the load targets which are configured for the correct platforms.
    
    Usage (Python or Go):
        multiplatform_image(
            name = "my_app_image",
            binary = ":my_app",  # Single binary target
            image_name = "demo-my_app",
            language = "python",  # or "go"
        )
    
    Local development:
        # These are configured to build for correct platform automatically
        bazel run //app:my_app_image_amd64_load
        bazel run //app:my_app_image_arm64_load
    
    Args:
        name: Base name for all generated targets
        binary: Single binary target (built for different platforms via --platforms flag)
        base: Base image (defaults to ubuntu:24.04)
        registry: Container registry (defaults to ghcr.io)
        repository: Organization/namespace (e.g., "whale-net")
        image_name: Image name in domain-app format (e.g., "demo-my_app") - REQUIRED
        language: Language of binary ("python" or "go") - REQUIRED
        **kwargs: Additional arguments passed to container_image
    """
    if not binary:
        fail("binary parameter is required")
    if not language:
        fail("language parameter is required")
    if not image_name:
        fail("image_name parameter is required for multiplatform_image")
    
    # Build platform-specific images using the SAME binary target
    # These must be built with explicit --platforms flag
    container_image(
        name = name + "_amd64",
        binary = binary,
        base = base,
        language = language,
        **kwargs
    )
    
    container_image(
        name = name + "_arm64",
        binary = binary,
        base = base,
        language = language,
        **kwargs
    )
    
    # Create multiplatform manifest list
    # Note: oci_image_index takes a list of images, not a dict
    # The platform info comes from the individual oci_image targets
    oci_image_index(
        name = name,
        images = [
            ":" + name + "_amd64",
            ":" + name + "_arm64",
        ],
        tags = ["manual"],
    )
    
    # Load targets - Bazel doesn't support select() in oci_load attributes,
    # so we need separate targets that can be called with --platforms flag
    # bazel run //app:app_image_amd64_load --platforms=//tools:linux_x86_64
    # bazel run //app:app_image_arm64_load --platforms=//tools:linux_arm64
    oci_load(
        name = name + "_amd64_load",
        image = ":" + name + "_amd64",
        repo_tags = [image_name + "-amd64:latest"],
        tags = ["manual"],
    )
    
    oci_load(
        name = name + "_arm64_load",
        image = ":" + name + "_arm64",
        repo_tags = [image_name + "-arm64:latest"],
        tags = ["manual"],
    )
    
    # Push target for release (OCI image index with both platforms)
    if repository and image_name:
        repo_path = registry + "/" + repository + "/" + image_name
        
        oci_push(
            name = name + "_push",
            image = ":" + name,
            repository = repo_path,
            remote_tags = [],
            tags = ["manual"],
        )
