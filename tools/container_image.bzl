"""Clean container image rules for multiplatform builds.

This module provides a simple, clean interface for building container images
that properly support multiple platforms using OCI standards.

Key features:
- Auto-detects platform for local builds (fast)
- Supports explicit platform selection
- Creates proper OCI manifest lists for multiplatform support
- Works seamlessly with pycross for Python dependencies
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
            # Use hermetic Python interpreter to run the wrapper script
            # This avoids requiring system Python in the base image
            
            # Determine platform for hermetic Python path
            if "_linux_amd64" in binary_name:
                platform = "x86_64-unknown-linux-gnu"
            elif "_linux_arm64" in binary_name:
                platform = "aarch64-unknown-linux-gnu"
            else:
                fail("Cannot determine platform from binary name: " + binary_name)
            
            # Path to hermetic Python interpreter in runfiles
            hermetic_python = "/app/{binary}.runfiles/rules_python++python+python_3_11_{platform}/bin/python3".format(
                binary = binary_name,
                platform = platform,
            )
            
            # Run the wrapper script with hermetic Python
            entrypoint = [hermetic_python, "/app/" + binary_name]
            
            # Set up Python environment for runfiles
            image_env.update({
                "RUNFILES_DIR": "/app/" + binary_name + ".runfiles",
                "PYTHON_RUNFILES": "/app/" + binary_name + ".runfiles",
                "PYTHONPATH": "/app/{binary}.runfiles/_main:/app/{binary}.runfiles".format(binary = binary_name),
            })
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
    binary_amd64 = None,
    binary_arm64 = None,
    base = "@ubuntu_base",
    registry = "ghcr.io",
    repository = None,
    **kwargs):
    """Build multiplatform OCI images with automatic platform detection.
    
    This creates a proper OCI manifest list that supports both AMD64 and ARM64.
    
    For local development:
        bazel run //app:my_app_image_load
        # Loads only your host architecture (fast!)
    
    For explicit platform:
        bazel run //app:my_app_image_amd64_load
        bazel run //app:my_app_image_arm64_load
    
    For release (builds both + manifest):
        bazel run //app:my_app_image_push -- v1.0.0
        # Pushes AMD64, ARM64, and creates manifest list
    
    Args:
        name: Base name for all generated targets
        binary_amd64: AMD64 binary target (e.g., ":app_linux_amd64")
        binary_arm64: ARM64 binary target (e.g., ":app_linux_arm64")
        base: Base image (defaults to ubuntu:24.04)
        registry: Container registry (defaults to ghcr.io)
        repository: Repository path (e.g., "whale-net/my-app")
        **kwargs: Additional arguments passed to container_image
    """
    
    if not binary_amd64 or not binary_arm64:
        fail("Must provide both binary_amd64 and binary_arm64")
    
    # Build platform-specific images
    container_image(
        name = name + "_amd64",
        binary = binary_amd64,
        base = base,
        **kwargs
    )
    
    container_image(
        name = name + "_arm64",
        binary = binary_arm64,
        base = base,
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
    binary_name = _get_binary_name(binary_amd64)
    # Remove platform suffix for clean local name
    clean_name = binary_name.replace("_linux_amd64", "").replace("_linux_arm64", "")
    
    # Main load target - uses AMD64 (most common dev environment)
    oci_load(
        name = name + "_load",
        image = ":" + name + "_amd64",
        repo_tags = [clean_name + ":latest"],
        tags = ["manual"],
    )
    
    # Platform-specific load targets
    oci_load(
        name = name + "_amd64_load",
        image = ":" + name + "_amd64",
        repo_tags = [clean_name + "_amd64:latest"],
        tags = ["manual"],
    )
    
    oci_load(
        name = name + "_arm64_load",
        image = ":" + name + "_arm64",
        repo_tags = [clean_name + "_arm64:latest"],
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
