# Release Cleanup

`release_helper cleanup-releases` prunes old release tags. For each tag it
deletes, it also deletes the matching GitHub Release and, optionally, the
GHCR package versions. Dry run is the default. The code is in
[`tools/release_helper_go/cmd/cleanup.go`](../tools/release_helper_go/cmd/cleanup.go).

> **Read [Retention rules](#retention-rules) before running it for real.**
> With the default settings, the tool deletes the *only remaining* release
> line of any app that has fewer minor versions than `--keep-minor-versions`.
> In November 2026, 38 of 63 tagged apps were in that state.

## Running it

From GitHub: Actions → **Cleanup Old Releases** → Run workflow. The
inputs are `keep_minor_versions` (default 2), `min_age_days` (14),
`dry_run` (true), `delete_packages` (true), and `use_source_tools` (false,
which uses prebuilt release tools). Run once with `dry_run` on, read the
log, then run again with it off.

Locally (it needs `GITHUB_TOKEN` and reads tags from your clone, so run
`git fetch --tags` first):

```bash
export GITHUB_TOKEN=...   # contents:write, plus packages:write if deleting packages
bazel run //tools:release -- cleanup-releases                       # dry run
bazel run //tools:release -- cleanup-releases --min-age-days 30 --keep-minor-versions 3
bazel run //tools:release -- cleanup-releases --no-dry-run          # deletes, no confirmation prompt
bazel run //tools:release -- cleanup-releases --no-dry-run --no-delete-packages
```

| Flag | Default | |
|------|---------|---|
| `--keep-minor-versions` | 2 | Newest minor lines to keep per app |
| `--min-age-days` | 14 | Tags newer than this are never deleted |
| `--dry-run` / `--no-dry-run` | dry run | `--no-dry-run` deletes immediately |
| `--delete-packages` / `--no-delete-packages` | delete | Also delete GHCR package versions |

The owner and repo come from `GITHUB_REPOSITORY_OWNER` and
`GITHUB_REPOSITORY`. They default to `whale-net/everything`.

## Retention rules

Only tags matching `<name>.vX.Y.Z` are considered, for example
`demo-hello-go.v1.2.3` or `helm-demo-app.v1.0.0`. Tags are grouped by
`<name>`. A tag's age is the date of the commit it points at. For each
`<name>`:

1. Within a minor line (`vX.Y.*`), every patch except the newest is
   deleted once it's older than `--min-age-days`.
2. The newest patch of each minor line is kept if it's newer than
   `--min-age-days`, or if it's in the newest `--keep-minor-versions`
   lines. When the app has more than one major version, the newest minor
   of each major is also kept.
3. Anything else is deleted.

The rule in step 2 about keeping the newest lines only applies when the
app has **at least** `--keep-minor-versions` minor lines. With fewer, that
protection is skipped. So an app that only ever released `v0.0.x` loses its
latest tag once that tag is older than `--min-age-days`. Until that's
fixed, pass `--keep-minor-versions 1` or review the dry-run list
carefully.

## What gets deleted

The steps run in this order, and errors are logged without stopping the run:

1. The GitHub Release for each tag, if one exists.
2. The tag itself, through the GitHub API (not your local clone).
3. With package deletion on: every version of
   `ghcr.io/<owner>/<name>` that carries a tag exactly equal to `vX.Y.Z`.
   Only the first 100 versions of each package are searched.

## Tests

```bash
bazel test //tools/release_helper_go:cmd_test --test_filter='Cleanup|IdentifyTagsToPrune|ParseSemver'
```
