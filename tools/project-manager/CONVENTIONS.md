# project-manager — GitHub workflow conventions

Shared conventions all `project-manager` personas follow. Everything lives in GitHub — no external tracker. Personas interact via `gh` CLI (Bash): Issues for the root plan, a **Discussion** for intake, and a **Project (v2)** board for task tracking. `OWNER` below is this repo's org, `whale-net` (repo `whale-net/everything`).

**This is the canonical reference, not required reading for every dispatch.** Each persona's own agent file (`tools/project-manager/agents/*.md`) inlines the mechanics it needs for routine execution, so a subagent shouldn't normally need to open this doc. It exists for: (a) mechanics too infrequently exercised or too easy to typo to duplicate safely (e.g. the Project field GraphQL mutation), and (b) as the fallback when a persona hits a situation its own file doesn't cover. The inlined copies can drift from this doc over time — that's an accepted tradeoff for keeping routine dispatches cheap and unambiguous, not an invitation to let them diverge carelessly; when you touch a mechanic here, check whether the same mechanic is duplicated in an agent file and update both.

## Where things live

| What | Lives in | Created by |
|---|---|---|
| Intake interview | GitHub Discussion, category `Ideas` | producer |
| Root plan (FR/NFR, personas, lifecycle) | GitHub Issue, `plan:*` labels | producer |
| Task tracking (phase, claim, done) | GitHub Project (v2), one per approved plan | planner |
| Task issues (scaffold/implementation/testing/validation/findings) | GitHub Issue, added as a Project item | planner / system-validator |
| Scope notes (deferred/cross-cutting decisions) | GitHub Issue, added as a Project item at `Status: Noted` | any persona; triaged by planner |

Labels (`plan:*`) still gate the root-plan review lifecycle — that's a human approval gate, not a phase pipeline, and stays as-is. Everything downstream of `plan:approved` (task phase, claim state, completion) is tracked on the Project board instead of `phase:*`/`status:*` labels.

## Intake discussion

Before a root plan issue exists, producer's Mode 0 interview happens as a GitHub Discussion, category `Ideas`, so the requirements-gathering conversation has a durable, linkable record independent of chat history.

1. On the first question, producer opens the discussion: `gh discussion create --title "Intake: <feature>" --category "Ideas" --body-file <tmpfile>` (opening body: the one-line request as given, plus the first round of questions).
2. As the interview proceeds turn by turn, producer posts each round (question and, once given, the answer) as a comment: `gh discussion comment <discussion-url> --body-file <tmpfile>`. This mirrors the live conversation — it does not replace conducting the interview directly with the requester.
3. When producer writes the root plan issue (Mode 1), the issue body's first line is `Intake discussion: <discussion-url>`, and producer posts a closing comment on the discussion linking back: `gh discussion comment <discussion-url> --body "Root plan: <issue-url>"`.

Follow-up rounds after the root issue exists (architect's open questions, human review feedback) stay as Issue comments per the root plan lifecycle below — only the initial intake interview lives in Discussions.

## Root plan lifecycle

```mermaid
stateDiagram-v2
    draft: plan:draft
    needsAnswers: plan:needs-answers
    architectApproved: plan:architect-approved
    approved: plan:approved
    breakdown: task breakdown (planner)

    [*] --> draft: producer opens the root issue
    draft --> needsAnswers: architect finds open questions
    draft --> architectApproved: architect approves on first pass (no questions)
    needsAnswers --> architectApproved: architect's open questions are all answered
    architectApproved --> approved: human sets plan:approved
    architectApproved --> needsAnswers: human requests changes\n(producer/architect re-loop)
    approved --> breakdown: planner starts
    breakdown --> [*]
```

While an issue sits at `plan:needs-answers`, producer and architect iterate purely via comments (`gh issue comment`) — the label does not change on every round, only when architect either raises a new open question (stays `plan:needs-answers`) or runs out of them (moves to `plan:architect-approved`).

| Transition | Trigger | Actor | Precondition | Label command |
|---|---|---|---|---|
| `[*] → plan:draft` | New feature request (after intake discussion) | producer | none | `gh issue create --title "Plan: <feature>" --label "plan:draft" --body-file <tmpfile>` |
| `plan:draft → plan:needs-answers` | Reconciliation finds ≥1 open question | architect | none | `gh issue edit <n> --add-label "plan:needs-answers" --remove-label "plan:draft"` |
| `plan:draft → plan:architect-approved` | Reconciliation finds 0 open questions (first pass) | architect | none | `gh issue edit <n> --add-label "plan:architect-approved" --remove-label "plan:draft"` |
| `plan:needs-answers → plan:architect-approved` | All of architect's numbered questions have been answered | architect | producer has replied to every open question | `gh issue edit <n> --add-label "plan:architect-approved" --remove-label "plan:needs-answers"` |
| `plan:architect-approved → plan:approved` | Human reviews and accepts | **human only** | plan reconciled, no open questions | `gh issue edit <n> --add-label "plan:approved" --remove-label "plan:architect-approved"` |
| `plan:architect-approved → plan:needs-answers` | Human requests changes | **human only** | human leaves feedback as a comment | `gh issue edit <n> --add-label "plan:needs-answers" --remove-label "plan:architect-approved"`, then producer/architect re-loop |
| `plan:approved → (task breakdown)` | Planner invoked | planner | `plan:approved` is set (not just `plan:architect-approved`) | planner begins creating the Project and task issues |

No persona ever adds or removes `plan:approved` — that label is set by a human only. Planner and every other persona must treat `plan:architect-approved` as *not yet* actionable.

## Project setup

Once the root issue is `plan:approved`, planner creates the plan's Project before creating any task issues:

1. **Idempotency check first.** `gh issue view <n> --comments` — if a prior comment contains `Project board: <url>`, the project already exists; extract its number from the URL and skip to step 5.
2. Create it: `gh project create --owner whale-net --title "Plan: <feature title> (#<n>)" --format json` — capture `.number`.
3. Link it to the repo so issues from this repo can be added: `gh project link <number> --owner whale-net --repo whale-net/everything`.
4. Every new project ships a built-in `Status` field (`Todo`/`In Progress`/`Done`) that cannot be deleted or duplicated by name — repurpose its options to the plan's phases via GraphQL (there is no `gh project field-*` flag for editing existing options):

   ```sh
   FIELD_ID=$(gh project field-list <number> --owner whale-net --format json \
     | jq -r '.fields[] | select(.name=="Status") | .id')
   gh api graphql -f query='
     mutation($fieldId: ID!) {
       updateProjectV2Field(input: {
         fieldId: $fieldId,
         singleSelectOptions: [
           {name: "Scaffold", color: GRAY, description: ""},
           {name: "Implementation", color: BLUE, description: ""},
           {name: "Testing", color: YELLOW, description: ""},
           {name: "Validation", color: ORANGE, description: ""},
           {name: "Done", color: GREEN, description: ""},
           {name: "Noted", color: PURPLE, description: ""},
           {name: "Carry-over", color: PINK, description: ""},
           {name: "Deferred", color: RED, description: ""}
         ]
       }) { projectV2Field { ... on ProjectV2SingleSelectField { id name options { id name } } } }
     }' -f fieldId="$FIELD_ID"
   ```

5. Post `gh issue comment <n> --body "Project board: <project-url>"` on the root issue (skip if already posted, per step 1).

Phases run in order: `Scaffold` → `Implementation` → `Testing` → `Validation` → `Done`. `Done` is the only value that means closed/finished — the other phase values just say what kind of task an item is, not how far along it is. `Noted`/`Carry-over`/`Deferred` are a second, parallel lifecycle riding the same field — see § Scope notes — never treated as a phase a task issue passes through.

## Task issues

Planner creates one Issue per unit of work (unchanged in shape) and adds it to the Project:

```sh
gh issue create --title "<phase>: <unit>" --body-file <tmpfile>
gh project item-add <number> --owner whale-net --url <issue-url>
gh project item-edit <number> --owner whale-net --url <issue-url> --field Status --value "<Phase>"
```

No `phase:*`/`status:*` labels — the item's `Status` value on the Project *is* the phase. Each issue body must still contain:

- `Part of #<root-issue-number>`
- `Depends on: #<n>, #<n>` (omit if none)
- Acceptance criteria

Create issues in dependency order (or two-pass: create all, then wire `Depends on:` bodies via `gh issue edit`). There is no separate "ready"/"blocked" value to set — readiness is always computed on demand from `Depends on:` (see below), never persisted.

## Worker lifecycle (worker / validator)

This section is the single canonical definition of the worker lifecycle — worker.md/validator.md link here rather than restating it, since they differ only in which `Status` phase they query and whether they touch files.

```mermaid
stateDiagram-v2
    phase: Status = <their phase>, unassigned
    claimed: Status = <their phase>, assigned to worker
    done: Status = Done, issue closed

    [*] --> phase: planner adds the item at its phase
    phase --> claimed: a worker claims it (all "Depends on:" issues closed)
    claimed --> done: worker finishes successfully
    claimed --> claimed: tester finds the failure is in the dependency, not the test\n(stays claimed, not closed, not reverted)
```

**Find work**, scoped to one plan's project:

```sh
gh project item-list <number> --owner whale-net --query "status:<Phase> no:assignee" --format json \
  | jq -r '.items[] | select(.content.body | test("Part of #<root>([^0-9]|$)")) | .content.number'
```

For each candidate, confirm readiness precisely — every issue in its `Depends on:` line must be closed (never trust `--search` on a literal `#<n>`; GitHub tokenizes it and can match numeric substrings like `#1` inside `#10`):

```sh
gh issue view <n> --json body,state
# extract the "Depends on: #a, #b" line, then for each:
gh issue view <dep> --json state   # must be "CLOSED" for every one
```

**Claim it:**

```sh
gh issue edit <n> --add-assignee @me
```

**Finish it:**

```sh
gh issue close <n> --comment "<summary>"
gh project item-edit <number> --owner whale-net --url <issue-url> --field Status --value "Done"
```

Unblocking dependents needs no action — the next worker's readiness check re-reads issue state live, so a dependent becomes pickable the instant its dependency issue closes. There is nothing to flip.

**Tester finds a bug in the dependency, not the test:** comment on both issues; do **not** close, do not touch `Status`, do not unassign.

**Verification discipline.** A Testing task issue is not done because a test exists and passes — it's done because the test was *seen* to catch the thing it guards. Before closing: deliberately break the behavior under test, confirm the test fails with the expected message, then revert. Note this in the close comment (e.g. "verified by breaking X, confirmed red, reverted"). A test nobody watched fail is not verified, regardless of whether it's green now.

## Git hygiene

All code-touching work for a plan happens on one shared branch, not the primary checkout's default branch, and not one branch per task — task issues are too fine-grained for that and the whole point is an incremental, reviewable trail on a single branch.

1. **Branch creation.** Before dispatching any worker, `/project-manager:implement` ensures the plan's branch exists and is checked out in the working tree it will dispatch workers from:
   ```sh
   git fetch origin main
   git checkout plan/<root-issue-number>-<short-slug> 2>/dev/null || git checkout -b plan/<root-issue-number>-<short-slug> origin/main
   ```
   Resuming a plan (implement invoked again later) must reuse the same branch name, not create a second one — derive `<short-slug>` from the root issue title deterministically (lowercase, hyphenated, first few words) so re-derivation is stable.
2. **Per-task commits.** Worker commits its changes as the last step before closing its issue — every Scaffold/Implementation/Testing task leaves the branch in a state that reflects everything `Done` so far:
   ```sh
   git add -A
   git commit -m "<phase>: <short summary>

   Part of #<root-issue-number>"
   ```
   Commit, never push — the branch stays local to the session/worktree doing the work until a PR is opened in step 4. Validator never commits (it doesn't edit files).
   If there is nothing to commit (e.g. a Testing task that only ran existing tests), skip the commit rather than creating an empty one.
3. **Safety net, not a substitute for review.** These commits exist so multi-hour agent work is never sitting only as uncommitted tree state — they are not a replacement for the PR review the human actually reads.
4. **PR creation.** Once `/project-manager:validate` finds every FR/NFR passing (§ System validation), it pushes the plan branch and opens a **draft PR** against `main`:
   ```sh
   git push -u origin plan/<root-issue-number>-<short-slug>
   gh pr create --draft --title "<root plan title> (#<root-issue-number>)" \
     --body "Closes #<root-issue-number>" --head plan/<root-issue-number>-<short-slug>
   ```
   Post `gh issue comment <root-issue-number> --body "PR: <pr-url>"` on the root issue. If validation finds blocking findings instead, no PR is opened yet — the branch keeps accumulating follow-up commits until a later `/project-manager:validate` pass is clean.

## System validation

After all `Implementation` and `Testing` items under a root plan's Project are `Done`, **system-validator** runs the system end-to-end (Tilt) and grades it against the root plan's acceptance criteria — confirm readiness the same precise way as above (`gh project item-list ... --query "status:Implementation"` / `"status:Testing"` scoped to the plan must both come back empty before findings are trusted). Findings become new issues added to the same Project at `Status: Validation`, titled descriptively, containing `Part of #<root-issue-number>` and `from:system-validator` in the body (no labels needed — the Project item's presence + phase is the tracking). If a finding represents new work rather than a pass/fail note, **planner** converts it into properly sequenced task issues on the same Project — linked back to the finding for traceability, but never `Depends on:` the finding issue itself (a finding is a report, not a work product any worker closes). Planner then closes the finding issue and sets its item to `Status: Done` once its follow-ups are filed and linked — the one case where planner closes an issue itself.

## Scope notes

Work gets consciously *not* done constantly: a worker defers something outside its issue's stated scope, a validator spots a cross-cutting gap, architect flags something bigger than this plan. Left as a stray issue comment, that decision is invisible to everyone who didn't read that exact thread — a deferred-work list nobody can query is exactly what let a prior generation of hand-maintained plan documents in this repo balloon past what an agent can read in one sitting before anyone noticed. A scope note is the fix: a lifecycle, not a comment, so a deferred decision stays queryable and is never silently dropped.

```mermaid
stateDiagram-v2
    noted: Status = Noted
    carryover: Status = Carry-over
    deferred: Status = Deferred
    rejected: closed (rejected)
    done: Status = Done, issue closed

    [*] --> noted: any persona notices scope not covered\nby an existing issue on the plan
    noted --> carryover: planner triages — cross-cutting,\nnot specific to this plan
    noted --> deferred: planner triages — this plan's own\nconscious scope cut
    noted --> rejected: planner triages — not worth tracking
    carryover --> done: planner (this plan or a later one) files\nreal task issue(s) and closes the note
    deferred --> done: planner (if this plan is revisited) files\nreal task issue(s) and closes the note
```

**Filing.** Any persona that notices scope outside its current issue — and not already covered by another issue on the plan — files a note instead of just leaving a comment: `gh issue create --title "Scope note: <short desc>" --body-file <tmpfile>`, body containing `Part of #<root-issue-number>`, `from:<persona>`, and why it's out of scope right now. Add it to the plan's Project at `Status: Noted` (`gh project item-add` / `gh project item-edit` per § Project setup).

**Triage.** Planner reads every `Status: Noted` item on a plan's Project whenever it revisits that plan — not a one-time pass — and sets each to exactly one of:

- **Carry-over** — cross-cutting, not specific to this plan; likely relevant to future plans too.
- **Deferred** — a conscious scope cut for *this* plan; revisit only if this plan itself is revisited.
- Closed immediately, with a one-line reason comment, if it isn't worth tracking at all.

**Scheduling.** When planner (now, or on a later pass) decides to act on a `Carry-over`/`Deferred` note, it opens real task issue(s) following the normal task-issue rules on the same Project, linked back to the note (`Part of #<root-issue-number>`, referencing the note's issue number), and closes the note with `Status: Done` — the same close mechanic already used for system-validator findings above.

This is deliberately a *lifecycle*, not a static label: a note can't sit unclassified forever (it moves to `Carry-over`/`Deferred`/rejected the next time anyone looks at the plan), and it can't be forgotten once classified (`Carry-over`/`Deferred` are `Status` values on a Project item, so `gh project item-list ... --query "status:Carry-over"` finds every one ever filed, across every plan). If a note doesn't fit these three outcomes, that's a signal to adjust the lifecycle itself — add a state, don't force the note into the nearest existing one.

## Model tiers

| Persona | Model | Why |
|---|---|---|
| producer, architect, planner | `opus` | Premium reasoning for requirements, design reconciliation, and dependency-aware sequencing |
| worker, validator | `haiku` | Cheap, small-context execution of a single well-scoped task issue |
| system-validator | `opus` (effort: max) | Expensive, holistic judgment call on whether the whole system actually works |
