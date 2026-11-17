---
name: krill-validate
description: Runs whole-system validation for a krill-work plan — dispatches system-validator to exercise the merged result in Tilt against the FeatureSet's Requirements (read live from krill), then routes any findings to planner for follow-up tasks. Takes a krill Milestone id; with none, it stops and says to cut the milestone first.
---

# krill-validate

Only meaningful once every task under the Milestone is `Done`.

## Usage

```
/krill-validate <milestone-id>
```

`<milestone-id>` is required; with none, stop and report the "Milestone
required" hard stop in `references/CONVENTIONS.md`.

## Subagent setup

Call `get_skill_resources` for `["references/CONVENTIONS.md", "references/agents/system-validator.md", "references/agents/mergepush.md", "references/agents/planner.md"]`.
Define once per conversation via `define_subagent`:
- `krill-system-validator`: `system_prompt` from `references/agents/system-validator.md`, `enable_mcp_tools: true`, `enable_write_tools: true` (needs `run_command` to exercise the running system).
- `krill-mergepush`: `system_prompt` from `references/agents/mergepush.md`, `enable_mcp_tools: true`, `enable_write_tools: true`.
- `krill-planner`: `system_prompt` from `references/agents/planner.md`, `enable_mcp_tools: true`, `enable_write_tools: false`.

## Steps

1. Confirm every task under the Milestone is `Done`: take the manifest you were
   handed, or `list_tasks {milestone_id}`, then `get_task {id}` on each — don't
   trust a stale snapshot.
2. Build the integration branch from every task's own tip (most will already be
   on `main` via continuous merge) right before dispatching
   `krill-system-validator`: a local, never-pushed branch off freshly pulled
   `main` (`git branch -D pm-<milestone>-integration 2>/dev/null; git checkout
   main && git pull && git checkout -b pm-<milestone>-integration`, then `git
   merge --no-edit <tip>` for every task's topmost branch). A conflict here is
   real cross-task integration work; resolve it and re-run.
3. Dispatch `krill-system-validator` via `invoke_subagent` (`Model: "inherit"`,
   `Workspace: "inherit"`) with the FeatureSet id and Milestone id. It calls
   `get_feature_set_slice {id}` for the live Requirements, exercises the
   system, and reports findings as `record_note` scope-notes.
4. **If everything passed:** re-dispatch `krill-mergepush` with every task as
   done and nothing new to integrate, then verify every PR is `MERGED` — one
   `gh pr list --json number,url,headRefName` call. Call `set_milestone_status
   {milestone_id, status: "shipped"}` and, per-item,
   `mark_delivered_item_shipped` for each delivered Feature/Requirement. Report
   the plan fully validated with the full PR list.
5. **If there are findings:** dispatch `krill-planner` with the finding note
   ids `krill-system-validator` reported plus the Milestone id to run its
   findings-handling process. Report the new task manifest additions and point
   to `/krill-implement <milestone-id>`.
