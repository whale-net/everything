# Audience Score System — Product Brief

## Vision

Audience Score System helps a YouTube creator and their analyst turn a hunch into a scheduled, published video, and then tells them whether they were right. It closes three coupled loops — research a topic into a grounded viability call, turn viable ideas into a schedule the creator commits to, and check the published outcome against what was predicted — so that judgment about "what will work" gets calibrated by real outcomes over time instead of staying confident guesswork. The product itself holds no opinion: it is a Postgres-backed research/schedule/outcome store wrapping the YouTube Data and Analytics APIs, exposed over MCP so any MCP-capable agent (Claude, Copilot, opencode, Antigravity, etc.) can do the reasoning while the system makes that reasoning durable, cited, and checkable against reality.

Language/runtime: **Go throughout** — API, MCP server, and Temporal workers, reusing `libs/go/temporal`, `libs/go/db`, `libs/go/migrate`. The MCP server is the first Go MCP server in this repo (no in-repo precedent); accepted as a known gap for M1, not a blocker.

## Personas

- **Creator** — owns Channels and holds final decision authority: only a Creator on a Channel can approve a schedule draft or turn a viability verdict into a commitment. Drives research closely alongside the Analyst in V1.
- **Analyst** — does research and builds proposed plans/schedules for the Creator's review; no approve/finalize authority on any Channel. Work is mutually visible with the Creator's.

## Load-bearing decisions

LB1 — YouTube OAuth consent scope requested at signup
  At risk: C9 (Now — CTR/impressions metrics) and C16 (Later — deeper Analytics: retention curves, traffic sources, subscriber conversion).
  Decide now: request the full YouTube Analytics **owner-level** scope (`yt-analytics.readonly` + `yt-analytics-monetary.readonly` if any monetary-adjacent field is ever wanted, otherwise just the owner-level non-monetary scope — confirm which C16 fields need) at every Creator's initial OAuth consent in M1, even though V1 only stores/surfaces views + retention + CTR/impressions. Re-consent is a re-auth flow through Google's consent screen for every already-connected Creator — cheap for a handful of dogfood users now, a real support/adoption cost once C11 (multi-Channel) and C12 (Next) exist and there are many connected accounts.
  Stays cheap: which Analytics *fields* get queried, cached, and surfaced (traffic sources, subscriber conversion, retention curves) — that's application-layer scope entirely inside the sync job and read models, addable without touching the OAuth grant at all once the scope is already held.

LB2 — Persona↔Channel association schema, asymmetric authority
  At risk: C12 (Next — Analyst across multiple Channels), C13 (Next — multiple Creators per Channel).
  Decide now: model Persona↔Channel as a join table from day one (`channel_id`, `person_id`, `role` ∈ {creator, analyst}, plus invite/accept state), with authorization checks (`can_approve`, `can_invite`) reading the role off that table rather than off a single `channel.owner_id` column or an assumption of "the one Creator" baked into schedule/verdict approval logic. M1 only ever populates one row of each role per Channel, but every approve/finalize check must already ask "is this person a Creator *on this Channel*" via the join, not via a hardcoded single-owner field.
  Stays cheap: the invite UI/flow itself, multi-Creator conflict resolution (what happens when two Creators disagree — not specified anywhere, rightly deferred), and the Analyst-works-across-Channels list view — all additive once the join table and role-scoped authority checks already exist.

LB3 — Prediction-vs-outcome record shape *(amended by `video-script-model`, issue #1562: chain changes from `idea → verdict → schedule_draft → committed_schedule_entry → published_video_metrics` to `idea → verdict → video_script → published_video_metrics`, with `video_script` also carrying a `strategy_id` for grouping/cadence context only — not part of the identity path)*
  At risk: C10 (Now — browsing past comparisons) and C14 (Next — calibration trends across many ideas).
  Decide now: `video_script` carries its own direct `verdict_id` FK (and `idea_id` transitively through the verdict) — mirroring what `schedule_entry` already does today — so the chain from idea to published outcome has a stable, non-lossy identity path that does not route through Strategy. `strategy_id` on `video_script` is grouping/cadence context only, never the identity path: Strategy is many-to-many with verdicts (`store/strategy.go`'s `SaveStrategyInput.VerdictIDs []uuid.UUID`; the doc comment is explicit that "the same verdict may be passed to more than one Strategy"), so if a `video_script`'s identity were resolved via `strategy_id` alone, a Strategy grouping 5 viable verdicts would yield 5 candidate verdicts per video_script — not one. That is precisely the identity-loss failure this LB exists to prevent. C14's calibration-trend view is an aggregate query over the idea→verdict→video_script→outcome chain; if idea identity is lost or duplicated anywhere in it, that aggregate requires a backfill/reconciliation pass instead of a `GROUP BY`.
  Stays cheap: the trend visualization itself, any cross-idea statistical aggregation (accuracy rate, confidence calibration curves) — all read-side, addable once the FK chain exists.

LB4 — MCP write-back tool idempotency and statelessness
  At risk: the "MCP-only, no hosted agent loop, ever" non-goal — not a numbered capability, but a permanent architectural constraint the brief states explicitly.
  Decide now: every write-back tool (`save_research_note`, `save_viability_verdict`, `save_video_script`) must be safely re-callable with the same logical input (client-supplied idempotency key, or natural-key upsert) and must not assume any server-side state survives between tool calls beyond what's in Postgres — no in-memory session, no assumption that the calling agent process is the same process across calls. MCP clients (per the brief's own list: Claude, Copilot, opencode, Antigravity) retry tool calls on ambiguous failures and don't guarantee call-to-call process affinity; a V1 shortcut that works today because one agent session happens to hold state in its own context is exactly the kind of thing that gets threaded through every tool handler and is expensive to unpick once several write-back tools exist and clients are already depending on the current (accidentally stateful) behavior.
  Stays cheap: adding more write-back tools, adding more read tools, changing what a tool returns — none of that touches the idempotency contract once every tool already treats Postgres as the only state.

## Non-goals

- No platforms beyond YouTube, ever.
- No hosted/in-product agent loop — MCP plus an external MCP-capable client is the permanent architecture, not a V1 shortcut to be replaced later.
- No monetization or billing on the roadmap.
- No personas beyond Creator and Analyst.

## Contents

| File | Contents |
|------|----------|
| [`product/01-current-state.md`](product/01-current-state.md) | What already exists in the repo that ASS should reuse, and the gaps it inherits (Temporal, OAuth, MCP framework, Postgres/SCD2, YouTube client) |
| [`product/02-capability-map.md`](product/02-capability-map.md) | Capability map — C1..C19, bucketed Now/Next/Later |
| [`product/03-roadmap.md`](product/03-roadmap.md) | Milestone definitions — M1, M2, M2.1, M3 |
