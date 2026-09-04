# App Registry — Product Brief

> Retroactive bootstrap. App Registry is mature (AR-M through well past AR-8
> are merged and running); this brief exists to give the domain a
> forward-looking vision/capability-map/roadmap, not to re-litigate what
> shipped. **Current state** and **Load-bearing decisions** below are
> architect's, from a full repo survey — `PLAN.md`'s own status table is
> stale by several phases and should not be trusted (a follow-up chore to
> fix `PLAN.md` itself is tracked separately; not this brief's job).

## Documents

| File | When to read it |
|------|-----------------|
| [product/01-current-state.md](product/01-current-state.md) | Before scoping new work — what's actually shipped vs. what `PLAN.md` claims, per-capability |
| [product/02-capability-map.md](product/02-capability-map.md) | Before scoping or designing anything in this domain — Now/Next/Later capability list (C1..C13) |
| [product/03-roadmap.md](product/03-roadmap.md) | Before running `/project-manager:design --milestone` — milestone definitions (M1..M3) only; live status lives on the tracking issue, not here |

## Vision

A year from now, App Registry is the single place CI and release operators go
to know what has been built and where it currently lives: every artifact CI
publishes is recorded automatically, every promotion between environments is
made through the registry with full before/after history, and the promotion
status UI is the trusted, no-surprises view of "what's running where" for
day-to-day release operations — with no manual bookkeeping in ArgoCD values
files or in anyone's head. Beyond that steady state, the interesting next
horizon is closing the loop after a promotion happens — surfacing whether a
promoted artifact is actually healthy in its environment, up to and including
an automatic rollback when it isn't — rather than treating "promoted" as the
end of the story.

## Personas

- **CI (via `release_helper_go`)** — records every build and published
  artifact as part of the existing release pipeline, with no human in the
  loop.
- **Release operator** — a human running or reviewing a release: decides
  what gets built and published (by triggering a release) and what gets
  promoted to which environment, checks current/historical promotion state,
  and uses the status UI to confirm a release or promotion landed cleanly.

No approver or on-call/incident-rollback persona is designed for today. An
approval-gated promotion flow and an SRE-driven rollback persona are
plausible future additions (the schema already leaves room for the former —
see Load-bearing decisions, LB1) but are explicitly deferred, not something
the current capability map designs around.

## Load-bearing decisions

**LB1 — Approval-gate seam: M3 commits to the promotion layer only.**
At risk: C11 (approval gate).
Two independent, currently-inert approval extension points exist: (a) the
promotion-layer schema (`environment.requires_approval`,
`PromotionState.PENDING_APPROVAL`, `PromotionAction.APPROVE/REJECT`, since
AR-3) gates *promoting to an environment*; (b) `worker/release/approval.go`'s
`CheckApproval` (since #889) gates *triggering a release at all*, called
once per `ReleaseWorkflow` before `ResolvePlan`. This is now resolved for
C11: M3 builds out (a) only, and reshapes it beyond a boolean state check
into a first-class **approval-request** artifact scoped to
(environment, build) — a promotion attempt creates the request, and the
promotion itself only executes once that specific request resolves
(approved/rejected). Promotion becomes downstream of, and an artifact of,
an approval workflow over that request. (b) is deliberately untouched by
M3 — whether the release-trigger stub is ever built out, reconciled into
the same approval-request model, or retired outright stays an **open
question beyond this brief**; do not assume M3's work generalizes to it
without a fresh decision. Stays cheap: the approval-request data model,
approve/reject RPCs, UI screens, and policy engine for the promotion-layer
seam — all still fully greenfield behind the existing stub; seam (b)
remains untouched and equally greenfield for whoever eventually picks it
up.

**LB2 — Keep `promotion_sync_event` split from `promotion_event`.**
At risk: C9 (post-deploy validation) and C10 (auto-rollback), both of which
read `promotion_sync_event`.
Already decided and merged (migration `020`'s own comment states the
rationale) — recorded here so it isn't undone later. `promotion_event` is
operator-*action* history (promote/rollback/approve/reject) with a
CHECK-constrained enum; `promotion_sync_event` is a different, append-only
*observed* stream whose vocabulary (sync/health status strings) will keep
growing as C9/C10 mature. Don't fold the two together in some future
audit-log cleanup — that would mean widening a CHECK constraint every time
ArgoCD's health vocabulary changes, and would couple two migrations that
today evolve independently. Stays cheap: adding new `source`/status values
to `promotion_sync_event`, and any aggregation view over it.

**LB3 — The RabbitMQ SSE event bus must not become C10's trigger path.**
At risk: C10 (auto-rollback).
`events/` is explicitly best-effort — no outbox, no redelivery, drops
silently on backpressure or a disconnected broker. It exists solely to
drive UI liveness. If C10 is ever built, its trigger must come from the
durable path — the Temporal `WritebackWorkflow`/`PollArgoSyncStatus`
activity chain and the `promotion_sync_event` table — not from a consumer
subscribed to the `app-registry.htmxsse` exchange. The package name
("events") invites exactly this mistake for whoever picks up C10 without
reading `architecture/21`. Stays cheap: everything about the SSE bus
itself — free-form, additively extensible, no wire-versioning concern.

**LB4 — Approver identity must reuse the existing Keycloak role model.**
At risk: C11 (approval gate).
The only actor-identity concept in the system today is a bare OIDC subject
string on `promotion_event.actor` (mixing GitHub-username-shaped CI
subjects and raw UUID human subjects) — there is no directory, no
roles-by-name table. When C11 is built, "who can approve for environment
X" must be expressed as a Keycloak role check through the existing
`server/auth`/`grpcauth` model (the same one `EnvironmentPromoterRole`
already uses), not a new bespoke "approvers" table keyed by raw subject
strings — the latter would need migrating onto the former the moment
anyone asks whether an approver role expires/rotates the same way a
promoter role does. Stays cheap: which specific roles map to "approver"
per environment, and the approve/reject UI itself.

## Non-goals

- **Not a replacement for GHCR.** The OCI registry remains the artifact
  store; App Registry only tracks versions and digests as metadata about
  what GHCR holds.
- **Never applies or mutates a cluster resource, and never decides on its
  own — but it does observe.** The registry never runs `kubectl`/client-go
  or touches the Kubernetes workload API directly, and nothing auto-acts on
  a health signal today — a human reads the readiness badge. It does,
  however, call out to cluster-adjacent infrastructure: after every
  writeback it calls ArgoCD's REST API directly (`libs/go/argocd`) to
  trigger a refresh and poll sync/health, so "doesn't talk to a cluster" is
  no longer accurate. The line that still holds is observing vs. deciding:
  if C9/C10 above are ever built into something that acts on health
  automatically, that's an explicit, separately-scoped capability, not an
  implicit side effect of promotion.
- **No historical GHCR backfill.** The registry indexes what CI publishes
  going forward; it does not attempt to reconstruct history for artifacts
  published before it existed.
- **No approval-gate persona/workflow today.** The schema leaves room for
  it at the promotion layer, and a separate stub exists at the
  release-trigger layer, but neither is built out — see C11 and LB1.
- **Release-trigger-layer approval (`worker/release/approval.go`'s
  `CheckApproval`) is out of scope for M3.** M3 builds the approval-request
  model for the promotion-layer seam only (C11). Whether the release-trigger
  stub ever gets a real implementation, gets reconciled into the same
  approval-request model, or is retired is an open question beyond this
  brief — see LB1.
