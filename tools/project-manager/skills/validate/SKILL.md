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

1. Confirm readiness the same precise way system-validator itself will: every `phase:implementation` and `phase:testing` issue with `Part of #<n>` must be closed.
   ```sh
   gh issue list --label "phase:implementation" --state open --json number,body \
     | jq -r '.[] | select(.body | test("Part of #<n>([^0-9]|$)")) | .number'
   ```
   (repeat for `phase:testing`). If either returns issue numbers, implementation isn't done — tell the user to finish `/project-manager:implement <n>` first and stop.

2. Dispatch `project-manager:system-validator` (foreground, via Agent tool) with the root issue number. Let it run its full Process: bring the system up via Tilt, exercise it against the FRs/NFRs, grade each one, and file `phase:validation`/`from:system-validator` finding issues for anything that isn't a clean pass.

3. **If everything passed:** report that the plan is fully validated. Nothing further to do.

4. **If there are findings:** dispatch `project-manager:planner` (foreground) with the set of finding issue numbers to run its "Handling system-validator findings" process — converting blocking findings into properly sequenced follow-up task issues and closing each finding once its follow-ups are linked. Report the new task issue numbers to the user, and tell them `/project-manager:implement <n>` will pick up the new `status:ready` work.
