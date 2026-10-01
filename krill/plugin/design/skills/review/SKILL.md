---
name: review
description: The human review gate for a krill-design design — reviews an architect-approved draft in a krill DesignSession, then either approves it (appending a signoff revision event with signoff_status approved, which makes the proposed Feature/Requirement entities the approved plan) or routes feedback back through producer/architect. For an unattended run with no human reviewer, see /krill-design:loop-design-panel instead — it appends the same signoff event via its reviewer persona.
---

# review

Drives the human review gate for a planned feature. Reviews the
architect-approved draft in a krill DesignSession, and upon approval appends
the `signoff` revision event that makes the design's proposed entities the
approved plan — there is no separate plan artifact to create (see
`krill/plugin/shared/CONVENTIONS.md`).

## Usage

```
/krill-design:review <design-session-id>
```

## Steps

1. Call `get_design_session {id}` and `list_open_questions {id, blocking:
   true}`. Confirm the last `reconciliation` event left zero blocking open
   questions (architect's sign-off signal — see `agents/architect.md`). If
   not, report that the draft is not yet architect-approved and point the
   user to `/krill-design:design <id>`.

2. Summarize for the user, via `get_design_session_slice {id}`:
   - The current Feature/Requirement entities (user stories are in your
     conversation history from intake, not a stored entity — recap from
     context).
   - Key points from architect's `reconciliation` events.

3. Ask the user how to proceed:
   - **Approve** — append the `signoff` event and release to implementation.
   - **Request changes** — provide feedback for producer/architect to
     address.

4. **If approved:** dispatch `krill-design:producer` with the design-session
   id to run Mode 3, or append the event directly:
   ```
   append_revision_event {
     krill_session_id, design_session_id,
     event_type: "signoff", signoff_status: "approved",
     entity_deltas: []
   }
   ```
   - **If this design is scoped to a milestone** — call
     `set_milestone_status {milestone_id, status: "designed"}` then
     `{status: "planned", note: "design session <design-session-id>
     signed off"}` (the edge table requires the `designed` rung between
     `in design` and `planned` — CONVENTIONS.md).
   - Tell the user the design is approved and that `/krill-work:plan
     <feature-set-id> --milestone-id <id>` is next — `plan` creates real
     krill `Task` entities via `create_task`/`declare_task_dependencies` and
     returns a task manifest (see `krill-work/CONVENTIONS.md` "Work axis").
     `plan` requires a Milestone; if this design wasn't scoped to one, cut
     it first (`create_milestone`/`add_delivers`, CONVENTIONS.md "Milestone
     required"). If no stakeholder meeting was held, mention
     `/krill-design:stakeholder-meeting <design-session-id>` is still
     available before implementation starts.

5. **If changes requested:**
   - Ask the user for feedback text.
   - Dispatch `krill-design:producer` (Mode 2) with the design-session id
     and the user's feedback text (live human input with no krill home yet —
     the one body it gets) to append an `answer` event
     addressing it (or, if it requires new/changed entities, a follow-up
     `propose_entities` call) and update the draft.
   - Dispatch `krill-design:architect` for a follow-up `reconciliation`.
   - Once architect's `reconciliation` clears (zero blocking open
     questions), return to step 2 to present the updated state to the user.
