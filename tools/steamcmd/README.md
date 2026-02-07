# SteamCMD Container Layer

This directory provides a reusable Bazel target for packaging [SteamCMD](https://developer.valvesoftware.com/wiki/SteamCMD) into container images with all necessary 32-bit library dependencies included.

## What is SteamCMD?

SteamCMD is Valve's command-line tool for installing and updating Steam game servers. It's essential for managing dedicated game servers for games like Counter-Strike, Team Fortress 2, Left 4 Dead, and many others.

**Important**: SteamCMD is a 32-bit application. This package bundles all required 32-bit libraries, so no additional setup is needed.

## Usage

### Adding to Your Container Image

To include SteamCMD in your container image, add the `additional_tars` parameter to your `release_app`:

```starlark
load("//tools/bazel:release.bzl", "release_app")

release_app(
    name = "my-worker",
    binary_name = "//path/to:my_binary",
    language = "python",
    domain = "myapp",
    description = "Worker that manages game servers",
    additional_tars = ["//tools/steamcmd:steamcmd_layers"],
)
```

### What's Included

The `steamcmd_layers` target bundles:
1. **SteamCMD installation** at `/opt/steamcmd/`
2. **32-bit libraries** at `/usr/lib32/` (from Ubuntu 24.04)
3. **Convenience symlink** at `/usr/local/bin/steamcmd`
4. **Dynamic linker symlink** at `/lib/ld-linux.so.2` (required for 32-bit execution)

No additional configuration or runtime installation required!

### File Locations in Container

Once included, SteamCMD will be available at:
- **Main installation**: `/opt/steamcmd/steamcmd.sh`
- **Symlink for convenience**: `/usr/local/bin/steamcmd`
- **32-bit libraries**: `/usr/lib32/*`

### Using SteamCMD in Your Application

```python
import subprocess

# RECOMMENDED: Use the full path to avoid shell script $0 resolution issues
result = subprocess.run(
    ["/opt/steamcmd/steamcmd.sh", "+login", "anonymous", "+quit"],
    capture_output=True,
    text=True,
)

# The symlink at /usr/local/bin/steamcmd is provided for convenience,
# but due to shell script $0 resolution, programmatic usage should use the full path
```

### Example: Installing a Game Server

```python
import subprocess

def install_game_server(app_id: str, install_dir: str):
    """Install a Steam game server using SteamCMD."""
    cmd = [
        "steamcmd",
        "+force_install_dir", install_dir,
        "+login", "anonymous",
        "+app_update", app_id, "validate",
        "+quit",
    ]
    
    result = subprocess.run(cmd, capture_output=True, text=True)
    
    if result.returncode != 0:
        raise RuntimeError(f"SteamCMD failed: {result.stderr}")
    
    return result.stdout

# Example: Install Counter-Strike 2 server (app_id 730)
install_game_server("730", "/opt/game-servers/cs2")
```

## Architecture

SteamCMD is architecture-independent:
- The downloaded package contains a shell script (`steamcmd.sh`)
- On first run, it automatically downloads the correct binaries for your architecture (x86_64 or ARM64)
- This means the same layer works for both AMD64 and ARM64 containers

## Implementation Details

The SteamCMD layer is built using multiple components:

1. **SteamCMD binary**: Downloaded from Valve's CDN and packaged to `/opt/steamcmd/`
2. **32-bit libraries**: Pre-extracted from Ubuntu 24.04 packages:
   - `lib32gcc-s1_14.2.0-4ubuntu2~24.04_amd64.deb`
   - `libc6-i386_2.39-0ubuntu8.7_amd64.deb`
3. **Symlinks**: Convenience and dynamic linker symlinks for proper execution

All components are combined into the `steamcmd_layers` target for easy inclusion.

## Testing

Build and test the layer:

```bash
# Build a test image with steamcmd
bazel build //manman:worker_image

# Load and test locally:
# On AMD64:
bazel run //manman:worker_image_load --platforms=//tools:linux_x86_64
docker run --rm manman-worker:latest steamcmd +quit

# On ARM64 Mac:
bazel run //manman:worker_image_load --platforms=//tools:linux_arm64
docker run --rm manman-worker:latest steamcmd +quit
```

## Updating Components

### Updating SteamCMD
To update to a newer SteamCMD version:
1. Download: `wget https://steamcdn-a.akamaihd.net/client/installer/steamcmd_linux.tar.gz`
2. Calculate SHA256: `sha256sum steamcmd_linux.tar.gz`
3. Update `sha256` in `MODULE.bazel`

Note: SteamCMD auto-updates itself on first run, so manual updates are rarely needed.

### Updating 32-bit Libraries
If Ubuntu updates the 32-bit library packages, recreate the lib32.tar using the Docker-based script in tmp/. The script downloads the latest packages and extracts them with deterministic timestamps.

## Future Enhancements

This pattern can be extended for other external tools:
- Game server executables
- Binary CLI tools
- Static assets
- Shared libraries

Just create a similar directory under `tools/` with a BUILD.bazel that exports tar layers.
