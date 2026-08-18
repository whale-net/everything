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
    bazel-remote-downloader-url: ${{ secrets.BAZEL_REMOTE_DOWNLOADER_URL || vars.BAZEL_REMOTE_DOWNLOADER_URL }}
```

### Configuration Details

- Remote cache is enabled when `bazel-remote-cache-url` input is provided.
- Remote asset downloader (Remote Asset API for caching `http_file` and `http_archive` dependencies) is enabled when `bazel-remote-downloader-url` is provided.
- Credentials (`BAZEL_REMOTE_CACHE_USER` and `BAZEL_REMOTE_CACHE_PASSWORD`) are passed to both the remote cache and remote downloader authorization headers if provided.
- Automatically sets `--remote_upload_local_results=true` for cache population.

### Secrets and Variables

- `BAZEL_REMOTE_CACHE_URL`: HTTP URL of the remote cache server (required for remote action/output caching)
- `BAZEL_REMOTE_DOWNLOADER_URL`: gRPC URL of the remote asset downloader (e.g. `grpc://cache.example.com:9092` or `grpcs://cache.example.com:443`, optional; can be set as secret or repository variable)
- `BAZEL_REMOTE_CACHE_USER`: Username for basic authentication (optional)
- `BAZEL_REMOTE_CACHE_PASSWORD`: Password for basic authentication (optional)

### Security Notes

- Secrets and variables are passed from workflow to action via inputs for proper access control
- Generated `.bazelrc.remote` file is excluded from git via `.gitignore`
- Basic authentication is embedded in the cache URL and passed as an Authorization header to the remote downloader during configuration
