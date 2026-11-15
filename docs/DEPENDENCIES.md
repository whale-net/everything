# Dependencies

Go dependencies are declared in `go.mod`, Python dependencies in
`pyproject.toml`/`uv.lock`. Bazel reads both through `MODULE.bazel`. After
adding or upgrading anything, re-run `bazel vendor //...` if you use the
shared vendor directory ([CONFIGURATION.md](CONFIGURATION.md#shared-vendor-directory-bazel-vendor-)).

Commit `MODULE.bazel.lock` along with any change to `go.mod`, `uv.lock`, or
`MODULE.bazel`.

## Go Dependencies

`MODULE.bazel` calls `go_deps.from_file(go_mod = "//:go.mod")`, so Bazel
resolves every module in `go.mod`, including transitive ones. You never write
`go_deps.module(...)` by hand. The Go SDK version comes from
`go_sdk.download(version = ...)` in `MODULE.bazel`. Keep it in step with the
`go` line in `go.mod`.

### Add or upgrade

```bash
bazel run @rules_go//go -- get github.com/google/uuid@v1.6.0
bazel run //:gazelle
bazel mod tidy
```

1. `bazel run @rules_go//go -- get` runs the repo's pinned Go SDK and updates
   `go.mod`/`go.sum`. Plain `go get` works too if your local Go matches, but
   see [Dependencies dropped from `use_repo`](#dependencies-dropped-from-use_repo).
2. Gazelle updates `BUILD.bazel` files from your imports. Pass a directory to
   limit it: `bazel run //:gazelle -- libs/go/rmq`.
3. `bazel mod tidy` rewrites the `use_repo(go_deps, ...)` list in
   `MODULE.bazel`. Don't edit that list by hand.

Use `@latest` to upgrade to the newest version.

### Remove

Delete the imports, then:

```bash
bazel run @rules_go//go -- mod tidy
bazel run //:gazelle
bazel mod tidy
```

### Referencing a module in BUILD files

Gazelle writes these for you. External repos are named from the import path:

| Import path | Label |
|-------------|-------|
| `github.com/google/uuid` | `@com_github_google_uuid//:uuid` |
| `github.com/jackc/pgx/v5/pgxpool` | `@com_github_jackc_pgx_v5//pgxpool` |
| `golang.org/x/net/html` | `@org_golang_x_net//html` |
| `google.golang.org/grpc` | `@org_golang_google_grpc//:grpc` |

The root `BUILD.bazel` sets `# gazelle:prefix github.com/whale-net/everything`
(must match the `module` line in `go.mod`) and excludes `generated/`.

### Dependencies dropped from `use_repo`

Symptom: after `bazel mod tidy`, a build or query fails with

```
No repository visible as '@com_github_foo_bar' from repository '@@gazelle++go_deps+...'
```

or a warning that a repo is "reported as indirect dependencies by the
extension".

`go_deps` only keeps a module in `use_repo()` if `go.mod` lists it as a direct
requirement. A module that none of our code imports stays `// indirect`, even
when a third-party `BUILD.bazel` references it, for example behind a
Windows-only `select()` that `bazel query` still has to resolve.

Fix it by promoting the module to direct with the rules_go wrapper, which
marks explicit `get`s as direct:

```bash
bazel run @rules_go//go -- get github.com/Microsoft/go-winio@v0.6.2
bazel mod tidy
```

`go-winio` (pulled in by `github.com/docker/docker`) is pinned in `go.mod`
this way.

### Other fixes

- Missing `go.sum` entries: `bazel run @rules_go//go -- mod download all`.
- Gazelle didn't pick up a change: `bazel run //:gazelle -- -mode=fix <dir>`.
  Use `-mode=print-only` to see what it would write.

## Python Dependencies

Python packages are locked with [uv](https://docs.astral.sh/uv/) and imported
into Bazel by `rules_pycross` as the `@pypi` repo. The lock covers
linux/amd64, linux/arm64, and macOS/arm64, so cross-compiled images get the
right wheels.

### Add or upgrade

1. Add the package to `dependencies` in `pyproject.toml`.
2. Regenerate the lock:

   ```bash
   uv lock --python 3.13
   ```

3. Reference it as `@pypi//:<name>`, using the exact PyPI name with hyphens:

   ```starlark
   deps = [
       "@pypi//:fastapi",
       "@pypi//:python-jose",
   ]
   ```

To upgrade one package: `uv lock --python 3.13 --upgrade-package <name>`.

Packages that ship only an sdist are built by pycross with `setuptools`
(`default_build_dependencies` in `MODULE.bazel`).

To change the Python version itself, see [PYTHON_UPGRADE.md](PYTHON_UPGRADE.md).
