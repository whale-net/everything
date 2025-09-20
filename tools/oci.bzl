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
    """Create an OCI image for a Python binary."""
    
    # Extract the package path from the binary target
    # e.g., "//demo/hello_fastapi:hello_fastapi" -> "//demo/hello_fastapi:main.py"
    binary_package = binary.rsplit(":", 1)[0]  # Get everything before the last ":"
    main_py_target = binary_package + ":main.py"
    
    # Instead of using the compiled binary, let's include the Python sources directly
    # This avoids cross-compilation issues and the runfiles complexity
    pkg_tar(
        name = name + "_python_sources_layer",
        files = {
            main_py_target: "app/main.py",
            "//libs/python:utils.py": "app/libs/python/utils.py",
            "//libs/python:__init__.py": "app/libs/python/__init__.py",
            "//libs:__init__.py": "app/libs/__init__.py",
        },
        tags = tags,
    )
    
    oci_image(
        name = name,
        base = "@distroless_python",
        tars = [name + "_python_sources_layer"],
        entrypoint = ["python3", "/app/main.py"],
        env = {
            "PYTHONPATH": "/app",
            "WORKDIR": "/app",
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
