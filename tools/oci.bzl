"""Generic OCI image building rules for the monorepo."""

load("@rules_oci//oci:defs.bzl", "oci_image", "oci_load")
load("@aspect_bazel_lib//lib:tar.bzl", "tar")

def oci_image_with_binary(
    name,
    binary,
    base_image,
    entrypoint = None,
    repo_tag = None,
    platform = "linux/amd64",
    **kwargs):
    """Build an OCI image with a binary.
    
    This is a generic rule that can build OCI images for any type of binary
    (Python, Go, Rust, C++, etc.) with sensible defaults but full customization.
    
    Args:
        name: Name of the image target
        binary: The binary target to package
        base_image: Base image to use (e.g., "@distroless_static")
        entrypoint: Custom entrypoint for the container. If None, auto-detects binary location
        repo_tag: Repository tag for the image (defaults to binary_name:latest)
        platform: Target platform (defaults to linux/amd64)
        **kwargs: Additional arguments passed to oci_image
    """
    if not repo_tag:
        # Extract binary name from target, handling cases like ":binary" or "//path:binary"
        binary_name = binary.split(":")[-1] if ":" in binary else binary
        repo_tag = binary_name + ":latest"
    
    # Create a layer with the binary
    tar(
        name = name + "_layer",
        srcs = [binary],
    )
    
    # Build the OCI image
    oci_image(
        name = name,
        base = base_image,
        entrypoint = entrypoint,
        tars = [":" + name + "_layer"],
        **kwargs
    )
    
    # Create a tarball for loading into Docker
    oci_load(
        name = name + "_tarball",
        image = ":" + name,
        repo_tags = [repo_tag],
    )

def _get_platform_base_image(base_prefix, platform = None):
    """Get the platform-specific base image name.
    
    Args:
        base_prefix: Base image prefix (e.g., "distroless_python3", "distroless_static")
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

def python_oci_image(name, binary, repo_tag = None, platform = None, **kwargs):
    """Build an OCI image for a Python binary.
    
    This is a convenience wrapper around oci_image_with_binary with Python-specific defaults.
    
    Args:
        name: Name of the image target
        binary: The Python binary target to package
        repo_tag: Repository tag for the image (defaults to binary_name:latest)
        platform: Target platform (defaults to linux/amd64)
        **kwargs: Additional arguments passed to oci_image_with_binary
    """
    base_image = _get_platform_base_image("distroless_python3", platform)
    binary_name = binary.split(":")[-1] if ":" in binary else binary
    # Python binaries are typically at /<binary_name>/<binary_name>
    entrypoint = ["/" + binary_name + "/" + binary_name]
    
    oci_image_with_binary(
        name = name,
        binary = binary,
        base_image = base_image,
        entrypoint = entrypoint,
        repo_tag = repo_tag,
        platform = platform,
        **kwargs
    )

def go_oci_image(name, binary, repo_tag = None, platform = None, **kwargs):
    """Build an OCI image for a Go binary.
    
    This is a convenience wrapper around oci_image_with_binary with Go-specific defaults.
    
    Args:
        name: Name of the image target
        binary: The Go binary target to package
        repo_tag: Repository tag for the image (defaults to binary_name:latest)
        platform: Target platform (defaults to linux/amd64)
        **kwargs: Additional arguments passed to oci_image_with_binary
    """
    base_image = _get_platform_base_image("distroless_static", platform)
    binary_name = binary.split(":")[-1] if ":" in binary else binary
    # Go binaries are typically at /<binary_name>/<binary_name>_/<binary_name>
    entrypoint = ["/" + binary_name + "/" + binary_name + "_/" + binary_name]
    
    oci_image_with_binary(
        name = name,
        binary = binary,
        base_image = base_image,
        entrypoint = entrypoint,
        repo_tag = repo_tag,
        platform = platform,
        **kwargs
    )

def python_oci_image_multiplatform(name, binary, repo_tag = None, **kwargs):
    """Build OCI images for a Python binary for both amd64 and arm64 platforms.
    
    Creates separate image targets for different platforms:
    - {name}_amd64: Linux amd64 image (for production)
    - {name}_arm64: Linux arm64 image (for Mac development)
    - {name}: Default image (amd64)
    
    Args:
        name: Base name of the image targets
        binary: The Python binary target to package
        repo_tag: Repository tag for the image (defaults to binary_name:latest)
        **kwargs: Additional arguments passed to oci_image_with_binary
    """
    # AMD64 version (default for production)
    python_oci_image(
        name = name,
        binary = binary,
        repo_tag = repo_tag,
        platform = "linux/amd64",
        **kwargs
    )
    
    # AMD64 version (explicit)
    python_oci_image(
        name = name + "_amd64",
        binary = binary,
        repo_tag = repo_tag,
        platform = "linux/amd64",
        **kwargs
    )
    
    # ARM64 version (for Mac development)
    python_oci_image(
        name = name + "_arm64",
        binary = binary,
        repo_tag = repo_tag,
        platform = "linux/arm64",
        **kwargs
    )

def go_oci_image_multiplatform(name, binary, repo_tag = None, **kwargs):
    """Build OCI images for a Go binary for both amd64 and arm64 platforms.
    
    Creates separate image targets for different platforms:
    - {name}_amd64: Linux amd64 image (for production)
    - {name}_arm64: Linux arm64 image (for Mac development)  
    - {name}: Default image (amd64)
    
    Args:
        name: Base name of the image targets
        binary: The Go binary target to package
        repo_tag: Repository tag for the image (defaults to binary_name:latest)
        **kwargs: Additional arguments passed to oci_image_with_binary
    """
    # AMD64 version (default for production)
    go_oci_image(
        name = name,
        binary = binary,
        repo_tag = repo_tag,
        platform = "linux/amd64",
        **kwargs
    )
    
    # AMD64 version (explicit)
    go_oci_image(
        name = name + "_amd64",
        binary = binary,
        repo_tag = repo_tag,
        platform = "linux/amd64",
        **kwargs
    )
    
    # ARM64 version (for Mac development)
    go_oci_image(
        name = name + "_arm64",
        binary = binary,
        repo_tag = repo_tag,
        platform = "linux/arm64",
        **kwargs
    )
