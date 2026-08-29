---
name: review
description: The human review gate for a project-manager plan — reviews an architect-approved draft in a GitHub Discussion, then either approves it (triggering creation of the final root plan Issue labeled plan:approved) or routes feedback back through producer/architect in the Discussion.
---

# review

Drives the human review gate for a planned feature. Reviews the architect-approved specification draft in a GitHub Discussion, and upon approval publishes the final root plan Issue. See `tools/project-manager/CONVENTIONS.md`.

## Usage

```
/project-manager:review <discussion-url>
```

## Steps

1. Inspect the Discussion (`gh discussion view <discussion-url>` or comment listing). Confirm that `Architect sign-off: approved` is present in the comment thread. If not, report that the draft is not yet architect-approved and point the user to `/project-manager:design <discussion-url>`.

2. Summarize for the user:
   - User stories, FRs, and NFRs from the latest draft
   - Key points from architect's reconciliation comments

3. Ask the user how to proceed:
   - **Approve** — publish the final root plan Issue and release to implementation.
   - **Request changes** — provide feedback for producer/architect to address.

4. **If approved:** Dispatch `project-manager:producer` with the discussion URL to run Mode 3:
   - **If this is a milestone of a product brief** — the discussion is titled `Intake: M<n> — <outcome>` and its draft's first line names the product issue — pass the product issue number and milestone id in the dispatch, so producer carries the `Product: #<p> — Milestone M<n>: <outcome>` line onto the root plan issue and posts `Ledger: M<n> → planned (#<root-issue>)` as a comment on the tracking issue (never a body edit — CONVENTIONS.md § Roadmap ledger). Without the `Product:` line the milestone is severed from its brief and architect stops running its load-bearing check on later rounds.
   - Create root plan Issue: `gh issue create --title "Plan: <feature>" --label "plan:approved" --body-file <tmpfile>` with first line `Intake discussion: <discussion-url>`.
   - Post closing comment on the discussion: `gh discussion comment <discussion-url> --body "Approved root plan issue: <issue-url>"`.
   - Capture the created issue number `<root-issue-number>`.
   - Tell the user the plan is approved and that `/project-manager:plan <root-issue-number>` is the next step (task breakdown into a Project board). If no stakeholder meeting was held during design (no `Stakeholder meeting round <N>: <url>` link comment on the discussion), mention that `/project-manager:stakeholder-meeting <root-issue-number>` can still convene the spec's personas against the approved plan before implementation starts.

5. **If changes requested:**
   - Ask user for feedback text.
   - Post feedback to the Discussion: `gh discussion comment <discussion-url> --body "<feedback>"`.
   - Dispatch `project-manager:producer` (Mode 2) to address feedback and update the draft.
   - Dispatch `project-manager:architect` for a follow-up reconciliation round in the Discussion.
   - Once architect posts sign-off, return to step 2 to present the updated state to the user.
