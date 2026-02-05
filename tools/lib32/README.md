# 32-bit Library Support for SteamCMD

This package provides 32-bit libraries needed to run SteamCMD (and other 32-bit applications) on 64-bit Linux systems.

## How It Works

The libraries are **downloaded and extracted at build time** from Ubuntu 24.04 packages:
- `lib32gcc-s1_14.2.0-4ubuntu2~24.04_amd64.deb`
- `libc6-i386_2.39-0ubuntu8.7_amd64.deb`

No binary artifacts are committed to the repository. The extraction happens during Bazel build using genrules.

## Prerequisites

**zstd must be installed** on your build system to extract the .deb files:

```bash
# Ubuntu/Debian
sudo apt-get install zstd

# macOS
brew install zstd
```

## Usage

This target is automatically included when you use `//tools/steamcmd:steamcmd_layers`. You don't need to reference it directly.

The libraries are installed to `/usr/lib32/` in the container image.

## Architecture

1. **Download**: Bazel fetches .deb files from Ubuntu archives (defined in MODULE.bazel)
2. **Extract**: genrule extracts the .deb archives using `ar` and `zstd`
3. **Package**: Creates a tar with deterministic timestamps for reproducible builds
4. **Layer**: Included in container images via `//tools/steamcmd:steamcmd_layers`

## Updating Libraries

To update to newer Ubuntu packages:

1. Find the latest versions at http://archive.ubuntu.com/ubuntu/pool/main/
2. Update SHA256 and URLs in `MODULE.bazel`:
   - `@lib32gcc_s1_deb`
   - `@libc6_i386_deb`
3. Rebuild: `bazel build //tools/lib32:lib32`

## Benefits

- **No committed binaries**: Keeps repository clean and small
- **Reproducible**: Deterministic builds with fixed timestamps
- **Cacheable**: Bazel caches the extraction step
- **Transparent**: Clear provenance from Ubuntu package archives
- **Updatable**: Easy to update to newer library versions
