---
name: vendor-sync
description: Use this skill after adding or updating an external dependency in this repo — a new/updated bazel_dep, git_override, or archive_override in MODULE.bazel, a Go dependency added via `go get` + `bazel mod tidy`, or a Python dependency added via pyproject.toml + `uv lock`. Refreshes the machine-level shared bazel vendor directory (docs/CONFIGURATION.md § Shared Vendor Directory) so newly added repos don't silently fall back to per-worktree fetching, which is what keeps worktree Bazel output-base disk usage down.
version: 1.0.0
---

# Bazel Vendor Sync

## Why this exists

`docs/CONFIGURATION.md` documents a machine-level `~/.bazelrc` entry
(`common --vendor_dir=<absolute path outside any worktree>`) that lets every git
worktree of this repo share one copy of external repos instead of each output base
fetching and extracting its own. That only works if the vendor directory is kept in
sync: a repo added to `MODULE.bazel`/`go.mod`/`pyproject.toml` after the last
`bazel vendor //...` run is **not** in the shared directory yet, so Bazel quietly falls
back to fetching it per-worktree again — the exact duplication the vendor dir exists to
prevent. This skill is the maintenance step that closes that gap every time a
dependency changes.

## When to run this

Any time one of these just happened in this session:

- `MODULE.bazel` changed — a `bazel_dep(...)`, `git_override(...)`, or
  `archive_override(...)` was added, removed, or had its version/pin changed
- Go: `go get ...` followed by `bazel run //:gazelle` and `bazel mod tidy`
- Python: a new/updated entry in `pyproject.toml`'s `dependencies` followed by
  `uv lock --python 3.13`

## Steps

1. **Check whether the shared vendor dir is even configured on this machine.**

   ```bash
   grep -h '^\s*common --vendor_dir=' ~/.bazelrc 2>/dev/null
   ```

   - If nothing is printed, this machine isn't using the shared vendor dir — tell the
     user and point them at `docs/CONFIGURATION.md` § "Shared Vendor Directory
     (`bazel vendor //...`)" to set it up. Don't silently skip vendoring or invent a
     path yourself; the path choice (and the decision to opt in at all) is the user's.
   - If it is configured, capture the path for step 3 (`VENDOR_DIR=$(grep ...)`).

2. **Confirm the dependency change actually landed** before re-vendoring — check
   `git status` / `git diff` for the relevant file (`MODULE.bazel`, `go.mod`/`go.sum`,
   `uv.lock`). Re-vendoring against a half-finished dependency change just wastes a
   fetch cycle.

3. **Re-run vendor:**

   ```bash
   bazel vendor //...
   ```

   Run this from the repo root of the worktree you're in — it reads `--vendor_dir` from
   `~/.bazelrc` automatically. This can take a while the first time or after a large
   dependency bump (it fetches everything needed to build `//...`); subsequent runs
   only fetch what's new or changed.

4. **Sanity-check** that the newly added dependency is actually usable:

   ```bash
   bazel build //path/to/a/target/that/uses/the/new/dep
   ```

   Use whatever target you were already working on — this isn't a full-repo rebuild,
   just confirmation the new repo resolved correctly out of the vendor dir.

5. **Report, don't commit.** Nothing under `--vendor_dir` is part of this repo (it lives
   outside every worktree, in the path from `~/.bazelrc`) — there is nothing to `git
   add`. Just tell the user vendoring is refreshed and, if useful, the vendor dir's
   current size:

   ```bash
   du -sh "$VENDOR_DIR" 2>/dev/null
   ```

## If the vendor dir doesn't exist yet on this machine

Don't create `~/.bazelrc` or pick a `--vendor_dir` path unilaterally — that's a
one-time, per-machine opt-in decision documented in `docs/CONFIGURATION.md`. Point the
user there. If they ask you to set it up, follow that doc's setup section exactly (an
absolute path outside any worktree, e.g. `~/.cache/bazel-vendor/everything`), then come
back to step 3 above.
