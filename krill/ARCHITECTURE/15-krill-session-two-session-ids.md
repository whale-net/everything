# `krill_session` and the two session ids (FR3, #2489)

Migration `003` adds `krill_session`, the row FR3's `init` primitive
writes and the write gate (`api/handlers/gate.go`, this task) reads. Two
identifiers matter here and must never be confused:

- **`krill_session.id`** — krill's own session identifier, a surrogate
  UUID minted by Postgres (`DEFAULT gen_random_uuid()`) every time `init`
  is called. This is the id the write gate requires on every mutating
  call this milestone exposes.
- **`libs/go/whagent`'s `Claim.WhagentSessionID`** — a *different*
  session concept, scoped to a whagent-net agent run, recorded on
  `krill_session.whagent_session_id` purely as a correlation field. It is
  nullable (a human/OAuth2 caller has none), it is never used as or in
  place of `krill_session.id`, and a whagent-authenticated call still
  gets its own, distinct krill session id. **In M1, `init` takes this
  value as-is from the request body** — `api` mounts no whagent-verifier
  middleware to extract it from a verified `Claim` (see "`init` and the
  write gate" below for why) — so it is only as trustworthy as every
  other field `init` accepts in this milestone.

`krill_session` also carries `acting_*`/`on_behalf_of_*` — two
`(iss, sub, kind)` triples (LB4, mirroring `whagent_net`'s LB2 and
`libs/go/whagent`'s `Claim` shape verbatim), both `NOT NULL`. When a
caller acts for itself the two triples are written identically; the
store layer (`krill/store/session.go`) never infers this — every caller
of `InitSession` passes both explicitly. The table is append-only, not
SCD2 (LB3): M1 ships only `init`, no update path over a session row.

