"""Clean container image rules for multiplatform builds.

IMPORTANT: Platform-specific dependencies and cross-compilation
-----------------------------------------------------------
While Python code itself is platform-agnostic, many packages (like pydantic, numpy, 
pillow) have compiled C extensions that are platform-specific. These packages distribute
"wheels" for different platforms (e.g., pydantic_core-2.33.2-cp311-manylinux_x86_64.whl
vs pydantic_core-2.33.2-cp311-manylinux_aarch64.whl).

CURRENT LIMITATION:
Pycross selects wheels based on the BUILD HOST platform at analysis time, not the target
platform. This means if you build on AMD64, both the AMD64 and ARM64 containers will
get AMD64 wheels, which won't work on ARM64 hardware.

WORKAROUND:
Until we implement proper platform transitions, apps with compiled dependencies should:
1. Build images on the actual target platform (build AMD64 on AMD64, ARM64 on ARM64), OR  
2. Use multiplatform_py_binary to create separate binaries (keeps old behavior), OR
3. Accept that cross-architecture images won't work (pure Python only)

For apps with only pure Python dependencies, the current approach works perfectly.

TODO: Implement platform transition in multiplatform_image() to fix this properly.
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
    
    For Python binaries, this uses the hermetic Python interpreter bundled by Bazel,
    avoiding the need for system Python in the base image.
    
    Args:
        name: Image name
        binary: Binary target to containerize
        base: Base image (defaults to ubuntu:24.04)
        env: Environment variables dict
        entrypoint: Override entrypoint (auto-detected from binary if not set)
        language: Language of the binary ("python" or "go") - auto-detected if not set
        **kwargs: Additional oci_image arguments
    """
    
    # Create application layer with runfiles
    pkg_tar(
        name = name + "_layer",
        srcs = [binary],
        package_dir = "/app",
        include_runfiles = True,
        tags = ["manual"],
    )
    
    # Auto-detect language from binary name if not specified
    binary_name = _get_binary_name(binary)
    if not language:
        # Python binaries have platform suffixes
        if "_linux_amd64" in binary_name or "_linux_arm64" in binary_name:
            language = "python"
        else:
            language = "go"
    
    # Set up environment
    image_env = env or {}
    
    # Determine entrypoint based on language
    if not entrypoint:
        if language == "python":
            # Use a shell script to dynamically find hermetic Python in runfiles
            # This works regardless of binary name or Python version
            #
            # The wrapper script expects to find its runfiles directory by its own name,
            # so we pass the actual binary name (which matches the runfiles dir)
            entrypoint = [
                "/bin/sh",
                "-c",
                # Find hermetic python3 in any rules_python runfiles directory
                'PYTHON=$(find /app -path "*/rules_python*/bin/python3" -type f 2>/dev/null | head -1) && exec "$PYTHON" /app/{binary}'.format(
                    binary = binary_name,
                ),
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
    language = None,
    **kwargs):
    """Build multiplatform OCI images from a single binary.
    
    IMPORTANT LIMITATION - Platform-specific dependencies:
    This function currently builds the SAME binary for both AMD64 and ARM64 images.
    Pycross selects wheels based on the BUILD HOST platform, NOT the target platform.
    
    This means:
    - Pure Python apps: ✅ Work perfectly on all platforms
    - Apps with compiled deps (pydantic, numpy, pillow, etc): ⚠️  Only work on build host platform
    
    For apps with compiled dependencies, you MUST either:
    1. Use multiplatform_py_binary (creates separate *_linux_amd64 and *_linux_arm64 binaries)
    2. Build on the actual target platform (cross-platform builds won't work)
    
    TODO: Add platform transition support to fix this properly.
    
    For local development:
        bazel run //app:my_app_image_load
        # Loads image tagged with clean name (fast!)
    
    For explicit platform:
        bazel run //app:my_app_image_amd64_load
        bazel run //app:my_app_image_arm64_load
    
    For release (builds both + manifest):
        bazel run //app:my_app_image_push -- v1.0.0
        # Pushes AMD64, ARM64, and creates manifest list
    
    Args:
        name: Base name for all generated targets
        binary: Binary target to containerize (e.g., ":my_app")
        base: Base image (defaults to ubuntu:24.04)
        registry: Container registry (defaults to ghcr.io)
        repository: Repository path (e.g., "whale-net/my-app")
        language: Language of binary ("python" or "go") - passed to container_image
        **kwargs: Additional arguments passed to container_image
    """
    
    if not binary:
        fail("Must provide binary")
    
    # Build platform-specific images from the same binary
    # The binary name determines the platform in the image tag
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
    
    # Create OCI manifest list (multiplatform manifest)
    oci_image_index(
        name = name,
        images = [
            ":" + name + "_amd64",
            ":" + name + "_arm64",
        ],
        tags = ["manual"],
    )
    
    # Local load targets - simple names without registry
    binary_name = _get_binary_name(binary)
    
    # Main load target - uses AMD64 (most common dev environment)
    oci_load(
        name = name + "_load",
        image = ":" + name + "_amd64",
        repo_tags = [binary_name + ":latest"],
        tags = ["manual"],
    )
    
    # Platform-specific load targets
    oci_load(
        name = name + "_amd64_load",
        image = ":" + name + "_amd64",
        repo_tags = [binary_name + "_amd64:latest"],
        tags = ["manual"],
    )
    
    oci_load(
        name = name + "_arm64_load",
        image = ":" + name + "_arm64",
        repo_tags = [binary_name + "_arm64:latest"],
        tags = ["manual"],
    )
    
    # Push targets for release
    if repository:
        repo_path = registry + "/" + repository
        
        # Push manifest list (includes both platforms)
        oci_push(
            name = name + "_push",
            image = ":" + name,
            repository = repo_path,
            remote_tags = ["latest", "{BUILD_TAG}"],
            tags = ["manual"],
        )
        
        # Individual platform push targets (for debugging/testing)
        oci_push(
            name = name + "_amd64_push",
            image = ":" + name + "_amd64",
            repository = repo_path,
            remote_tags = ["latest-amd64", "{BUILD_TAG}-amd64"],
            tags = ["manual"],
        )
        
        oci_push(
            name = name + "_arm64_push",
            image = ":" + name + "_arm64",
            repository = repo_path,
            remote_tags = ["latest-arm64", "{BUILD_TAG}-arm64"],
            tags = ["manual"],
        )
