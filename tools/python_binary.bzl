"""Multiplatform Python binary wrapper.

Creates platform-specific py_binary targets for proper multiplatform container builds.
This ensures each platform gets the correct compiled dependencies (pydantic, numpy, etc).

Usage - exactly like py_binary:
    multiplatform_py_binary(
        name = "my_app",
        srcs = ["main.py"],
        deps = [":app_lib", "@pypi//:fastapi", "@pypi//:uvicorn"],
    )
    
    release_app(
        name = "my_app",
        language = "python",
        domain = "api",
    )

This creates two binaries: my_app_linux_amd64 and my_app_linux_arm64.
The release_app macro automatically detects and uses both."""

load("@rules_python//python:defs.bzl", "py_binary")

def multiplatform_py_binary(
    name,
    srcs = None,
    main = None,
    deps = None,
    visibility = None,
    **kwargs):
    """Creates platform-suffixed py_binary targets for multiplatform builds.
    
    Takes the exact same parameters as py_binary. Creates *_linux_amd64 and 
    *_linux_arm64 targets that will get platform-specific compiled dependencies
    when building on the target architecture.
    
    Args:
        name: Base name for the binaries (will create {name}_linux_amd64 and {name}_linux_arm64)
        srcs: Source files (same as py_binary)
        main: Main entry point (same as py_binary)
        deps: Dependencies including @pypi// packages (same as py_binary)
        visibility: Visibility (same as py_binary)
        **kwargs: Additional py_binary arguments (env, args, data, etc)
    """
    # Default main to name.py if not provided
    if not main and srcs:
        # Find the main file from srcs
        main_candidates = [src for src in srcs if src.endswith("main.py") or src == name + ".py"]
        if main_candidates:
            main = main_candidates[0]
        elif len(srcs) == 1:
            main = srcs[0]
        else:
            fail("Could not determine main file for {}, please specify main= parameter".format(name))
    
    # Platform-specific binaries for container deployment
    # Each needs its own target to get platform-specific compiled dependencies
    py_binary(
        name = name + "_linux_amd64",
        srcs = srcs,
        main = main,
        deps = deps,
        visibility = visibility,
        **kwargs
    )
    
    py_binary(
        name = name + "_linux_arm64",
        srcs = srcs,
        main = main,
        deps = deps,
        visibility = visibility,
        **kwargs
    )
