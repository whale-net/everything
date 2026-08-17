---
name: validate
description: Runs whole-system validation for a project-manager plan — dispatches system-validator to exercise the merged result in Tilt against the root plan's acceptance criteria, then routes any findings to planner for follow-up tasks.
---

# validate

Drives `tools/project-manager/CONVENTIONS.md` § System validation. Only meaningful once all swimlane tasks are `Done`.

## Usage

```
/project-manager:validate 123
```

## Steps

1. Find the plan's Project (`Project board: <url>` comment on the root issue) and confirm readiness: every task item with `Part of #<n>` must be in `Status: Done`:
   ```sh
   gh project item-list <project-number> --owner whale-net --query "-status:Done" --format json \
     | jq -r '.items[] | select(.content.body | test("Part of #<n>([^0-9]|$)")) | .content.number'
   ```
   If any task issues remain in `Scaffold`, `Implementation`, `Testing`, or `Validation`, implementation is not complete — inform the user to finish `/project-manager:implement <n>` first and stop.

2. Dispatch `project-manager:system-validator` with the root issue number. System-validator brings the system up via Tilt, exercises it against the FRs/NFRs, and files finding issues (added to the plan's Project at `Status: Validation` with `from:system-validator`) for anything that isn't a clean pass.

3. **If everything passed:** Open the PR per CONVENTIONS.md § Git hygiene:
   - Push the plan branch: `git push -u origin plan/<n>-<short-slug>`
   - Open a draft PR against `main`:
     ```sh
     gh pr create --draft --title "<root plan title> (#<n>)" --body "Closes #<n>" --head plan/<n>-<short-slug>
     ```
   - Post `gh issue comment <n> --body "PR: <pr-url>"` on the root issue.
   - Report that the plan is fully validated along with the PR URL.

4. **If there are findings:** Dispatch `project-manager:planner` with the finding issue numbers to run its "Handling system-validator findings" process — converting blocking findings into properly sequenced follow-up task issues starting in `Scaffold` or `Implementation` on the same Project. Report the new task issue numbers to the user, and point them to `/project-manager:implement <n>`.
