---
name: stakeholder-meeting
description: Runs a stakeholder meeting round on a krill design — dispatches one stakeholder persona per persona named in the specification, collects guidance, non-blocking feedback, and numbered blockers, and posts consolidated minutes. Blockers route the design back through the producer/architect loop. Use after architect sign-off, or against a design that already received a signoff revision event.
---

# stakeholder-meeting

Convenes every persona named in a design's specification for one round of
feedback. The meeting mechanic (a dedicated GitHub Discussion per round, one
link comment on the target, consolidated minutes) stays on GitHub — krill
has no meeting entity. See `krill/plugin/shared/CONVENTIONS.md`.

Callable directly, or automatically by `/krill-design:design
--stakeholder-meeting`.

## Usage

```
/krill-design:stakeholder-meeting <design-session-id>
/krill-design:stakeholder-meeting <target> --personas "Operator,Release engineer"
/krill-design:stakeholder-meeting <target> --add-persona "On-call SRE"
```

## Steps

1. **Resolve the target and read the design.** Call `get_design_session
   {id}` and `list_open_questions {id, blocking: true}`. If the last
   `reconciliation` event still has open blocking questions, say so and ask
   the user whether to hold the meeting anyway — a meeting on an
   unreconciled draft usually just re-raises what architect is about to ask.
   Read the spec from `get_design_session_slice {id}` — authoritative.

2. **Determine the round number.** Count existing `Stakeholder meeting round
   <N>: <url>` link comments on the design (posted per step 4); this
   meeting is round `N+1`.

3. **Enumerate the personas.** Take the intake's named personas (from your
   own conversation history / prior `draft` event notes — krill has no
   dedicated Personas entity), apply `--personas`/`--add-persona`,
   deduplicate. Stop if none are named.

4. **Open the meeting.** `gh discussion create --title "Stakeholder meeting
   round <N>: <feature>" ...` with the agenda (personas attending, the
   entity slice under review, the three response sections), then post the
   `Stakeholder meeting round <N>: <meeting-discussion-url>` link comment —
   but on the design (no dedicated home for it besides the same GitHub
   Discussion trail linked from wherever the design was announced, e.g. the
   product tracking issue's `Ledger:` comment for a milestone).

5. **Collect feedback.** Dispatch one `krill-design:stakeholder` subagent
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
   - **Cleared** — report to the user. If invoked from `/krill-design:design`,
     control returns there for hand-off to `/krill-design:review`.
   - **Blocked, session not yet signed off** — dispatch `krill-design:producer`
     (Mode 2) to append an `answer` event resolving each `SB-<N>.<n>`, then
     dispatch `krill-design:architect` for a fresh `reconciliation`. Once
     clear, re-run this skill for round `N+1`. Cap at 3 meeting rounds; if
     blockers persist, stop and summarize for the user.
   - **Blocked, design already signed off** — the plan is already the entity
     set of record, so don't silently re-propose. Report the blockers and
     confirm before proceeding; on confirmation, run the same
     producer/architect loop (a follow-up `draft`/`answer`/`reconciliation`
     round on the same design session), then have producer note
     `Amended after stakeholder meeting round <N>: <summary>` as a comment on
     wherever the design's ledger lives (the product tracking issue, for a
     milestone).

8. **Do not create task issues or a Project board.** A blocker changes the
   design; it does not become a task.
