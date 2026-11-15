# Development

How to add an app or a shared library. The `demo/` apps are small, working
templates: copy from `demo/hello_go`, `demo/hello_python`, or
`demo/hello_fastapi` rather than starting from scratch.

## Adding a Go app

1. Create a package under its domain, e.g. `mydomain/myapp/main.go`.
2. Generate the BUILD file with Gazelle. For `package main` it creates a
   `myapp_lib` go_library and a `myapp` go_binary that embeds it:

   ```bash
   bazel run //:gazelle -- mydomain/myapp
   ```

3. Add release metadata to the generated `BUILD.bazel`:

   ```starlark
   load("//tools/bazel:release.bzl", "release_app")

   release_app(
       name = "myapp",
       binary_name = ":myapp",
       language = "go",
       domain = "mydomain",
       description = "What this app does",
       app_type = "internal-api",
       port = 8080,
   )
   ```

Long-running Go services must call `logging.Configure` with OTLP and tracing
enabled. See [skills/architecture-observability/SKILL.md](skills/architecture-observability/SKILL.md)
before writing `main.go`.

## Adding a Python app

Use `py_library`/`py_binary` from `rules_python`, and `py_test` from
`//tools/bazel:pytest.bzl`. That wrapper runs pytest and adds `@pypi//:pytest`
for you, so test files don't need a `__main__` block.

```starlark
load("@rules_python//python:defs.bzl", "py_binary", "py_library")
load("//tools/bazel:pytest.bzl", "py_test")
load("//tools/bazel:release.bzl", "release_app")

py_library(
    name = "main_lib",
    srcs = ["__init__.py", "main.py"],
    deps = ["//libs/python", "@pypi//:fastapi"],
)

py_binary(
    name = "myapp",
    srcs = ["main.py"],
    main = "main.py",
    deps = [":main_lib"],
    visibility = ["//visibility:public"],
)

py_test(
    name = "test_main",
    srcs = ["test_main.py"],
    deps = [":main_lib"],
    size = "small",
)

release_app(
    name = "myapp",
    language = "python",
    domain = "mydomain",
    description = "What this app does",
    app_type = "worker",
)
```

Third-party packages come from `@pypi//:<name>`. See
[DEPENDENCIES.md](DEPENDENCIES.md#python-dependencies).

## `release_app`

`release_app` (in `tools/bazel/release.bzl`) creates the image, push/load
targets, and the `<name>_metadata` target that release tooling and Helm chart
generation read. `name` and `language` are required, and `domain` should
always be set because the image is named `<domain>-<name>`. App names use
dashes, not underscores (the macro fails otherwise). `binary_name` defaults to
`:<name>`. Set `app_type`
(`external-api`, `internal-api`, `worker`, `job`, `cli`, `firmware`, ...) and
`port` for anything that gets deployed. The macro's docstring lists every
attribute. [RELEASE.md](RELEASE.md) and [HELM.md](HELM.md) cover what happens
downstream.

## Check a new app

```bash
bazel build //mydomain/myapp:all
bazel test //mydomain/myapp/...
bazel run //mydomain/myapp:myapp
bazel query "kind('app_metadata', //mydomain/...)"   # should list //mydomain/myapp:myapp_metadata
```

To run the image in Docker, load it for your host's Linux architecture:

```bash
bazel run //mydomain/myapp:myapp_image_load --platforms=//tools:linux_x86_64   # or //tools:linux_arm64
docker run --rm mydomain-myapp:latest
```

## Adding a shared library

Put Go libraries in `libs/go/<name>/` (a `go_library` with
`importpath = "github.com/whale-net/everything/libs/go/<name>"`; Gazelle will
generate it) and Python libraries in `libs/python/<name>/` as a `py_library`.
Check [libs/TOC.md](../libs/TOC.md) first in case one already exists.

Before changing an existing library, find its users:

```bash
bazel query 'rdeps(//..., //libs/go/<name>)'
```

## Generated OpenAPI clients

FastAPI apps that set `fastapi_app` on `release_app` get an OpenAPI spec
target, and clients for them live under `//generated/py/...` and
`//generated/go/...`. Depend on those targets directly. For IDE
autocompletion, copy the built clients into the source tree with
`./tools/scripts/sync_generated_clients.sh`. Details are in
[generated/README.md](../generated/README.md).
