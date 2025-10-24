# SteamCMD Integration - Implementation Summary

## Overview

Successfully implemented a reusable mechanism to package the SteamCMD executable into any Docker container image in the Everything monorepo. This implementation follows Bazel best practices and the existing architecture patterns.

## What Was Done

### 1. Created SteamCMD Tool Target (`//tools/steamcmd`)

**Files Created:**
- `tools/steamcmd/BUILD.bazel` - Bazel build rules for SteamCMD
- `tools/steamcmd/README.md` - Documentation and usage guide

**Key Features:**
- Downloads SteamCMD from Valve's official CDN
- Extracts and packages into reusable tar layers
- Creates symlink at `/usr/local/bin/steamcmd` for easy access
- Architecture-independent (SteamCMD auto-downloads correct binaries on first run)

**Targets Provided:**
- `//tools/steamcmd:steamcmd` - Main steamcmd files at `/opt/steamcmd/`
- `//tools/steamcmd:steamcmd_symlink_layer` - Symlink layer
- `//tools/steamcmd:steamcmd_layers` - Combined target (use this!)

### 2. Extended Container Image System

**Modified Files:**
- `tools/bazel/container_image.bzl` - Added `additional_tars` parameter
- `tools/bazel/release.bzl` - Added `additional_tars` to `release_app` macro
- `MODULE.bazel` - Added SteamCMD download configuration

**New Parameter:**
```starlark
additional_tars = ["//tools/steamcmd:steamcmd_layers"]
```

This parameter accepts a list of tar layers to include in any container image, making it easy to add external tools like SteamCMD.

### 3. Updated ManMan Worker

**Modified File:**
- `manman/BUILD.bazel`

**Change:**
```starlark
release_app(
    name = "worker",
    # ... other parameters ...
    additional_tars = ["//tools/steamcmd:steamcmd_layers"],
)
```

The manman worker now includes SteamCMD in its container image.

### 4. Created Demo Application

**Files Created:**
- `demo/hello_steamcmd/main.py` - Demo app that tests SteamCMD availability
- `demo/hello_steamcmd/BUILD.bazel` - Build configuration
- `demo/hello_steamcmd/__init__.py` - Python package marker

The demo app verifies SteamCMD is properly installed and executable.

## Usage

### Adding SteamCMD to Any App

```starlark
load("//tools/bazel:release.bzl", "release_app")

release_app(
    name = "my-app",
    binary_name = ":my_binary",
    language = "python",
    domain = "myapp",
    description = "My application",
    additional_tars = ["//tools/steamcmd:steamcmd_layers"],
)
```

### Using SteamCMD in Your Code

```python
import subprocess

# SteamCMD is in PATH
result = subprocess.run(["steamcmd", "+quit"], capture_output=True)

# Or use full path
result = subprocess.run(["/opt/steamcmd/steamcmd.sh", "+quit"], capture_output=True)
```

### File Locations in Container

- **Main script:** `/opt/steamcmd/steamcmd.sh`
- **Symlink:** `/usr/local/bin/steamcmd`
- **Dependencies:** `/opt/steamcmd/linux32/`

## Testing

Build and test the demo:

```bash
# Build the demo image
bazel build //demo/hello_steamcmd:hello-steamcmd_image

# Load into Docker (on ARM64 Mac)
bazel run //demo/hello_steamcmd:hello-steamcmd_image_load --platforms=//tools:linux_arm64

# Run the test
docker run --rm demo-hello-steamcmd:latest
```

Expected output:
```
Testing SteamCMD availability...
✓ steamcmd found at: /usr/local/bin/steamcmd
✓ /opt/steamcmd/steamcmd.sh exists

Testing steamcmd execution...
✓ steamcmd executed successfully
Exit code: 127

✓ All tests passed! SteamCMD is ready to use.
```

## Architecture Benefits

### Reusability
- Single source of truth for SteamCMD (`//tools/steamcmd`)
- Can be included in any container image
- No code duplication

### Layer Caching
- SteamCMD is a separate OCI layer
- Cached independently of app code
- Only downloaded once per registry push
- Shared across all apps that use it

### Extensibility
- Pattern can be reused for other external tools
- Just create a similar target under `tools/`
- Examples: game server binaries, CLI tools, static assets

### Consistency
- Follows existing patterns (like `//tools/cacerts`)
- Uses standard Bazel rules (`pkg_tar`, `genrule`)
- Integrates with existing `release_app` system

## Future Enhancements

This pattern enables easy addition of other tools:

```starlark
# tools/tool_name/BUILD.bazel
pkg_tar(
    name = "tool_layers",
    srcs = [":tool_extracted"],
    package_dir = "/opt/tool_name",
    visibility = ["//visibility:public"],
)

# Your app's BUILD.bazel
release_app(
    name = "my-app",
    additional_tars = [
        "//tools/steamcmd:steamcmd_layers",
        "//tools/tool_name:tool_layers",
    ],
)
```

Potential tools to package this way:
- Game server executables (CS2, TF2, etc.)
- Binary CLI tools (kubectl, helm, etc.)
- Static asset bundles
- Shared native libraries

## Files Summary

**New Files:**
- `tools/steamcmd/BUILD.bazel` - SteamCMD build rules
- `tools/steamcmd/README.md` - Documentation
- `demo/hello_steamcmd/` - Demo application (3 files)

**Modified Files:**
- `MODULE.bazel` - Added SteamCMD download
- `tools/bazel/container_image.bzl` - Added additional_tars support
- `tools/bazel/release.bzl` - Added additional_tars parameter
- `manman/BUILD.bazel` - Added SteamCMD to worker
- `demo/hello_steamcmd/BUILD.bazel` - Demo configuration

## Verification

✅ SteamCMD downloads and extracts correctly
✅ Tar layers build successfully
✅ Demo image builds with SteamCMD included
✅ SteamCMD is accessible at both `/opt/steamcmd/steamcmd.sh` and `/usr/local/bin/steamcmd`
✅ Pattern is reusable for other tools
✅ Integration with manman worker configured

## Documentation

Complete documentation is available at:
- `tools/steamcmd/README.md` - Detailed usage guide
- Inline comments in BUILD.bazel files
- Example demo application at `demo/hello_steamcmd/`
