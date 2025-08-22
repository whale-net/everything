# Everything Monorepo

This is a Bazel monorepo that supports both Python and Go development with a clean, organized structure.

## Structure

```
├── hello_python/          # Python application
├── hello_go/              # Go application
├── libs/                  # Shared libraries
│   ├── python/            # Python common library
│   └── go/                # Go common library
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
# Run all tests (most common workflow)
bazel test //...

# Run specific tests
bazel test //hello_python:test_main
bazel test //hello_go:main_test

# Run applications
bazel run //hello_python:hello_python
bazel run //hello_go:hello_go

# Build all targets
bazel build //...

# Using convenient aliases
bazel run //:run-python
bazel run //:run-go

# Run tests with detailed output
bazel test --config=ci //...
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
1. Create directory at top level
2. Add Python source files
3. Create `BUILD.bazel` with appropriate `py_binary` and `py_test` targets
4. Reference shared libraries from `//libs/python`

#### Adding a New Go App
1. Create directory at top level
2. Add Go source files
3. Create `BUILD.bazel` with appropriate `go_binary` and `go_test` targets
4. Reference shared libraries from `//libs/go`

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

## CI/CD Pipeline

The repository uses GitHub Actions for continuous integration with a sequential build → test workflow:

```mermaid
graph TD
    A[Push/PR] --> B[Build Job]
    B --> C{Build Success?}
    C -->|Yes| D[Test Job]
    C -->|Yes| E[Docker Job]
    C -->|No| F[Pipeline Fails]
    D --> G[Upload Test Results]
    E --> H[Build Docker Images]
    B --> I[Upload Build Artifacts]
    E --> J{Main Branch?}
    J -->|Yes| K[Push to Registry]
    J -->|No| L[Save as Artifacts]
    
    style B fill:#e1f5fe
    style D fill:#f3e5f5
    style E fill:#fff3e0
    style F fill:#ffebee
    style G fill:#e8f5e8
    style H fill:#e8f5e8
    style I fill:#e8f5e8
    style K fill:#e3f2fd
    style L fill:#f1f8e9
```

### CI Jobs:
- **Build**: Compiles applications and uploads artifacts
- **Test**: Runs all tests (only if build succeeds)
- **Docker**: Builds container images for each binary
- **Future**: Deploy job will use the Docker images

## Docker Images

Each application is automatically containerized using Bazel's integrated OCI image rules with optimized distroless images.

### Generic OCI Image Rules
The repository provides reusable OCI image building rules in `//tools:oci.bzl`:

#### Python Applications
```starlark
load("//tools:oci.bzl", "python_oci_image")

python_oci_image(
    name = "my_app_image",
    binary = "my_app_binary",
    repo_tag = "my-app:latest",  # optional, defaults to name:latest
)
```

#### Go Applications  
```starlark
load("//tools:oci.bzl", "go_oci_image")

go_oci_image(
    name = "my_app_image", 
    binary = "my_app_binary",
    repo_tag = "my-app:latest",  # optional, defaults to name:latest
)
```

#### Generic Applications
```starlark
load("//tools:oci.bzl", "generic_oci_image")

generic_oci_image(
    name = "my_app_image",
    binary = "my_app_binary", 
    base_image = "@some_base_image",
    binary_path = "/app/myapp",  # optional, defaults to /binary_name/binary_name
    repo_tag = "my-app:latest",  # optional, defaults to name:latest
)
```

### Features of Generic Rules
- **Automatic layer creation**: Binary is automatically packaged into a tar layer
- **Consistent naming**: Image and tarball targets follow predictable patterns
- **Distroless base images**: Uses secure, minimal base images by default
- **Docker integration**: Each rule creates both an image and a tarball for Docker loading
- **Flexible configuration**: Support for custom base images, paths, and tags

### Building Images with Bazel
```bash
# Build individual images
bazel build --platforms=@rules_go//go/toolchain:linux_amd64 //hello_python:hello_python_image
bazel build --platforms=@rules_go//go/toolchain:linux_amd64 //hello_go:hello_go_image

# Build all images in the workspace
bazel build --platforms=@rules_go//go/toolchain:linux_amd64 $(bazel query "kind('oci_image', //...)")

# Build and load into Docker
bazel run --platforms=@rules_go//go/toolchain:linux_amd64 //hello_python:hello_python_image_tarball
bazel run --platforms=@rules_go//go/toolchain:linux_amd64 //hello_go:hello_go_image_tarball
```

### CI/CD Docker Workflow
- **Integrated builds**: CI uses Bazel OCI rules directly, no external Dockerfiles needed
- **Automatic image discovery**: CI automatically finds and builds all OCI image targets
- **PR builds**: Docker images saved as artifacts for testing
- **Main branch**: Images automatically tagged and pushed to Docker registry
- **Consistent tagging**: Images tagged with both `latest` and commit SHA
