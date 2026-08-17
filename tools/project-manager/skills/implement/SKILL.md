---
name: implement
description: Runs the implementation phase of a project-manager plan — dispatches planner to create the task breakdown Project with swimlanes (if not already done), then loops worker and validator personas over ready tasks across swimlanes until all tasks reach Done.
---

# implement

Drives implementation from `plan:approved` through swimlane task execution. See `tools/project-manager/CONVENTIONS.md`.

## Usage

```
/project-manager:implement 123
```

## Steps

1. `gh issue view <n>` — confirm the root issue is labeled `plan:approved`. If not, point the user to `/project-manager:plan` or `/project-manager:review`.

2. **Branch.** Ensure the plan's shared branch exists and is checked out:
   ```sh
   git fetch origin main
   git checkout plan/<n>-<short-slug> 2>/dev/null || git checkout -b plan/<n>-<short-slug> origin/main
   ```
   Derive `<short-slug>` deterministically from the root issue title.

3. **Task breakdown (skip if already done).** `gh issue view <n> --comments` — if a `Project board: <url>` comment exists, extract the project number. Otherwise dispatch `project-manager:planner` with the root issue number to create the Project board with swimlanes, create cohesive task issues, and post the summary comment.

4. **Swimlane work loop.** Repeat until a full pass finds no ready unassigned work:
   a. Check active swimlanes in progression order: `Scaffold`, `Implementation`, `Testing`, `Validation`. For each swimlane, list unassigned items scoped to this plan:
      ```sh
      gh project item-list <project-number> --owner whale-net --query "status:<Swimlane> no:assignee" --format json \
        | jq -r '.items[] | select(.content.body | test("Part of #<n>([^0-9]|$)")) | .content.number'
      ```
      For each candidate, verify that every issue in its `Depends on:` line is in `state: CLOSED`:
      ```sh
      gh issue view <candidate> --json body
      # extract "Depends on: #a, #b" and confirm each:
      gh issue view <dep> --json state   # must be "CLOSED"
      ```
   b. For each ready candidate:
      - **`Scaffold` / `Implementation` / `Testing`:** Dispatch `project-manager:worker` with `<root>`, `<project-number>`, `<Swimlane>`, and candidate issue number. Worker claims, executes phase work, commits to branch, advances item to next swimlane, and unassigns itself.
      - **`Validation`:** Dispatch `project-manager:validator` with `<root>`, `<project-number>`, and candidate issue number. Validator checks criteria: if passing, sets `Status: Done` and closes the issue; if failing, moves item back to `Implementation` and unassigns.
   c. Re-scan swimlanes after each batch. Repeat until all tasks reach `Done` or no more ready unassigned tasks remain.

5. **Report.** Summarize progress across swimlanes. If every task is in `Status: Done`, tell the user `/project-manager:validate <n>` is the next step. If items remain blocked or waiting on dependency fixes, report them clearly.
