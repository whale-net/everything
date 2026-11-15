# Setup

## Required

- **Bazelisk**, installed as `bazel`. It reads `.bazelversion` (currently 8.3.1)
  and downloads that Bazel. Don't install Bazel directly.
- **Docker**, for loading images locally and for container-backed integration
  tests.
- **git**.
- A few host packages that Bazel's toolchains still shell out to. On
  Debian/Ubuntu:

  ```bash
  sudo apt-get install -y python3 build-essential unzip zip git zstd xz-utils \
      pkg-config cmake ninja-build rsync
  ```

  This list comes from `.devcontainer/Dockerfile`, which is the tested
  minimal set for `bazel build //...` on a bare Ubuntu 24.04.

Install Bazelisk with `brew install bazelisk`, or download a release binary
from <https://github.com/bazelbuild/bazelisk/releases> and put it on your
`PATH` as `bazel`.

Go and Python toolchains are hermetic, so Bazel fetches them itself. You don't
need a local Go or Python install to build or test.

## Optional

| Tool | Needed for |
|------|------------|
| [uv](https://docs.astral.sh/uv/) | Adding or upgrading Python dependencies (`uv lock`) |
| [Tilt](https://tilt.dev), `kubectl`, a local Kubernetes cluster | Running a domain locally with `tilt up` (see [tools/tilt/README.md](../tools/tilt/README.md)) |
| Go | Only if you prefer plain `go get` over `bazel run @rules_go//go -- get` (see [DEPENDENCIES.md](DEPENDENCIES.md#go-dependencies)) |

## Check it works

```bash
bazel version                          # should report 8.3.1
bazel run //demo/hello_go:hello-go
bazel test //demo/hello_go:main_test //demo/hello_python:test_main
```

The first build downloads toolchains and external repos and takes a while.

## Per-machine Bazel config

If you keep more than one git worktree of this repo, set up a `~/.bazelrc`
with a shared vendor directory and disk cache. Each worktree otherwise
downloads and caches every external repo on its own. Copy
[`.bazelrc.user.example`](../.bazelrc.user.example) to `~/.bazelrc` and fill
in your username. See [CONFIGURATION.md](CONFIGURATION.md) for what each
setting does.

## Devcontainer

`.devcontainer/` has a container with Bazelisk, the host packages above, Go,
Python with uv, Docker-in-Docker, and kubectl/helm. Use it if you don't want
to install things on your host.

## WSL2

- Docker Desktop with WSL integration works for both image loading and the
  testcontainers-based integration tests. The `docker-desktop` Kubernetes
  context works for Tilt.
- Flashing ESP32 boards needs USB passthrough via `usbipd`. See
  [tools/firmware/README.md](../tools/firmware/README.md).
