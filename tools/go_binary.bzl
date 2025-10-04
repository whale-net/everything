"""Multiplatform Go binary wrapper for cross-compilation.

Creates platform-specific go_binary targets with proper GOOS/GOARCH settings,
enabling true cross-compilation for container images.

Usage - exactly like go_binary:
    multiplatform_go_binary(
        name = "my_app",
        srcs = ["main.go"],
        deps = [":app_lib", "//libs/go"],
    )
    
    release_app(
        name = "my_app",
        language = "go",
        domain = "api",
    )

This creates three binaries:
  - my_app (default, uses host platform for local development)
  - my_app_linux_amd64 (cross-compiled for Linux x86_64)
  - my_app_linux_arm64 (cross-compiled for Linux ARM64)

The platform-specific binaries are automatically used by the release system
for building multiplatform container images."""

load("@rules_go//go:def.bzl", "go_binary")

def multiplatform_go_binary(
    name,
    srcs = None,
    deps = None,
    visibility = None,
    **kwargs):
    """Creates platform-suffixed go_binary targets with proper cross-compilation.
    
    Uses Go's native cross-compilation support (GOOS/GOARCH) to build binaries
    for different platforms. Creates *_linux_amd64 and *_linux_arm64 targets
    for container images, plus a default target for local development.
    
    Takes the exact same parameters as go_binary. Creates {name}, {name}_linux_amd64,
    and {name}_linux_arm64 targets.
    
    Args:
        name: Base name for the binaries (will create {name}, {name}_linux_amd64, {name}_linux_arm64)
        srcs: Source files (same as go_binary)
        deps: Dependencies (same as go_binary)
        visibility: Visibility (same as go_binary)
        **kwargs: Additional go_binary arguments (data, embedsrcs, etc)
    """
    
    # Linux AMD64 binary for container images
    go_binary(
        name = name + "_linux_amd64",
        srcs = srcs,
        deps = deps,
        goarch = "amd64",
        goos = "linux",
        visibility = visibility,
        **kwargs
    )
    
    # Linux ARM64 binary for container images
    go_binary(
        name = name + "_linux_arm64",
        srcs = srcs,
        deps = deps,
        goarch = "arm64",
        goos = "linux",
        visibility = visibility,
        **kwargs
    )
    
    # Default binary for local development (uses host platform)
    # This is useful for bazel run during development
    go_binary(
        name = name,
        srcs = srcs,
        deps = deps,
        visibility = visibility,
        **kwargs
    )
