---
name: status
description: Read-only status dashboard for a project-manager root plan — shows its lifecycle state and a breakdown of Project items across swimlanes. Use to check where a plan stands before deciding which orchestration skill to run next.
---

# status

Pure read — never edits items or dispatches personas. See `tools/project-manager/CONVENTIONS.md` for what each swimlane means.

## Usage

```
/project-manager:status 123
```

## Steps

1. `gh issue view <n> --comments` — report the root issue's title and status:
   - If labeled `plan:approved` with a `Project board: <url>` comment, proceed to step 2.
   - If labeled `plan:approved` without a `Project board:` comment, report that task breakdown hasn't started yet and `/project-manager:implement <n>` is next.
   - Report the last `Stakeholder meeting: cleared` / `Stakeholder meeting: blocked (<k> blockers)` line found on the root issue or on its intake discussion (first body line, `Intake discussion: <url>`). If neither has any, report that no stakeholder meeting has been held and that `/project-manager:stakeholder-meeting <n>` is available.

2. List every item on the plan's Project and its `Status` (swimlane):
   ```sh
   gh project item-list <project-number> --owner whale-net --field "Status" --format json \
     | jq '[.items[] | select(.content.body | test("Part of #<n>([^0-9]|$)"))]'
   ```
   Group items by `Status` (`Scaffold`, `Implementation`, `Testing`, `Validation`, `Done`, `Noted`, `Carry-over`, `Deferred`). For each item not yet `Done`, check its `assignees` field (claimed vs. unclaimed) and whether its `Depends on:` issues are closed (ready vs. blocked).

3. Report a compact table: Swimlane × (Blocked / Ready / Claimed / Done count). Highlight items currently in progress or waiting to be claimed.

4. If all task items are `Done` and there are no open `Validation` finding items, report that `/project-manager:validate <n>` is available.
