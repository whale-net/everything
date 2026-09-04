# Current state

Surveyed against the actual repo (migrations through `024`, `worker/`,
`server/`, `ui/`, `events/`, architecture docs, and git log), not against
`PLAN.md`'s table, which understates current state by several phases.

**Recording & promotion.** CI records every published artifact automatically
(C1); promotability is derivable for any artifact (C2); promotion writes back
to the gitops repo for ArgoCD to act on (C3).

**History is two independent SCD2 timelines, not one (C4).** `promotion`/
`promotion_event` tracks what was promoted to an environment and when.
Separately, AR-8 (issue #587, PR #592, merged 2026-08-13, migration
`010_manifest_history`) added `app_manifest_history`/`chart_manifest_history`
— content-addressed history of *manifest content* drift on `main`. The two
are deliberately kept apart: one is deploy-state history, the other is
source-content history. `PLAN.md` still describes AR-8 prospectively
("Goal:", "Scope:", no PR link); it's done.

**Release-run tracking (C5) vs. release triggering (new, C13) are different
capabilities.** C5 is passive: per-release build/attempt tracking
(`app_build_log`, `release_run`). Separately, `worker/release/`'s
`ReleaseWorkflow` (#886/#889) is a Temporal actor that drives an entire
release — trigger → build → publish → record — kicked off from the UI itself
(`TriggerRelease` RPC), dispatching to `release.yml` or `release-v2.yml`
depending on the domain. It is live today for `manmanv2`, `leaflab`, and
`app-registry` itself (`release_run_target` rows exist since 2026-08-22),
replacing a human running `gh workflow run` by hand. This is "trigger and
drive a release," not "record what CI already did," and doesn't fit under
C5.

**The admin UI (C6) is a full read+write operator surface, not a read-only
view.** In production for a few weeks, no notable complaints. It includes:
promote/rollback forms (dry-run enforced above dev, reason required), an
environments/deployments matrix with as-of/SCD2 time travel, a drift & audit
screen, per-release build/attempt history, a manual ArgoCD retry-sync
action, RBAC-gated (presentation-only, server-enforced) admin actions, and —
merged the day of the survey (`d54b8654`) — a live SSE-driven Promotion
Details readiness banner. `PLAN.md`'s "Explicitly out of scope: Web UI" line
is flatly wrong.

**Post-deploy validation (C9) is substantially shipped, not upcoming.**
`promotion_sync_event` (migration `020`, issue #1028) is an append-only
ArgoCD sync/health observation log, deliberately kept separate from
`promotion_event`'s audit trail (see LB2). `TriggerArgoRefresh`/
`PollArgoSyncStatus` (issue #1030) poll ArgoCD's REST API directly
(`libs/go/argocd`) after every writeback (3 attempts, ~6 min bound).
`GetPromotionDetails` + the Promotion Details page (#1043/#1044) surface it
via an inline badge, a manual retry-sync action (#1045, admin-gated), and
the live SSE readiness banner. What's genuinely still missing: no
aggregate, environment-level "is this environment healthy right now" view —
today it's per-promotion only, and polling is bounded/writeback-triggered
rather than continuously scheduled.

**Cheap and low-priority (C7, C8, C12).** Per-chart ArgoCD Application name
overrides; structured operator-facing logs; `environment` is already a
managed table with the writeback path templated on `(domain, chart,
environment.key)` with no environment-count assumption anywhere —
ephemeral environments (C12) would be genuinely cheap to add on top of
this, no schema change needed.

**Unbuilt: C10 and C11.** Auto-rollback (C10) — no code exists anywhere,
but it's cheaper than `PLAN.md` claims: the health signal it needs (C9) and
the rollback mechanism (`Rollback` RPC, `PROMOTION_ACTION_ROLLBACK`, which
promotes back to the SCD2-tracked "superseded" artifact) both already
exist. It's a decision-policy-and-trigger problem, not new plumbing.
Approval gate (C11) — unbuilt at the promotion layer
(`environment.requires_approval`, `PromotionState.PENDING_APPROVAL`,
`PromotionAction.APPROVE/REJECT` all exist in schema/proto since
AR-3b/AR-3c; no caller reads or writes any of them). See LB1: a second,
independent, unreconciled approval stub also exists at the release-trigger
layer.

**Eventing is not a general event bus.** `events/` (RabbitMQ, #1147/#1186/
#1227) is a best-effort, non-durable publish path used solely to drive the
UI's SSE liveness — it drops silently on backpressure or a disconnected
broker, with no outbox or redelivery (explicit and load-bearing per
`architecture/21-promotion-sse.md`). Nothing outside the UI/writeback-worker/
server trio consumes it today. See LB3: it must never become an actual
trigger path.
