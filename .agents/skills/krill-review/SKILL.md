---
name: krill-review
description: The human review gate for a krill design — reviews an architect-approved draft in a krill DesignSession, then either approves it (appending a signoff revision event with signoff_status approved, which makes the proposed Feature/Requirement entities the approved plan) or routes feedback back through producer/architect. For an unattended run with no human reviewer, see /krill-loop-design-panel instead — it appends the same signoff event via its reviewer persona.
---

# krill-review

Drives the human review gate for a planned feature. Reviews the
architect-approved draft in a krill `DesignSession`, and upon approval appends
the `signoff` revision event that makes the design's proposed entities the
approved plan — there is no separate plan artifact to create (see
`references/CONVENTIONS.md`).

## Usage

```
/krill-review <design-session-id>
```

## Steps

1. Call `get_design_session {id}` and `list_open_questions {id, blocking:
   true}`. Confirm the last `reconciliation` event left zero blocking open
   questions (architect's sign-off signal). If not, report that the draft is
   not yet architect-approved and point the user to `/krill-design <id>`.

2. Summarize for the user, via `get_design_session_slice {id}`:
   - The current Feature/Requirement entities (recap user stories from context).
   - Key points from architect's `reconciliation` events.

3. Ask the user how to proceed (using `ask_question` or direct prompt):
   - **Approve** — append the `signoff` event and release to implementation.
   - **Request changes** — provide feedback for producer/architect to address.

4. **If approved:** load `references/agents/producer.md` via `get_skill_resources`
   and dispatch `krill-producer` (Mode 3) with the design-session id, or append
   the event directly:
   ```
   append_revision_event {
     krill_session_id, design_session_id,
     event_type: "signoff", signoff_status: "approved",
     entity_deltas: []
   }
   ```
   - **If this design is scoped to a milestone** — call
     `set_milestone_status {milestone_id, status: "designed"}` then
     `{status: "planned", note: "design session <design-session-id> signed off"}`.
   - Tell the user the design is approved and that `/krill-plan
     <feature-set-id> --milestone-id <id>` is next. `plan` requires a
     Milestone; if this design wasn't scoped to one, cut it first
     (`create_milestone`/`add_delivers`, `CONVENTIONS.md` "Milestone required").
     If no stakeholder meeting was held, mention `/krill-stakeholder-meeting
     <design-session-id>` is still available before implementation starts.

5. **If changes requested:**
   - Ask the user for feedback text.
   - Load `references/CONVENTIONS.md`, `references/agents/producer.md`, and
     `references/agents/architect.md` via `get_skill_resources`, define
     `krill-producer` (`enable_write_tools: true, enable_mcp_tools: true`) and
     `krill-architect` (`enable_write_tools: false, enable_mcp_tools: true`).
   - Dispatch `krill-producer` (Mode 2) with the design-session id and the
     user's feedback text to append an `answer` event addressing it (or a
     follow-up `propose_entities` call) and update the draft.
   - Dispatch `krill-architect` for a follow-up `reconciliation`.
   - Once architect's `reconciliation` clears (zero blocking open questions),
     return to step 2 to present the updated state to the user.
