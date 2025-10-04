"""Simplified Python binary wrapper with app metadata.

Creates a standard py_binary with optional metadata for the release system.
No platform transitions or variants needed - Bazel handles cross-compilation
when building with different --platforms flags.

Usage - exactly like py_binary:
    multiplatform_py_binary(
        name = "my_app",
        srcs = ["main.py"],
        deps = [":app_lib", "@pypi//:fastapi", "@pypi//:uvicorn"],
        args = ["start-server"],  # Optional: baked-in args
        port = 8000,  # Optional: for app metadata
        app_type = "external-api",  # Optional: for app metadata
    )
    
    release_app(
        name = "my_app",
        language = "python",
        domain = "api",
    )

This creates:
  - my_app: Standard py_binary (works on any platform)
  - my_app_info: AppInfo provider with metadata (args, port, app_type)

Cross-compilation happens automatically when you build with --platforms flag:
  bazel build //app:my_app --platforms=//tools:linux_x86_64
  bazel build //app:my_app --platforms=//tools:linux_arm64
"""

load("@rules_python//python:defs.bzl", "py_binary")
load("//tools:app_info.bzl", "AppInfo", "app_info")

def multiplatform_py_binary(
    name,
    srcs = None,
    main = None,
    deps = None,
    visibility = None,
    port = 0,
    app_type = "",
    **kwargs):
    """Creates a standard py_binary with optional app metadata.
    
    This is a thin wrapper around py_binary that also creates an AppInfo provider
    for the release system. No platform-specific variants are created - instead,
    the binary is built for different platforms using Bazel's --platforms flag.
    
    Creates these targets:
        {name}       - Standard py_binary
        {name}_info  - AppInfo provider (metadata: args, port, app_type)
    
    Args:
        name: Binary name
        srcs: Source files (same as py_binary)
        main: Main entry point (same as py_binary)
        deps: Dependencies including @pypi// packages (same as py_binary)
        visibility: Visibility (same as py_binary)
        port: Port the application listens on (0 if no HTTP server, default: 0)
        app_type: Application type (external-api, internal-api, worker, job, default: empty)
        **kwargs: Additional py_binary arguments (env, args, data, etc)
    """
    # Default main to name.py if not provided
    if not main and srcs:
        main_candidates = [src for src in srcs if src.endswith("main.py") or src == name + ".py"]
        if main_candidates:
            main = main_candidates[0]
        elif len(srcs) == 1:
            main = srcs[0]
        else:
            fail("Could not determine main file for {}, please specify main= parameter".format(name))
    
    # Create standard py_binary
    py_binary(
        name = name,
        srcs = srcs,
        main = main,
        deps = deps,
        visibility = visibility,
        **kwargs
    )
    
    # Create app_info target to expose metadata (args, port, app_type) to release system
    args = kwargs.get("args", [])
    app_info(
        name = name + "_info",
        args = args,
        binary_name = name,
        port = port,
        app_type = app_type,
        visibility = visibility,
    )
