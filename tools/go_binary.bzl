"""Multiplatform Go binary wrapper for cross-compilation.

Creates platform-specific go_binary targets with proper GOOS/GOARCH settings,
enabling true cross-compilation for container images.

WHEN TO USE THIS:
  ✅ Use multiplatform_go_binary for:
     - Applications that will be containerized (with release_app)
     - Services that need to run on both AMD64 and ARM64
     - Any binary that gets deployed via container images

  ❌ Use standard go_binary for:
     - Build tools (e.g., //tools/helm:helm_composer)
     - CLI utilities that run on the host during builds
     - Development scripts that don't need containerization
     
     Why? Build tools only need to run on the host platform. Creating 
     multiple platform variants would be unnecessary overhead and could
     cause confusion about which binary to use during builds.

Usage - exactly like go_binary:
    multiplatform_go_binary(
        name = "my_app",
        srcs = ["main.go"],
        deps = [":app_lib", "//libs/go"],
        port = 8080,  # Optional: if app is an HTTP server
        app_type = "external-api",  # Optional: external-api, internal-api, worker, job
    )
    
    release_app(
        name = "my_app",
        language = "go",
        domain = "api",
        # port and app_type automatically extracted from binary
    )

This creates multiple targets:
  - my_app (default, uses host platform for local development)
  - my_app_linux_amd64 (cross-compiled for Linux x86_64)
  - my_app_linux_arm64 (cross-compiled for Linux ARM64)
  - my_app_info (AppInfo provider with metadata for release system)

The platform-specific binaries are automatically used by the release system
for building multiplatform container images."""

load("@rules_go//go:def.bzl", "go_binary")
load("//tools:app_info.bzl", "app_info")

def multiplatform_go_binary(
    name,
    srcs = None,
    deps = None,
    visibility = None,
    port = 0,
    app_type = "",
    **kwargs):
    """Creates platform-suffixed go_binary targets with proper cross-compilation.
    
    Uses Go's native cross-compilation support (GOOS/GOARCH) to build binaries
    for different platforms. Creates *_linux_amd64 and *_linux_arm64 targets
    for container images, plus a default target for local development.
    
    Takes the exact same parameters as go_binary, plus additional metadata parameters
    that are exposed through AppInfo provider for the release system.
    
    Args:
        name: Base name for the binaries (will create {name}, {name}_linux_amd64, {name}_linux_arm64)
        srcs: Source files (same as go_binary)
        deps: Dependencies (same as go_binary)
        visibility: Visibility (same as go_binary)
        port: Port the application listens on (0 if no HTTP server, default: 0)
        app_type: Application type (external-api, internal-api, worker, job, default: empty)
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
    
    # NOTE: We do NOT create a base name binary (e.g., "hello_go").
    # Instead, release_app and change detection use {name}_linux_amd64 directly.
    # This is safe because all platform binaries (_linux_amd64, _linux_arm64)
    # are built from the same source files and deps, so checking one platform's dependencies
    # is sufficient for change detection. We chose linux_amd64 as it's the most common platform.
    
    # Create app_info target to expose metadata (port, app_type) to release system
    # Note: Go binaries typically don't have args since they're compiled executables
    app_info(
        name = name + "_info",
        args = [],  # Go binaries don't use runtime args like Python
        binary_name = name,
        port = port,
        app_type = app_type,
        visibility = visibility or ["//visibility:public"],
    )
