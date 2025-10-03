"""Clean Python binary rules that eliminate multi-platform boilerplate.

This module provides a single macro that generates all necessary py_binary targets
for multi-platform deployment while keeping the BUILD.bazel files clean and simple.

The multiplatform_py_binary macro generates Linux AMD64 and ARM64 binaries for
container deployment, plus a development binary.

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
    - A main py_binary for development
    - Platform-specific binaries for container deployment (linux_amd64, linux_arm64)
    
    Args:
        name: Name for the binary
        srcs: Source files for the binary
        main: Main entry point file
        deps: Dependencies (py_library targets)
        requirements: List of requirement names (e.g., ["fastapi", "uvicorn"])
                     These are converted to @pypi//package_name targets
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
    
    # Build unified deps list
    # pycross automatically selects the correct platform-specific wheels
    all_deps = deps[:] if deps else []
    for req in requirements:
        # Use package name as-is (pycross preserves original package names including hyphens)
        all_deps.append("@pypi//:{}".format(req))
    
    # Main binary for development and all platforms
    # pycross handles platform selection automatically
    py_binary(
        name = name,
        srcs = srcs,
        main = main,
        deps = all_deps,
        visibility = visibility,
        **kwargs
    )
    
    # Platform-specific binaries for container deployment
    # Same deps work for all platforms thanks to pycross
    py_binary(
        name = name + "_linux_amd64",
        srcs = srcs,
        main = main,
        deps = all_deps,
        visibility = visibility,
        **kwargs
    )
    
    py_binary(
        name = name + "_linux_arm64",
        srcs = srcs,
        main = main,
        deps = all_deps,
        visibility = visibility,
        **kwargs
    )
