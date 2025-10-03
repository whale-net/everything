"""Clean container image rules for multiplatform builds.

IMPORTANT: Platform-specific dependencies and cross-compilation
-----------------------------------------------------------
While Python code itself is platform-agnostic, many packages (like pydantic, numpy, 
pillow) have compiled C extensions that are platform-specific. These packages distribute
"wheels" for different platforms (e.g., pydantic_core-2.33.2-cp311-manylinux_x86_64.whl
vs pydantic_core-2.33.2-cp311-manylinux_aarch64.whl).

IMPLEMENTATION:
This repository uses platform transitions to ensure each target platform gets the correct
wheels. When building multiplatform images:
- Each binary target (e.g., hello_python_linux_amd64, hello_python_linux_arm64) is built
  with exec_transition_for_inputs to force the correct target platform
- Pycross dependencies are resolved separately for each platform
- The resulting images contain architecture-appropriate wheels for AMD64 and ARM64

This allows building both AMD64 and ARM64 containers from a single build command on any
host platform (AMD64 or ARM64), with each container getting the correct wheels for its
target architecture.
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
    binary = None,
    binary_amd64 = None,
    binary_arm64 = None,
    base = "@ubuntu_base",
    registry = "ghcr.io",
    repository = None,
    language = None,
    **kwargs):
    """Build multiplatform OCI images with optional platform-specific binaries.
    
    Cross-compilation support with platform transitions:
    - Python apps: Use binary_amd64/binary_arm64 for correct wheel selection per platform
    - Go apps: Use single binary parameter (Go cross-compiles natively)
    
    Usage for Python apps with compiled dependencies:
        multiplatform_image(
            name = "my_app_image",
            binary_amd64 = ":my_app_linux_amd64",
            binary_arm64 = ":my_app_linux_arm64",
            language = "python",
        )
    
    Usage for Go apps or pure Python:
        multiplatform_image(
            name = "my_app_image",
            binary = ":my_app",
            language = "go",
        )
    
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
        binary: Binary target for both platforms (optional if binary_amd64/arm64 provided)
        binary_amd64: AMD64-specific binary (optional, overrides binary)
        binary_arm64: ARM64-specific binary (optional, overrides binary)
        base: Base image (defaults to ubuntu:24.04)
        registry: Container registry (defaults to ghcr.io)
        repository: Repository path (e.g., "whale-net/my-app")
        language: Language of binary ("python" or "go") - passed to container_image
        **kwargs: Additional arguments passed to container_image
    """
    
    # Use platform-specific binaries if provided, otherwise use common binary
    amd64_binary = binary_amd64 if binary_amd64 else binary
    arm64_binary = binary_arm64 if binary_arm64 else binary
    
    if not amd64_binary or not arm64_binary:
        fail("Must provide either 'binary' or both 'binary_amd64' and 'binary_arm64'")
    
    # Build platform-specific images with appropriate binaries
    container_image(
        name = name + "_amd64",
        binary = amd64_binary,
        base = base,
        language = language,
        **kwargs
    )
    
    container_image(
        name = name + "_arm64",
        binary = arm64_binary,
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
    # Use the amd64 binary name for the base tag
    binary_name = _get_binary_name(amd64_binary)
    
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
