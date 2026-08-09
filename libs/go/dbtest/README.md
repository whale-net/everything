# dbtest

A reusable helper that starts a real Postgres container (via
[testcontainers-go](https://github.com/testcontainers/testcontainers-go)) and hands back a ready
`*pgxpool.Pool`, for tests that need to exercise actual SQL — unique constraints, partial
indexes, triggers, `EXCLUDE` constraints — none of which an in-memory fake
(`NewMockServerPortRepository()`-style) can verify.

This is a **proof of concept with a real deliverable**. It works, is documented below exactly as
run, and is deliberately kept out of `bazel test //...` so a Docker-less machine (or CI runner
without Docker) stays green.

**Verified working** (WSL2 + Docker Desktop 28.4.0, `docker-desktop` k8s context):
`bazel test //libs/go/dbtest:postgres_constraints_test --test_output=all` starts two real
`postgres:16-alpine` containers (one per test function) plus one shared `testcontainers/ryuk:0.14.0`
reaper container. Each Postgres container went from "Starting" to "database system is ready to
accept connections" (occurrence 2) in ~2s; the whole `bazel test` invocation, including Bazel's
own overhead, completed in 7.6s wall time on a warm image cache. Confirmed via container logs
showing real container IDs (e.g. `92ac935e15d0`), not a mock.

## Usage

```go
func TestSomethingThatNeedsRealSQL(t *testing.T) {
    ctx := context.Background()
    db := dbtest.NewPostgres(ctx, t, dbtest.Options{
        Schema: `
            CREATE TABLE widget (
                id   BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
                name TEXT NOT NULL UNIQUE
            );
        `,
    })
    // db.Pool is a *pgxpool.Pool, ready to use.
    // db.Close() is registered with t.Cleanup automatically.

    _, err := db.Pool.Exec(ctx, `INSERT INTO widget (name) VALUES ($1)`, "a")
    // ...
}
```

`NewPostgres` fails the test immediately (`t.Fatalf`) on any setup error, so callers never need
to handle an error return. `t.Cleanup` is registered for you; call `db.Close()` yourself only if
you want to shut the container down early within a single test.

`Options.Schema` should be self-contained DDL — do not depend on another package's migrations.
Keep each test's schema scoped to only what it needs to prove.

## Why this is a separate, "manual" target

`bazel test //...` must stay green on a machine with no Docker daemon. This package's own
library (`:dbtest`) builds everywhere — it has no test-time dependency on Docker. Only the
**test that calls it** talks to Docker, so that's the target that's isolated:

```starlark
go_test(
    name = "postgres_constraints_test",
    ...
    tags = [
        "external",          # never trust the cache, always re-run
        "integration",        # category marker, mirrors test_cross_compilation
        "manual",             # excluded from `bazel test //...` and other wildcards
        "no-cache",            # a container-backed test result must never be cached as PASS
        "no-sandbox",          # needs /var/run/docker.sock + network; blocked by the default sandbox
        "requires-network",    # documents/enforces network access to pull images
    ],
)
```

Any test in this repo that talks to a real Postgres via `dbtest` should use the same tag set on
its own `go_test` target — copy this one rather than adding `dbtest` as a dep of an
otherwise-unmarked test.

## Running it

```bash
bazel test //libs/go/dbtest:postgres_constraints_test --test_output=all
```

`manual` only removes a target from wildcard expansion (`//...`, `//libs/go/dbtest/...`,
`bazel test //libs/go/dbtest:all`) and from `--test_tag_filters` inclusion lists — it does
**not** block running the target when you name its exact label, and no `--test_tag_filters` flag
is needed or wanted here (passing `--test_tag_filters=-manual` together with an exact label
actually causes Bazel to report `ERROR: No test targets were found, yet testing was requested` —
don't combine them).

Add `--nocache_test_results` (or just edit the file, which invalidates the cache naturally) if
you want to force a fresh run despite `no-cache` normally already guaranteeing that.

No extra environment variables were needed in this repo's dev environment (WSL2 + Docker
Desktop, `docker-desktop` context): with the `no-sandbox` tag, the test action inherits the full
host environment, and testcontainers-go auto-detects `/var/run/docker.sock` as its default
Docker host without `DOCKER_HOST` being set explicitly.

If your environment exposes Docker on a non-default socket or over TCP, pass it through
explicitly — the `no-sandbox` tag alone does not create a `DOCKER_HOST` variable that doesn't
already exist in your shell:

```bash
bazel test //libs/go/dbtest:postgres_constraints_test \
    --test_env=DOCKER_HOST \
    --test_output=all
```

### Disabling the Ryuk reaper (only if you have to)

testcontainers-go starts a small "Ryuk" reaper container that watches the test process and force
-removes any containers/networks it left behind if the process dies uncleanly. In this repo's
verification run it started and worked fine with no special configuration
(`testcontainers/ryuk:0.14.0`, ~1s to start). If your environment can't run Ryuk (e.g. it can't
itself talk to the Docker socket, or a restricted CI runner blocks nested containers), disable it
with:

```bash
--test_env=TESTCONTAINERS_RYUK_DISABLED=true
```

**Tradeoff:** with Ryuk disabled, a killed/crashed test process (e.g. `bazel test` interrupted,
OOM-killed) will leak the Postgres container instead of it being force-cleaned. `dbtest`'s own
`t.Cleanup`-based teardown still handles the normal pass/fail paths; Ryuk is only a backstop for
abnormal termination. Prefer leaving Ryuk enabled unless you hit a concrete failure that requires
disabling it.

## CI

**These tests already run in CI** — the `Test Database Integration` job in
[`.github/workflows/ci.yml`](../../../.github/workflows/ci.yml). **You do not need to add
anything to CI when you write a new dbtest-backed test.** The job discovers its targets by
query rather than from a hardcoded list:

```bash
SCOPE='//libs/... + //tools/... + //manmanv2/...'
bazel query "tests($SCOPE) intersect rdeps($SCOPE, //libs/go/dbtest:dbtest)"
```

so any test that depends on this package is picked up the moment it exists. The job fails
loudly if the query returns nothing, since a silently-empty run would report green while
testing nothing.

Two gotchas that cost a debugging cycle when this job was written:

- **`bazel query` cannot take `--config=ci`.** Only `build:ci` and `test:ci` are defined in
  `.bazelrc`, so `bazel query --config=ci` hard-errors with `Config value 'ci' is not defined
  in any .rc file`. Pass it to `bazel test`, not to the discovery query.
- **Do not add `--test_tag_filters`.** Combined with explicit labels, Bazel reports
  `No test targets were found, yet testing was requested` — see the "Running it" section above.

Requirements, if you are mirroring this elsewhere:

- Docker must be available to the runner — `setup-docker: 'true'` on the `setup-build-env`
  action, the same step `//tools/scripts:test_cross_compilation`'s job uses.
- Network egress to pull `postgres:16-alpine` (and `testcontainers/ryuk:0.14.0` unless Ryuk is
  disabled) the first time; both are small (Alpine-based) so this is fast even uncached.
- No new secrets or credentials are required — both images are public.
- Do **not** add these targets to a default `bazel test //...` CI step; they stay `manual` so
  that step keeps working on a Docker-less machine.

## Verifying the constraints are actually being checked

`postgres_constraints_test.go` proves two things a fake cannot:

1. A plain `UNIQUE (owner, kind, version)` index rejects a duplicate insert.
2. A **partial** unique index (`... WHERE valid_to IS NULL`, this repo's SCD2 "current row"
   convention — see `AGENTS.md`) rejects a second "current" row for the same key while still
   allowing any number of historical (`valid_to IS NOT NULL`) rows.

Both assertions were deliberately broken (constraint/index commented out of the schema) and
re-run to confirm the test fails with a real assertion, not a false-positive PASS:

```
--- FAIL: TestUniqueConstraint_RejectsDuplicateOwnerKindVersion (18.48s)
    postgres_constraints_test.go:73: duplicate (owner, kind, version) insert succeeded; the UNIQUE constraint did not fire

--- FAIL: TestPartialUniqueIndex_OnlyGuardsCurrentRow (10.50s)
    postgres_constraints_test.go:116: second current (valid_to IS NULL) row for the same app_id succeeded; the partial unique index did not fire
```

Both schema changes were reverted after this check.

## Rough edges hit while building this

- `go mod tidy` cannot be run as a full/whole-repo command here: this repo has several
  `go.mod`-visible import paths (`.../manmanv2/protos`, `.../generated/go/...`,
  `.../firmware/proto`, etc.) that only exist as Bazel-generated output, not as real files on
  disk, so a bare `go mod tidy` fails trying to resolve them. Add dependencies with a scoped
  `go get <module>@<version>` instead, then reconcile `go.mod`/`MODULE.bazel` by hand or with
  `bazel run //:gazelle` + `bazel mod tidy` for the paths that matter.
- `bazel mod tidy` did not pick up the two new `use_repo()` entries
  (`com_github_testcontainers_testcontainers_go`,
  `com_github_testcontainers_testcontainers_go_modules_postgres`) automatically in this
  environment — it exited with no output and no diff. They were added to `MODULE.bazel` by hand;
  `bazel build` then only emitted a warning ("reported as indirect dependencies... Fix the
  use_repo calls by running 'bazel mod tidy'") rather than an error, so the build was not
  blocked.
- `gazelle` correctly skipped generating a `go_test` rule for `postgres_constraints_test.go`
  because it's gated behind `//go:build integration`; the `go_test` target (including
  `gotags = ["integration"]` and the manual/no-cache/etc. tag set) was written by hand.
- Docker and the Docker socket were reachable directly from this environment's shell without any
  extra `no-sandbox`-equivalent workaround — `docker pull`/`docker ps` worked outside Bazel with
  no special flags. Interestingly, the test **also passed with the `no-sandbox` tag temporarily
  removed** (forcing Bazel's default sandboxed test execution) in this specific dev environment,
  contradicting the general expectation that a sandboxed test action can't reach
  `/var/run/docker.sock`. The tag is kept anyway: it's the documented, portable posture (matches
  `tools/appmeta` and `tools/lib32` precedent) and other environments — GitHub Actions runners in
  particular — are far more likely to enforce the sandbox boundary this repo's own docs warn
  about. Don't treat "it worked without `no-sandbox` once" as proof it's unnecessary elsewhere.
