# The DesignSession/RevisionEvent HTTP surface (FR1-FR4, FR8, issue #2543)

`api/handlers/design_session.go` and `api/handlers/revision_event.go` are
the HTTP surface over `store.DesignSessionStore`/`store.RevisionEventStore`
(#2542, above), wired in `routes.go`:

- `POST /design-sessions` (`OpenDesignSessionHandler`, FR1/FR8) — opens a
  session scoped to one Product, carrying a Requirement Contributor's
  plain-language `opening_submission`. **No entity reference of any kind
  is required or accepted in the body** — FR8's whole point. `scope_id`
  and `opened_by_krill_session_id` come from the gating `krill_session`
  only (provenance, per "`design_session` vs `krill_session`" above), never
  from the request.
- `GET /design-sessions/{id}` — the `design_session` row plus its ordered
  `revision_event` log (`seq_no` ascending), each event carrying both
  identity triples in full.
- `POST /design-sessions/{id}/revision-events` (`AppendRevisionEventHandler`,
  FR2/FR3/FR4) — appends one round.

Both `POST` routes are wrapped in `handlers.RequireSession` (gate.go),
exactly like every other write endpoint below; the `GET` route is
ungated, like every read path in this milestone (root plan issue #2485).

**`AppendRevisionEventHandler` never reads acting/on-behalf-of/scope_id
from the request body** — its request struct (`appendRevisionEventRequest`)
has no field for any of them at all, so there is nothing for a caller to
even attempt to override. All three come from `SessionFromContext`
(gate.go) and are copied verbatim onto the new `revision_event` row. This
is the invariant NFR2's later task depends on: a caller cannot assert an
identity the gate did not mint.

**Validation layering.** `event_type` outside FR2's five-value enum is
rejected by the handler itself (`parseEventType`) before the store is ever
called — `RevisionEventStore.Append`'s own validation only checks FR3/FR4's
conditional-presence rules and `entity_deltas[].change`, so an
unrecognized `event_type` would otherwise reach a raw Postgres CHECK
violation. FR3's `verified_against` rule, FR4's `signoff_status` rule
(including "no free-text signoff" — an unrecognized `signoff_status`
string is still passed through to the store and rejected there), and
`entity_deltas[].change` outside `created|updated` are all left to
`store.ErrInvalidRevisionEvent`, which `writeRevisionEventError` maps to
400 with the store's own named message. `writeRevisionEventError` also
maps `store.ErrNotFound` (an unknown `design_session` id, from
`errParentNotFound`) to 404 — a different mapping from `types.go`'s
`writeStoreError` (which maps `ErrNotFound` to 400, the right status for
`OpenDesignSessionHandler`'s unknown-`product_id` case, but wrong for a
path-parameter id that names the resource itself).

