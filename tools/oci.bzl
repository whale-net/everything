"""Generic OCI image building rules for the monorepo."""

load("@rules_oci//oci:defs.bzl", "oci_image", "oci_load")
load("@bazel_skylib//lib:paths.bzl", "paths")
load("@rules_pkg//:pkg.bzl", "pkg_tar")

# Using appropriate base images for maximum compatibility:
# - Python: python:3.11-slim (includes Python runtime and common libraries)
# - Go: gcr.io/distroless/base-debian12 (lightweight and secure)

def oci_image_with_binary(name, binary, base_image, entrypoint = None, repo_tag = None, extra_layers = None, tags = None, binary_path = None, workdir = None, **kwargs):
    """Build an OCI image with a binary using cache-optimized layering.
    
    Uses oci_load for efficient building and loading. This approach completely 
    eliminates the need for traditional tarball targets that were never actually
    used in the CI pipeline.
    
    Args:
        name: Name of the image target
        binary: The binary target to package
        base_image: Base image to use (e.g., "@distroless_static")
        entrypoint: Custom entrypoint for the container. If None, auto-detects binary location
        repo_tag: Repository tag for the image (defaults to binary_name:latest)
        extra_layers: List of additional tar targets to include as separate layers
                     for better cache efficiency (e.g., dependency layers)
        tags: Tags to apply to all generated targets (e.g., ["manual", "release"])
        binary_path: Custom path for the binary in the container (defaults to /binary_name)
        workdir: Working directory for the container
        **kwargs: Additional arguments passed to oci_image
    """
    if not repo_tag:
        # Extract binary name from target, handling cases like ":binary" or "//path:binary"
        binary_name = binary.split(":")[-1] if ":" in binary else binary
        repo_tag = binary_name + ":latest"
    
    # Determine binary path in container
    if not binary_path:
        binary_path = "/" + paths.basename(binary)
    
    # Set default entrypoint if not provided
    if not entrypoint:
        entrypoint = [binary_path]
    
    # Create binary layer with propagated tags
    pkg_tar(
        name = name + "_binary_layer",
        files = {binary: binary_path.lstrip("/")},  # Remove leading slash for pkg_tar
        tags = tags,
    )
    
    # Assemble all layers with optimal ordering for cache efficiency
    all_layers = []
    
    # Add extra layers first (dependencies, static files, etc. - less frequently changed)
    if extra_layers:
        all_layers.extend(extra_layers)
    
    # Add binary layer last (most frequently changed)
    all_layers.append(":" + name + "_binary_layer")
    
    # Build the OCI image with optimized layer ordering and propagated tags
    oci_image(
        name = name,
        base = base_image,
        entrypoint = entrypoint,
        tars = all_layers,
        workdir = workdir,
        tags = tags,
        **kwargs
    )
    
    # Add oci_load target for efficient container runtime loading with propagated tags
    # This replaces the unused tarball targets and integrates with CI workflows
    oci_load(
        name = name + "_load",
        image = ":" + name,
        repo_tags = [repo_tag],
        tags = tags,
    )

def python_oci_image(name, binary, repo_tag = None, tags = None, target_platform = None, **kwargs):
    """Create an OCI image for a Python binary.
    
    Creates a complete Python application container with ALL dependencies baked in as layers.
    Uses distroless Python image and properly includes runfiles.
    
    Args:
        target_platform: Target platform for the container (e.g., "linux_x86_64").
                        If specified, creates platform-specific targets.
    """
    
    # Extract package info
    binary_name = binary.split(":")[-1] if ":" in binary else binary
    binary_package = binary.rsplit(":", 1)[0] if ":" in binary else "//" + binary
    
    # Create platform-specific binaries if target_platform is specified
    if target_platform:
        # Create a platform-specific version of the binary
        native.alias(
            name = name + "_" + target_platform + "_binary",
            actual = binary,
            target_compatible_with = [
                "@platforms//os:linux" if "linux" in target_platform else "@platforms//os:macos",
                "@platforms//cpu:x86_64" if "x86_64" in target_platform else "@platforms//cpu:arm64",
            ],
            tags = tags,
        )
        binary_target = ":" + name + "_" + target_platform + "_binary"
    else:
        binary_target = binary
    
    # Create a layer with the binary and all its runfiles
    # This includes the pip dependencies in the correct structure
    pkg_tar(
        name = name + "_complete_binary",
        srcs = [binary_target],
        remap_paths = {
            binary_name: "main.py",  # Main Python script
        },
        package_dir = "app",
        include_runfiles = True,  # This should include runfiles if supported
        tags = tags,
    )
    
    # Create layers for our internal libraries
    pkg_tar(
        name = name + "_lib_sources", 
        srcs = ["//libs/python"],
        package_dir = "app/libs/python",
        tags = tags,
    )
    
    pkg_tar(
        name = name + "_libs_init",
        srcs = ["//libs"],
        package_dir = "app/libs", 
        tags = tags,
    )
    
    # Create runner script layer
    pkg_tar(
        name = name + "_runner",
        files = {"//tools:python_runner.py": "python_runner.py"},
        package_dir = "app",
        tags = tags,
    )
    
    # Use distroless Python image with all dependencies baked in
    oci_image(
        name = name,
        base = "@distroless_python",
        tars = [
            name + "_complete_binary",   # Binary with all runfiles/dependencies
            name + "_libs_init",         # Our library structure
            name + "_lib_sources",       # Our library code
            name + "_runner",            # Python path setup script
        ],
        entrypoint = ["python3", "/app/python_runner.py", "/app/main.py"],
        env = {
            # Let Python find all runfiles automatically
            "PYTHONPATH": "/app:/app/libs",
            "RUNFILES_DIR": "/app/" + binary_name + ".runfiles",
        },
        workdir = "/app",
        tags = tags,
    )
    
    # Create oci_load target for local testing
    oci_load(
        name = name + "_load",
        image = ":" + name,
        repo_tags = [repo_tag] if repo_tag else [paths.basename(binary) + ":latest"],
        tags = tags,
    )

def go_oci_image(name, binary, repo_tag = None, tags = None, **kwargs):
    """Build an OCI image for a Go binary.
    
    This is a convenience wrapper around oci_image_with_binary with Go-specific defaults.
    Uses gcr.io/distroless/base-debian12 base image for maximum security.
    
    Args:
        name: Name of the image target
        binary: The Go binary target to package
        repo_tag: Repository tag for the image (defaults to binary_name:latest)
        tags: Tags to apply to all generated targets (e.g., ["manual", "release"])
        **kwargs: Additional arguments passed to oci_image_with_binary
    """
    base_image = "@distroless_base"
    
    binary_name = paths.basename(binary)
    
    oci_image_with_binary(
        name = name,
        binary = binary,
        base_image = base_image,
        entrypoint = ["/app/" + binary_name],
        repo_tag = repo_tag,
        tags = tags,
        workdir = "/app",
        binary_path = "/app/" + binary_name,
        **kwargs
    )
