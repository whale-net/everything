"""Multi-platform OCI image building rules for the Everything monorepo.

This module provides simplified, clean macros for building OCI images that support
multiple platforms (AMD64 and ARM64) using oci_image_index to create manifest lists.
Each app gets three deployment options:
1. Multi-platform manifest list (main target - supports both AMD64 and ARM64)
2. Platform-specific AMD64 image (for AMD64-only deployments)  
3. Platform-specific ARM64 image (for ARM64-only deployments)

The multi-platform manifest list is created using oci_image_index which references
both platform-specific images, providing true cross-platform container support.
"""

load("@rules_oci//oci:defs.bzl", "oci_image", "oci_image_index", "oci_load", "oci_push")
load("@rules_pkg//:pkg.bzl", "pkg_tar")
load("@bazel_skylib//lib:paths.bzl", "paths")

def multiplatform_python_image(
    name,
    binary = None,
    binary_amd64 = None,
    binary_arm64 = None,
    base_image = "@python_base",
    repository = None,
    extra_layers = None,
    tags = None,
    visibility = None,
    **kwargs):
    """Build multi-platform Python OCI images that actually work.
    
    Creates three image targets:
    - {name}: Multi-platform manifest list supporting both AMD64 and ARM64
    - {name}_amd64: Platform-specific AMD64 image  
    - {name}_arm64: Platform-specific ARM64 image
    
    And corresponding load targets for local testing:
    - {name}_load: Load multi-platform manifest (defaults to AMD64 for local testing)
    - {name}_amd64_load: Load AMD64-specific image
    - {name}_arm64_load: Load ARM64-specific image
    
    Args:
        name: Base name for all generated targets
        binary: Python binary target to containerize (used for both platforms if platform-specific binaries not provided)
        binary_amd64: AMD64-specific binary target (overrides binary for AMD64)
        binary_arm64: ARM64-specific binary target (overrides binary for ARM64)
        base_image: Base OCI image (defaults to @python_base)
        repository: Container repository name (e.g., "myregistry/my-app")
        extra_layers: Additional tar layers to include
        tags: Tags to apply to all targets
        visibility: Visibility for all targets
        **kwargs: Additional arguments passed to oci_image
    """
    if not tags:
        tags = ["manual"]
    
    # Determine which binaries to use for each platform
    amd64_binary = binary_amd64 if binary_amd64 else binary
    arm64_binary = binary_arm64 if binary_arm64 else binary
    
    if not amd64_binary or not arm64_binary:
        fail("Must provide either 'binary' for both platforms or both 'binary_amd64' and 'binary_arm64'")
    
    # Create application layers with the platform-specific binaries AND their runfiles
    # This is critical for Python applications to work with platform-specific dependencies
    pkg_tar(
        name = name + "_app_layer_amd64",
        srcs = [amd64_binary],
        package_dir = "/app",
        include_runfiles = True,  # Include all Python dependencies
        tags = tags,
        visibility = visibility,
    )
    
    pkg_tar(
        name = name + "_app_layer_arm64",
        srcs = [arm64_binary],
        package_dir = "/app",
        include_runfiles = True,  # Include all Python dependencies
        tags = tags,
        visibility = visibility,
    )
    
    # Build platform-specific images with their corresponding binaries
    _build_python_platform_image(
        name = name + "_amd64",
        base_image = base_image,
        binary = amd64_binary,
        app_layer = ":" + name + "_app_layer_amd64",
        extra_layers = extra_layers,
        tags = tags + ["platform-amd64"],
        visibility = visibility,
        **kwargs
    )
    
    _build_python_platform_image(
        name = name + "_arm64",
        base_image = base_image, 
        binary = arm64_binary,
        app_layer = ":" + name + "_app_layer_arm64",
        extra_layers = extra_layers,
        tags = tags + ["platform-arm64"],
        visibility = visibility,
        **kwargs
    )
    
    # Create multi-platform manifest list using oci_image_index
    # This creates a true multi-platform manifest that references both platform images
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
    if repository:
        repo_tags = [repository + ":latest"]
        repo_tags_amd64 = [repository + ":latest-amd64"]
        repo_tags_arm64 = [repository + ":latest-arm64"]
    else:
        # Get binary name for repo tags - handle target labels properly
        if ":" in binary:
            binary_name = binary.split(":")[-1]
        else:
            binary_name = paths.basename(binary)
        repo_tags = [binary_name + ":latest"]
        repo_tags_amd64 = [binary_name + ":latest-amd64"]
        repo_tags_arm64 = [binary_name + ":latest-arm64"]
    
    oci_load(
        name = name + "_load",
        image = ":" + name + "_amd64",  # Load AMD64 image by default for local testing
        repo_tags = repo_tags,
        tags = tags,
        visibility = visibility,
    )
    
    # Create oci_load targets for local testing
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

def _build_python_platform_image(
    name,
    base_image,
    binary,
    app_layer,
    extra_layers = None,
    tags = None,
    visibility = None,
    **kwargs):
    """Internal helper to build a platform-specific Python image that actually works."""
    
    # Collect all layers in optimal order (dependencies first, app last)
    all_layers = []
    
    if extra_layers:
        all_layers.extend(extra_layers)
        
    all_layers.append(app_layer)
    
    # Get binary name for entrypoint - handle target labels properly
    if ":" in binary:
        binary_name = binary.split(":")[-1]
    else:
        binary_name = paths.basename(binary)
    
    # Set up proper environment for Python runfiles
    env = {
        "RUNFILES_DIR": "/app/" + binary_name + ".runfiles",
        "PYTHON_RUNFILES": "/app/" + binary_name + ".runfiles",
        "PYTHONPATH": "/app:" + "/app/" + binary_name + ".runfiles",
    }
    
    oci_image(
        name = name,
        base = base_image,
        tars = all_layers,
        entrypoint = ["/app/" + binary_name],  # Use the binary directly, not python3
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
    
    Creates the same three image targets as multiplatform_python_image but for Go binaries.
    Go binaries are statically linked, so no platform-specific dependency resolution needed.
    
    Args:
        name: Base name for all generated targets
        binary: Go binary target to containerize
        base_image: Base OCI image (defaults to @distroless_base)
        repository: Container repository name (e.g., "myregistry/my-app")
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
    
    # Build platform-specific images (Go is cross-compiled at build time)
    _build_go_platform_image(
        name = name + "_amd64",
        base_image = base_image,
        binary = binary,
        app_layer = ":" + name + "_app_layer",
        extra_layers = extra_layers,
        tags = tags + ["platform-amd64"],
        visibility = visibility,
        **kwargs
    )
    
    _build_go_platform_image(
        name = name + "_arm64",
        base_image = base_image,
        binary = binary,
        app_layer = ":" + name + "_app_layer", 
        extra_layers = extra_layers,
        tags = tags + ["platform-arm64"],
        visibility = visibility,
        **kwargs
    )
    
    # Create multi-platform manifest list using oci_image_index
    # This creates a true multi-platform manifest that references both platform images
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
    if repository:
        repo_tags = [repository + ":latest"]
        repo_tags_amd64 = [repository + ":latest-amd64"]
        repo_tags_arm64 = [repository + ":latest-arm64"]
    else:
        # Get binary name for repo tags - handle target labels properly
        if ":" in binary:
            binary_name = binary.split(":")[-1]
        else:
            binary_name = paths.basename(binary)
        repo_tags = [binary_name + ":latest"]
        repo_tags_amd64 = [binary_name + ":latest-amd64"]
        repo_tags_arm64 = [binary_name + ":latest-arm64"]
    
    oci_load(
        name = name + "_load",
        image = ":" + name + "_amd64",  # Load AMD64 image by default for local testing
        repo_tags = repo_tags,
        tags = tags,
        visibility = visibility,
    )
    
    # Create oci_load targets for local testing  
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

def _build_go_platform_image(
    name,
    base_image,
    binary,
    app_layer,
    extra_layers = None,
    tags = None,
    visibility = None,
    **kwargs):
    """Internal helper to build a platform-specific Go image."""
    
    # Collect all layers in optimal order (dependencies first, app last)
    all_layers = []
    
    if extra_layers:
        all_layers.extend(extra_layers)
        
    all_layers.append(app_layer)
    
    # Get binary name for entrypoint - handle target labels properly
    if ":" in binary:
        binary_name = binary.split(":")[-1]
    else:
        binary_name = paths.basename(binary)
    
    # Go binaries don't need RUNFILES since they're statically linked
    
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

def multiplatform_push(
    name,
    image,
    repository,
    tag = "latest",
    tags = None,
    visibility = None):
    """Create push targets for multi-platform images.
    
    Creates three push targets:
    - {name}: Push multi-platform manifest list
    - {name}_amd64: Push AMD64-specific image
    - {name}_arm64: Push ARM64-specific image
    
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