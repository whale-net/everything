"""Helper macros for creating layered container images with optimized caching.

This module provides utilities to create multi-layer container images where dependencies
are separated into different layers based on their change frequency, enabling better
Docker layer cache hit rates.
"""

load("@rules_python//python:defs.bzl", "py_library")
load("@rules_pkg//:pkg.bzl", "pkg_tar")

def py_layer_group(name, deps, visibility = None):
    """Create a py_library that groups dependencies for a specific layer.
    
    This is a helper macro to create explicit dependency groups that can be
    packaged into separate Docker layers for better caching.
    
    Usage:
        # In your app's BUILD.bazel:
        py_layer_group(
            name = "pip_deps_layer",
            deps = [
                "@pypi//fastapi",
                "@pypi//uvicorn",
                "@pypi//pydantic",
            ],
        )
        
        py_layer_group(
            name = "libs_layer",
            deps = ["//libs/python"],
        )
        
        py_binary(
            name = "my_app",
            srcs = ["main.py"],
            deps = [
                ":pip_deps_layer",
                ":libs_layer",
            ],
        )
        
        # Then use layered_container_image instead of container_image
        
    Args:
        name: Name of the layer group
        deps: List of dependency labels to include in this layer
        visibility: Visibility of the generated py_library
    """
    py_library(
        name = name,
        deps = deps,
        visibility = visibility,
    )
    
    # Also create a pkg_tar for this layer
    pkg_tar(
        name = name + "_tar",
        deps = [":" + name],
        package_dir = "/app",
        include_runfiles = True,
        tags = ["manual"],
        visibility = ["//visibility:private"],
    )

def create_dependency_layers(name, binary, pip_deps = None, internal_deps = None):
    """Create explicit dependency layer groups for a Python binary.
    
    This macro creates py_library wrapper targets for different dependency categories,
    enabling better Docker layer caching by separating frequently-changed code from
    stable dependencies.
    
    Args:
        name: Base name for generated targets
        binary: The py_binary target (as a string label, e.g., ":my_app")
        pip_deps: List of @pypi// dependencies (optional)
        internal_deps: List of internal workspace dependencies like //libs/python (optional)
    
    Returns:
        A dict mapping layer names to tar target labels that can be passed to container_image
    """
    layers = {}
    
    if pip_deps:
        py_layer_group(
            name = name + "_pip_deps_layer",
            deps = pip_deps,
        )
        layers["00_pip_deps"] = ":" + name + "_pip_deps_layer_tar"
    
    if internal_deps:
        py_layer_group(
            name = name + "_internal_deps_layer",
            deps = internal_deps,
        )
        layers["01_internal_deps"] = ":" + name + "_internal_deps_layer_tar"
    
    return layers
