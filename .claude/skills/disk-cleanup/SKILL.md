---
name: disk-cleanup
description: This skill should be used when the user asks about low disk space, "where is my disk space going", cleaning up orphaned bazel caches, or stale git worktrees in this repo. Diagnoses the root filesystem's real usage (bypassing the dotfile-glob blind spot) and cleans up orphaned per-worktree bazel output bases plus stale git worktrees.
version: 1.0.0
---

# Disk Cleanup

## Root cause pattern

Every git worktree (`.claude/worktrees/*`, `.pm-worktrees/*`, or elsewhere) gets its own
Bazel **output base** under `~/.cache/bazel/_bazel_$(whoami)/<hash>/`, keyed by the
worktree's absolute path. When a worktree is deleted (`git worktree remove`, or just
`rm -rf`'d), its output base is **never cleaned up automatically** — it silently keeps
consuming disk, often multiple GB each (full execroot, external deps, compiled Go
stdlib, etc). With dozens of worktrees created and removed over time, this accumulates
into hundreds of GB.

On a single-disk dev environment (e.g. a WSL distro), when the root filesystem hits
100%, things break in confusing ways — builds fail, git operations stall.

## Diagnostic gotcha

`du -sh ~/*` (glob) **silently skips dotfiles** — it will never show `~/.cache`, which
is where the bloat actually lives. Always check hidden dirs explicitly:

```bash
ls ~/.cache/          # look for a "bazel" entry
du -sh ~/.cache/bazel  # this is usually the single biggest offender
```

`du` piped through `sort` buffers all output until `du` fully exits — on a
near-full/slow disk this can look "stuck" for many minutes with zero output. Prefer
running it via a backgrounded/timeout'd command and polling, rather than assuming it hung.

## Step 1 — Map bazel output bases to worktrees

Each output base directory contains a `DO_NOT_BUILD_HERE` file whose contents are the
absolute path of the worktree it belongs to:

```bash
BAZEL_CACHE=~/.cache/bazel/_bazel_$(whoami)
for f in "$BAZEL_CACHE"/*/DO_NOT_BUILD_HERE; do
  hash=$(basename "$(dirname "$f")")
  path=$(cat "$f" 2>/dev/null)
  exists="MISSING"
  [ -d "$path" ] && exists="EXISTS"
  echo "$exists|$hash|$path"
done > /tmp/bazel_cache_map.txt

grep -c '^MISSING' /tmp/bazel_cache_map.txt   # orphaned — safe to delete
grep -c '^EXISTS'  /tmp/bazel_cache_map.txt   # still belongs to a live worktree
```

Some older output bases lack `DO_NOT_BUILD_HERE` (no marker) — inspect these manually
(check mtime, execroot contents) before deleting; don't assume they're orphaned.

## Step 2 — Delete orphaned output bases

Confirm scale with the user first (this is regenerable build cache, not source — but it's
often hundreds of GB across 100+ directories, so a heads-up is worth it). Bazel marks many
output files read-only, so `chmod -R u+w` before `rm -rf` or you'll hit `Permission denied`
partway through and stall:

```bash
grep '^MISSING' /tmp/bazel_cache_map.txt | cut -d'|' -f2 > /tmp/orphaned_hashes.txt

cd ~/.cache/bazel/_bazel_$(whoami)
while read -r h; do
  [ -d "$h" ] || continue
  chmod -R u+w "$h" 2>/dev/null
  rm -rf "$h" 2>/dev/null
done < /tmp/orphaned_hashes.txt
```

Run this via `run_in_background` with a generous timeout — on a very full disk, I/O is
slow and this can take several minutes. Check progress with `df -h /` between runs; space
is typically freed incrementally as each directory is removed, not all at once at the end.

## Step 3 — Clean up stale git worktrees themselves

Orphaned bazel caches are a symptom; stale worktrees are the cause. List and review:

```bash
git worktree list
```

For worktrees whose branch is merged/closed (check `gh pr list --state merged` or ask the
user), remove properly rather than `rm -rf`ing the directory directly:

```bash
git worktree remove <path>          # add --force if it has uncommitted changes worth discarding — confirm with user first
git worktree prune                  # clean up stale worktree metadata after manual dir removal
```

Removing the worktree does **not** clean up its bazel output base — re-run Step 1/2
afterward (or periodically) to catch newly-orphaned caches.

## Step 4 — Re-discover / verify

After cleanup, re-run the diagnostic to confirm and report freed space:

```bash
df -h /
```

If disk pressure recurs later, re-run this whole skill — it's idempotent (Step 2 only
touches dirs already confirmed orphaned in Step 1).
