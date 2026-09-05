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

- `BAZEL_REMOTE_CACHE_URL`: URL of the remote cache server (HTTP or gRPC; must be `grpc://` or `grpcs://` if remote downloader is enabled)
- `BAZEL_REMOTE_DOWNLOADER_URL`: gRPC URL of the remote asset downloader (e.g. `grpc://cache.example.com:9092` or `grpcs://cache.example.com:443`, optional; can be set as secret or repository variable)
- `BAZEL_REMOTE_CACHE_USER`: Username for basic authentication (optional)
- `BAZEL_REMOTE_CACHE_PASSWORD`: Password for basic authentication (optional)

### Security Notes

- Secrets and variables are passed from workflow to action via inputs for proper access control
- Generated `.bazelrc.remote` file is excluded from git via `.gitignore`
- Basic authentication is embedded in the cache URL and passed as an Authorization header to the remote downloader during configuration

## Shared Vendor Directory (`bazel vendor //...`)

**Problem this solves:** every git worktree gets its own Bazel output base (see the
`disk-cleanup` skill), and by default each output base independently
fetches and extracts a full copy of every external repo (`bazel_dep`s, Go modules, the
Python interpreter/pip wheels, etc.) under its own `external/` directory. With several
worktrees checked out at once this duplicates gigabytes of identical external-repo
bytes per worktree. `bazel vendor` lets every worktree on a machine share **one** copy
instead.

### One-time setup — machine-level `~/.bazelrc`

This must go in your **personal, machine-level `~/.bazelrc`** (the file Bazel reads
from your home directory), **not** the repo's checked-in `.bazelrc`. It's a per-machine
disk optimization, not a project build setting, and must never be committed:

```
# ~/.bazelrc — shared vendor dir, reused by every worktree of this repo on this machine
common --vendor_dir=/home/<you>/.cache/bazel-vendor/everything
```

Use an **absolute path outside any git worktree**. A relative path (e.g. `vendor`)
resolves against each worktree's own directory and would just recreate one copy per
worktree — the opposite of what this is for.

### Populate / refresh the vendor directory

```bash
bazel vendor //...
```

This fetches every external repo needed to build everything in the repo and writes it
into `--vendor_dir`. Once vendored, builds in *any* worktree that has the same
`~/.bazelrc` entry resolve those repos from the shared directory instead of fetching
and extracting their own copy into the worktree's output base.

### Re-run whenever external deps change

`bazel vendor` is a snapshot, not a live sync — a repo added after the last vendor run
falls back to the normal per-worktree fetch path (silently reintroducing the
duplication `--vendor_dir` was meant to avoid) until you vendor it. Re-run
`bazel vendor //...` after any change that adds or updates an external repo:

- A new/updated `bazel_dep`, `git_override`, or `archive_override` in `MODULE.bazel`
- `go get` + `bazel run //:gazelle` + `bazel mod tidy` (see [DEPENDENCIES.md](DEPENDENCIES.md#go-dependencies))
- A new Python dependency + `uv lock --python 3.13` (see [DEPENDENCIES.md](DEPENDENCIES.md#python-dependencies))

The `vendor-sync` skill (`.claude/skills/vendor-sync/`) automates this re-run and
should be invoked any time one of the above happens.

### Notes

- This is distinct from the common "commit `vendor/` to the repo for airgapped/offline
  builds" use of `bazel vendor` — here the directory lives outside the repo entirely
  (in `~/.cache`), is never committed, and exists purely to de-duplicate disk usage
  across worktrees. Nothing under `--vendor_dir` should ever be added to git.
- `bazel help vendor` documents advanced options, including excluding specific repos
  from vendoring (e.g. ones that require network access at build time).
