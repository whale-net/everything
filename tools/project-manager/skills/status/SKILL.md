---
name: status
description: Read-only status dashboard for a project-manager root plan — shows its plan:* lifecycle state and a breakdown of the plan's Project items by phase. Use to check where a plan stands before deciding which orchestration skill (plan/review/build/validate) to run next.
---

# status

Pure read — never edits labels, comments, project items, or dispatches any persona. See `tools/project-manager/CONVENTIONS.md` for what each phase means.

## Usage

```
/project-manager:status 123
```

## Steps

1. `gh issue view <n> --comments` — report the root issue's title and current `plan:*` label, and tell the user which orchestration skill applies next:
   - `plan:draft` / `plan:needs-answers` → `/project-manager:plan <n>`
   - `plan:architect-approved` → `/project-manager:review <n>`
   - `plan:approved` → `/project-manager:implement <n>`

   If there's no `Project board: <url>` comment, task breakdown hasn't started — report that and stop; there's nothing further to break down by phase.

2. List every item on the plan's Project and its `Status`:
   ```sh
   gh project item-list <project-number> --owner whale-net --field "Status" --format json \
     | jq '[.items[] | select(.content.body | test("Part of #<n>([^0-9]|$)"))]'
   ```
   Group by `Status` (`Scaffold`/`Implementation`/`Testing`/`Validation`/`Done`). For every item not yet `Done`, check its `assignees` field (claimed/in-progress vs. unclaimed) and, from its issue body's `Depends on:` line, whether every dependency is closed (ready) or not (blocked).

3. Report a compact table: phase × (blocked / ready / claimed / done count). Call out anything that looks stuck — an item sitting unclaimed for a while whose dependencies are all closed (a sign nothing has picked it up yet), or a claimed item whose issue has stale activity.

4. If every `Implementation`/`Testing` item is `Done` and there are no open `Validation` finding items, mention that `/project-manager:validate <n>` is available.
