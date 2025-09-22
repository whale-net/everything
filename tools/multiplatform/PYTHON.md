# Python Multi-Platform Binary Macro

This directory contains a simplified macro for creating Python binaries that work across multiple platforms without boilerplate.

## Problem Solved

Previously, creating a Python binary that worked across platforms required defining multiple `py_binary` targets manually:

```starlark
# OLD WAY - lots of boilerplate! 😢
py_binary(name = "hello_fastapi", ...)
py_binary(name = "hello_fastapi_linux_amd64", ...)  
py_binary(name = "hello_fastapi_linux_arm64", ...)
py_binary(name = "hello_fastapi_macos_arm64", ...)
```

## Solution

The `multiplatform_py_binary` macro generates all platform-specific binaries from a single declaration:

```starlark
# NEW WAY - clean and simple! 🎉
multiplatform_py_binary(
    name = "hello_fastapi",
    srcs = ["main.py"],
    main = "main.py", 
    deps = [":main_lib"],
    requirements = ["fastapi", "uvicorn"],
    visibility = ["//visibility:public"],
)
```

This automatically generates:
- `hello_fastapi` - Development binary (uses dev requirements)
- `hello_fastapi_linux_amd64` - AMD64 container binary
- `hello_fastapi_linux_arm64` - ARM64 container binary

## Usage

### Basic Binary

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

- `name`: Binary name (required)
- `srcs`: Source files (required)
- `main`: Main entry point (auto-detected if not provided)
- `deps`: Python library dependencies
- `requirements`: List of pip requirement names
- `visibility`: Target visibility
- `**kwargs`: Additional arguments passed to `py_binary`

## Benefits

1. **Less Boilerplate**: Single macro instead of 4+ binary definitions
2. **Consistency**: All platforms use the same configuration
3. **Maintainability**: Change requirements in one place
4. **Type Safety**: Compile-time validation of requirement names
5. **Integration**: Works seamlessly with existing release system
6. **Auto-Detection**: `release_app` automatically finds platform binaries

## Migration

To migrate existing apps:

1. Replace multiple `py_binary` targets with single `multiplatform_py_binary`
2. Move requirements from `deps` to `requirements` parameter
3. Simplify `release_app` - remove `binary_amd64`/`binary_arm64` parameters
4. Remove manual platform-specific requirement imports
5. Replace `simple_py_library` with standard `py_library`

## Examples

See these apps for complete examples:
- `//demo/hello_python` - Simple app with no external requirements
- `//demo/hello_fastapi` - Web app with FastAPI and uvicorn requirements