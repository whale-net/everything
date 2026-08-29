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

2. **Build and check out the integration branch** per CONVENTIONS.md § Git hygiene step 7 — every task's branch is still its own separate `gh stack`, so system-validator needs one local ref with all of them merged together to exercise the whole system in Tilt. Find each task's branch first via `git branch --list 'pm*-<n>/<task>-*'` (CONVENTIONS.md § Git hygiene step 2 — local, then remote, if not found locally):
   ```sh
   git branch -D pm-<n>-integration 2>/dev/null
   git checkout main && git pull
   git checkout -b pm-<n>-integration
   for tip in <topmost active branch of every task on this plan>; do
     git merge --no-edit "$tip"
   done
   ```
   Resolve any conflict (real cross-task integration work) before proceeding — it doesn't affect the individual task PRs.

3. Dispatch `project-manager:system-validator` with the root issue number. System-validator brings the system up via Tilt, exercises it against the FRs/NFRs, and files finding issues (added to the plan's Project at `Status: Validation` with `from:system-validator`) for anything that isn't a clean pass.

4. **If everything passed:** Finalize per CONVENTIONS.md § Git hygiene step 8:
   - For each task branch (found via `git branch --list 'pm*-<n>/<task>-*'`), make sure it has an open PR: `git checkout <branch> && gh stack submit --auto`. If this is the first time it creates the PR, set title/body per CONVENTIONS.md § Git hygiene, "PR content".
   - Collect every task's PR URL (`gh stack view --json` per branch, or the URLs already gathered during `/project-manager:implement`).
   - Post `gh issue comment <n> --body "PRs: <url>, <url>, ..."` on the root issue.
   - **If the root issue's first line names a product brief** (`Product: #<p> — Milestone M<k>`), post `gh issue comment <p> --body "Ledger: M<k> → shipped"` on the product tracking issue — never a body edit (CONVENTIONS.md § Roadmap ledger). Ordinary single-feature plans skip this; it's the only product-aware step this skill has.
   - Report that the plan is fully validated, with the full list of PR URLs — the deliverable is this stack of small, individually reviewable PRs, not one PR for the whole plan.

5. **If there are findings:** Dispatch `project-manager:planner` with the finding issue numbers to run its "Handling system-validator findings" process — converting blocking findings into properly sequenced follow-up task issues starting in `Scaffold` or `Implementation` on the same Project. Report the new task issue numbers to the user, and point them to `/project-manager:implement <n>`.
