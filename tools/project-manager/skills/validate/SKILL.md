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

2. **Build the integration branch** per CONVENTIONS.md § Git hygiene step 6 — `mergepush` cherry-picks each task onto the previous one's stack tip (CONVENTIONS.md § Git hygiene step 5), so the topmost registered branch's remote state already contains every task's combined work in principle, but treat that as an implementation detail rather than something to build on directly. Build a local integration branch explicitly, right before dispatching system-validator, and never push it:
   ```sh
   git branch -D pm-<n>-integration 2>/dev/null
   git checkout main && git pull
   git checkout -b pm-<n>-integration
   for tip in <topmost active branch of every task on this plan>; do
     git merge --no-edit "$tip"
   done
   ```
   A conflict here is real cross-task integration work — resolve it and re-run; it doesn't affect the individual task PRs.

3. Dispatch `project-manager:system-validator` with the root issue number. System-validator brings the system up via Tilt, exercises it against the FRs/NFRs, and files finding issues (added to the plan's Project at `Status: Validation` with `from:system-validator`) for anything that isn't a clean pass.

4. **If everything passed:** Finalize per CONVENTIONS.md § Git hygiene step 7:
   - Make sure every task branch is registered into the plan's stack: dispatch `mergepush` once more with `<n>`, the full `<plan-branches>` list (every task branch on this plan, in dependency order — reconstruct it the same way `implement`'s "Plan stack" section does if this session didn't just run `/project-manager:implement` itself), and an empty batch-to-integrate list if every task was already registered during implementation — this is a no-op re-run of `gh stack link` for anything already in the stack, which just confirms every PR is open with the correct base.
   - `gh pr view <top-branch> --json number` to get the PR number for `<plan-branches>`'s last (topmost) entry, then `gh stack merge <that-pr-number> --yes` — merges the whole stack, bottom to top, into `main` in one all-or-nothing operation (CONVENTIONS.md § Git hygiene step 7). Pick a merge method with `--squash`, `--rebase`, or `--merge` if the plan needs something other than whatever method was last used.
   - Collect every task's PR URL (`gh pr view <branch> --json number,url` per branch, or the URLs already gathered during `/project-manager:implement`).
   - Post `gh issue comment <n> --body "PRs: <url>, <url>, ..."` on the root issue.
   - **If the root issue's first line names a product brief** (`Product: #<p> — Milestone M<k>`), post `gh issue comment <p> --body "Ledger: M<k> → shipped"` on the product tracking issue — never a body edit (CONVENTIONS.md § Roadmap ledger). Ordinary single-feature plans skip this; it's the only product-aware step this skill has.
   - Report that the plan is fully validated, with the full list of PR URLs — the deliverable is this stack of small, individually reviewable PRs, not one PR for the whole plan.

5. **If there are findings:** Dispatch `project-manager:planner` with the finding issue numbers to run its "Handling system-validator findings" process — converting blocking findings into properly sequenced follow-up task issues starting in `Scaffold` or `Implementation` on the same Project. Report the new task issue numbers to the user, and point them to `/project-manager:implement <n>`.
