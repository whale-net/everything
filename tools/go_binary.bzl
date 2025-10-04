"""Simplified Go binary wrapper with app metadata.

Creates a standard go_binary with optional metadata for the release system.
Cross-compilation happens automatically when building with different --platforms flags.

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
    )

This creates:
  - my_app: Standard go_binary (works on any platform)
  - my_app_info: AppInfo provider with metadata (port, app_type)

Cross-compilation happens automatically when you build with --platforms flag:
  bazel build //app:my_app --platforms=//tools:linux_x86_64
  bazel build //app:my_app --platforms=//tools:linux_arm64
"""

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
    """Creates a standard go_binary with optional app metadata.
    
    This is a thin wrapper around go_binary that also creates an AppInfo provider
    for the release system. No platform-specific variants are created - instead,
    the binary is built for different platforms using Bazel's --platforms flag.
    
    Creates these targets:
        {name}       - Standard go_binary
        {name}_info  - AppInfo provider (metadata: port, app_type)
    
    Args:
        name: Binary name
        srcs: Source files (same as go_binary)
        deps: Dependencies (same as go_binary)
        visibility: Visibility (same as go_binary)
        port: Port the application listens on (0 if no HTTP server, default: 0)
        app_type: Application type (external-api, internal-api, worker, job, default: empty)
        **kwargs: Additional go_binary arguments (data, embedsrcs, etc)
    """
    
    # Create standard go_binary
    go_binary(
        name = name,
        srcs = srcs,
        deps = deps,
        visibility = visibility,
        **kwargs
    )
    
    # Create app_info target to expose metadata to release system
    app_info(
        name = name + "_info",
        args = [],  # Go binaries typically don't have baked-in args
        binary_name = name,
        port = port,
        app_type = app_type,
        visibility = visibility,
    )
        srcs = srcs,
        deps = deps,
        visibility = visibility or ["//visibility:public"],
        **kwargs
    )
    
    # Create alias for local development (bazel run //demo/hello_go:hello_go)
    # Points to host binary which runs on the developer's machine (macOS, Linux, etc)
    native.alias(
        name = name,
        actual = ":" + name + "_host",
        visibility = visibility,
    )
    
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
