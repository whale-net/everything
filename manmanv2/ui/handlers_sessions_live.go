package main

import (
	"context"
	"fmt"
	"io"
	"log"
	"net/http"

	"github.com/a-h/templ"

	"github.com/whale-net/everything/libs/go/grpcauth"
	"github.com/whale-net/everything/libs/go/htmxsse"
	"github.com/whale-net/everything/libs/go/htmxsse/templadapter"
	"github.com/whale-net/everything/manmanv2/events"
	manmanpb "github.com/whale-net/everything/manmanv2/protos"
	"github.com/whale-net/everything/manmanv2/ui/pages"
)

// handleDeploymentsLiveSSE handles SSE connections for live per-deployment
// row updates (FR6, FR7, FR8, NFR1, NFR2, NFR5, NFR6, NFR7, NFR8; issue
// #1724). Mounted at /api/live/deployments, wrapped with RequireAuthFunc
// only (never WithAccessToken -- see main.go's route registration comment):
// WithAccessToken redirects (or sends HX-Redirect + 401) on a stale token,
// which would corrupt an established SSE stream; the token is re-acquired
// per delivery inside the fragment (deploymentRowFragment.Render) instead.
func (app *App) handleDeploymentsLiveSSE(w http.ResponseWriter, r *http.Request) {
	if app.sseHub == nil {
		http.Error(w, "live updates unavailable", http.StatusServiceUnavailable)
		return
	}

	// Per-request cancellable context so a terminal auth failure inside the
	// fragment (session gone) can end the stream via cancel(), while a
	// transient failure (token refresh failed) leaves the stream open.
	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()
	r = r.WithContext(ctx)

	// Topic set (FR7): exactly the SGCs handleSessions would render for the
	// request's selected-server scope, via the helper shared with it
	// (handlers_sessions.go) so the two can't drift.
	servers, err := app.grpc.ListServers(ctx)
	if err != nil {
		log.Printf("WARNING: error fetching servers for live deployment stream: %v", err)
		servers = []*manmanpb.Server{}
	}

	_, serverConfigs, err := app.resolveScopedServerGameConfigs(ctx, r, servers)
	if err != nil {
		log.Printf("WARNING: error fetching server configs for live deployment stream: %v", err)
	}

	authorized := make(map[int64]struct{}, len(serverConfigs))
	topics := make([]string, 0, len(serverConfigs))
	for _, sgc := range serverConfigs {
		authorized[sgc.ServerGameConfigId] = struct{}{}
		topics = append(topics, events.TopicForDeployment(sgc.ServerGameConfigId))
	}

	// No selected-server scope or no SGCs in it: htmxsse.Handler panics on
	// an empty topic list, so there is nothing to subscribe to. Degrade the
	// same way an unavailable hub does (NFR8) rather than panic; the
	// client's reconnect loop retries once a scope exists. Shared with
	// handleActivityLiveSSE's fleet-wide derivation via requireLiveTopics
	// (handlers_live_common.go, #2277) so this guard can't fork.
	if !requireLiveTopics(w, topics, "no deployments to stream for the selected server") {
		return
	}

	log.Printf("INFO: live deployment stream opened: %d topics", len(topics))

	fragment := templadapter.Adapt(func(req *http.Request, topic string) templ.Component {
		return deploymentRowFragment{
			r:          req,
			topic:      topic,
			cancel:     cancel,
			app:        app,
			authorized: authorized,
		}
	})

	htmxsse.Handler(app.sseHub, topics, fragment)(w, r)

	log.Printf("INFO: live deployment stream closed: %d topics", len(topics))
}

// deploymentRowFragment renders one deployment row's <td>s for a single SSE
// delivery (FR6, FR8, NFR11). It implements templ.Component so
// templadapter.Adapt can turn it into an htmxsse.Fragment; a fresh value is
// built per delivery (see handleDeploymentsLiveSSE's componentFunc closure)
// so Render always re-acquires credentials and re-derives state rather than
// caching either (FR3, FR27-equivalent).
type deploymentRowFragment struct {
	r      *http.Request
	topic  string
	cancel context.CancelFunc
	app    *App
	// authorized is the connection's topic-set SGC ids (handleDeploymentsLiveSSE).
	// Render refuses to draw a row for any sgcID not in this set, in case a
	// hub-internal bug ever delivered an event this connection didn't
	// subscribe to (defence in depth for FR7).
	authorized map[int64]struct{}
}

// Render implements templ.Component. An error is always treated by
// htmxsse.Handler as transient (no bytes written for this delivery, stream
// stays open, per FR8) *unless* deploymentRowFragment.cancel was already
// called to end the stream for a terminal (session-gone) failure.
func (f deploymentRowFragment) Render(ctx context.Context, w io.Writer) error {
	sgcID, ok := events.ParseDeploymentTopic(f.topic)
	if !ok {
		// Transient: unparseable/unknown topic shape -- FR8, keep the
		// stream open, write nothing for this delivery.
		return fmt.Errorf("unparseable deployment topic %q", f.topic)
	}

	if _, isAuthorized := f.authorized[sgcID]; !isAuthorized {
		// Defence in depth for FR7: never render a row for an SGC this
		// connection wasn't subscribed to.
		return fmt.Errorf("sgc %d not authorized for this connection", sgcID)
	}

	// Re-acquire the access token on every delivery (mirrors
	// tools/app_registry/ui/handlers_sse.go's
	// renderPromoDetailsFragmentComponent).
	token, err := f.app.auth.GetAccessToken(f.r)
	if err != nil {
		if _, checkErr := f.app.auth.CurrentUser(f.r); checkErr != nil {
			// Terminal: session is gone, end the stream.
			f.cancel()
			return fmt.Errorf("session lost: %w", checkErr)
		}
		// Transient: credential refresh failed, session intact.
		return fmt.Errorf("token refresh failed: %w", err)
	}

	grpcCtx := grpcauth.WithUserToken(f.r.Context(), token)

	data, err := f.app.buildDeploymentRowData(grpcCtx, sgcID)
	if err != nil {
		// A single row failing (control-api down, SGC deleted) skips just
		// this delivery -- FR8, the rest of the connection keeps working.
		log.Printf("WARNING: live deployment row skipped for sgc %d: %v", sgcID, err)
		return fmt.Errorf("build deployment row data for sgc %d: %w", sgcID, err)
	}

	// DeploymentRowInner, not DeploymentRow: the swap target is the
	// existing <tr id="deployment-row-<sgcID>"> rendered by the page, and
	// htmx's SSE extension swaps innerHTML, so the fragment must be the
	// <td>s only (NFR11: also byte-stable for unchanged state -- see
	// DeploymentRowInner's own doc comment for the audit this relies on).
	// ActionError stays empty: a pushed update is not an action attempt.
	return pages.DeploymentRowInner(data).Render(ctx, w)
}
