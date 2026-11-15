# Adding SteamCMD (or Another Tool) to an Image

`release_app` and `multiplatform_image` take an `additional_tars` list of tar
layers to add to the image. `//tools/steamcmd` uses it to ship Valve's
SteamCMD. The same pattern works for any external tool you need inside a
container.

## Adding SteamCMD to an app

```starlark
load("//tools/bazel:release.bzl", "release_app")

release_app(
    name = "my-worker",
    binary_name = ":my_binary",
    language = "python",
    domain = "myapp",
    app_type = "worker",
    description = "Worker that installs game servers",
    additional_tars = ["//tools/steamcmd:steamcmd_layers"],
)
```

`steamcmd_layers` is an alias for `steamcmd_with_lib32`, which bundles:

| Path in the image | Contents | Target |
|-------------------|----------|--------|
| `/opt/steamcmd/steamcmd.sh`, `/opt/steamcmd/linux32/` | SteamCMD from Valve's CDN (`@steamcmd_linux` in `MODULE.bazel`) | `//tools/steamcmd:steamcmd` |
| `/usr/local/bin/steamcmd` → `/opt/steamcmd/steamcmd.sh` | Convenience symlink | `//tools/steamcmd:steamcmd_symlink_layer` |
| `/lib/ld-linux.so.2` → `/usr/lib32/ld-linux.so.2` | 32-bit dynamic linker symlink | `//tools/steamcmd:steamcmd_symlink_layer` |
| `/usr/lib32/` | 32-bit libc and libgcc from Ubuntu 24.04 `.deb`s | `//tools/lib32` |

SteamCMD is a 32-bit x86 program and the lib32 packages are amd64 `.deb`s, so
the layer only runs in `linux/amd64` images.

Call it by full path from code. The `/usr/local/bin` symlink is fine in a
shell, but `steamcmd.sh` resolves its own directory from `$0`:

```python
subprocess.run(["/opt/steamcmd/steamcmd.sh", "+login", "anonymous", "+quit"], check=True)
```

Current users: `//manman:worker` and `//demo/hello_steamcmd:hello-steamcmd`.
`tools/steamcmd/README.md` covers game-server install examples and how to bump
the SteamCMD or lib32 versions.

## Trying it

```bash
bazel run //demo/hello_steamcmd:hello-steamcmd_image_load --platforms=//tools:linux_x86_64
docker run --rm demo-hello-steamcmd:latest
```

The demo checks that `steamcmd` is on `PATH`, that `/opt/steamcmd/steamcmd.sh`
exists, and that `steamcmd +quit` runs. It ends with
`All tests passed! SteamCMD is ready to use.`

## Packaging another tool

Write a `BUILD.bazel` under `tools/<tool>/` that produces one or more tar
files, then list them in `additional_tars`. Use `pkg_tar` with
`portable_mtime = True`, or a genrule that runs `tar --mtime='@0'`, so the
layer is reproducible and its digest stays stable across builds. A stable
digest lets the registry and image pulls reuse the layer.

```starlark
# tools/mytool/BUILD.bazel
load("@rules_pkg//:pkg.bzl", "pkg_tar")

pkg_tar(
    name = "mytool",
    srcs = [":mytool_files"],
    package_dir = "/opt/mytool",
    portable_mtime = True,
    visibility = ["//visibility:public"],
)
```

Fetch the upstream artifact with `http_file` or `http_archive` in
`MODULE.bazel` and pin its `sha256`. After adding it, run the `vendor-sync`
skill so worktrees pick it up from the shared vendor directory.
