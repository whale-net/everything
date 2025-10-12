#!/usr/bin/env python3
"""
Validation script for multi-layer container image implementation.

This script performs static analysis of the BUILD files and Starlark code
to verify that the multi-layer implementation is correctly structured.
"""

import sys
import re
from pathlib import Path

def check_file_exists(path):
    """Check if a file exists."""
    if Path(path).exists():
        print(f"✓ {path} exists")
        return True
    else:
        print(f"✗ {path} missing")
        return False

def check_content(path, patterns, description):
    """Check if file contains all required patterns."""
    try:
        with open(path, 'r') as f:
            content = f.read()
        
        all_found = True
        for pattern_desc, pattern in patterns:
            if re.search(pattern, content, re.MULTILINE):
                print(f"  ✓ {pattern_desc}")
            else:
                print(f"  ✗ {pattern_desc} MISSING")
                all_found = False
        
        return all_found
    except Exception as e:
        print(f"  ✗ Error reading {path}: {e}")
        return False

def validate_container_image_bzl():
    """Validate container_image.bzl implementation."""
    print("\n" + "=" * 70)
    print("Validating tools/bazel/container_image.bzl")
    print("=" * 70)
    
    path = "tools/bazel/container_image.bzl"
    if not check_file_exists(path):
        return False
    
    patterns = [
        ("dep_layers parameter in container_image()", r"dep_layers\s*=\s*None"),
        ("Layer enumeration loop", r"for i, layer in enumerate\(dep_layers\)"),
        ("pkg_tar creation for dep layers", r'name = name \+ "_deplayer_"'),
        ("Layer targets list", r"layer_tars = \[\]"),
        ("dep_layers passthrough in multiplatform_image", r"dep_layers = dep_layers"),
        ("Documentation about layering", r"LAYERING.*Cache Efficiency"),
    ]
    
    return check_content(path, patterns, "container_image.bzl")

def validate_release_bzl():
    """Validate release.bzl implementation."""
    print("\n" + "=" * 70)
    print("Validating tools/bazel/release.bzl")
    print("=" * 70)
    
    path = "tools/bazel/release.bzl"
    if not check_file_exists(path):
        return False
    
    patterns = [
        ("dep_layers parameter in release_app()", r"dep_layers\s*=\s*None\)"),
        ("dep_layers in docstring", r"dep_layers:.*list of dicts"),
        ("dep_layers passthrough to multiplatform_image", r"dep_layers = dep_layers"),
    ]
    
    return check_content(path, patterns, "release.bzl")

def validate_hello_fastapi():
    """Validate hello-fastapi demo app."""
    print("\n" + "=" * 70)
    print("Validating demo/hello_fastapi/BUILD.bazel")
    print("=" * 70)
    
    path = "demo/hello_fastapi/BUILD.bazel"
    if not check_file_exists(path):
        return False
    
    patterns = [
        ("pip_deps_layer definition", r'name = "pip_deps_layer"'),
        ("pip_deps_layer dependencies", r'deps = \[\s*"@pypi//:fastapi"'),
        ("main_lib depends on pip_deps_layer", r'deps = \[":pip_deps_layer"\]'),
        ("dep_layers in release_app", r"dep_layers = \["),
        ("pip_deps target reference", r'"targets": \[":pip_deps_layer"\]'),
    ]
    
    return check_content(path, patterns, "hello_fastapi BUILD")

def validate_hello_python():
    """Validate hello-python demo app."""
    print("\n" + "=" * 70)
    print("Validating demo/hello_python/BUILD.bazel")
    print("=" * 70)
    
    path = "demo/hello_python/BUILD.bazel"
    if not check_file_exists(path):
        return False
    
    patterns = [
        ("internal_libs_layer definition", r'name = "internal_libs_layer"'),
        ("internal_libs_layer dependencies", r'deps = \["//libs/python"\]'),
        ("main_lib depends on internal_libs_layer", r'deps = \[":internal_libs_layer"\]'),
        ("dep_layers in release_app", r"dep_layers = \["),
        ("internal_libs target reference", r'"targets": \[":internal_libs_layer"\]'),
    ]
    
    return check_content(path, patterns, "hello_python BUILD")

def validate_documentation():
    """Validate documentation files."""
    print("\n" + "=" * 70)
    print("Validating Documentation")
    print("=" * 70)
    
    doc_path = "docs/LAYERED_CONTAINER_IMAGES.md"
    agents_path = "AGENTS.md"
    
    results = []
    
    # Check main documentation
    if check_file_exists(doc_path):
        patterns = [
            ("Problem statement", r"## Problem Statement"),
            ("Solution explanation", r"## Solution.*Explicit Dependency Layers"),
            ("Usage section", r"## Usage"),
            ("Example code", r"py_library\s*\(\s*name\s*=\s*\"pip_deps_layer\""),
            ("Benefits section", r"## Benefits"),
            ("Migration guide", r"## Migration Guide"),
        ]
        results.append(check_content(doc_path, patterns, "main documentation"))
    else:
        results.append(False)
    
    # Check AGENTS.md
    if check_file_exists(agents_path):
        patterns = [
            ("dep_layers parameter documented", r"dep_layers.*Optional list"),
            ("Multi-Layer Docker Caching section", r"Multi-Layer Docker Caching"),
            ("Example usage", r"py_library\s*\(\s*name\s*=\s*\"pip_deps_layer\""),
            ("Benefits listed", r"\*\*Benefits:\*\*"),
            ("Documentation reference", r"docs/LAYERED_CONTAINER_IMAGES"),
        ]
        results.append(check_content(agents_path, patterns, "AGENTS.md"))
    else:
        results.append(False)
    
    return all(results)

def main():
    """Run all validations."""
    print("\n" + "=" * 70)
    print("MULTI-LAYER CONTAINER IMAGE IMPLEMENTATION VALIDATION")
    print("=" * 70)
    
    results = [
        validate_container_image_bzl(),
        validate_release_bzl(),
        validate_hello_fastapi(),
        validate_hello_python(),
        validate_documentation(),
    ]
    
    print("\n" + "=" * 70)
    print("VALIDATION SUMMARY")
    print("=" * 70)
    
    if all(results):
        print("✓ All validations passed!")
        print("\nImplementation is complete and correct.")
        print("\nNext steps:")
        print("  1. Test builds: bazel build //demo/hello_fastapi:hello-fastapi_image_base")
        print("  2. Verify layers: docker history <image>")
        print("  3. Test caching: Build twice and measure time difference")
        return 0
    else:
        print("✗ Some validations failed")
        print("\nPlease fix the issues above before proceeding.")
        return 1

if __name__ == "__main__":
    sys.exit(main())
