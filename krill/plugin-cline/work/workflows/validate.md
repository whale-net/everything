# validate

*Runs whole-system validation for a krill-work plan — dispatches system-validator to exercise the merged result in Tilt against the FeatureSet's Requirements (read live from krill), then routes any findings to planner for follow-up tasks. On the Milestone path this never touches GitHub Issues/Projects.*


Only meaningful once every task in the manifest (Milestone path) or every
task item (no-Milestone path) is `Done`.

## Usage

```
the `validate` workflow <milestone-id>     # Milestone path — needs the task manifest plan/implement produced
the `validate` workflow 123                # no-Milestone GitHub fallback: the tracking issue number
```

## Steps (Milestone path)

1. Confirm every task in the manifest (CONVENTIONS.md "No task-discovery
   query exists" — you must already have it) is `Done`: `get_task {id}` on
   each, don't trust a stale snapshot.
2. Build the integration branch from every task's own tip (most will
   already be on `main` via continuous merge) right before dispatching
   system-validator: a local, never-pushed branch off freshly pulled
   `main` (`git branch -D pm-<milestone>-integration 2>/dev/null; git
   checkout main && git pull && git checkout -b pm-<milestone>-integration`,
   then `git merge --no-edit <tip>` for every task's topmost branch —
   merging an already-merged tip is a harmless no-op). A conflict here is
   real cross-task integration work; resolve it and re-run.
3. Spawn a subagent (Cline: new_task) with the `krill-system-validator` agent as its mode with the FeatureSet id (and
   Milestone id). It calls `get_feature_set_slice {id}` for the live
   Requirements, brings the system up via Tilt, exercises it, and reports
   findings as `record_note` scope-notes (expected to hit the known
   `record_note` blocker — see `the krill-system-validator agent in .cline/agents`).
4. **If everything passed:** re-dispatch `mergepush` with every task as
   done and nothing new to integrate (it merges anything not yet landed,
   a no-op otherwise), then verify every PR is `MERGED` — one `gh pr list
   --json number,url,headRefName` call, not one `gh pr view` per branch; a
   PR still open means something is blocking it (failing check, stale
   approval), so resolve that first. Call `set_milestone_status
   {milestone_id, status: "shipped"}` and, per-item,
   `mark_delivered_item_shipped` for each delivered Feature/Requirement —
   both work from this dispatch today. Report the plan fully validated with
   the full PR list.
5. **If there are findings:** spawn a subagent (Cline: new_task) with the `krill-planner` agent as its mode with the
   finding text `system-validator` reported (not note ids, since
   `record_note` itself is blocked — the one sanctioned body-passing
   exception in CONVENTIONS.md "Subagent dispatch: ids, not bodies"; switch
   to note ids once it's unblocked) plus the Milestone id to run its
   findings-handling process.
   Report the new task manifest additions and point to
   the `implement` workflow <milestone-id>`.

## Steps (no-Milestone GitHub fallback)

`<n>` is the GitHub tracking issue. Confirm every task item with `Part of
#<n>` is `Status: Done` (`gh project item-list <project-number> --owner
whale-net --query "-status:Done" --format json`, filtered to `Part of
#<n>`); if any remain, tell the user to finish the `implement` workflow <n>`
and stop. Then build the integration branch and dispatch
the `krill-system-validator` agent with `<n>` (findings become issues on the
Project at `Status: Validation`). If everything passed: re-dispatch
`mergepush`, verify every PR is `MERGED`, and post `gh issue comment <n>
--body "PRs: <url>, ..."` on the tracking issue. If there are findings:
spawn a subagent (Cline: new_task) with the `krill-planner` agent as its mode with the finding issue numbers to create
follow-up task issues in `Scaffold`/`Implementation`, and point to
the `implement` workflow <n>`.
