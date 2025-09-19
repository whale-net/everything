"""Generic OCI image building rules for the monorepo."""

load("@rules_oci//oci:defs.bzl", "oci_image", "oci_load")
load("@aspect_bazel_lib//lib:tar.bzl", "tar")

# Using appropriate base images for maximum compatibility:
# - Python: python:3.11-slim (includes Python runtime and common libraries)
# - Go: alpine:3.18 (lightweight Linux with package manager for dependencies)

def oci_image_with_binary(
    name,
    binary,
    base_image,
    entrypoint = None,
    repo_tag = None,
    platform = "linux/amd64",
    extra_layers = None,
    tags = None,
    **kwargs):
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
        platform: Target platform (defaults to linux/amd64)
        extra_layers: List of additional tar targets to include as separate layers
                     for better cache efficiency (e.g., dependency layers)
        tags: Tags to apply to all generated targets (e.g., ["manual", "release"])
        **kwargs: Additional arguments passed to oci_image
    """
    if not repo_tag:
        # Extract binary name from target, handling cases like ":binary" or "//path:binary"
        binary_name = binary.split(":")[-1] if ":" in binary else binary
        repo_tag = binary_name + ":latest"
    
    # Create a directory structure with binary placed in app/
    binary_name = binary.split(":")[-1] if ":" in binary else binary
    native.genrule(
        name = name + "_app_structure",
        srcs = [binary],
        outs = [name + "_app/" + binary_name],
        cmd = "mkdir -p $$(dirname $(OUTS)); cp $(SRCS) $(OUTS); chmod +x $(OUTS)",
        tags = tags,
    )
    
    # Create binary layer with the app directory structure
    tar(
        name = name + "_binary_layer",
        srcs = [":" + name + "_app_structure"],
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

def _get_platform_base_image(base_prefix, platform = None):
    """Get the platform-specific base image name.
    
    Args:
        base_prefix: Base image prefix (e.g., "python_slim", "alpine")
        platform: Target platform (defaults to linux/amd64)
    
    Returns:
        Platform-specific base image target
    """
    if not platform:
        platform = "linux/amd64"
    
    # Convert platform to bazel target suffix
    if platform == "linux/amd64":
        suffix = "_linux_amd64"
    elif platform == "linux/arm64" or platform == "linux/arm64/v8":
        suffix = "_linux_arm64_v8"
    else:
        # Default to amd64 for unknown platforms
        suffix = "_linux_amd64"
    
    return "@" + base_prefix + suffix

def python_oci_image(name, binary, repo_tag = None, platform = None, tags = None, **kwargs):
    """Build an OCI image for a Python binary.
    
    This is a convenience wrapper around oci_image_with_binary with Python-specific defaults.
    Uses python:3.11-slim base image for compatibility and included Python runtime.
    For Python applications, we use the source files from the runfiles with the container's Python interpreter.
    
    Args:
        name: Name of the image target
        binary: The Python binary target to package
        repo_tag: Repository tag for the image (defaults to binary_name:latest)
        platform: Target platform (defaults to linux/amd64)
        tags: Tags to apply to all generated targets (e.g., ["manual", "release"])
        **kwargs: Additional arguments passed to oci_image_with_binary
    """
    base_image = _get_platform_base_image("python_slim", platform)
    binary_name = binary.split(":")[-1] if ":" in binary else binary
    
    # For Python, the binary will be placed in /app/{binary_name} and can be executed directly
    # Bazel py_binary targets are self-contained with all dependencies included
    entrypoint = ["/app/" + binary_name]
    
    oci_image_with_binary(
        name = name,
        binary = binary,
        base_image = base_image,
        entrypoint = entrypoint,
        repo_tag = repo_tag,
        platform = platform,
        tags = tags,
        **kwargs
    )

def go_oci_image(name, binary, repo_tag = None, platform = None, tags = None, **kwargs):
    """Build an OCI image for a Go binary.
    
    This is a convenience wrapper around oci_image_with_binary with Go-specific defaults.
    Uses alpine:3.18 base image for compatibility with unknown host dependencies.
    
    Args:
        name: Name of the image target
        binary: The Go binary target to package
        repo_tag: Repository tag for the image (defaults to binary_name:latest)
        platform: Target platform (defaults to linux/amd64)
        tags: Tags to apply to all generated targets (e.g., ["manual", "release"])
        **kwargs: Additional arguments passed to oci_image_with_binary
    """
    base_image = _get_platform_base_image("alpine", platform)
    
    # For Go, use platform-specific binary if available and place at /app/{binary_name}
    binary_name = binary.split(":")[-1] if ":" in binary else binary
    if platform == "linux/arm64":
        platform_binary = binary.replace(":" + binary_name, ":" + binary_name + "_linux_arm64")
    else:
        platform_binary = binary.replace(":" + binary_name, ":" + binary_name + "_linux_amd64")
    
    # The Go binary will be placed in /app/{platform_binary_name}
    platform_binary_name = platform_binary.split(":")[-1]
    entrypoint = ["/app/" + platform_binary_name]
    
    oci_image_with_binary(
        name = name,
        binary = platform_binary,
        base_image = base_image,
        entrypoint = entrypoint,
        repo_tag = repo_tag,
        platform = platform,
        tags = tags,
        **kwargs
    )

def python_oci_image_multiplatform(name, binary, repo_tag = None, tags = None, **kwargs):
    """Build OCI images for a Python binary for both amd64 and arm64 platforms.
    
    Creates separate image targets for different platforms:
    - {name}_amd64: Linux amd64 image (for production)
    - {name}_arm64: Linux arm64 image (for Mac development)
    - {name}: Default image (amd64)
    
    Args:
        name: Base name of the image targets
        binary: The Python binary target to package
        repo_tag: Repository tag for the image (defaults to binary_name:latest)
        tags: Tags to apply to all generated targets (e.g., ["manual", "release"])
        **kwargs: Additional arguments passed to oci_image_with_binary
    """
    # AMD64 version (default for production)
    python_oci_image(
        name = name,
        binary = binary,
        repo_tag = repo_tag,
        platform = "linux/amd64",
        tags = tags,
        **kwargs
    )
    
    # AMD64 version (explicit)
    python_oci_image(
        name = name + "_amd64",
        binary = binary,
        repo_tag = repo_tag,
        platform = "linux/amd64",
        tags = tags,
        **kwargs
    )
    
    # ARM64 version (for Mac development)
    python_oci_image(
        name = name + "_arm64",
        binary = binary,
        repo_tag = repo_tag,
        platform = "linux/arm64",
        tags = tags,
        **kwargs
    )

def go_oci_image_multiplatform(name, binary, repo_tag = None, tags = None, **kwargs):
    """Build OCI images for a Go binary for both amd64 and arm64 platforms.
    
    Creates separate image targets for different platforms:
    - {name}_amd64: Linux amd64 image (for production)
    - {name}_arm64: Linux arm64 image (for Mac development)  
    - {name}: Default image (amd64)
    
    Args:
        name: Base name of the image targets
        binary: The Go binary target to package
        repo_tag: Repository tag for the image (defaults to binary_name:latest)
        tags: Tags to apply to all generated targets (e.g., ["manual", "release"])
        **kwargs: Additional arguments passed to oci_image_with_binary
    """
    # AMD64 version (default for production)
    go_oci_image(
        name = name,
        binary = binary,
        repo_tag = repo_tag,
        platform = "linux/amd64",
        tags = tags,
        **kwargs
    )
    
    # AMD64 version (explicit)
    go_oci_image(
        name = name + "_amd64",
        binary = binary,
        repo_tag = repo_tag,
        platform = "linux/amd64",
        tags = tags,
        **kwargs
    )
    
    # ARM64 version (for Mac development)
    go_oci_image(
        name = name + "_arm64",
        binary = binary,
        repo_tag = repo_tag,
        platform = "linux/arm64",
        tags = tags,
        **kwargs
    )
