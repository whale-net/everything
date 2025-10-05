"""Simplified OCI container image rules with multiplatform support.

ARCHITECTURE: Single Binary → Multiple Loads → Single Push
================================================================

1. SINGLE BINARY TARGET
   - One py_binary or go_binary (no platform-specific variants)
   - Cross-compilation handled by Bazel + rules_pycross
   - Built for different platforms using --platforms flag

2. MULTIPLE LOAD TARGETS (Local Testing Only)
   - {name}_amd64_load: Load AMD64 image to local Docker
   - {name}_arm64_load: Load ARM64 image to local Docker
   - Separate targets needed due to oci_load rule limitations
   - Used only for local development/testing
   - Creates platform-specific tags (e.g., demo-app-amd64:latest)

3. SINGLE PUSH TARGET (Production Release)
   - {name}_push: Pushes OCI image index (contains BOTH platforms)
   - Single manifest automatically serves correct architecture
   - Users pull ONE tag that works on any platform
   - Release system ONLY uses this target

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
    
    ARCHITECTURE: 1 Binary → 2 Load Targets (local) → 1 Push Target (release)
    ===========================================================================
    
    Generated Targets:
    
    BUILD TARGETS (intermediate, not directly used):
        {name}_amd64: Platform-specific oci_image (AMD64)
        {name}_arm64: Platform-specific oci_image (ARM64)
        {name}: oci_image_index combining both platforms
    
    LOAD TARGETS (local testing only):
        {name}_amd64_load: Load AMD64 image to local Docker
            → Creates: {image_name}-amd64:latest
            → Usage: bazel run //app:my_app_image_amd64_load
        
        {name}_arm64_load: Load ARM64 image to local Docker
            → Creates: {image_name}-arm64:latest
            → Usage: bazel run //app:my_app_image_arm64_load
    
    PUSH TARGET (production release - SINGLE TAG):
        {name}_push: Push OCI image index with BOTH platforms
            → Publishes ONE tag containing both amd64 and arm64
            → Docker automatically serves correct architecture
            → Used by: bazel run //tools:release -- release-multiarch
            → Result: Users pull ONE tag, works on any platform
    
    NOTE: Platform-specific images must be built with explicit --platforms flag:
        bazel build //app:my_app_image_amd64 --platforms=//tools:linux_x86_64
        bazel build //app:my_app_image_arm64 --platforms=//tools:linux_arm64
    
    The release system handles this automatically.
    
    Usage Example:
        multiplatform_image(
            name = "my_app_image",
            binary = ":my_app",  # Single binary target
            image_name = "demo-my_app",
            language = "python",  # or "go"
        )
    
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
    # Platform transitions: Bazel builds the base image once per platform
    container_image(
        name = name + "_base",
        binary = binary,
        base = base,
        language = language,
        **kwargs
    )
    
    # Create multiplatform manifest list using platform transitions
    # The platforms parameter triggers Bazel's configuration transition:
    # - Builds _base image for each platform automatically
    # - Creates proper OCI index with platform metadata
    # 
    # NOTE ON STRUCTURE: This creates a nested index (outer index -> inner index)
    # - Outer index: Points to the inner index blob
    # - Inner index: Contains platform-specific manifests with proper metadata
    # - This is valid per OCI spec and supported by Docker/container registries
    # - When pushed, Docker resolves through the nesting to get the right platform
    #
    # This nested structure is the expected behavior when using platform transitions
    # and is more maintainable than manually creating platform-specific image targets.
    oci_image_index(
        name = name,
        images = [
            ":" + name + "_base",
        ],
        platforms = [
            "//tools:linux_x86_64",
            "//tools:linux_arm64",
        ],
        tags = ["manual"],
    )
    
    # =======================================================================
    # LOAD TARGET: Local testing (native platform only)
    # =======================================================================
    # NOTE: oci_load doesn't support loading image indexes directly.
    # This loads only the base image for your native platform.
    # To test multiarch, use the push target and pull from registry.
    #
    # Usage:
    #   bazel run //app:app_image_load  # Loads for your current platform
    #
    # This is NEVER used by the release system - only for local dev/testing.
    oci_load(
        name = name + "_load",
        image = ":" + name + "_base",
        repo_tags = [image_name + ":latest"],
        tags = ["manual"],
    )
    
    # =======================================================================
    # PUSH TARGET: Production releases (SINGLE TAG with both platforms)
    # =======================================================================
    # Pushes the OCI image index which contains BOTH amd64 and arm64 images.
    # Docker automatically serves the correct architecture when users pull.
    #
    # Usage (via release tool):
    #   bazel run //tools:release -- release-multiarch my_app --version v1.0.0
    #
    # Result: ONE tag published (e.g., ghcr.io/owner/app:v1.0.0)
    #         Manifest contains both platforms, Docker auto-selects correct one
    if repository and image_name:
        repo_path = registry + "/" + repository + "/" + image_name
        
        oci_push(
            name = name + "_push",
            image = ":" + name,
            repository = repo_path,
            remote_tags = [],
            tags = ["manual"],
        )
