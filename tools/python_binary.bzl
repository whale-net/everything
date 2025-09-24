"""Clean Python binary and library rules that eliminate multi-platform boilerplate.

This module provides macros that generate all necessary py_binary and py_library targets
for multi-platform deployment while keeping the BUILD.bazel files clean and simple.

The multiplatform_py_binary macro generates Linux AMD64 and ARM64 binaries for
container deployment, plus a development binary using dev requirements.

The multiplatform_py_library macro generates platform-specific libraries with
proper requirements for each platform, eliminating requirement duplication.

The multiplatform_py_binary macro works seamlessly with the release_app macro,
which automatically detects the platform-specific binaries without requiring
explicit binary_amd64/binary_arm64 parameters.

Example usage:
    multiplatform_py_library(
        name = "my_lib",
        srcs = ["lib.py"],
        requirements = ["fastapi", "uvicorn"],
    )
    
    multiplatform_py_binary(
        name = "my_app",
        srcs = ["main.py"], 
        deps = [":my_lib"],  # Requirements automatically extracted
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

def multiplatform_py_library(
    name,
    srcs = None,
    deps = None,
    requirements = None,
    visibility = None,
    **kwargs):
    """Create a py_library that works across all platforms with minimal boilerplate.
    
    This macro generates:
    - A main py_library for development (uses dev requirements)
    - Platform-specific libraries for container deployment (linux_amd64, linux_arm64)
    
    Args:
        name: Name for the library
        srcs: Source files for the library
        deps: Dependencies (py_library targets)
        requirements: List of requirement names (e.g., ["fastapi", "uvicorn"])
        visibility: Visibility for all targets
        **kwargs: Additional arguments passed to py_library
    """
    if not requirements:
        requirements = []
    
    # Build deps lists for each platform
    dev_deps = deps[:] if deps else []
    amd64_deps = deps[:] if deps else []
    arm64_deps = deps[:] if deps else []
    
    # Add platform-specific requirements
    for req in requirements:
        dev_deps.append(requirement(req))
        amd64_deps.append(requirement_linux_amd64(req))
        arm64_deps.append(requirement_linux_arm64(req))
    
    # Main library for development (uses dev requirements)
    py_library(
        name = name,
        srcs = srcs,
        deps = dev_deps,
        visibility = visibility,
        **kwargs
    )
    
    # Platform-specific libraries for container deployment
    py_library(
        name = name + "_linux_amd64",
        srcs = srcs,
        deps = amd64_deps,
        visibility = visibility,
        **kwargs
    )
    
    py_library(
        name = name + "_linux_arm64",
        srcs = srcs,
        deps = arm64_deps,
        visibility = visibility,
        **kwargs
    )

def _extract_requirements_from_deps(deps):
    """Extract requirements from multiplatform_py_library dependencies.
    
    This function looks for dependencies that follow the multiplatform pattern
    and extracts their requirements to avoid duplication.
    
    Args:
        deps: List of dependency targets
        
    Returns:
        List of requirement names that should be added to platform-specific builds
    """
    # For now, this is a placeholder. In practice, we'd need to analyze
    # the build graph to determine which deps are multiplatform_py_library targets
    # and extract their requirements. This would require more advanced Bazel
    # introspection capabilities.
    return []

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
    
    There are two usage patterns:
    1. Legacy pattern: Specify requirements directly in the binary
    2. New pattern: Use multiplatform_py_library and omit requirements (auto-detected)
    
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
    
    # Determine if we should use the new pattern (no requirements = auto-detect from libs)
    use_auto_platform_deps = len(requirements) == 0
    
    # Build deps lists for each platform
    dev_deps = deps[:] if deps else []
    amd64_deps = []
    arm64_deps = []
    
    # Process dependencies
    if deps:
        for dep in deps:
            if use_auto_platform_deps and dep.startswith(":"):
                # New pattern: Try to use platform-specific variants from multiplatform_py_library
                base_name = dep[1:]  # Remove the ":"
                amd64_variant = ":" + base_name + "_linux_amd64"
                arm64_variant = ":" + base_name + "_linux_arm64"
                
                amd64_deps.append(amd64_variant)
                arm64_deps.append(arm64_variant)
            else:
                # Legacy pattern or external dependency - use as-is for platform binaries
                amd64_deps.append(dep)
                arm64_deps.append(dep)
    
    # Add platform-specific requirements (legacy pattern)
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