"""Placeholder for future custom Python toolchain implementation.

Currently unused - optimization happens in container_image.bzl via genrule.
This file documents what a full toolchain solution would look like.
"""

load("@rules_pkg//:pkg.bzl", "pkg_tar")

def optimized_python_layer(name, python_runtime_tar, python_version = "3.13", visibility = None):
    """Create optimized Python runtime layer (unused - kept for reference)."""
    pass

def optimized_python_runtime(name, python_version, platform, base_runtime, **kwargs):
    """Placeholder for custom toolchain (unused)."""
    native.alias(name = name, actual = base_runtime, **kwargs)
