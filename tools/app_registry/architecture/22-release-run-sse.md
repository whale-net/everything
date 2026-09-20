# Release-Run SSE — Architecture

**A second event family, and the second `htmxsse`/`htmxauth` adopter, after `/promotions/{id}`.** Every `release_run_target` state write announces itself on the shared promotion exchange (`events.ExchangeName`, `tools/app_registry/events/publisher.go`) under that release run's own routing key, `release_run.<release_run_id>` (`events.TopicForReleaseRun`); `/releases/{id}/status/sse` streams that release run's live status to the release-status page's pushed region. This document records only what's specific to this family and route — see [`21-promotion-sse.md`](21-promotion-sse.md) for the auth composition, terminal/transient discrimination, the non-redirecting writer shim, heartbeat/degradation behavior, and token-lifecycle detail this page reuses **verbatim** — none of it is restated here.

## No library change (#1699 out of scope)

This adopter introduces zero changes to `libs/go/htmxsse` or `libs/go/htmxauth`. The route (`ui/main.go`) composes `RequireAuthFunc` behind `newNoRedirectWriter` exactly as `/promotions/{id}/status/sse` does; the handler (`ui/handlers_release_sse.go`) wires `htmxsse.Handler(app.sseHub, topics, fragment)` against the **same shared `app.sseHub`** — no second `Hub` is constructed. The Hub's `registerHandler` already binds `#` on the shared exchange (`libs/go/htmxsse/hub.go`), so the `release_run.*` routing-key family needs no Hub-side change, and NFR1's multi-replica fan-out follows from the Hub's existing non-durable auto-delete per-replica queue, same as the promotion family.

## A second routing-key family on the shared exchange

`events.TopicForReleaseRun(id)` produces `"release_run.<id>"` (`tools/app_registry/events/events.go`), a sibling of `TopicForPromotion`'s `"promotion.<id>"` on the same `app-registry.htmxsse` topic exchange (`events.ExchangeName`). `events_test.go`'s `TestTopicForReleaseRun` asserts the two families never collide for the same id, guarding against an accidental shared-helper refactor merging them.

## The two publish points

1. **Row creation at `QUEUED`.** `ReleaseServer.TriggerRelease` (`server/handlers/release.go`) publishes once, after `repo.ReleaseRuns().CreateReleaseRun` commits — one publish per `TriggerRelease` call, not one per target in the batch, since every target in that batch starts `QUEUED` in the same write.

2. **Every transition `RecordTargetState` lands.** `worker/release/record.go`'s `RecordTargetState` is the single funnel every `BUILDING`/`PUBLISHING`/`RECORDING`/`SUCCEEDED`/`FAILED` write goes through — both `ReleaseWorkflow`'s `workflow.ExecuteActivity` dispatches and `FinalizePublish`'s direct in-process calls. It publishes once per `UpdateTargetState` call that actually lands (a single `RecordTargetState` call can walk several intermediate states to catch up — each landed step gets its own event, in order), and publishes nothing on its idempotent no-op path (already at the desired state, or already terminal).

## Payload shape

`PublisherInterface.PublishReleaseRun(releaseRunID, eventKind, eventStatus)` and `Publisher.PublishReleaseRun` (`events/publisher.go`) exist as of #1700, wired at both publish points as of #1702. `eventKind` is `"release_target_" + state` (`release_target_queued`, `release_target_building`, `release_target_publishing`, `release_target_recording`, `release_target_succeeded`, `release_target_failed`); `eventStatus` is `"pending"` for every non-terminal state and `"succeeded"`/`"failed"` for the two terminal ones. Both fields are **advisory only**, same convention as promotion events (see `21-promotion-sse.md`'s Post-commit best-effort publish section): the `/releases/{id}` SSE route re-reads `release_run`/`release_run_target` state at delivery time rather than rendering this payload, and never treats the target identity carried implicitly by a publish as something to act on directly.

## NFR3 / NFR6 (never fail, retry, or delay a write; nil-safe)

Every publish happens strictly **after** its write returns successfully, and its return value is discarded — a publish can never turn a successful write into an activity error, and (unlike the write itself) is never retried. `Activities.Publisher` and `ReleaseServer.pub` are both `nil` in any deployment with `RABBITMQ_URL` unset (`initializePublisher`, `worker/main.go` and `server/main.go`) — every call site is nil-checked, matching `promotion.go`'s existing `s.pub != nil` guards exactly. With no broker, the release pipeline is entirely unaffected: `TriggerRelease`, `ReleaseWorkflow`, and `FinalizePublish` all run to completion unchanged, and `/releases/{id}` still renders current state by ordinary GET (same degradation path the route itself relies on — see below).

## FR11 pushed-region boundary

`GetRelease` cannot render state that depends on per-request context outside the region — same constraint `21-promotion-sse.md`'s "FR29's pushed-region boundary rule" documents for `GetPromotionDetails`. The fragment reads at delivery time via `app.registry.Release.GetRelease(grpcCtx, &pb.GetReleaseRequest{ReleaseRunId: id})` — the identical RPC `handleReleaseStatus` (the plain-GET handler in `handlers_release.go`) uses for the page's initial render. No new gRPC method or proto field exists for this route.

## NFR2 — one render path

The fragment renders exactly `pages.ReleaseStatusLiveBody(rel, commits)` — the same `templ` component `handleReleaseStatus`'s initial GET renders inside `pages.ReleaseStatus`'s full page (see `ui/pages/release_status.templ`, and #1704's `TestReleaseStatusLiveBody...` byte-identity coverage). The publisher payload's advisory `event_kind`/`event_status` fields are never rendered by the fragment; the fragment is produced from the fresh `GetRelease` read on every delivery, so if the payload and the fragment ever disagree, the fragment wins — same rule `21-promotion-sse.md` states for promotion events.

## FR12/NFR9 — commit resolution through the process-lifetime cache

Unlike the promotion fragment, this one also resolves each target's `build_id` to the commit its build was cut from, via `app.resolveTargetCommits(grpcCtx, resp.GetTargets())` (`handlers_release.go`, #1703). A `build_id`'s `git_sha` is immutable, so a successful resolution is cached process-lifetime in `app.buildCommits` and read through on every later render — **this is what bounds NFR9**: a heartbeat or an event delivery for a run whose targets' builds have already been resolved issues zero additional `GetBuild` calls, only the one `GetRelease` per delivery. A `GetBuild` failure, or a build with an empty `git_sha`, is never cached (retried on a later render) and is omitted from the map; the page renders "unknown" for that target rather than failing the whole fragment over one commit link.

## FR16 — terminal runs are not special

There is no "all targets terminal → close the stream" branch anywhere in `handleReleaseStatusSSE` or `renderReleaseStatusFragmentComponent.Render`, and none in `pages.ReleaseStatusLiveBody`. A release run whose every target is already `SUCCEEDED`/`FAILED` at page load still establishes a live connection and heartbeats exactly like an in-progress run — the terminal state is rendered, not treated as a signal to stop streaming. (The release-history list page is unaffected and stays out of scope; it is not made live.)

## Degradation (NFR6)

With RabbitMQ unreachable, the exchange absent, or the Hub unattached, `/releases/{id}` still renders current state via the ordinary `handleReleaseStatus` GET — a plain Postgres read through `GetRelease`, independent of the publish path — and the release tool's own state writes are unaffected. Live updates are simply absent until the Hub (re)attaches; there is no 500 and no blank page. Same three broker cases `21-promotion-sse.md`'s "Liveness and degradation behaviour" section documents for the promotion route apply unchanged here, since both routes share one `htmxsse.Handler` implementation and one `app.sseHub`.
