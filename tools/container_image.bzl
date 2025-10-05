"""Simplified OCI container image rules with multiplatform support.

ARCHITECTURE: Single Binary → Single Load → Single Push
================================================================

1. SINGLE BINARY TARGET
   - One py_binary or go_binary (no platform-specific variants)
   - Cross-compilation handled by Bazel + rules_pycross
   - Built for different platforms using --platforms flag

2. SINGLE LOAD TARGET (Local Testing)
   - {name}_load: Load image to local Docker
   - Builds for host platform by default
   - Use --platforms flag for cross-platform testing:
     bazel run //app:app_image_load --platforms=//tools:linux_x86_64
     bazel run //app:app_image_load --platforms=//tools:linux_arm64

3. SINGLE PUSH TARGET (Production Release)
   - {name}_push: Pushes OCI image index (contains BOTH platforms)
   - Single manifest automatically serves correct architecture
   - Users pull ONE tag that works on any platform
   - Release system ONLY uses this target

MULTIPLATFORM BUILD (rules_oci approach):
==========================================
The oci_image_index uses a single base image with a platforms attribute.
rules_oci automatically builds that image for each specified platform:
  - Builds {name}_image with --platforms=//tools:linux_x86_64
  - Builds {name}_image with --platforms=//tools:linux_arm64
  - Creates a manifest index pointing to both platform-specific builds

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
    
    ARCHITECTURE: 1 Binary → 1 Load Target (local) → 1 Push Target (release)
    ===========================================================================
    
    Generated Targets:
    
    BUILD TARGETS (intermediate, not directly used):
        {name}_image: Base oci_image (platform-agnostic definition)
        {name}: oci_image_index that builds _image for multiple platforms
    
    LOAD TARGET (local testing):
        {name}_load: Load image to local Docker
            → Creates: {image_name}:latest
            → Usage: bazel run //app:my_app_image_load
            → Builds for host platform by default
            → For cross-platform: add --platforms=//tools:linux_x86_64 or linux_arm64
    
    PUSH TARGET (production release - SINGLE TAG):
        {name}_push: Push OCI image index with BOTH platforms
            → Publishes ONE tag containing both amd64 and arm64
            → Docker automatically serves correct architecture
            → Used by: bazel run //tools:release -- release-multiarch
            → Result: Users pull ONE tag, works on any platform
    
    NOTE: The oci_image_index automatically builds the base image for each platform
    using the platforms attribute. No need for separate platform-specific targets.
    
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
    
    # Build a single base image
    # The oci_image_index will build this for multiple platforms
    container_image(
        name = name + "_image",
        binary = binary,
        base = base,
        language = language,
        **kwargs
    )
    
    # Create multiplatform manifest list
    # Using platforms attribute with a single image tells rules_oci to build
    # that image for each specified platform automatically
    oci_image_index(
        name = name,
        images = [
            ":" + name + "_image",
        ],
        platforms = [
            "//tools:linux_x86_64",
            "//tools:linux_arm64",
        ],
        tags = ["manual"],
    )
    
    # =======================================================================
    # LOAD TARGETS: Local testing only (NOT used in production releases)
    # =======================================================================
    # These load the base image (built for the host platform) to local Docker.
    # For cross-platform testing, use the platform-specific build flags:
    #   bazel run //app:app_image_load --platforms=//tools:linux_x86_64
    #   bazel run //app:app_image_load --platforms=//tools:linux_arm64
    #
    # These are NEVER used by the release system.
    oci_load(
        name = name + "_load",
        image = ":" + name + "_image",
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
