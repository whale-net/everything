# SteamCMD Container Layer

This directory provides a reusable Bazel target for packaging [SteamCMD](https://developer.valvesoftware.com/wiki/SteamCMD) into container images.

## What is SteamCMD?

SteamCMD is Valve's command-line tool for installing and updating Steam game servers. It's essential for managing dedicated game servers for games like Counter-Strike, Team Fortress 2, Left 4 Dead, and many others.

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
    additional_tars = ["//tools/steamcmd:steamcmd"],
)
```

### File Locations in Container

Once included, SteamCMD will be available at:
- **Main installation**: `/opt/steamcmd/steamcmd.sh`
- **Symlink for convenience**: `/usr/local/bin/steamcmd`

### Using SteamCMD in Your Application

```python
import subprocess

# Run steamcmd from your application
result = subprocess.run(
    ["steamcmd", "+login", "anonymous", "+quit"],
    capture_output=True,
    text=True,
)

# Or use the full path
result = subprocess.run(
    ["/opt/steamcmd/steamcmd.sh", "+login", "anonymous", "+quit"],
    capture_output=True,
    text=True,
)
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

The SteamCMD layer is built using:
1. **Download**: `http_file` rule in `MODULE.bazel` fetches the official SteamCMD tarball
2. **Extract**: `genrule` unpacks the archive
3. **Package**: `pkg_tar` creates a container layer at `/opt/steamcmd/`
4. **Symlink**: Additional tar layer creates convenience symlink at `/usr/local/bin/steamcmd`

## Updating SteamCMD

To update to a newer version:
1. Download the latest version: `wget https://steamcdn-a.akamaihd.net/client/installer/steamcmd_linux.tar.gz`
2. Calculate the SHA256: `sha256sum steamcmd_linux.tar.gz`
3. Update the `sha256` in `MODULE.bazel`

Note: SteamCMD auto-updates itself on first run, so manual updates are rarely needed.

## Testing

Build and test the layer:

```bash
# Build the steamcmd layer
bazel build //tools/steamcmd:steamcmd

# Build a test image with steamcmd
bazel build //manman:worker_image

# Load and test locally (on ARM64 Mac):
bazel run //manman:worker_image_load --platforms=//tools:linux_arm64
docker run --rm manman-worker:latest steamcmd +quit
```

## Future Enhancements

This pattern can be extended for other external tools:
- Game server executables
- Binary CLI tools
- Static assets
- Shared libraries

Just create a similar directory under `tools/` with a BUILD.bazel that exports a tar layer.
