# SSE Integration on `/promotions/{id}` — Architecture

**Server-Sent Events (SSE) stream live updates to the Promotion Details page** (`/promotions/{id}`), **eliminating the need for manual reload.** This document records the design decisions, constraints, failure modes, and load-bearing components that enable this integration.

## The three publish points (FR7)

Publish events originate from three points in the system:

1. **Server-side event on promotion accept:** `PromotionRegistry.Promote` or `PromotionRegistry.Rollback` records a promotion/rollback and enqueues a `writeback_outbox` entry, then publishes an event synchronously before returning to the caller.

2. **Temporal workflow completion:** `app-registry-worker` drains the outbox and runs `WritebackWorkflow`, which publishes an event when the ArgoCD sync completes (or fails).

3. **User-triggered retry:** `PromotionRegistry.RetryArgoSync` publishes an event when the user clicks "Retry refresh/sync" on the UI.

## Asynchronous publish component (FR7b) — non-blocking hand-off

The **first publish point** (promotion accept) is backed by an asynchronous component that decouples the write from the broker publish. The `Publisher` in `tools/app_registry/events/publisher.go` implements **four load-bearing properties**:

### Non-blocking hand-off (Property 1)
`Publish(promotionID, eventKind, eventStatus string)` enqueues the event to a bounded in-process buffer and returns immediately to the caller. The actual broker publish happens on a background goroutine. The caller's context is **never blocked** on broker I/O.

**Timeout budget:** Broker publishes use a 5-second bounded context (line 245), independent of the caller's context. This 5-second bound is **the same as the `rmq.Publisher.Publish` timeout itself** — the component does not add its own timeout atop that; it just enforces the library's existing bound.

### Process-lifetime context (Property 2)
The background publish goroutine uses a process-lifetime context (`doneCtx`, initialized on line 108 from `context.Background()`), not the caller's context. Cancelling the caller's context does not cancel an in-flight broker publish that has already been accepted into the buffer. **This separation is load-bearing for FR28:** the SSE handler's per-request context (FR28e, line 37 in `handlers_sse.go`) can be cancelled to terminate a stream without dropping an event that was already enqueued.

### Non-fatal construction (Property 3)
Construction does not fail the host process. Connect, channel open, and `ExchangeDeclare` happen in the background with exponential backoff (line 172, `attach` function); the process starts and serves immediately. Publishes enqueued while unattached are dropped and logged. The component attaches with no operator intervention once the broker becomes reachable.

### Best-effort shutdown (Property 4)
On process shutdown, the component drains the buffer within a bounded 5-second deadline (line 283). Anything undrained is dropped and logged. No shutdown path blocks the Temporal worker or gRPC server shutdown beyond that bound.

## Post-commit best-effort publish vs. writeback_outbox pattern divergence

The traditional `writeback_outbox` pattern writes a side-effect intent to the database **in the same transaction as the main write**, ensuring atomicity. **SSE publish diverges:** events are enqueued **after the database transaction commits** (on the success path only) and are **not part of the transaction's atomicity**.

This design choice means:
- **A dropped event is not recoverable from the outbox.** Unlike the `writeback_outbox` pattern, there is no retry/redelivery mechanism; if the publisher is unattached or the buffer is full, the event is logged and gone.
- **NFR11's heartbeat is the publish side's only compensating control.** Producers (the page's SSE consumer, in this case) must detect a stale state via the heartbeat mechanism, not via re-delivery of a lost event. Without the heartbeat, a dropped event would be invisible — the page would never know to refresh. The heartbeat exists precisely to handle this case, making it **load-bearing for data correctness, not only for liveness**.

## FR7a standing dependency: promotion id keying

**The page renders no supersession field today.** Therefore, keying the SSE stream by `promotion_id` is **complete and correct** — every `promotion_id` that exists has exactly one row in the `promotion` table (at any given SCD2 window). **The day this page gains a "superseded by" line, or any field written by a *different* promotion's `Promote`, this assumption breaks.** A later plan must revisit the publish-point set and ensure every relevant `promotion_id` has an SSE topic subscription. This is recorded here so that plan's author finds it before shipping.

## FR29's pushed-region boundary rule

`GetPromotionDetails` cannot render state that depends on per-request state outside the region — the response must be reproducible from the read replica's view of the database alone. This constraint is **intrinsic to the region boundary**, not specific to SSE: a request for stale data from the read replica may see an older version of the promotion row, and the fragment must still render *correctly* from that stale state. **The boundary is per-request state**, which the read replica cannot reproduce — it has no access to session state, user context, or request-scoped decisions.

## Per-connection fragment decision and its cost (FR3, NFR18)

**The fragment function is called on every delivery (connect, reconnect, and heartbeat).** This means:
- Every heartbeat executes `GetAccessToken` (line 97 in `handlers_sse.go`), which takes `FOR UPDATE` on the `ui_sessions` row (line 228 in `libs/go/htmxauth/db_session.go`).
- On the fast path (token has >2 minutes of life, line 235), the lock is rolled back immediately (line 236, `tx.Rollback(ctx)`), but the lock *was still acquired and held* for the duration of the query.
- **Same-operator streams contend on the session row every heartbeat**, not only when a token refresh fires.

**Load-bearing cost:** The heartbeat's per-connection DB read cost scales with concurrent connections. This cost is **accepted at `/promotions/{id}` scale** — the page is not expected to handle thousands of concurrent viewers. The planned dashboards follow-up must revisit this constraint. **Per the library maintainer, the fix should be an addition alongside FR3** (a shared, cacheable fragment path for viewer-independent regions) **rather than a change to FR3's signature itself** — the per-connection read is the price of per-connection rendering, and that cost should not block a different path that doesn't need it.

## Token re-acquisition decision (FR27)

The fragment function re-acquires the access token on every delivery (line 97 in `handlers_sse.go`). This means:
- The token is never stale by more than one heartbeat cycle.
- A token refresh (when the token is within 2 minutes of expiry) applies immediately to the next delivery, not on a background schedule.
- The SSE stream is **not a bare proxy of the user's token** — each delivery uses the **current** token, not the one the stream originally established.

**Token injection:** The fresh token is injected into the gRPC context via `grpcauth.WithUserToken` (line 113 in `handlers_sse.go`), not via the session row's credentials directly. This injection step is essential for the SSE handler to work with the grpcauth library's token-passing conventions.

## Roles are snapshotted at connect

**FR3's per-connection render does not imply per-heartbeat authorization freshness.** Roles are extracted from the OIDC token **at login time** (during `SetUserInfo`, `libs/go/htmxauth/db_session.go:141`) and stored in the `ui_sessions` table's `user_info` JSONB column. They are **never refreshed** on token refresh (`GetAccessToken`, lines 266-270, only updates `access_token` and `refresh_token`). **A user whose realm roles changed mid-session will not see that change reflected in the Promotion Details fragment.** The page's contents are gated by the roles at the time of connection, not at the time of each heartbeat. An operator who revokes a user's editor role while they are viewing the page will find that revision is not reflected until they reload or their session expires.

## Terminal vs. transient discrimination and reconnect loop foreclosure (FR27)

The fragment function (lines 95–146 in `handlers_sse.go`) must discriminate between two failure modes:

1. **Terminal error (session gone):** The user's session has been deleted or expired. In this case, no reconnect will succeed; the browser should stop retrying.
2. **Transient error (credential failed):** The access token refresh failed (e.g., IdP timeout), but the session row still exists. The browser may reconnect and retry after backoff.

**The discriminator is `DBSessionManager.GetUserInfo` specifically.** The fragment checks whether the session row exists (line 101, `GetUserInfo` query on line 183–186 in `db_session.go`), **recorded as loop-foreclosure and not as an identity claim**. The error from `GetUserInfo` is not used for authorization; it is used only to decide whether to cancel the stream. This is the only place in the system where `GetUserInfo` is called for this non-identity reason.

**Error-string matching is prohibited.** The discriminator must not rely on parsing the error message — `db_session.go:231` returns a bare `fmt.Errorf("session not found or expired")` with no structured error type. Future changes to error text or wrapping must not break the discrimination. The behavior is to call `GetUserInfo` and check its error, period.

**DB outage behavior:** If the database is down, both `GetAccessToken` and `GetUserInfo` fail, and the fragment treats the outcome as **terminal** — the stream cancels and does not reconnect. This is **correct behavior: fails closed.** A database outage is bad; leaving a stream connected and silently dropping updates is worse. The page will go grey, prompting the user to reload, which will surface the database error instead of leaving them staring at stale content.

**Why `*DBSessionManager` is retained on `App`:** The `sessionMgr` field is retained on `App` (line 100 in `handlers_sse.go`) **for this and nothing else** — all other authentication decisions use the gRPC auth layer (`grpcauth`). The session manager is only needed here to make the terminal/transient call. If a refactor removes the session manager, this code path must be updated to discriminate by a different method (e.g., a special RPC on the promotion server that checks whether the session is valid).

## SSE route's non-redirecting auth failure (FR6, FR28)

The SSE handler receives authenticated requests via `RequireAuthFunc` (composed, not replaced), but **cannot use `WithAccessToken`**, which redirects on auth failure. A 3xx redirect violates FR28's "no redirect, ever" constraint for streaming responses.

**Solution:** A non-redirecting `ResponseWriter` shim wraps the response writer at the route level, intercepting redirect responses (3xx) and converting them to 401 Unauthorized before sending. The shim is implemented in `tools/app_registry/ui/noredirect_writer.go` and marks itself as a **prototype** — it is not a permanent app-registry-ism. The four required properties (lines 13–19 in `noredirect_writer.go`) are:

(i) **WriteHeader intercepts 3xx and converts to 401, deleting Location and Content-Type** — prevents the browser from following the redirect.

(ii) **Suppresses the body that follows a converted redirect** — the converted status is 401, not 302, so any HTML document that would have been part of the redirect is not sent.

(iii) **Delegates Flush()** — missing `Flush()` breaks FR2's flush contract (SSE requires flushing on every delivery) and breaks recorder-based tests by buffering.

(iv) **Implements Unwrap()** — `ResponseController` methods like `SetWriteDeadline` resolve only through `Unwrap()`, essential for low-level response control.

**Future:** This shim is the "first-class streaming-safe middleware" the Out-of-scope block defers to the second `htmxsse` adopter. The follow-up should **lift it into `htmxauth` rather than re-derive it**, including the four required properties — a re-derivation would most plausibly miss `Unwrap` and the `Content-Type` deletion.

## Context-cancellation termination path (FR2, FR28e)

The handler creates a cancellable per-request context (line 37 in `handlers_sse.go`), captured by the fragment closure (line 56, `cancel` parameter). On terminal error, the fragment calls `cancel()` (lines 104, 125), which cancels the handler's context and terminates the stream.

**Why credential failure is not expressed as a library error:** The `grpcauth` library does not export a way to distinguish terminal from transient failures. The terminal/transient discrimination (above) is custom logic specific to this page's SSE use case. The fragment does not call into `grpcauth` to ask "was this a terminal error?"; it inspects the session state directly (via `GetUserInfo`) to decide. This decoupling is intentional: session-based terminal detection is a UI-layer concern, not a gRPC library concern.

## Per-heartbeat session re-check with real bound (NFR8, #1114)

The fragment re-checks the session (via `GetUserInfo`, lines 101, 121) on every delivery. This means:
- A session that expires between heartbeats is detected and the stream is cancelled.
- A **revoked-but-unexpired token is forwarded to the registry and accepted** — it is not detected until `token_expires_at − 2m` because of `GetAccessToken`'s fast path (line 235 in `db_session.go`), which skips the refresh if the token has >2 minutes left.

**NFR8 bound computation:** IdP-side revocation is not detected until the token is within 2 minutes of expiry. The **real bound is not a fixed 3–3.5 minute figure** — it is a **function of the realm's access-token lifespan**. If the realm issues tokens with a 1-hour expiry, the window is (1 hour − 2 minutes) from the token's issue time until its revocation is noticed. If tokens are 10 minutes, the window is (10 minutes − 2 minutes = 8 minutes). **A second adopter must compute its own bound** rather than copying the 3–3.5 minute figure as a property of the design.

**Two token-expiry writes to check:** The refresh path applies the same `token.Expiry`-with-5-minute-fallback rule (lines 261–264 in `db_session.go`) as the initial write (lines 157–160), then writes to `token_expires_at` via the UPDATE statement (lines 266–270). A reader reasoning about when the NFR8 window reopens must find both, so they are cited here together.

**Tracked limitation:** #1114 is the accepted, tracked limitation. It gates nothing in this plan — revocation detection is best-effort, and the SSE stream is expected to handle transient failures via the reconnect loop.

## Liveness and degradation behaviour (FR5, FR23, NFR11)

The page is **live** when:
1. The SSE connection is open.
2. Heartbeats arrive at regular intervals (FR23 suppressess reconnect baseline, preventing false "not live" detections from normal backoff).
3. The broker connection is intact (if the broker is down, heartbeats still fire from the handler's loop, but no events are published; the page remains live and silent).

The page is **not-live** when:
1. The SSE connection is closed (tab closed, network failure, server restart).
2. The session is gone (terminal error, section above).
3. The browser's reconnect loop exhausts backoff (browser-side, not server-side).

**Three broker cases in which the page stays live (NFR7):**
1. **Broker disconnected:** The handler's background goroutine marks itself unattached and continues heartbeating. New events are dropped. The page remains live and silent until the broker reconnects and events resume flowing.
2. **Broker accepting publishes but not delivering:** The handler's `htmxsse.Handler` continues heartbeating. Events are enqueued but not delivered to subscribers. The page is live from the handler's perspective (heartbeat fires), and the operator should check broker logs.
3. **Broker offline during graceful shutdown:** The `Publisher.Close` drains the buffer within 5 seconds and closes the underlying `rmq.Publisher`. Any undrained events are logged. The handler is unaffected by the close — its own shutdown (from the gRPC server or Temporal worker shutdown) takes precedence.

## File name of the non-redirecting ResponseWriter shim and prototype status

The shim is implemented in `tools/app_registry/ui/noredirect_writer.go` (package `main`; a standalone file in the server binary, not a library). It is **a prototype**, not a permanent app-registry-ism. The first-class streaming-safe middleware pattern it embodies should be **lifted into `htmxauth`** as part of the second adopter's work, including the four required properties, so it does not need to be re-derived. See the "SSE route's non-redirecting auth failure" section above for the property list.
