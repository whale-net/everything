# krill — Product brief

Product discussion: https://github.com/whale-net/everything/discussions/2463
Origin issue: https://github.com/whale-net/everything/issues/2423

This file is the index. Vision, Personas, Load-bearing decisions, and Non-goals are inline; the three sections with no natural ceiling are split out (`tools/project-manager/CONVENTIONS.md` § Layout):

| Section | File | Read it when |
|---|---|---|
| Current state | [`product/01-current-state.md`](product/01-current-state.md) | Checking what already exists, what is in the way, and what is genuinely missing before scoping a milestone |
| Capability map | [`product/02-capability-map.md`](product/02-capability-map.md) | Citing a `Cn` from an FR, or deciding whether a request is a new capability |
| Roadmap | [`product/03-roadmap.md`](product/03-roadmap.md) | Running `/project-manager:design <tracking-issue> --milestone M<n>`; the milestone entry is the scope contract |

Live milestone status is not in this file or its splits — it is the last `Ledger: M<n> → <status>` comment on the tracking issue (`Product: krill`, label `product:approved`).

## Vision

Krill is the spec-of-record and work-tracking substrate for agent swarms — **unifying all the personas the way Jira did for humans, except for agents.** Today a product's requirements are flat markdown read cold in one pass and its work lives in GitHub Issues, Discussions, and Projects; nearly every convention the `project-manager` pipeline carries (zero FRs in a brief, doc-splitting thresholds, drafts hidden in gists, "the last ledger comment wins") is a workaround for that shape, and GitHub's rate limits cap how many agents can run against it at once. Krill replaces *read the document* with *query the slice you need*: requirements, decisions, and work become typed entities with history, so an agent receives exactly the context its task requires and nothing else, and the markdown everyone reads today becomes a generated projection rather than the source. A year out it is three things at once — a console a Swarm Operator checks, a platform requirements get shaped on, and the surface agents actually perform work through.

## Personas

- **Swarm Operator** — the admin who wants to see everything and occasionally needs to bang a wrench on it: what is claimed, what is stuck, what dead-lettered, and what to do about it.
- **Requirement Contributor** — someone with no interest in operating the swarm who wants to add to the natural-language requirements. The mental model is the uncle with an idea for the app who cannot write code; the entity model must never be his problem.
- **Agent** — the generic worker persona: needs to know what its task is, what its success condition is, and exactly the right amount of information — no more.

> *Swarm Host* was considered and deliberately folded into **Swarm Operator**: it added no capability of its own, which is the intake's own test for whether a persona is real. Not to be resurrected.

> **The Requirement Contributor exists in the model and in permissions from M1, but has no unmediated path into krill until C12 lands in M2.** That deferral is deliberate (round 3, Q3) and matches the intake's "in a v1 this persona doesn't need a lot of attention." It is why M1's outcome sentence names an Agent rather than a human, and why no RC-facing surface is added to M1 to close the gap.

## Load-bearing decisions

*(architect, discussion #2463 — Product mode. Seven entries. Each protects a `Next` or `Later` capability; the `Stays cheap` clause is what keeps the list this short. Two candidates raised at intake are **rejected** below with reasons.)*

```
LB1 — Scope is a surrogate key, and it is on every row from M1
  At risk: C22 (second repo/tenant), C23 (decisions spanning several products) — both
  `Later`; C9 (the GitHub pointer artifact) needs somewhere to hang forge coordinates.
  Decide now: every entity row carries a non-null `scope_id` FK from M1, and the `scope`
  table — not the entity rows — owns the forge coordinates (`repo_full_name`, default
  branch, pointer-issue number). One seeded row in M1, no UI exposes it, but every
  uniqueness constraint and every query predicate is scope-qualified from day one.
  Stays cheap: the scope selector, scope CRUD, per-scope authorization, cross-scope reads,
  and whether a scope is a repo or an org — those are handlers and policy.
  Why the indirection, not just a `repo_full_name` column: with a natural key, a repo
  rename is a rewrite of every row, and C23 (a decision above any single product) has no
  place to live at all. With a surrogate, both are a row edit.
```

```
LB2 — Entity identity: immutable surrogate ids, and display numbers are rendered, never stored
  At risk: C5/C6 (as-of reads and supersession — an id must outlive the revision that
  minted it), C7 (generated docs cite `FR4`/`LB3`/`C7` by number), C8 (import must
  round-trip), C21 (drift detection compares by identity).
  Decide now: the chain is `Product → FeatureSet → Feature → {FR, NFR}` and
  `FeatureSet → LoadBearingDecision`, strict single-parent, parent FK as a column on the
  child — never an array, never a join table. Each entity has (a) an immutable surrogate
  id, stable across every supersession, and (b) a human-facing display number derived
  per-render and scoped to its parent, which is *not* a stored key.
  Stays cheap: adding new entity kinds under Feature; display-number format; a later
  many-to-many for LoadBearingDecision specifically (C23) — it is a leaf nobody walks
  *through*, so widening it does not touch the scoped query.
  The trap this closes: if `FR4` is the primary key, inserting an FR between 3 and 4, or
  superseding one, renumbers its siblings — and every citation in every generated doc,
  code comment, and CI check that greps for it. `AGENTS.md`'s own splitting rules lean on
  numbered citations staying grep-discoverable across moves. Renumbering is the migration.
```

```
LB3 — SCD2 is the spec axis's mutation pattern; the work axis is append-only + claimed
  At risk: C5 (value at time T), C6 (supersede, never overwrite), C15/C17/C18 (a run's
  attempt history, dead-letter state, and operator intervention are all work-axis writes).
  Decide now: spec entities are SCD2 per `AGENTS.md` § SCD2 — `valid_from`/`valid_to`,
  partial index on `valid_to IS NULL`, as-of via the window-function form. Work-axis
  tables (task/queue rows, claims, leases, attempts, run records, notes) are append-only
  or append-only-plus-claimed and explicitly **not** SCD2, matching the shipped precedent
  in `tools/app_registry/migrate/schema/migrations/004_writeback_outbox.up.sql` and
  `AGENTS.md`'s own "Do not apply SCD2 to append-only event logs." The boundary is drawn
  per table in M1 and written into the schema comments.
  Stays cheap: which columns an entity carries, new entity kinds, offloading superseded
  bodies to S3, deriving an SCD2-shaped *view* over a work table with `LEAD(...) OVER`
  if a consumer ever wants one.
  This contradicts the intake's "Mutation. SCD2 throughout." See open question 6 — I am
  reading that as "throughout the spec axis," and if it was meant literally it needs
  re-deciding before M1, not after.
```

```
LB4 — Every mutation records two subjects, in the shape already shipped in this repo
  At risk: C24 (attribute an action to an agent identity, not just to a run) — `Later`;
  C15/C17/C18 (who ran it, who is stuck, who intervened).
  Decide now: every mutating call records an **acting** identity and an **on-behalf-of**
  identity, each as `(iss, sub, kind)` with `iss` a real column, both always populated
  (`on_behalf_of = acting` when a caller acts for itself) — `whagent_net`'s LB2 verbatim,
  already in production there, and already the wire shape of `libs/go/whagent`'s `Claim`
  (`sub` / `sub_iss` / `act`). In M1 the acting subject is the human who called `init`.
  Stays cheap: authorization policy (read-vs-control is a rule, not a schema); whether
  krill mints its own capability or verifies whagent's JWKS; run-id TTL; revocation.
  Why this makes C24 cheap rather than merely possible: `libs/go/whagent`'s `Claim.Actor`
  already carries `AgentID` next to `Subject`. If krill's records carry the same pair,
  C24 is populating a column that was always there. If krill records only "the run" and
  "the human," C24 is a backfill against rows whose agent identity is unrecoverable.
```

```
LB5 — Krill owns an FR's identity; committed docs are one-way projections with provenance
  At risk: C7 (generated read-only docs), C8 (import), C21 (drift detection) — C21 is not
  merely enabled by this, it is *computable only because of* it.
  Decide now: the direction of ownership, from M1. Krill's surrogate id (LB2) is the
  identity of an FR; a rendered file is a projection carrying a header naming the entity
  and the render revision it came from. Nothing ever parses a committed doc back into
  krill except the one-time importer (C8), which is a separate, deliberately one-way code
  path with no reverse.
  Stays cheap: when rendering runs (hook, schedule, manual), split thresholds and file
  naming (renderer config), whether a render lands as a PR or a push, drift tolerance
  windows, and the `AGENTS.md` carve-out itself.
  Why it is load-bearing and not a preference: two writable sources makes the reconciler a
  permanent tax rather than a temporary one, and the reverse direction is unpickable —
  the first hand-edit to a generated file that someone expects to survive turns "generated"
  into "merged," and there is no migration back.
```

```
LB6 — The delivery axis is an association, never a second parent
  At risk: C13 (milestones and milepebbles as a separate axis), C6 (an FR must outlive the
  milestone that shipped it, for "truncate, never rewind" to mean anything), C14/C15
  (the entire work axis hangs off delivery, not off the spec chain).
  Decide now: M1 ships the spec chain only — but the moment it records *any* delivery
  notion, including "shipped in M1" on the migrated conformance product, that is a row in
  an association table keyed `(entity_id, milestone_id)`, never a `milestone_id` column on
  FR or Feature.
  Stays cheap: milepebble granularity, the status set (including `partially complete`),
  whether one FR may appear in two milestones, re-cutting a milestone, and the whole work
  surface built on top.
  The failure this avoids: a `milestone_id` column makes the *when* axis a second parent
  in a chain LB2 declares single-parent. Unpicking it later is a migration of every
  requirement row plus a rewrite of the scoped query that walks the chain.
```

```
LB7 — The scoped slice is one versioned document, and it is what a claim payload later carries
  At risk: C14 (a self-contained claim payload the worker never supplements) and C16
  (resume on any host) — both `Next`; C19 (a UI reading the same thing).
  Decide now: C3's scoped query returns a typed, self-describing document — an explicit
  schema version, plus the entity ids and the as-of revisions it was assembled from — and
  M1's MCP tool (C4) is a thin wrapper over it, not the thing itself.
  Stays cheap: adding fields to the document, the verb set's payload contents, lane
  sequences, cap values, routing, and which fields any given consumer reads.
  Why M1 decides this even though the work surface is `Next`: the claim payload's core
  *is* this slice. Built as a typed document, the work surface is enrichment. Built as a
  tool response shaped for an interactive reader, the work surface needs a second
  projection of the same data, and the two drift — which is `whagent_net`'s LB1 ("the same
  record, not three projections") learned the expensive way.
```

### Two intake candidates I am **not** making load-bearing

**Postgres-as-queue (`SKIP LOCKED` + leases) rather than an external bus.** I agree with the choice and the reasoning — the queue is a dependency-gated view, not a FIFO, and that join is cheap in PG and impossible in RMQ. But it fails the third clause. The 6-verb wire contract is unchanged by the storage underneath it; swapping implementations later rewrites the work surface's internals without touching a single consumer. It is also not novel here — `tools/app_registry`'s `writeback_outbox` already does exactly this, shipped. This belongs in the work-axis milestone's own design pass and in `krill/ARCHITECTURE.md`, not in a document whose job is to constrain M1.

**"Don't foreclose owning branch/PR lifecycle" (C20).** Storing references in v1 and owning the lifecycle later is additive — more columns, more handlers, a forge client. Cheap. There is exactly one way to make it expensive, and it is the mistake today's pipeline is forced into: `CONVENTIONS.md` § Git hygiene derives a task's branch name deterministically from the issue title and attempt number and states it is "never invented fresh or persisted anywhere separately" — so the retry counter lives in a git ref. **If krill's task record ever derives state from a branch name rather than storing it, C20 becomes expensive.** That is a one-line constraint on the work-axis milestone, not a load-bearing decision M1 makes. Recording it here so it is not lost.

### `Later` capabilities — protection check

| Capability | Protected by | Verdict |
|---|---|---|
| C19 — web UI | nothing, and nothing needed | Cheap. Every UI in this repo is Go + `templ` + htmx over the same API (`whagent_net/ui`, ASS `web`, `tools/app_registry/ui`). A UI is a reader. |
| C20 — own branch/PR lifecycle | nothing, **with one condition** | Cheap *provided* task state is never derived from a branch name (above). |
| C21 — spec/code drift detection | **LB5** | Only computable because the projection is one-way and carries provenance. Without LB5 this is not cheap — it is a diff between two writable sources. |
| C22 — second repo/tenant | **LB1** | Handlers and auth, as the requester intended. |
| C23 — cross-product decisions | **LB1** (surrogate scope) + **LB2** (LB is a leaf) | Note this also needs `CONVENTIONS.md`'s "a product maps 1:1 to a domain" to relax — outside krill, and not M1's problem. |
| C24 — agent identity | **LB4** | Populating a column, not adding one. |

## Non-goals

**Permanent:**

- **Writing code.** Krill does requirement gathering, planning, triage, and spec-of-record maintenance. Code execution stays with whatever harness runs it.
- **Being a general-purpose issue tracker for humans.** Krill unifies personas around agent work; it is not where humans file bugs.
- **Pluggable storage backends.** One backend, many harnesses. Making a forge (gitea, GitHub) able to back krill would mean keeping issues, comments, and labels as the lowest common denominator — throwing away the typed structure that is the entire point. *This is about what krill is **stored in**, not about who it serves: it does not conflict with C22, which puts a second repo or tenant on the same instance and the same single backend.*
- **Owning git.** The forge keeps owning the repository and the merge.
- **Orchestrating or dispatching agents.** Krill hands out work and records what came back; spawning, scheduling, and supervising agent runs belong to the harness.
- **Rendering anything but the product doc set.** C7's renderer owns `PRODUCT.md` and `product/*` and nothing else. `ARCHITECTURE.md` is not in scope and is not a deferral either — *architecture always lives with the code, as it is the essence of how the code is implemented; product is the first pass.* `README.md`, `ENV.md`, and `TOC.md` likewise stay hand-written. This keeps the `AGENTS.md` carve-out for generated docs narrow: only product docs become non-hand-editable. Revisitable eventually, but not v1 and probably not v2.

**Explicitly *not* non-goals — deferred, not foreclosed:**

- **A web UI.** In the vision, not in v1 (C19). An API-only console closes M5.
- **Owning branch and PR lifecycle.** v1 stores references only — M4 holds them as rows, per its first recorded condition — and owning the lifecycle later is plausible and must not be designed out (C20), protected by that same condition: krill never derives task state from a branch name.
- **Multi-repo / multi-tenant use.** One repo in practice; scope is carried on every row from M1 (LB1) so this is later a matter of handlers and auth.
