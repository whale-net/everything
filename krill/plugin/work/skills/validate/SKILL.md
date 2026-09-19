---
name: validate
description: Runs whole-system validation for a krill-work plan (fork of project-manager's validate) — dispatches system-validator to exercise the merged result in Tilt against the FeatureSet's Requirements (read live from krill), then routes any findings to planner for follow-up tasks.
---

# validate

Forked from `tools/project-manager/skills/validate` — mechanics unchanged
except step 3's grading rubric now comes from krill instead of the issue
body's copied-in text. Only meaningful once all swimlane tasks are `Done`.

## Usage

```
/krill-work:validate 123
```

## Steps

1-2. Identical to project-manager's `validate` steps 1-2: confirm every task
   item is `Status: Done`, then build the `pm-<n>-integration` branch from
   every task's own tip (most will already be on `main` via continuous
   merge) right before dispatching system-validator.

3. Dispatch `krill-work:system-validator` with the tracking issue number.
   It reads the tracking issue's `krill feature-set-id: <id>` first line,
   calls `get_feature_set_slice {id}` for the live Requirements (see
   `agents/system-validator.md`), brings the system up via Tilt, exercises
   it, and files finding issues for anything that isn't a clean pass.

4. **If everything passed:** identical finalize sequence to project-manager's
   `validate` step 4 — re-dispatch `mergepush` as a no-op re-confirmation,
   verify every PR is `MERGED`, post `PRs: ...` on the tracking issue. Post
   `Ledger: M<k> → shipped` on the product tracking issue if this is a
   milestone not hosted in krill; call `set_milestone_status {milestone_id,
   status: "shipped"}` instead for a krill-hosted milestone (and, per-item,
   `mark_delivered_item_shipped` for each delivered Feature/Requirement —
   CONVENTIONS.md). Report the plan fully validated with the full PR list.

5. **If there are findings:** dispatch `krill-work:planner` with the finding
   issue numbers to run its findings-handling process. Report the new task
   issue numbers and point to `/krill-work:implement <n>`.

Once krill's M4 work-tracking surface ships, this skill is the second
candidate (after `implement`) for replacing its `gh project item-list`/
`gh pr`/`gh issue comment` calls with native MCP calls — see
`krill/plugin/shared/CONVENTIONS.md`.
