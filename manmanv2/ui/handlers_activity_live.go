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

// handleActivityLiveSSE handles SSE connections for Activity's fleet-wide
// live status (FR15, NFR10, NFR6; issue #2277). Mounted at
// /api/live/activity, wrapped with RequireAuthFunc only (never
// WithAccessToken) for the same reason handleDeploymentsLiveSSE is
// (handlers_sessions_live.go's doc comment): WithAccessToken would
// redirect/401 an established SSE stream on a stale token instead of
// leaving it open for the fragment to re-acquire credentials per delivery.
//
// Unlike handleDeploymentsLiveSSE's server-scoped
// resolveScopedServerGameConfigs, the topic set here is fleet-wide
// (resolveFleetWideActivitySet, handlers_activity.go) -- the exact same
// authorized set buildActivityPageData's own ActivityPageData.LiveTopics
// derives for the page's sse-swap subscription, so the two can never drift
// (the issue's explicit authorization-parity requirement). The
// empty-topic-set 503 guard is shared with handleDeploymentsLiveSSE via
// requireLiveTopics (handlers_live_common.go) rather than forked, per the
// issue's option-(b) constraint.
func (app *App) handleActivityLiveSSE(w http.ResponseWriter, r *http.Request) {
	if app.sseHub == nil {
		http.Error(w, "live updates unavailable", http.StatusServiceUnavailable)
		return
	}

	// Per-request cancellable context so a terminal auth failure inside the
	// fragment (session gone) can end the stream via cancel(), while a
	// transient failure (token refresh failed) leaves the stream open --
	// mirrors handleDeploymentsLiveSSE.
	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()
	r = r.WithContext(ctx)

	servers, err := app.grpc.ListServers(ctx)
	if err != nil {
		log.Printf("WARNING: error fetching servers for live activity stream: %v", err)
		servers = []*manmanpb.Server{}
	}

	// Fleet-wide topic set (NFR10): every authorized SGC across every
	// server, not the request's selected-server scope
	// resolveScopedServerGameConfigs resolves for /sessions and its own
	// SSE stream.
	sgcByID, _ := app.resolveFleetWideActivitySet(ctx, servers)

	topics := make([]string, 0, len(sgcByID))
	for id := range sgcByID {
		topics = append(topics, events.TopicForDeployment(id))
	}

	if !requireLiveTopics(w, topics, "no deployments to stream fleet-wide") {
		return
	}

	log.Printf("INFO: live activity stream opened: %d topics", len(topics))

	fragment := templadapter.Adapt(func(req *http.Request, topic string) templ.Component {
		return activityLiveFragment{r: req, cancel: cancel, app: app}
	})

	htmxsse.Handler(app.sseHub, topics, fragment)(w, r)

	log.Printf("INFO: live activity stream closed: %d topics", len(topics))
}

// activityLiveFragment re-renders the fleet-wide Live/History region for
// every delivery (FR15), ignoring which specific topic fired: a session
// moving live<->terminal moves it between the two tables rather than
// updating a single row in place, so any authorized deployment's status
// change invalidates the whole region, not just one row -- see
// pages.ActivityLiveContent's doc comment for why that's the smallest
// correct swap unit here. buildActivityPageData (handlers_activity.go)
// re-derives and re-filters the fleet-wide authorized set fresh on every
// call -- the same call handleActivity itself uses for a plain page
// load -- so a delivery can never render a session outside the set this
// connection was authorized for (NFR10), even across a long-lived stream
// where the fleet-wide set has since changed.
type activityLiveFragment struct {
	r      *http.Request
	cancel context.CancelFunc
	app    *App
}

// Render implements templ.Component. Mirrors deploymentRowFragment.Render's
// credential-refresh contract (handlers_sessions_live.go): an error is
// always transient (no bytes written for this delivery, stream stays
// open) unless cancel was already called for a terminal session-gone
// failure.
func (f activityLiveFragment) Render(ctx context.Context, w io.Writer) error {
	// Re-acquire the access token on every delivery -- mirrors
	// deploymentRowFragment.Render (handlers_sessions_live.go).
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

	data := f.app.buildActivityPageData(grpcCtx, f.r)
	return pages.ActivityLiveContentInner(data).Render(ctx, w)
}
