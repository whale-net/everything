# stakeholder-meeting

*Runs a stakeholder meeting round on a krill design — dispatches one stakeholder persona per persona named in the specification, collects guidance, non-blocking feedback, and numbered blockers, and posts consolidated minutes. Blockers route the design back through the producer/architect loop. Use after architect sign-off, or against a design that already received a signoff revision event.*


Convenes every persona named in a design's specification for one round of
feedback. The meeting mechanic (a dedicated GitHub Discussion per round,
consolidated minutes) stays on GitHub — krill has no meeting entity. Where
the round's link comment lands has two paths depending on whether the
design has a GitHub anchor at all (step 4). See
`krill/plugin-cline/shared/CONVENTIONS.md` § "record_note fallback for
anchor-less designs".

Callable directly, or automatically by the `design` workflow
--stakeholder-meeting`.

## Usage

```
the `stakeholder-meeting` workflow <design-session-id>
the `stakeholder-meeting` workflow <target> --personas "Operator,Release engineer"
the `stakeholder-meeting` workflow <target> --add-persona "On-call SRE"
```

## Steps

1. **Resolve the target and read the design.** Call `get_design_session
   {id}` and `list_open_questions {id, blocking: true}`. If the last
   `reconciliation` event still has open blocking questions, say so and ask
   the user whether to hold the meeting anyway — a meeting on an
   unreconciled draft usually just re-raises what architect is about to ask.
   Read the spec from `get_design_session_slice {id}` — authoritative.

2. **Determine the round number.** Count existing `Stakeholder meeting round
   <N>: <url>` link comments for this design — on the product tracking
   issue if it has one, or among the anchoring FeatureSet's `record_note`
   entries (`kind: "comment"`) if it doesn't (see step 4); this meeting is
   round `N+1`.

3. **Enumerate the personas.** Take the intake's named personas (from your
   own conversation history / prior `draft` event notes — krill has no
   dedicated Personas entity), apply `--personas`/`--add-persona`,
   deduplicate. Stop if none are named.

4. **Open the meeting.** `gh discussion create --title "Stakeholder meeting
   round <N>: <feature>" ...` with the agenda (personas attending, the
   entity slice under review, the three response sections). Capture the
   discussion URL, then record the `Stakeholder meeting round <N>:
   <meeting-discussion-url>` link comment so it's durably discoverable from
   the design — where it lands depends on whether this design has a GitHub
   anchor:
   - **Has an anchor** (the design was announced via a `Ledger:` comment on
     a product tracking issue — i.e. not a krill-hosted product/milestone)
     — post the link comment there, same as project-manager's.
   - **No anchor** (a krill-hosted product/milestone, tracked purely via
     `set_milestone_status`/`get_milestone_status` — there is no tracking
     issue to comment on) — call `record_note {entity_kind: "feature_set",
     entity_id: <the design's anchoring FeatureSet>, kind: "comment", body:
     "Stakeholder meeting round <N>: <meeting-discussion-url>"}` instead.
     Resolve the anchoring FeatureSet from `get_design_session_slice`'s
     FeatureSet entries; if the session never touched the FeatureSet itself
     (only Features/Requirements under a pre-existing one), resolve it via
     that Feature's/Requirement's `feature_set_id`. This is the
     standardized fallback (CONVENTIONS.md) — always the same fixed body
     string and the same entity, not something improvised per run.
     `record_note` works normally from this dispatch — make the call, and
     on failure report the exact error plus the link text to the user so
     it isn't lost. Do not silently fall back to opening a GitHub
     Discussion/issue to route around it.



5. **Collect feedback.** Dispatch one the `krill-stakeholder` custom mode subagent
   **per persona, in parallel**. Each gets: the persona name, the
   design-session id (to read the spec from via `get_design_session_slice`),
   the meeting discussion URL, and the round number. Each posts its own
   `Stakeholder feedback — <persona> (round <N>)` comment on the meeting
   discussion.

6. **Post minutes.** A persona/blockers/
   summary table, consolidated deduplicated `SB-<N>.<n>` blockers, grouped
   non-blocking guidance/feedback, and a terminal `Stakeholder meeting:
   blocked (<k> blockers)` / `Stakeholder meeting: cleared` line.

7. **Route the outcome.**
   - **Cleared** — report to the user. If invoked from the `design` workflow,
     control returns there for hand-off to the `review` workflow.
   - **Blocked, session not yet signed off** — spawn a subagent (Cline: new_task) with the `krill-producer` custom mode as its mode
     (Mode 2) with the design-session id and meeting discussion URL (it reads
     the minutes there, not from your prompt) to append an `answer` event
     resolving each `SB-<N>.<n>`, then
     spawn a subagent (Cline: new_task) with the `krill-architect` custom mode as its mode for a fresh `reconciliation`. Once
     clear, re-run this workflow for round `N+1`. Cap at 3 meeting rounds; if
     blockers persist, stop and summarize for the user.
   - **Blocked, design already signed off** — the plan is already the entity
     set of record, so don't silently re-propose. Report the blockers and
     confirm before proceeding; on confirmation, run the same
     producer/architect loop (a follow-up `draft`/`answer`/`reconciliation`
     round on the same design session), then have producer note
     `Amended after stakeholder meeting round <N>: <summary>` as a comment on
     wherever the design's ledger lives (the product tracking issue, for a
     milestone with one) — or, when there is none, via the same
     `record_note {entity_kind: "feature_set", entity_id: <anchor>, kind:
     "comment"}` fallback as step 4.

8. **Do not create task issues or a Project board.** A blocker changes the
   design; it does not become a task.

## Task lifecycle blocker (from shared/snippets)

The `PersonaAgent`-only blocker that once made `claim_task`, `heartbeat_task`, `complete_task`, `abandon_task` and `record_note` fail `forbidden` from an ordinary Cline session is **fixed** (#2930, #2933) — every one of those tools' allow-lists now names `PersonaSwarmOperator` alongside `PersonaAgent`, so a krill-work subagent resolves a persona the gate accepts. Call them normally.

**If one of these five ever does return `forbidden`, that is a real regression, not a known condition.** Report the exact error and the tool name, and do not fall back to `gh issue`/`gh project` to route around it. Tracking: whale-net/everything#3027 (this snippet asserted the blocker was still live).
