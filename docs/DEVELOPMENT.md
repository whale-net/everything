# Development Guide

This guide covers the development workflow for adding new applications and shared libraries to the monorepo.

## Adding a New Python App

### 1. Create a directory
```bash
mkdir my_python_app
```

### 2. Add Python source files
- `__init__.py` - Required for Python package structure
- `main.py` - Your main application code
- `test_main.py` - Tests for your application

### 3. Create `BUILD.bazel` with the required targets

```starlark
load("@rules_python//python:defs.bzl", "py_binary", "py_library", "py_test")
load("//tools:release.bzl", "release_app")

py_library(
    name = "main_lib",
    srcs = ["__init__.py", "main.py"],
    deps = [
        "//libs/python",
        "@pypi//:fastapi",
        "@pypi//:uvicorn",
    ],
    visibility = ["//my_python_app:__pkg__"],
)

py_binary(
    name = "my_python_app",
    srcs = ["main.py"],
    main = "main.py",
    deps = [":main_lib"],
    args = ["run-server"],  # Command-line arguments
    visibility = ["//visibility:public"],
)

py_test(
    name = "test_main",
    srcs = ["test_main.py"],
    deps = [
        ":main_lib",
        "@pypi//:pytest",  # Direct reference with top-level colon
    ],
    size = "small",
)

# Release metadata and OCI images for this app
release_app(
    name = "my_python_app",
    binary_target = ":my_python_app",
    language = "python",
    domain = "demo",  # Required: categorizes your app (e.g., "api", "web", "demo")
    description = "Description of what this app does",
    # Note: args, port, app_type are automatically extracted from the binary
)
```

### 4. Reference shared libraries
From `//libs/python` (already included in the example above)

## Example Python App Structure

Here's a complete example of a minimal Python app structure:

```
my_python_app/
├── __init__.py
├── main.py
├── test_main.py
└── BUILD.bazel
```

**`__init__.py`**:
```python
"""My Python App."""
```

**`main.py`**:
```python
"""My Python application."""

from libs.python.utils import format_greeting, get_version

def get_message():
    """Get a greeting message."""
    return format_greeting("My App")

def main():
    """Main entry point."""
    print(get_message())
    print(f"Version: {get_version()}")

if __name__ == "__main__":
    main()
```

**`test_main.py`**:
```python
"""Tests for my app."""

import pytest
from my_python_app.main import get_message

def test_get_message():
    """Test the get_message function."""
    message = get_message()
    assert "Hello, My App from Python!" in message

def test_get_message_not_empty():
    """Test that get_message returns a non-empty string."""
    assert len(get_message()) > 0
```

## Adding a New Go App

### 1. Create a directory
```bash
mkdir my_go_app
```

### 2. Add Go source files
- `main.go` - Your main application code
- `main_test.go` - Tests for your application

### 3. Create `BUILD.bazel` with the required targets

```starlark
load("@rules_go//go:def.bzl", "go_binary", "go_test")
load("//tools:release.bzl", "release_app")

go_binary(
    name = "my_go_app",
    srcs = ["main.go"],
    deps = ["//libs/go"],
    visibility = ["//visibility:public"],
)

go_test(
    name = "main_test",
    srcs = ["main_test.go"],
    deps = ["//libs/go"],
    size = "small",
)

# Release metadata and OCI images for this app
release_app(
    name = "my_go_app",
    binary_target = ":my_go_app",
    language = "go",
    domain = "demo",  # Required: categorizes your app (e.g., "api", "web", "demo")
    description = "Description of what this app does",
    # Note: port and app_type are automatically extracted from the binary
)
```

### 4. Reference shared libraries
From `//libs/go` (already included in the example above)

## Example Go App Structure

Here's a complete example of a minimal Go app structure:

```
my_go_app/
├── main.go
├── main_test.go
└── BUILD.bazel
```

**`main.go`**:
```go
// My Go application
package main

import (
	"fmt"
	"github.com/example/everything/libs/go"
)

func main() {
	message := go_lib.FormatGreeting("My Go App")
	fmt.Println(message)
	fmt.Printf("Version: %s\n", go_lib.GetVersion())
}
```

**`main_test.go`**:
```go
package main

import (
	"testing"
	"github.com/example/everything/libs/go"
)

func TestMain(t *testing.T) {
	message := go_lib.FormatGreeting("Test")
	if message == "" {
		t.Error("Expected non-empty message")
	}
}
```

## Verifying Your New App

After creating your app, verify it's set up correctly:

### 1. Check that your app can be built
```bash
# For Python apps
bazel build //my_python_app:my_python_app

# For Go apps  
bazel build //my_go_app:my_go_app
```

### 2. Run your tests
```bash
# For Python apps
bazel test //my_python_app:test_main

# For Go apps
bazel test //my_go_app:main_test
```

### 3. Verify the release system can discover your app
```bash
bazel query "kind('app_metadata', //...)"
```
Your app should appear in the list as `//my_app:my_app_metadata`

### 4. Test running your app
```bash
# For Python apps
bazel run //my_python_app:my_python_app

# For Go apps
bazel run //my_go_app:my_go_app
```

## Adding Shared Libraries

- **Python**: Create under `libs/` with appropriate `py_library` targets
- **Go**: Create under `libs/` with appropriate `go_library` targets

## Working with Generated Code

### OpenAPI Client Generation

The repository uses Bazel rules to generate OpenAPI clients from specs. Clients are generated on-demand during builds.

**For Bazel Builds (Production)**:
```starlark
# In BUILD.bazel
py_binary(
    name = "my_app",
    deps = [
        "//generated/namespace:client_name",
    ],
)
```

**For Local Development (IDE Support)**:

To get IDE autocomplete and type hints for generated clients:

```bash
# Sync generated clients to local directory
./tools/scripts/sync_generated_clients.sh
```

This script:
- Discovers all `openapi_client` targets using `bazel query`
- Builds them with Bazel
- Copies to local `generated/` directories for IDE support

**Example imports**:
```python
from generated.namespace.client_name import DefaultApi
from generated.namespace.client_name.models import SomeModel
```

**Discovery**:
```bash
# Find all OpenAPI client targets
bazel query 'kind("openapi_client_rule", //generated/...)'

# Build specific namespace
bazel build //generated/namespace:all
```
