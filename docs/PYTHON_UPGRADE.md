# Upgrading Python

The repo pins one Python minor version (currently 3.13) in several places.
Builds and tests use the `rules_python` toolchain. Container images ship a
separate stripped interpreter from
[python-build-standalone](https://github.com/astral-sh/python-build-standalone)
(about 20MB instead of about 108MB). Keep both on the same minor version.

## Places to change

| File | What to change |
|------|----------------|
| `MODULE.bazel` | `python.toolchain(python_version = "3.13")` |
| `MODULE.bazel` | `environments.create_for_python_toolchains(python_versions = ["3.13"])` |
| `MODULE.bazel` | `python_stripped_x86_64` and `python_stripped_arm64` URLs (below) |
| `tools/bazel/container_image.bzl` | `python_version = "3.13"` default and every hard-coded `/opt/python3.13` path. Grep for `python3.13` |
| `.bazelrc` | `build:esp32 --@rules_python//python/config_settings:python_version=3.13` |
| `pyproject.toml` | `requires-python` |
| `uv.lock` | Regenerate with `uv lock --python 3.X` |
| Docs | `uv lock --python 3.13` in [DEPENDENCIES.md](DEPENDENCIES.md) and `.claude/skills/vendor-sync/SKILL.md`; version mentions in `DOCKER.md` and `IMAGE_LAYER_CACHING.md` |

To catch anything new, run `git grep -n '3\.13'`.

## Stripped interpreter for containers

Pick a release from
<https://github.com/astral-sh/python-build-standalone/releases> and update
both archives in `MODULE.bazel`. Use the `install_only_stripped` builds:

```starlark
http_archive(
    name = "python_stripped_x86_64",
    build_file_content = """
exports_files(["python"])
""",
    url = "https://github.com/astral-sh/python-build-standalone/releases/download/<DATE>/cpython-<X.Y.Z>%2B<DATE>-x86_64-unknown-linux-gnu-install_only_stripped.tar.gz",
)
```

The arm64 archive is the same with `aarch64-unknown-linux-gnu`. Neither
archive has a `sha256` today. Add one if you're touching them anyway.

## Verify

```bash
bazel test //demo/hello_python:test_main
bazel test //tools/scripts:test_cross_compilation
bazel run //demo/hello_python:hello-python_image_load --platforms=//tools:linux_x86_64
docker run --rm demo-hello-python:latest
```

The container should print the hello-python greeting. A wrong
`/opt/python3.X` path in `container_image.bzl` shows up here as a missing
interpreter.
