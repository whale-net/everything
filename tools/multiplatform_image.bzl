"""Multi-platform OCI image building rules for the Everything monorepo.

This module provides simplified macros for building OCI images that support
multiple platforms (AMD64 and ARM64). Thanks to pycross, platform-specific
Python dependencies are handled automatically during the build process.

Each app gets:
1. Multi-platform manifest list (main target - supports both AMD64 and ARM64)
2. Individual platform images (amd64, arm64) 
3. Load targets for local testing

For Python apps: pycross automatically selects the correct wheels for each platform
For Go apps: the Go toolchain cross-compiles binaries automatically
"""

load("@rules_oci//oci:defs.bzl", "oci_image", "oci_image_index", "oci_load", "oci_push")
load("@rules_pkg//:pkg.bzl", "pkg_tar")
load("@bazel_skylib//lib:paths.bzl", "paths")

def multiplatform_python_image(
    name,
    binary = None,
    binary_amd64 = None,
    binary_arm64 = None,
    base_image = "@ubuntu_base",
    repository = None,
    extra_layers = None,
    tags = None,
    visibility = None,
    **kwargs):
    """Build multi-platform Python OCI images.
    
    Thanks to pycross, Python dependencies are automatically resolved for each platform
    during the build. Uses ubuntu:24.04 (117MB) as the base, which is smaller than 
    python:3.11-slim (186MB) while still providing the necessary runtime libraries.
    
    The Python interpreter from rules_python is bundled in the binary runfiles (~242MB),
    which provides hermetic, reproducible builds but results in larger images:
    - ubuntu:24.04 base: 117MB (runtime libs + shell)
    - rules_python interpreter: ~242MB (hermetic)
    - Python packages: varies (~75MB+ depending on deps)
    
    Total size: ~630-750MB depending on application dependencies
    
    Creates:
    - {name}: Multi-platform manifest list supporting both AMD64 and ARM64
    - {name}_amd64: AMD64 image  
    - {name}_arm64: ARM64 image
    - {name}_load: Load target for local testing (uses AMD64)
    - {name}_amd64_load, {name}_arm64_load: Platform-specific load targets
    
    Args:
        name: Base name for all generated targets
        binary: Python binary target (used for both platforms if platform-specific not provided)
        binary_amd64: AMD64-specific binary (optional)
        binary_arm64: ARM64-specific binary (optional)
        base_image: Base OCI image (defaults to @ubuntu_base - ubuntu:24.04 at 117MB)
        repository: Container repository name (e.g., "ghcr.io/org/app")
        extra_layers: Additional tar layers to include
        tags: Tags to apply to all targets
        visibility: Visibility for all targets
        **kwargs: Additional arguments passed to oci_image
    """
    if not tags:
        tags = ["manual"]
    
    # Use platform-specific binaries if provided, otherwise use the same binary for both
    amd64_binary = binary_amd64 if binary_amd64 else binary
    arm64_binary = binary_arm64 if binary_arm64 else binary
    
    if not amd64_binary or not arm64_binary:
        fail("Must provide either 'binary' or both 'binary_amd64' and 'binary_arm64'")
    
    # Create application layers with platform-specific binaries
    # pycross ensures the right dependencies are included for each platform
    pkg_tar(
        name = name + "_app_layer_amd64",
        srcs = [amd64_binary],
        package_dir = "/app",
        include_runfiles = True,
        tags = tags,
        visibility = visibility,
    )
    
    pkg_tar(
        name = name + "_app_layer_arm64",
        srcs = [arm64_binary],
        package_dir = "/app",
        include_runfiles = True,
        tags = tags,
        visibility = visibility,
    )
    
    # Build platform-specific images
    _build_python_image(
        name = name + "_amd64",
        base_image = base_image,
        binary = amd64_binary,
        app_layer = ":" + name + "_app_layer_amd64",
        extra_layers = extra_layers,
        tags = tags + ["platform-amd64"],
        visibility = visibility,
        **kwargs
    )
    
    _build_python_image(
        name = name + "_arm64",
        base_image = base_image, 
        binary = arm64_binary,
        app_layer = ":" + name + "_app_layer_arm64",
        extra_layers = extra_layers,
        tags = tags + ["platform-arm64"],
        visibility = visibility,
        **kwargs
    )
    
    # Create multi-platform manifest list
    oci_image_index(
        name = name,
        images = [
            ":" + name + "_amd64",
            ":" + name + "_arm64",
        ],
        tags = tags,
        visibility = visibility,
    )
    
    # Create load targets for local testing
    _create_load_targets(
        name = name,
        binary = binary,
        repository = repository,
        tags = tags,
        visibility = visibility,
    )

def _build_python_image(
    name,
    base_image,
    binary,
    app_layer,
    extra_layers = None,
    tags = None,
    visibility = None,
    **kwargs):
    """Internal helper to build a Python container image."""
    
    all_layers = []
    if extra_layers:
        all_layers.extend(extra_layers)
    all_layers.append(app_layer)
    
    # Extract binary name for entrypoint
    if ":" in binary:
        binary_name = binary.split(":")[-1]
    else:
        binary_name = paths.basename(binary)
    
    # Set up Python runfiles environment
    env = {
        "RUNFILES_DIR": "/app/" + binary_name + ".runfiles",
        "PYTHON_RUNFILES": "/app/" + binary_name + ".runfiles",
        "PYTHONPATH": "/app:" + "/app/" + binary_name + ".runfiles",
    }
    
    oci_image(
        name = name,
        base = base_image,
        tars = all_layers,
        entrypoint = ["/app/" + binary_name],
        workdir = "/app",
        env = env,
        tags = tags,
        visibility = visibility,
        **kwargs
    )

def multiplatform_go_image(
    name,
    binary,
    base_image = "@distroless_base",
    repository = None,
    extra_layers = None,
    tags = None,
    visibility = None,
    **kwargs):
    """Build multi-platform Go OCI images.
    
    Go binaries are statically linked, so cross-compilation is handled by the Go toolchain.
    
    Creates:
    - {name}: Multi-platform manifest list supporting both AMD64 and ARM64
    - {name}_amd64: AMD64 image  
    - {name}_arm64: ARM64 image
    - {name}_load: Load target for local testing (uses AMD64)
    - {name}_amd64_load, {name}_arm64_load: Platform-specific load targets
    
    Args:
        name: Base name for all generated targets
        binary: Go binary target to containerize
        base_image: Base OCI image (defaults to @distroless_base)
        repository: Container repository name (e.g., "ghcr.io/org/app")
        extra_layers: Additional tar layers to include
        tags: Tags to apply to all targets
        visibility: Visibility for all targets
        **kwargs: Additional arguments passed to oci_image
    """
    if not tags:
        tags = ["manual"]
    
    # Create application layer with the binary
    pkg_tar(
        name = name + "_app_layer",
        srcs = [binary],
        package_dir = "/app",
        tags = tags,
        visibility = visibility,
    )
    
    # Build platform-specific images
    _build_go_image(
        name = name + "_amd64",
        base_image = base_image,
        binary = binary,
        app_layer = ":" + name + "_app_layer",
        extra_layers = extra_layers,
        tags = tags + ["platform-amd64"],
        visibility = visibility,
        **kwargs
    )
    
    _build_go_image(
        name = name + "_arm64",
        base_image = base_image,
        binary = binary,
        app_layer = ":" + name + "_app_layer", 
        extra_layers = extra_layers,
        tags = tags + ["platform-arm64"],
        visibility = visibility,
        **kwargs
    )
    
    # Create multi-platform manifest list
    oci_image_index(
        name = name,
        images = [
            ":" + name + "_amd64",
            ":" + name + "_arm64",
        ],
        tags = tags,
        visibility = visibility,
    )
    
    # Create load targets for local testing
    _create_load_targets(
        name = name,
        binary = binary,
        repository = repository,
        tags = tags,
        visibility = visibility,
    )

def _build_go_image(
    name,
    base_image,
    binary,
    app_layer,
    extra_layers = None,
    tags = None,
    visibility = None,
    **kwargs):
    """Internal helper to build a Go container image."""
    
    all_layers = []
    if extra_layers:
        all_layers.extend(extra_layers)
    all_layers.append(app_layer)
    
    # Extract binary name for entrypoint
    if ":" in binary:
        binary_name = binary.split(":")[-1]
    else:
        binary_name = paths.basename(binary)
    
    oci_image(
        name = name,
        base = base_image,
        tars = all_layers,
        entrypoint = ["/app/" + binary_name],
        workdir = "/app",
        tags = tags,
        visibility = visibility,
        **kwargs
    )

def _create_load_targets(name, binary, repository, tags, visibility):
    """Internal helper to create oci_load targets for local testing.
    
    Note: repository parameter is ignored for load targets. Load targets use
    simple image names without registry prefixes since they're for local Docker use.
    """
    
    # Extract binary name for local Docker image tags
    # Always use simple names without registry prefix for local images
    if ":" in binary:
        binary_name = binary.split(":")[-1]
    else:
        binary_name = paths.basename(binary)
    
    # Use simple image names for local Docker (no registry prefix)
    repo_tags = [binary_name + ":latest"]
    repo_tags_amd64 = [binary_name + ":latest-amd64"]
    repo_tags_arm64 = [binary_name + ":latest-arm64"]
    
    # Main load target (uses AMD64 for local testing)
    oci_load(
        name = name + "_load",
        image = ":" + name + "_amd64",
        repo_tags = repo_tags,
        tags = tags,
        visibility = visibility,
    )
    
    # Platform-specific load targets
    oci_load(
        name = name + "_amd64_load",
        image = ":" + name + "_amd64",
        repo_tags = repo_tags_amd64,
        tags = tags,
        visibility = visibility,
    )
    
    oci_load(
        name = name + "_arm64_load",
        image = ":" + name + "_arm64",
        repo_tags = repo_tags_arm64,
        tags = tags,
        visibility = visibility,
    )

def multiplatform_push(
    name,
    image,
    repository,
    tag = "latest",
    tags = None,
    visibility = None):
    """Create push targets for multi-platform images.
    
    Creates:
    - {name}: Push multi-platform manifest list
    - {name}_amd64: Push AMD64 image with -amd64 tag suffix
    - {name}_arm64: Push ARM64 image with -arm64 tag suffix
    
    Args:
        name: Base name for push targets
        image: Base name of the image targets to push (without platform suffix)
        repository: Container repository (e.g., "ghcr.io/org/app")
        tag: Tag to use (defaults to "latest")
        tags: Bazel tags to apply
        visibility: Visibility for targets
    """
    if not tags:
        tags = ["manual"]
        
    # Push multi-platform manifest list
    oci_push(
        name = name,
        image = ":" + image,
        repository = repository,
        remote_tags = [tag],
        tags = tags,
        visibility = visibility,
    )
    
    # Push platform-specific images with platform suffix
    oci_push(
        name = name + "_amd64",
        image = ":" + image + "_amd64",
        repository = repository,
        remote_tags = [tag + "-amd64"],
        tags = tags,
        visibility = visibility,
    )
    
    oci_push(
        name = name + "_arm64", 
        image = ":" + image + "_arm64",
        repository = repository,
        remote_tags = [tag + "-arm64"],
        tags = tags,
        visibility = visibility,
    )
