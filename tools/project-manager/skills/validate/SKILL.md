---
name: validate
description: Runs whole-system validation for a project-manager plan — dispatches system-validator to exercise the merged result in Tilt against the root plan's acceptance criteria, then routes any findings to planner for follow-up tasks.
---

# validate

Drives `tools/project-manager/CONVENTIONS.md` § System validation. Only meaningful once implementation is actually done.

## Usage

```
/project-manager:validate 123
```

## Steps

1. Find the plan's Project (`Project board: <url>` comment on the root issue) and confirm readiness the same precise way system-validator itself will: every `Implementation` and `Testing` item with `Part of #<n>` must be `Done`.
   ```sh
   gh project item-list <project-number> --owner whale-net --query "status:Implementation" --format json \
     | jq -r '.items[] | select(.content.body | test("Part of #<n>([^0-9]|$)")) | .content.number'
   ```
   (repeat for `status:Testing`). If either returns issue numbers, implementation isn't done — tell the user to finish `/project-manager:implement <n>` first and stop.

2. Dispatch `project-manager:system-validator` (foreground, via Agent tool) with the root issue number. Let it run its full Process: bring the system up via Tilt, exercise it against the FRs/NFRs, grade each one, and file finding issues (added to the plan's Project at `Status: Validation`, `from:system-validator` in the body) for anything that isn't a clean pass.

3. **If everything passed:** open the PR per CONVENTIONS.md § Git hygiene — push the plan branch (`git push -u origin plan/<n>-<short-slug>`), open a draft PR against `main` (`gh pr create --draft --title "<root plan title> (#<n>)" --body "Closes #<n>" --head plan/<n>-<short-slug>`), and post `gh issue comment <n> --body "PR: <pr-url>"` on the root issue. Report that the plan is fully validated and the PR URL.

4. **If there are findings:** dispatch `project-manager:planner` (foreground) with the set of finding issue numbers to run its "Handling system-validator findings" process — converting blocking findings into properly sequenced follow-up task issues on the same Project and closing each finding once its follow-ups are linked. Report the new task issue numbers to the user, and tell them `/project-manager:implement <n>` will pick up the new ready work.
