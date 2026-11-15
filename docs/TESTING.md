# Testing

All tests run through Bazel.

```bash
bazel test //...                              # everything
bazel test //krill/...                        # one domain
bazel test //demo/hello_go:main_test          # one target
bazel test //krill/... --test_output=all      # show output of passing tests too
bazel test //demo/hello_go:main_test --test_filter=TestGetVersion   # one Go test function
bazel test //krill/... --nocache_test_results # re-run even if cached
```

`.bazelrc` sets `--test_output=errors` and caches test results, so only
failing output is printed and unchanged tests aren't re-run.

## Running what CI will run

On pull requests, CI's **Test** job runs only the tests affected by the diff
against `main`. To get the same list locally:

```bash
git fetch origin main
bazel run //tools:release -- changed-targets \
  --base-commit=origin/main \
  --candidates='tests(//...)'
```

Pushes to `main` run `bazel coverage --config=ci //...`.
[RELEASE.md](RELEASE.md) covers `changed-targets` in more detail.

## Writing tests

- **Go**: `go_test` from `@rules_go//go:def.bzl`. `bazel run //:gazelle -- <dir>`
  generates the target from `*_test.go` files.
- **Python**: `py_test` from `//tools/bazel:pytest.bzl`, not the `rules_python`
  one. It runs pytest and adds `@pypi//:pytest` itself.
- **Migrations**: don't hard-code migration version numbers in tests. See
  [AGENTS.md](../AGENTS.md#migration-tests) and
  [libs/go/migrate/README.md](../libs/go/migrate/README.md#writing-migration-tests).

## Tests that need Docker

Tests that need a real Postgres use `libs/go/dbtest` (testcontainers-go).
They are tagged `manual` so `bazel test //...` stays green on machines without
Docker. Run them by exact label:

```bash
bazel test //libs/go/dbtest:postgres_constraints_test --test_output=all
```

Don't add `--test_tag_filters=-manual` when naming an exact label. Bazel then
reports "No test targets were found". Copy the tag set from
`libs/go/dbtest/BUILD.bazel` when writing a new one;
[libs/go/dbtest/README.md](../libs/go/dbtest/README.md) explains each tag and
covers non-default Docker sockets. CI runs these in the **Test Database
Integration** job.

Two other `manual` targets that CI runs explicitly:

```bash
bazel test //tools/scripts:test_cross_compilation    # arm64 images get arm64 wheels
bazel run //tools/appmeta:manifest_contract_test     # release_app attrs match the manifest proto
```

`manifest_contract_test` must be run with `bazel run`, not `bazel test`,
because it queries the workspace.
