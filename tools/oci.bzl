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

def python_oci_image(name, binary, repo_tag = None, tags = None, **kwargs):
    """Create an OCI image for a Python binary.
    
    Creates a self-contained Python application by including source files 
    and dependencies, avoiding Bazel's runtime requirements.
    """
    
    # Create separate layers to ensure proper directory structure
    pkg_tar(
        name = name + "_app_sources",
        srcs = [
            # Include the main_lib which contains main.py and __init__.py
            binary.rsplit(":", 1)[0] + ":main_lib" if ":" in binary else ":main_lib",
        ],
        package_dir = "app",
        tags = tags,
    )
    
    pkg_tar(
        name = name + "_lib_sources",
        srcs = [
            # Include the utility library with proper path structure
            "//libs/python",
        ],
        package_dir = "app/libs/python", 
        tags = tags,
    )
    
    # Also need to include the libs __init__.py
    pkg_tar(
        name = name + "_libs_init",
        srcs = [
            "//libs",
        ],
        package_dir = "app/libs",
        tags = tags,
    )
    
    oci_image(
        name = name,
        base = "@distroless_python",
        tars = [
            name + "_app_sources",
            name + "_libs_init", 
            name + "_lib_sources",
        ],
        entrypoint = ["python3", "/app/main.py"],
        env = {
            "PYTHONPATH": "/app",
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
