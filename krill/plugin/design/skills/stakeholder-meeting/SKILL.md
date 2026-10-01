---
name: stakeholder-meeting
description: Runs a stakeholder meeting round on a krill design — dispatches one stakeholder persona per persona named in the specification, collects guidance, non-blocking feedback, and numbered blockers, and records the round on the DesignSession. Blockers route the design back through the producer/architect loop. Use after architect sign-off, or against a design that already received a signoff revision event.
---

# stakeholder-meeting

Convenes every persona named in a design's specification for one round of
feedback. krill has no meeting entity, so a round is recorded on the
DesignSession itself as open questions (`SM-`/`SB-`/`SF-` ids — see
`krill/plugin/shared/CONVENTIONS.md` § "Stakeholder meeting records"); no
issue, discussion, or other external store is involved.

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

2. **Determine the round number.** Count the `SM-<N>` question ids opened in
   `get_design_session`'s events; this meeting is round `N+1`.

3. **Enumerate the personas.** Take the personas the spec names
   (`list_personas {product_id}` for the design's product, plus any named
   in prior `draft` event notes), apply `--personas`/`--add-persona`, deduplicate. Stop if none are
   named.

4. **Collect feedback.** Dispatch one `krill-design:stakeholder` subagent
   **per persona, in parallel**. Each gets: the persona name, the
   design-session id (to read the spec and prior rounds' open questions
   from), and the round number. Each returns its Guidance / Feedback /
   Blockers sections as its dispatch response (live subagent output with no
   krill home yet — the one body it hands back); it writes nothing to krill.

5. **Record the round.** Deduplicate blockers across personas, then append
   one `reconciliation` event (`verified_against: "main@<sha>"` from
   `git rev-parse origin/main`, `entity_deltas: []`) whose
   `open_questions_delta` follows CONVENTIONS.md § "Stakeholder meeting
   records": the `SM-<N>` marker (opened and resolved in the same event,
   text = attendees plus `cleared` or `blocked (<k> blockers)`), one
   blocking `SB-<N>.<n>` per consolidated blocker, one non-blocking
   `SF-<N>.<n>` per guidance/feedback item. Present the same minutes to the
   user: a persona/blockers/summary table, the `SB-` blockers, grouped
   non-blocking guidance/feedback, and a terminal `Stakeholder meeting:
   blocked (<k> blockers)` / `Stakeholder meeting: cleared` line.

6. **Route the outcome.**
   - **Cleared** — report to the user. If invoked from `/krill-design:design`,
     control returns there for hand-off to `/krill-design:review`.
   - **Blocked, session not yet signed off** — dispatch `krill-design:producer`
     (Mode 2) with the design-session id and round number (it reads the
     `SB-`/`SF-` questions via `list_open_questions`, not from your prompt)
     to append an `answer` event resolving each `SB-<N>.<n>`, then dispatch
     `krill-design:architect` for a fresh `reconciliation`. Once clear,
     re-run this skill for round `N+1`. Cap at 3 meeting rounds; if blockers
     persist, stop and summarize for the user.
   - **Blocked, design already signed off** — the plan is already the entity
     set of record, so don't silently re-propose. Report the blockers and
     confirm before proceeding; on confirmation, run the same
     producer/architect loop (a follow-up `draft`/`answer`/`reconciliation`
     round on the same design session). The producer's `answer` event, with
     its `entity_deltas`, is the durable amendment record.

7. **Do not create tasks.** A blocker changes the design; it does not become
   a task.
