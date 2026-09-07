package main

import (
	"net/http"
)

// handleDeploymentsLiveSSE handles SSE connections for live per-deployment
// row updates (FR6, FR7, FR8, NFR1, NFR2, NFR5, NFR6, NFR7, NFR8; issue
// #1724). Mounted at /api/live/deployments, wrapped with RequireAuthFunc
// only (never WithAccessToken -- see main.go's route registration comment).
//
// This handler will, once implemented:
//   - Return 503 immediately if app.sseHub is nil (RABBITMQ_URL unset or the
//     broker was unreachable at startup -- see initializeSSEHub in main.go).
//     The client's SSE reconnect loop retries on its own.
//   - Install a per-request cancellable context so a terminal auth failure
//     inside the fragment (session gone) can end the stream via cancel(),
//     while a transient failure (token refresh failed) leaves the stream
//     open.
//   - Compute the topic set (FR7) as events.TopicForDeployment(sgcID) for
//     exactly the SGCs handleSessions would render for the request's
//     selected-server scope -- resolved via a helper shared with
//     handleSessions (handlers_sessions.go) rather than a second copy, so
//     the two can't drift and the stream can't leak rows the page itself
//     wouldn't show.
//   - Serve the connection via htmxsse.Handler(app.sseHub, topics, fragment).
//
// The fragment (rendered per delivery, not once at subscribe time) will:
//   - Parse the sgc_id back out of the topic (inverse of
//     events.TopicForDeployment); an unparseable/unknown topic is a
//     transient error -- htmxsse writes nothing for that delivery and keeps
//     the stream open (FR8).
//   - Re-acquire the access token per delivery via app.auth.GetAccessToken
//     and build the gRPC context with grpcauth.WithUserToken, mirroring
//     tools/app_registry/ui/handlers_sse.go's renderPromoDetailsFragmentComponent.
//     A session-gone failure is terminal (calls cancel()); a token-refresh
//     failure alone is transient.
//   - Verify the parsed sgc_id is in the connection's authorized topic set
//     before rendering -- defence in depth for FR7, never render a row for
//     an SGC the connection wasn't subscribed to.
//   - Render pages.DeploymentRowInner(data), where data comes from the
//     existing app.buildDeploymentRowData(ctx, sgcID)
//     (handlers_deployment_actions.go) -- reused, not re-derived. ActionError
//     stays empty (a pushed update is not an action attempt).
//   - Stay byte-stable for unchanged state (NFR11): no relative timestamps,
//     nonces, or map-iteration-ordered output, or every heartbeat becomes a
//     spurious swap that destroys scroll/selection client-side.
//   - Skip (not fail the connection) a single row on a
//     buildDeploymentRowData error (e.g. control-api down, SGC deleted) --
//     FR8, the rest of the connection keeps working.
//
// TODO(#1724 implementation phase): fill in the body above; this is
// currently a scaffold stub so //manmanv2/ui builds with the route and SSE
// hub wired ahead of the handler itself.
func (app *App) handleDeploymentsLiveSSE(w http.ResponseWriter, r *http.Request) {
	if app.sseHub == nil {
		http.Error(w, "live updates unavailable", http.StatusServiceUnavailable)
		return
	}

	http.Error(w, "not implemented", http.StatusNotImplemented)
}
