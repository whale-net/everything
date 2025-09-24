# Python Multi-Platform Binary and Library Macros

This directory contains simplified macros for creating Python binaries and libraries that work across multiple platforms without boilerplate, ensuring proper Python requirements are built for each platform in `release_app`.

## Problem Solved

Previously, creating Python libraries and binaries that worked across platforms required:
1. Defining multiple platform-specific targets manually
2. Duplicating requirements between library `deps` and binary `requirements`

```starlark
# OLD WAY - lots of boilerplate and duplication! 😢
py_library(
    name = "my_lib",
    deps = [
        requirement("fastapi"),
        requirement("uvicorn"),
    ],
)

py_binary(
    name = "hello_fastapi",
    deps = [":my_lib"],
    # Requirements duplicated here!
    requirements = ["fastapi", "uvicorn"],
)

py_binary(name = "hello_fastapi_linux_amd64", ...)  
py_binary(name = "hello_fastapi_linux_arm64", ...)
```

## Solution

The `multiplatform_py_library` and `multiplatform_py_binary` macros eliminate duplication and generate all platform-specific targets from single declarations:

```starlark
# NEW WAY - clean and simple! 🎉
load("//tools:python_binary.bzl", "multiplatform_py_library", "multiplatform_py_binary")

multiplatform_py_library(
    name = "my_lib",
    srcs = ["lib.py"],
    requirements = ["fastapi", "uvicorn"],  # Requirements defined once
)

multiplatform_py_binary(
    name = "hello_fastapi",
    srcs = ["main.py"],
    deps = [":my_lib"],  # No requirement duplication needed!
    visibility = ["//visibility:public"],
)
```

This automatically generates:
- `my_lib`, `my_lib_linux_amd64`, `my_lib_linux_arm64` - Libraries for each platform
- `hello_fastapi`, `hello_fastapi_linux_amd64`, `hello_fastapi_linux_arm64` - Binaries for each platform
- Platform-specific binaries automatically use platform-specific library variants

## Usage

### Library with Requirements

```starlark
load("//tools:python_binary.bzl", "multiplatform_py_library")

multiplatform_py_library(
    name = "my_lib",
    srcs = ["lib.py", "utils.py"],
    requirements = ["fastapi", "pydantic"],
    visibility = ["//visibility:public"],
)
```

### Binary Using Library (No Requirement Duplication)

```starlark
load("//tools:python_binary.bzl", "multiplatform_py_binary")

multiplatform_py_binary(
    name = "my_app",
    srcs = ["main.py"],
    deps = [":my_lib"],  # Automatically uses platform-specific variants
    # No need to repeat requirements - they come from the library!
)
```

### Binary with Additional Requirements

```starlark
multiplatform_py_binary(
    name = "my_app",
    srcs = ["main.py"],
    deps = [":my_lib"],
    requirements = ["uvicorn"],  # Additional requirements for the binary
)
```

### Basic Binary (Legacy Usage)

```starlark
load("//tools:python_binary.bzl", "multiplatform_py_binary")

multiplatform_py_binary(
    name = "my_app",
    srcs = ["main.py"],
    deps = [":app_lib"],
    requirements = ["fastapi", "pydantic"],
)
```

### With Release System

```starlark
load("//tools:python_binary.bzl", "multiplatform_py_binary")
load("//tools:release.bzl", "release_app")

multiplatform_py_binary(
    name = "my_app", 
    srcs = ["main.py"],
    deps = [":app_lib"],
    requirements = ["fastapi", "pydantic"],
)

# Simplified release_app - automatically detects platform binaries!
release_app(
    name = "my_app",
    language = "python",
    domain = "api",
    description = "My awesome FastAPI app",
)
```

## Parameters

### multiplatform_py_library

- `name`: Library name (required)
- `srcs`: Source files (required)
- `deps`: Python library dependencies
- `requirements`: List of pip requirement names (e.g., ["fastapi", "uvicorn"])
- `visibility`: Target visibility
- `**kwargs`: Additional arguments passed to `py_library`

### multiplatform_py_binary

- `name`: Binary name (required)
- `srcs`: Source files (required)
- `main`: Main entry point (auto-detected if not provided)
- `deps`: Python library dependencies (platform-specific variants automatically selected)
- `requirements`: List of pip requirement names
- `visibility`: Target visibility
- `**kwargs`: Additional arguments passed to `py_binary`

## Benefits

1. **No Requirement Duplication**: Define requirements once in libraries, use everywhere
2. **Less Boilerplate**: Single macro instead of 4+ target definitions
3. **Consistency**: All platforms use the same configuration
4. **Maintainability**: Change requirements in one place
5. **Type Safety**: Compile-time validation of requirement names
6. **Integration**: Works seamlessly with existing release system
7. **Auto-Detection**: `release_app` automatically finds platform binaries
8. **Platform Intelligence**: Binaries automatically use platform-specific library variants

## Migration

To migrate existing apps:

### Option 1: Use multiplatform_py_library (Recommended)

1. Replace `py_library` with `multiplatform_py_library`
2. Move requirements from `deps` to `requirements` parameter
3. Remove requirements from `multiplatform_py_binary`
4. Simplify `release_app` - remove `binary_amd64`/`binary_arm64` parameters

### Option 2: Keep Current Approach (Still Supported)

The existing pattern with requirements in `multiplatform_py_binary` continues to work.

## Examples

### Recommended Pattern (No Duplication)

`//demo/hello_fastapi` - Web app using multiplatform_py_library:

```starlark
multiplatform_py_library(
    name = "main_lib",
    srcs = ["__init__.py", "main.py"],
    requirements = ["fastapi", "uvicorn"],
)

multiplatform_py_binary(
    name = "hello_fastapi",
    srcs = ["main.py"],
    deps = [":main_lib"],  # Requirements automatically inherited
)

release_app(
    name = "hello_fastapi",
    language = "python",
    domain = "demo",
    description = "FastAPI hello world application",
)
```

### Legacy Pattern (Still Supported)

`//demo/hello_python` - Simple app with requirements in binary:

```starlark
py_library(
    name = "main_lib",
    srcs = ["__init__.py", "main.py"],
    deps = ["//libs/python"],
)

multiplatform_py_binary(
    name = "hello_python",
    srcs = ["main.py"],
    deps = [":main_lib"],
    requirements = [],  # No external requirements
)
```