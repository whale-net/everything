"""Clean Python binary rules that eliminate multi-platform boilerplate.

This module provides a single macro that generates all necessary py_binary targets
for multi-platform deployment while keeping the BUILD.bazel files clean and simple.

The multiplatform_py_binary macro generates Linux AMD64 and ARM64 binaries for
container deployment, plus a development binary using dev requirements.

The multiplatform_py_binary macro works seamlessly with the release_app macro,
which automatically detects the platform-specific binaries without requiring
explicit binary_amd64/binary_arm64 parameters.

Example usage:
    multiplatform_py_binary(
        name = "my_app",
        srcs = ["main.py"], 
        deps = [":app_lib"],
        requirements = ["fastapi", "uvicorn"],
    )
    
    release_app(
        name = "my_app",  # Must match multiplatform_py_binary name
        language = "python",
        domain = "api",
        description = "My FastAPI app",
    )
"""

load("@rules_python//python:defs.bzl", "py_binary", "py_library")
load("@pip_deps_dev//:requirements.bzl", "requirement")
load("@pip_deps_linux_amd64//:requirements.bzl", requirement_linux_amd64 = "requirement")
load("@pip_deps_linux_arm64//:requirements.bzl", requirement_linux_arm64 = "requirement")

def multiplatform_py_binary(
    name,
    srcs = None,
    main = None,
    deps = None,
    requirements = None,
    visibility = None,
    **kwargs):
    """Create a py_binary that works across all platforms with minimal boilerplate.
    
    This macro generates:
    - A main py_binary for development (uses dev requirements)
    - Platform-specific binaries for container deployment (linux_amd64, linux_arm64)
    
    Args:
        name: Name for the binary
        srcs: Source files for the binary
        main: Main entry point file
        deps: Dependencies (py_library targets)
        requirements: List of requirement names (e.g., ["fastapi", "uvicorn"])
        visibility: Visibility for all targets
        **kwargs: Additional arguments passed to py_binary
    """
    if not requirements:
        requirements = []
    
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
    
    # Build deps lists for each platform
    dev_deps = deps[:] if deps else []
    amd64_deps = deps[:] if deps else []
    arm64_deps = deps[:] if deps else []
    
    # Add platform-specific requirements
    for req in requirements:
        dev_deps.append(requirement(req))
        amd64_deps.append(requirement_linux_amd64(req))
        arm64_deps.append(requirement_linux_arm64(req))
    
    # Main binary for development (uses dev requirements)
    py_binary(
        name = name,
        srcs = srcs,
        main = main,
        deps = dev_deps,
        visibility = visibility,
        **kwargs
    )
    
    # Platform-specific binaries for container deployment
    py_binary(
        name = name + "_linux_amd64",
        srcs = srcs,
        main = main,
        deps = amd64_deps,
        visibility = visibility,
        **kwargs
    )
    
    py_binary(
        name = name + "_linux_arm64",
        srcs = srcs,
        main = main,
        deps = arm64_deps,
        visibility = visibility,
        **kwargs
    )