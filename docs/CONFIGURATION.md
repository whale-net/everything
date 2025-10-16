# Configuration Guide

This guide covers the configuration files and build settings for the monorepo.

## Configuration Files

The repository uses several configuration files for build and dependency management:

- **`.bazelrc`**: Contains common Bazel configuration including CI optimizations, test settings, and build flags
- **`MODULE.bazel`**: Defines external dependencies using Bazel's bzlmod system, including rules for Python, Go, and OCI containers
- **`pyproject.toml`**: Python dependencies specification managed by uv
- **`uv.lock`**: Locked Python dependency versions with platform-specific wheels

## Key Configuration Details

- Bazel uses Python version PY3 with symlink prefix `bazel-`
- CI configuration includes aggressive remote caching (downloads all outputs) and test result caching
- OCI images use Python 3.13-slim and Alpine 3.20 as base images with multi-platform support
- **Remote cache support**: Optional HTTP-based remote caching with basic authentication

## Remote Cache Configuration

The repository supports optional Bazel remote caching for improved CI performance and build sharing. Remote cache is configured through the shared `setup-build-env` action:

### Usage in GitHub Actions workflows

```yaml
- name: Setup Build Environment
  uses: ./.github/actions/setup-build-env
  with:
    cache-suffix: 'test'
    bazel-remote-cache-url: ${{ secrets.BAZEL_REMOTE_CACHE_URL }}
    bazel-remote-cache-user: ${{ secrets.BAZEL_REMOTE_CACHE_USER }}
    bazel-remote-cache-password: ${{ secrets.BAZEL_REMOTE_CACHE_PASSWORD }}
```

### Configuration Details

- Remote cache is enabled when `bazel-remote-cache-url` input is provided
- Credentials are passed from GitHub secrets to action inputs
- Automatically sets `--remote_upload_local_results=true` for cache population

### Required Secrets

- `BAZEL_REMOTE_CACHE_URL`: HTTP URL of the remote cache server (required for remote caching)
- `BAZEL_REMOTE_CACHE_USER`: Username for basic HTTP authentication (optional)
- `BAZEL_REMOTE_CACHE_PASSWORD`: Password for basic HTTP authentication (optional)

### Security Notes

- Secrets are passed from workflow to action via inputs for proper access control
- Generated `.bazelrc.remote` file is excluded from git via `.gitignore`
- Basic HTTP authentication is embedded in the cache URL during configuration
