# Container Images

Every containerized app gets its image from the `release_app` macro. There
are no Dockerfiles for apps: images are assembled by
[rules_oci](https://github.com/bazel-contrib/rules_oci) from Bazel-built
layers, for `linux/amd64` and `linux/arm64`. The rules live in
[`tools/bazel/release.bzl`](../tools/bazel/release.bzl) and
[`tools/bazel/container_image.bzl`](../tools/bazel/container_image.bzl).

Read the [Gotchas](#gotchas) before changing anything here. Most image
breakage passes the build and only shows up when the container starts.

## Declaring an image

```starlark
load("//tools/bazel:release.bzl", "release_app")

release_app(
    name = "hello-fastapi",       # dashes, not underscores
    language = "python",          # "python" or "go" for container apps
    domain = "demo",
    app_type = "external-api",
    port = 8000,
)
```

`release_app` only builds an image when `app_type` is not `cli`, `binary`,
or `firmware`. The image name is `<domain>-<name>`, here
`demo-hello-fastapi`. It is pushed to `ghcr.io/whale-net/<domain>-<name>`.
See the macro's docstring for every attribute (`base`, `additional_tars`,
`args`, resources, and so on).

## Targets

For `release_app(name = "hello-fastapi", ...)`:

| Target | What it is |
|--------|------------|
| `hello-fastapi_image` | `oci_image_index` with amd64 and arm64 variants |
| `hello-fastapi_image_base` | Single-platform `oci_image`. Its platform comes from `--platforms` |
| `hello-fastapi_image_load` | Loads `_image_base` into local Docker as `demo-hello-fastapi:latest` |
| `hello-fastapi_image_push` | Pushes the index to GHCR. The release tooling calls this; you normally don't |
| `hello-fastapi_metadata` | Release metadata JSON used by `release_helper` and the Helm composer |

All image targets are tagged `manual`, so `bazel build //...` skips them.

## Building and running locally

```bash
# Multi-arch index (both platforms)
bazel build //demo/hello_fastapi:hello-fastapi_image

# Load into Docker. Pick the platform matching your Docker daemon.
bazel run //demo/hello_fastapi:hello-fastapi_image_load --platforms=//tools:linux_x86_64
bazel run //demo/hello_fastapi:hello-fastapi_image_load --platforms=//tools:linux_arm64   # Apple Silicon

docker run --rm -p 8000:8000 demo-hello-fastapi:latest
```

`release_helper` wraps the load step. It takes the full `<domain>-<name>`
(or a whole domain):

```bash
bazel run //tools:release -- build demo-hello-fastapi --platform amd64
```

Pushing and tagging releases is covered in [RELEASE.md](RELEASE.md).

## What's in an image

The base is `@ubuntu_base` (`ubuntu:24.04`, pinned by digest in
`MODULE.bazel`). The only other base is `@git_base`, which the App Registry
worker uses because it needs `git`. Override the base per app with
`release_app(base = ...)`.

Layers, bottom to top:

| Python | Go |
|--------|----|
| `//tools/cacerts:cacerts` (CA bundle) | `//tools/cacerts:cacerts` |
| `<name>_image_base_python_layer`: stripped CPython 3.13 at `/opt/python3.13/<arch>/` | `additional_tars`, if any |
| `<name>_image_base_deps_layer`: third-party wheels | `<name>_image_base_layer`: the binary and runfiles under `/app` |
| `additional_tars`, if any | |
| `<name>_image_base_app_layer`: app and in-repo library code | |

Each layer is its own Bazel target, so it is cached and pushed separately.
Editing app code rebuilds only the app layer. Changing `uv.lock` rebuilds
the deps layer. The Python layer is byte-identical across all Python apps,
so the registry stores it once. To see what a layer depends on:

```bash
bazel query 'deps(//demo/hello_fastapi:hello-fastapi_image_base_deps_layer)' --noimplicit_deps
```

Every image gets these environment variables: `APP_NAME`, `APP_DOMAIN`, and
`APP_TYPE` (read by [logging auto-detection](LOGGING_AUTO_DETECTION.md)), plus
`SSL_CERT_FILE=/etc/ssl/certs/ca-certificates.crt`. The entrypoint is
generated: Go runs `/app/<binary>_/<binary>`, and Python runs the
interpreter through a runfiles symlink that points at `/opt/python3.13/<arch>/`.

## Gotchas

- **Always pass `--platforms` to `_image_load`.** Without it you get a
  host-platform image. On macOS that means Darwin binaries and
  `exec format error` at container start.
- **Python ARM64 breakage is silent at build time.** `rules_pycross` picks
  wheels per target platform. If the wrong wheel lands in the arm64 image,
  the build succeeds and the container crashes on import, usually on
  compiled packages like pydantic. `//tools:test_cross_compilation` checks
  that each platform variant has the right wheel. It is tagged `manual`,
  and CI runs it only on pushes to `main`, so run it yourself when you
  touch dependencies, pycross, or image rules:

  ```bash
  bazel test //tools:test_cross_compilation
  ```

- **Use a glibc base image, never Alpine/musl.** Don't assume Go binaries
  are static. The App Registry worker links `libc.so.6` dynamically. An
  `alpine/git` base built cleanly and then failed at startup with
  `exec: no such file or directory`. See the `git_base` comment in
  `MODULE.bazel`.
- **Image builds must stay hermetic.** No Bazel action may call a registry
  or the network. For example, the chart composer pins image tags, not
  digests, because resolving a digest needs a registry call. Digest
  lookups run at publish time in `release_helper`. Keep non-Bazel
  toolchains (Node, CSS bundlers) out of image builds too.
- **App names use dashes.** `release_app` fails on an underscore in a
  container app's name. The image name must match `release_helper`'s
  `<domain>-<name>`. Don't name an app with its own domain prefix, or it
  ends up doubled (`whagent-net-whagent-net-ui`).
- **Extra system files go in `additional_tars`.** For example, SteamCMD
  uses `//tools/steamcmd:steamcmd_layers` (see
  [STEAMCMD_INTEGRATION.md](STEAMCMD_INTEGRATION.md)). Don't fork the base
  image to add them.

## CI and caching

CI builds with `--config=ci`, which uses `--remote_download_minimal`, so
outputs that hit the remote cache are never downloaded. Anything that needs
an image or binary on local disk afterwards uses `--config=ci-images`.
That config adds `--remote_download_toplevel` and turns off cache uploads,
because uploading the large image artifacts from release runs stalled pushes
for minutes. Both are defined in [`.bazelrc`](../.bazelrc). Remote cache
credentials are covered in [CONFIGURATION.md](CONFIGURATION.md#remote-cache-configuration).
