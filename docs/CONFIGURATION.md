# Configuration

Bazel config files in this repo, the per-machine `~/.bazelrc`, and how CI
wires up the remote cache.

## Files

| File | What it holds |
|------|---------------|
| `.bazelversion` | Bazel version Bazelisk downloads (8.3.1) |
| `.bazelrc` | Repo-wide flags and the named configs below |
| `.bazelrc.remote` | Remote cache flags, written by CI and `try-import`ed by `.bazelrc`. Gitignored |
| `.bazelrc.user.example` | Template for your personal `~/.bazelrc` |
| `MODULE.bazel` / `MODULE.bazel.lock` | External deps (bzlmod): rules, toolchains, Go and Python deps, OCI bases |
| `go.mod` / `go.sum` | Go dependencies, read by `go_deps.from_file` |
| `pyproject.toml` / `uv.lock` | Python dependencies, imported by `rules_pycross` |

`.bazelrc.local` is gitignored, but `.bazelrc` does not import it, so flags
put there have no effect. Put personal flags in `~/.bazelrc` or pass them on
the command line.

## Named configs in `.bazelrc`

| Config | Use |
|--------|-----|
| `--config=ci` | CI builds and tests: Build without the Bytes (`--remote_download_minimal`), retries, timestamps, test-result caching |
| `--config=ci-images` | `ci` plus `--remote_download_toplevel`, for steps that need real files on disk (images, tool binaries). Never uploads to the remote cache |
| `--config=esp32` | Cross-compile firmware for the ESP32 platform, e.g. `bazel build //demo/blink:blink_bin --config=esp32` |
| `--config=fast` | `--jobs=auto` |

Defaults that apply to every build include `--incompatible_strict_action_env`,
`--host_platform=@platforms//host` (so exec tools like the OpenAPI generator's
JVM always run on the host architecture), and test settings
`--test_output=errors` and `--cache_test_results=yes`.

## Sample Machine-Level Config (`~/.bazelrc`)

Copy [`.bazelrc.user.example`](../.bazelrc.user.example) to `~/.bazelrc` and
replace `<you>` with your username. It sets up:

- a shared vendor directory (next section),
- a shared `--disk_cache` so worktrees reuse each other's action outputs,
- a commented-out remote cache entry.

Bazel reads `~/.bazelrc` for every workspace, so one copy covers every
worktree. Don't commit it.

## Shared Vendor Directory (`bazel vendor //...`)

Every git worktree has its own Bazel output base, and by default each one
fetches and extracts its own copy of every external repo (Go modules, Python
wheels, toolchains). With several worktrees that adds up to gigabytes of
duplicates. A shared vendor directory lets all worktrees on a machine use one
copy.

In `~/.bazelrc`, never the repo's `.bazelrc`:

```
common --vendor_dir=/home/<you>/.cache/bazel-vendor/everything
```

The path must be absolute and outside any worktree. A relative path resolves
per worktree and defeats the purpose.

Populate it:

```bash
bazel vendor //...
```

The vendor directory is a snapshot. A repo added after the last run silently
falls back to per-worktree fetching. Re-run `bazel vendor //...` after:

- adding or updating a `bazel_dep`, `git_override`, or `archive_override` in `MODULE.bazel`
- adding or upgrading a Go dependency ([DEPENDENCIES.md](DEPENDENCIES.md#go-dependencies))
- adding or upgrading a Python dependency ([DEPENDENCIES.md](DEPENDENCIES.md#python-dependencies))

The `vendor-sync` skill (`.claude/skills/vendor-sync/`) does this for agents.
This directory lives in `~/.cache` and is never committed. It isn't the
"commit `vendor/` for offline builds" use of `bazel vendor`. `bazel help vendor`
lists options such as excluding specific repos.

## Remote Cache Configuration

CI workflows set up the remote cache through the `setup-build-env` composite
action:

```yaml
- name: Setup Build Environment
  uses: ./.github/actions/setup-build-env
  with:
    cache-suffix: 'ci'
    bazel-remote-cache-url: ${{ secrets.BAZEL_REMOTE_CACHE_URL || vars.BAZEL_REMOTE_CACHE_URL }}
    bazel-remote-cache-user: ${{ secrets.BAZEL_REMOTE_CACHE_USER || vars.BAZEL_REMOTE_CACHE_USER }}
    bazel-remote-cache-password: ${{ secrets.BAZEL_REMOTE_CACHE_PASSWORD || vars.BAZEL_REMOTE_CACHE_PASSWORD }}
    bazel-remote-downloader-url: ${{ secrets.BAZEL_REMOTE_DOWNLOADER_URL || vars.BAZEL_REMOTE_DOWNLOADER_URL }}
```

It calls `.github/actions/setup-bazel-remote-cache-config`, which writes
`.bazelrc.remote`:

- If `BAZEL_REMOTE_CACHE_URL` is set, it adds `--remote_cache` and
  `--remote_upload_local_results=true`. For `grpc://`/`grpcs://` URLs,
  credentials go in an `Authorization: Basic` header. For `http(s)://` URLs,
  they're embedded in the URL.
- If `BAZEL_REMOTE_DOWNLOADER_URL` is set (a `grpc://` or `grpcs://` Remote
  Asset API endpoint), it adds `--experimental_remote_downloader` with the same
  credentials, so `http_archive`/`http_file` downloads go through the cache.
- With neither set, it writes an empty stub.

Release builds use `--config=ci-images`, which overrides
`--remote_upload_local_results=false`. They read from the cache but don't
write to it. `ci.yml`'s test job is what populates the cache. The comment on
that flag in `.bazelrc` explains why: uploading large image layers stalled
release runs.

| Secret / variable | Purpose |
|-------------------|---------|
| `BAZEL_REMOTE_CACHE_URL` | Cache endpoint, HTTP(S) or gRPC(S). Must be gRPC if the downloader is used |
| `BAZEL_REMOTE_DOWNLOADER_URL` | Remote Asset API endpoint, e.g. `grpcs://cache.example.com:443`. Optional |
| `BAZEL_REMOTE_CACHE_USER` / `BAZEL_REMOTE_CACHE_PASSWORD` | Basic auth for both. Optional |

To use the same cache locally, uncomment the remote cache lines in your
`~/.bazelrc`.
