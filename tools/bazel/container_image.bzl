"""Simplified OCI container image rules with multiplatform support.

ARCHITECTURE: Single Binary → Platform Transitions → Single Push
==================================================================

1. SINGLE BINARY TARGET
   - One py_binary or go_binary (no platform-specific variants)
   - Cross-compilation handled by Bazel + rules_pycross
   - Built for different platforms using --platforms flag

2. OCI IMAGE INDEX with Platform Transitions
   - Single {name} target that builds for all platforms via Bazel transitions
   - oci_image_index automatically builds base image for each platform
   - Creates proper OCI index manifest with platform metadata
   - Used by oci_push to push multi-arch images

3. LOAD TARGET (Local Testing Only)
   - {name}_load: Load image to local Docker
   - Must specify --platforms flag (e.g., --platforms=//tools:linux_arm64)
   - Used only for local development/testing

4. PUSH TARGET (Production Release)
   - {name}_push: Pushes OCI image index (contains ALL platforms)
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
    python_version = "3.13",
    **kwargs):
    """Build a single-platform OCI container image.
    
    Simple, clean wrapper around oci_image that uses hermetic toolchains.
    The same binary target will be built for different platforms when invoked
    with different --platforms flags.
    
    LAYERING STRATEGY - Optimized for Cache Efficiency:
    ====================================================
    Uses a SINGLE layer containing binary + all runfiles (dependencies, interpreter, etc).
    
    Why not split into multiple layers?
    - Bazel's hermetic runfiles tree is tightly coupled (binary references exact paths)
    - Splitting would require custom rules to separate app code from dependencies
    - Breaking apart the runfiles structure defeats Bazel's hermetic guarantees
    - The complexity and brittleness outweighs the caching benefits
    
    Caching still works efficiently because:
    1. Bazel's action cache: If binary unchanged, pkg_tar is cached (no rebuild)
    2. OCI layer digests: If tar unchanged, Docker/registries cache the layer
    3. Most rebuilds happen during development (where layer caching helps less anyway)
    
    The real optimization opportunity would be at the Bazel level (e.g., rules_python
    generating separate outputs for app vs deps), but that's outside our control here.
    
    Args:
        name: Image name
        binary: Binary target to containerize (single target, built for current platform)
        base: Base image (defaults to ubuntu:24.04)
        env: Environment variables dict
        entrypoint: Override entrypoint (auto-detected from language)
        language: Language of the binary ("python" or "go") - REQUIRED
        python_version: Python version for path construction (default: "3.13")
        **kwargs: Additional oci_image arguments
    """
    if not language:
        fail("language parameter is required for container_image")
    
    # Create single application layer with binary and all runfiles
    # This is a monolithic layer but it's the most maintainable approach given
    # Bazel's hermetic runfiles structure.
    pkg_tar(
        name = name + "_layer",
        srcs = [binary],
        package_dir = "/app",
        include_runfiles = True,
        tags = ["manual"],
    )
    
    binary_name = _get_binary_name(binary)
    image_env = env or {}
    
    # Add SSL_CERT_FILE environment variable for Python's SSL module
    # This points to the CA certificates bundle from rules_distroless
    if "SSL_CERT_FILE" not in image_env:
        image_env["SSL_CERT_FILE"] = "/etc/ssl/certs/ca-certificates.crt"
    
    # Determine entrypoint based on language
    if not entrypoint:
        if language == "python":
            # Use select() to choose the correct Python interpreter path based on target platform
            # The path is deterministic: /app/{binary}.runfiles/rules_python++python+python_{version}_{arch}/bin/python3
            # We use a simple shell pattern to match either architecture
            entrypoint = [
                "/bin/sh",
                "-c",
                # Use glob pattern to match the Python interpreter for the current architecture
                # This is much faster than find and the path is deterministic at build time
                'exec /app/{binary}.runfiles/rules_python++python+python_{version}_*/bin/python3 /app/$0 "$@"'.format(
                    binary = binary_name,
                    version = python_version.replace(".", "_"),
                ),
                binary_name,
            ]
        else:
            # Go binaries are self-contained executables
            entrypoint = ["/app/" + binary_name]
    
    oci_image(
        name = name,
        base = base,
        tars = [
            ":" + name + "_layer",
            "//tools/cacerts:cacerts",  # Add CA certificates for SSL/TLS
        ],
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
    """Build multiplatform OCI images using platform transitions.
    
    ARCHITECTURE: 1 Binary → Platform Transitions → 1 Index → 1 Push
    ==================================================================
    
    Generated Targets:
    
    BUILD TARGETS:
        {name}_base: Base oci_image (built per-platform via transitions)
        {name}: oci_image_index with platform transitions
            → Automatically builds {name}_base for each platform
            → Creates proper OCI index manifest with platform metadata
    
    LOAD TARGET (local testing - must specify platform):
        {name}_load: Load image to local Docker
            → Must use --platforms flag (e.g., --platforms=//tools:linux_arm64)
            → Creates: {image_name}:latest
            → Usage: bazel run //app:my_app_image_load --platforms=//tools:linux_arm64
        
    PUSH TARGET (production release - SINGLE TAG):
        {name}_push: Push OCI image index with ALL platforms
            → Publishes ONE tag containing both amd64 and arm64
            → Docker automatically serves correct architecture
            → Used by: bazel run //tools:release -- release my_app
            → Result: Users pull ONE tag, works on any platform
    
    Platform transitions handle cross-compilation automatically via Bazel's
    configuration system. No platform-specific targets needed!
    
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
        visibility = ["//visibility:public"],  # Allow cross-compilation test to access
    )
    
    # =======================================================================
    # LOAD TARGET: Local testing with explicit platform
    # =======================================================================
    # NOTE: oci_load doesn't support loading image indexes directly.
    # You must specify which Linux platform to load using --platforms flag.
    #
    # Usage:
    #   # On M1/M2 Macs (load Linux ARM64 image):
    #   bazel run //app:app_image_load --platforms=//tools:linux_arm64
    #
    #   # On Intel Macs/PCs (load Linux AMD64 image):
    #   bazel run //app:app_image_load --platforms=//tools:linux_x86_64
    #
    # Without the --platforms flag, you may get macOS binaries which won't run in Docker.
    # The platform flag ensures the image contains Linux binaries for Docker.
    #
    # Example test:
    #   bazel run //demo/hello_fastapi:hello-fastapi_image_load --platforms=//tools:linux_arm64
    #   docker run --rm -p 8000:8000 demo-hello-fastapi:latest
    #   curl http://localhost:8000/
    
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
