# Everything Monorepo

This is a Bazel monorepo that supports both Python and Go development with a clean, organized structure.

## Structure

```
├── apps/                   # Applications
│   ├── hello_python/      # Python application
│   └── hello_go/          # Go application
├── libs/                  # Shared libraries
│   ├── common_py/         # Python common library
│   └── common_go/         # Go common library
├── MODULE.bazel           # Bazel module definition
├── BUILD.bazel            # Root build file
├── go.mod                 # Go module definition
├── requirements.in        # Python dependencies
├── requirements.lock.txt  # Locked Python dependencies
└── .bazelrc              # Bazel configuration
```

## Quick Start

### Prerequisites
- Bazel 7.0+ with bzlmod support
- Python 3.11+
- Go 1.21+

### Building and Testing

```bash
# Run all tests
bazel test //...

# Run specific tests
bazel test //apps/hello_python:test_main
bazel test //apps/hello_go:main_test

# Run applications
bazel run //apps/hello_python:hello_python
bazel run //apps/hello_go:hello_go

# Build all targets
bazel build //...
```

### Adding Dependencies

#### Python Dependencies
1. Add package to `requirements.in`
2. Run `bazel run //:pip_compile` to update `requirements.lock.txt`
3. Use `requirement("package-name")` in BUILD.bazel files

#### Go Dependencies
1. Add dependency to `go.mod`
2. Run `bazel run //:gazelle-update-repos` to update Bazel dependencies
3. Import normally in Go code

### Development Workflow

#### Adding a New Python App
1. Create directory under `apps/`
2. Add Python source files
3. Create `BUILD.bazel` with appropriate `py_binary` and `py_test` targets
4. Reference shared libraries from `//libs/common_py`

#### Adding a New Go App
1. Create directory under `apps/`
2. Add Go source files
3. Create `BUILD.bazel` with appropriate `go_binary` and `go_test` targets
4. Reference shared libraries from `//libs/common_go`

#### Adding Shared Libraries
- Python: Create under `libs/` with appropriate `py_library` targets
- Go: Create under `libs/` with appropriate `go_library` targets

## Features

- **Multi-language Support**: Both Python and Go in the same repository
- **Hermetic Builds**: All dependencies managed by Bazel
- **Fast Testing**: Incremental builds and test caching
- **Code Sharing**: Common libraries shared between applications
- **Modern Tooling**: Uses bzlmod for dependency management

## Configuration

- `.bazelrc`: Contains common Bazel configuration
- `MODULE.bazel`: Defines external dependencies
- `go.mod`: Go module configuration
- `requirements.in`: Python dependencies specification
