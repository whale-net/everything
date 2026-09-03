---
name: design
description: Design one feature's or one milestone's specification — interviews you for requirements/user stories in a GitHub Discussion, drafts FRs/NFRs, then loops producer/architect in the Discussion until architect signs off, ready for /project-manager:review. Optionally holds a stakeholder meeting with every persona in the spec before hand-off. Run this before a Project board or task issues exist — for task breakdown after the root plan is approved, use /project-manager:plan instead. Takes --milestone M<n> against a product:approved brief to spec exactly one milestone; if the request is a whole product rather than one feature, run /project-manager:product first instead of designing it all at once.
---

# design

Orchestrates the project-manager design pipeline inside a GitHub Discussion from a feature idea up to architect sign-off, optionally including a stakeholder meeting round. Comes before `/project-manager:review` (human approval) and `/project-manager:plan` (task breakdown) — this skill produces the spec, not the Project board. See `tools/project-manager/CONVENTIONS.md` for the full lifecycle.

## Usage

```
/project-manager:design "short feature description"
/project-manager:design <discussion-url>            # resume an existing intake discussion
/project-manager:design 42 --milestone M2           # spec milestone M2 of product brief issue #42
/project-manager:design                             # no args — ask what the feature is
```

### Parameters

| Parameter | Default | Effect |
|---|---|---|
| `--milestone M<n>` | none | Scope this design to one milestone of a `product:approved` brief (the positional argument is then the product issue number). Producer specs only that milestone's outcome; architect additionally checks the draft against the milestone's `Must not foreclose` load-bearing decisions. |
| `--stakeholder-meeting` | off | After architect sign-off, run `/project-manager:stakeholder-meeting` on the discussion: every persona in the spec gives a round of feedback, and any blocker it raises goes back through the producer/architect loop before hand-off to review. |
| `--stakeholder-rounds <n>` | `2` | Maximum stakeholder meeting rounds before stopping and summarizing standing blockers for the user. Implies `--stakeholder-meeting`. |
| `--personas "<a,b>"` | spec personas | Passed through to the stakeholder meeting — meet with only these personas instead of every persona named in the spec. Implies `--stakeholder-meeting`. |
| `--agent-sync` | off | Run steps 4-6's draft/reconcile loop as one long-lived producer+architect session pair over `agentsync-mcp` instead of a fresh subagent dispatch every round. See CONVENTIONS.md § Agent-sync mode. |
| `--resume-agents` | off | Resume the same producer/architect subagent for a follow-up round (steps 5-6, and step 7's blocker loop) instead of spawning a fresh one — avoids re-grounding on the whole Discussion each round. Composes with `--agent-sync`: applies to rounds after its dispatch returns. See CONVENTIONS.md § Resume mode. |

Example: `/project-manager:design "device firmware rollback" --stakeholder-meeting`

## Steps

0. **Check the scope of the request.** If it is a whole product, app, or subsystem that does not exist yet — rather than one feature added to something that does — stop and recommend `/project-manager:product` instead. A single design pass over a product yields 60-80 FRs, which is both context-hostile and unsafe to implement in one shot; the product skill cuts it into milestones that each come back here. Skip the *whole-product* recommendation when `--milestone` is given — that scope question is already settled — but the mid-flight check still applies to a milestone's own draft: if it's heading past roughly 20 FRs, say so and recommend `/project-manager:product` on the milestone's draft, noting that since a product maps 1:1 to a domain this only cleanly applies if the milestone genuinely spans a new domain-sized subsystem — otherwise recommend splitting it into an extra milestone of the existing roadmap instead (CONVENTIONS.md § When a milestone re-balloons). Either way it's a recommendation: let the user decide whether to split now or continue.

1. **Survey related issues.** Before intake, search GitHub for standing issues that already bear on this feature so the interview and draft don't duplicate or contradict them. Run `gh issue list --label domain:<domain> --state all --json number,title,state,labels,body --search "<keywords>"`, taking `<domain>` from the target domain and `<keywords>` from the request (with `--milestone`, draw them from the milestone's outcome sentence and its `Delivers` capability names in `<domain>/product/03-roadmap.md` instead). Pay particular attention to `idea` and `source:scope-note` labeled issues — free-floating feature ideas and deferred scope notes are exactly what this step exists to catch, since neither shows up on a Project board yet. Skim hits for genuine overlap, not keyword coincidence. A real match gets quoted by issue number in the intake discussion's opening post (step 3) so the interview addresses it directly, and later cited by number in the draft's requirement or **Out of scope** entry, whichever fits. This is read-only research — closing, relabeling, or otherwise triaging the found issues is still planner's job once a root plan exists (CONVENTIONS.md § Scope notes).

2. **Resolve the discussion.**
   - **Check `--milestone` first** — with it, the positional argument is a product issue number, not a discussion. `gh issue view <n>` and confirm the `product:approved` label; its body's first line points at `<domain>/PRODUCT.md`. Read that index from `main` — if it isn't there yet, the docs PR from `/project-manager:product`'s publish step hasn't merged; tell the user to merge it first and stop. Follow its jump table to `<domain>/product/03-roadmap.md` for the actual milestone entry; if it is absent, list the ones that exist and stop. `gh issue view <n> --comments` and take the **last** `Ledger: M<n> → <status> (<link>)` comment for this milestone (CONVENTIONS.md § Roadmap ledger) — already `in design` or later means that comment carries a discussion URL, so resume that discussion instead of opening a second one; no such comment means `not started`. Warn (don't block) if an earlier milestone is still `not started`; milestones are ordered so each builds on the last, and designing out of order is occasionally right but usually a mistake.
   - If given a discussion URL/number, inspect its latest comments. If architect has already signed off, skip to step 7 when a stakeholder meeting was requested and none has been held yet; otherwise stop and point the user to `/project-manager:review <discussion-url>`.
   - If given a description (or nothing — ask for one), proceed to intake.

3. **Intake (new plans only).** Open the intake discussion first:
   ```sh
   gh discussion create --title "Intake: <feature>" --category "Ideas" --body-file <tmpfile>
   ```
   Conduct the interview conversationally directly in this session — do not delegate to a subagent since it needs live back-and-forth. Follow `tools/project-manager/agents/producer.md` Mode 0: ask who is affected, gather *"As a <persona>, I want <capability>, so that <benefit>"* user stories, constraints, and out-of-scope boundaries. If step 1 turned up real overlap, open with those issue numbers so the requester can say whether each is superseded, folded in, or still separate. Post each Q&A round as a discussion comment.

   **With `--milestone`:** title the discussion `Intake: M<n> — <outcome sentence>` and open it with the milestone's roadmap entry quoted, so the scope contract is visible in the discussion itself. Then post `gh issue comment <product-issue> --body "Ledger: M<n> → in design (<discussion-url>)"` before interviewing — never edit the tracking issue's body. Interview only about this milestone's outcome — vision, personas, and non-goals are already settled in the brief, and re-opening them turns a milestone spec back into a product spec.

4. **Draft the specification.** In every case, dispatch with an explicit `name: "producer-<discussion-number>"` (and, where architect is dispatched too, `name: "architect-<discussion-number>"`) so a later round has a stable target to resume under `--resume-agents`.
   - **Default:** Dispatch the `project-manager:producer` subagent with the intake transcript and discussion URL, instructing it to run Mode 1: write the draft specification (user stories, FRs, NFRs, personas, out-of-scope) to the working-draft gist and post a short summary comment on the Discussion (CONVENTIONS.md § Working draft).
   - **With `--agent-sync`:** call `mcp__agentsync-mcp__start_session("design-<discussion-number>")`, then dispatch `project-manager:producer` **and** `project-manager:architect` together in one message (two parallel `Agent` calls) — producer with the intake transcript and discussion URL, architect with the discussion URL — each told the session id and to run in agent-sync mode (producer.md / architect.md § Agent-sync mode; CONVENTIONS.md § Agent-sync mode) for steps 4-6. Skip straight to step 7 once both return: they run the draft/reconcile loop themselves and only report back once architect has signed off or the round cap is hit.

   **With `--milestone`:** also pass the product issue number and milestone id, and instruct producer to follow § Drafting under a product brief — first line `Product: #<n> — Milestone M<k>: <outcome>`, every FR citing a capability from the milestone's `Delivers` list, and out-of-scope entries naming the milestone each deferral went to. In agent-sync mode, pass the same to both dispatches so architect knows to run its Load-bearing check.

5. **Reconcile in Discussion.** *(Default only — `--agent-sync` folds this into step 4's dispatch.)* Dispatch the `project-manager:architect` subagent with the discussion URL, instructing it to run its Process: reconcile against repo conventions (Bazel, cross-compilation, SCD2, shared libs, domain architectures), post open questions / nitpicks as discussion comments, or post `Architect sign-off: approved` if clean.

   **With `--milestone`:** architect picks up the product issue from the draft's first line and runs its **Load-bearing check** (architect.md § Process) — the pass that blocks a draft foreclosing a `Later` capability the milestone was supposed to protect. This is the step that makes small milestones safe rather than merely small, so do not skip architect on a milestone that looks trivially scoped.

6. **Loop in Discussion until architect sign-off.** *(Default only — `--agent-sync` runs this loop inside the two dispatches from step 4.)*
   - If architect raised open questions: dispatch `project-manager:producer` with the discussion URL to run Mode 2 (answer questions in a bounded comment, update draft in the working-draft gist), then dispatch `project-manager:architect` again.
   - Repeat until architect posts `Architect sign-off: approved`, or cap at 5 rounds and summarize for the user if stuck.
   - **With `--resume-agents`:** each of these redispatches targets the same `producer-<discussion-number>` / `architect-<discussion-number>` names from step 4-5 via `SendMessage` instead of a fresh `Agent` call — see CONVENTIONS.md § Resume mode.

7. **Stakeholder meeting (only with `--stakeholder-meeting`).** Once architect has signed off, invoke `/project-manager:stakeholder-meeting <discussion-url>` — passing `--personas` through if given — and follow that skill's steps: it dispatches one `project-manager:stakeholder` subagent per persona named in the spec, then posts consolidated minutes ending in `Stakeholder meeting: cleared` or `Stakeholder meeting: blocked (<k> blockers)`.
   - **Cleared** — continue to step 8.
   - **Blocked** — the plan is not ready for review. Dispatch `project-manager:producer` (Mode 2) with the consolidated blockers to answer each one and update the draft, dispatch `project-manager:architect` for a fresh reconciliation round, then hold the next meeting round. Cap at `--stakeholder-rounds` (default 2); if blockers still stand, stop and summarize them for the user instead of looping. **With `--resume-agents`:** target the same `producer-<discussion-number>` / `architect-<discussion-number>` names via `SendMessage` — this is still the same skill invocation that dispatched them, even if `--agent-sync` already returned control once (CONVENTIONS.md § Resume mode).
   - Non-blocking guidance and feedback never block the hand-off — surface it to the user with the minutes so they can decide what producer folds in.

   Without the flag, skip this step entirely. The meeting can still be held later against the approved root plan issue: `/project-manager:stakeholder-meeting <root-issue-number>`.

8. **Hand off.** Once architect signs off — and the stakeholder meeting is cleared, if one was requested — tell the user the draft is ready and that `/project-manager:review <discussion-url>` is the next step.

   **With `--milestone`:** also report the FR count against the milestone's budget, and anything producer moved into the brief's `Later` bucket during drafting. `review` publishes the root plan issue as usual; producer posts `Ledger: M<n> → planned (#<issue>)` on the tracking issue at the same time.
