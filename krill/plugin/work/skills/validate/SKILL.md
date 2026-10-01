---
name: validate
description: Runs whole-system validation for a krill-work plan — dispatches system-validator to exercise the merged result in Tilt against the FeatureSet's Requirements (read live from krill), then routes any findings to planner for follow-up tasks. Takes a krill Milestone id; with none, it stops and says to cut the milestone first.
---

# validate

Only meaningful once every task under the Milestone is `Done`.

## Usage

```
/krill-work:validate <milestone-id>
```

`<milestone-id>` is required; with none, stop and report the "Milestone
required" hard stop in CONVENTIONS.md.

## Steps

1. Confirm every task under the Milestone is `Done`: take the manifest
   you were handed, or `list_tasks {milestone_id}` (CONVENTIONS.md "Work
   axis"), then `get_task {id}` on each — don't trust a stale snapshot.
2. Build the integration branch from every task's own tip (most will
   already be on `main` via continuous merge) right before dispatching
   system-validator: a local, never-pushed branch off freshly pulled
   `main` (`git branch -D pm-<milestone>-integration 2>/dev/null; git
   checkout main && git pull && git checkout -b pm-<milestone>-integration`,
   then `git merge --no-edit <tip>` for every task's topmost branch —
   merging an already-merged tip is a harmless no-op). A conflict here is
   real cross-task integration work; resolve it and re-run.
3. Dispatch `krill-work:system-validator` with the FeatureSet id (and
   Milestone id). It calls `get_feature_set_slice {id}` for the live
   Requirements, brings the system up via Tilt, exercises it, and reports
   findings as `record_note` scope-notes (see `agents/system-validator.md`).
4. **If everything passed:** re-dispatch `mergepush` with every task as
   done and nothing new to integrate (it merges anything not yet landed,
   a no-op otherwise), then verify every PR is `MERGED` — one `gh pr list
   --json number,url,headRefName` call, not one `gh pr view` per branch; a
   PR still open means something is blocking it (failing check, stale
   approval), so resolve that first. Call `set_milestone_status
   {milestone_id, status: "shipped"}` and, per-item,
   `mark_delivered_item_shipped` for each delivered Feature/Requirement —
   both work from this dispatch. Report the plan fully validated with
   the full PR list.
5. **If there are findings:** dispatch `krill-work:planner` with the
   finding note ids `system-validator` reported (CONVENTIONS.md "Subagent
   dispatch: ids, not bodies") plus the Milestone id to run its
   findings-handling process.
   Report the new task manifest additions and point to
   `/krill-work:implement <milestone-id>`.
