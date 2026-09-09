package main

import (
	"context"
	"fmt"
	"io"
	"net/http"

	"github.com/a-h/templ"
	"github.com/google/uuid"

	"github.com/whale-net/everything/libs/go/grpcauth"
	"github.com/whale-net/everything/libs/go/htmxsse"
	"github.com/whale-net/everything/libs/go/htmxsse/templadapter"
	"github.com/whale-net/everything/libs/go/logging"
	"github.com/whale-net/everything/whagent_net/events"
	"github.com/whale-net/everything/whagent_net/ui/components"
)

// sessionUsageTopics computes the htmxsse topic set for sessionID's usage
// panel SSE stream (FR4, issue #2248): assistant_message and capped only --
// deliberately narrower than sessionEventTopics' (handlers_session_live.go)
// full session-lifecycle-plus-tool-call set. turn_commit.go's CommitTurn is
// the *only* write path that inserts a turn_usage row, and it does so
// exactly once per committed assistant_message event (never on
// user_message/tool_call/tool_result/failure) -- so assistant_message is
// the one event type a fresh GetSessionUsage read can actually reflect
// something new for. capped is included too even though it adds no
// turn_usage row of its own, so the "which cap tripped" label
// (components.SessionUsage's usageCapLabel, sourced from GetSession's
// cap_kind) swaps into the panel the same moment the state badge and
// terminal banner do, rather than lagging until the next heartbeat.
// Subscribing to the remaining topics would only add SSE churn with
// nothing new for this panel to show.
func sessionUsageTopics(sessionID uuid.UUID) []string {
	return []string{
		events.RoutingKey(sessionID, events.EventTypeAssistantMessage),
		events.RoutingKey(sessionID, events.EventTypeCapped),
	}
}

// handleSessionUsageEvents is the SSE endpoint backing the session detail
// page's live usage panel (FR4, issue #2248). Mounted at
// GET /sessions/{id}/usage-events, wrapped with RequireAuthFunc only --
// never WithAccessToken -- mirrors handlers_session_live.go's
// handleSessionEvents: WithAccessToken redirects on a stale token, which
// would corrupt an established SSE stream, so the token is re-acquired per
// delivery inside sessionUsageFragment.Render instead. Reuses the same
// *htmxsse.Hub (app.sseHub) handleSessionEvents does -- the same "hub
// subscription" issue #2248's Implementation section calls for -- as a
// second, independently-topic-scoped connection, not a shared one:
// htmx's SSE extension swaps an element with the literal payload of the
// named event it is listening for, so #transcript and #usage-panel (two
// differently-rendered regions) cannot both swap correctly off one
// delivery without an out-of-band swap this codebase does not otherwise
// use -- a second connection with its own narrower topic set
// (sessionUsageTopics) is the simpler, explicit alternative, and still
// only ever the same underlying `whagent/events` bus and Hub instance.
func (app *App) handleSessionUsageEvents(w http.ResponseWriter, r *http.Request) {
	logger := logging.Get("main")

	if app.sseHub == nil {
		http.Error(w, "live updates unavailable", http.StatusServiceUnavailable)
		return
	}

	sessionID, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		http.Error(w, "invalid session id", http.StatusBadRequest)
		return
	}

	// Per-request cancellable context so a terminal auth failure inside
	// the fragment (session gone) can end the stream via cancel(), while a
	// transient failure (token refresh failed) leaves the stream open --
	// mirrors handlers_session_live.go's handleSessionEvents.
	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()
	r = r.WithContext(ctx)

	topics := sessionUsageTopics(sessionID)

	fragment := templadapter.Adapt(func(req *http.Request, topic string) templ.Component {
		return sessionUsageFragment{
			app:       app,
			r:         req,
			sessionID: sessionID,
			cancel:    cancel,
		}
	})

	logger.Info("session usage live stream opened", "session_id", sessionID, "topics", len(topics))
	htmxsse.Handler(app.sseHub, topics, fragment)(w, r)
	logger.Info("session usage live stream closed", "session_id", sessionID)
}

// sessionUsageFragment renders the usage panel's current state for a
// single SSE delivery (FR4, NFR5). Unlike sessionTranscriptFragment
// (handlers_session_live.go), it keeps no per-connection accumulated
// state: app.readUsage's GetSessionUsage-backed response is already the
// full, authoritative summed-rows figure on every call (NFR5), so there is
// nothing to merge or dedup client-side -- a re-published event on a
// subscribed topic simply re-reads the identical current figure, never a
// doubled one. That is precisely the property the Testing section's
// red/green note exercises: accumulating from the delivered event's
// payload instead (a running counter) is the violation this shape avoids
// by construction, not by explicit dedup bookkeeping.
type sessionUsageFragment struct {
	app       *App
	r         *http.Request
	sessionID uuid.UUID
	cancel    context.CancelFunc
}

// Render implements templ.Component. An error is always treated by
// htmxsse.Handler as transient (no bytes written for this delivery, stream
// stays open) unless f.cancel was already called to end the stream for a
// terminal (session-gone) failure -- mirrors sessionTranscriptFragment.Render.
func (f sessionUsageFragment) Render(ctx context.Context, w io.Writer) error {
	token, err := f.app.auth.GetAccessToken(f.r)
	if err != nil {
		if _, checkErr := f.app.auth.CurrentUser(f.r); checkErr != nil {
			f.cancel()
			return fmt.Errorf("session lost: %w", checkErr)
		}
		return fmt.Errorf("token refresh failed: %w", err)
	}
	grpcCtx := grpcauth.WithUserToken(f.r.Context(), token)

	// Read the session alongside its usage on every delivery, not just at
	// connect, so the capped-cap-tripped label (components.SessionUsage's
	// usageCapLabel) swaps in lockstep with the figures it annotates --
	// mirrors sessionTranscriptFragment.Render's own re-read of the session
	// for the state badge.
	sessionView, err := f.app.readSession(grpcCtx, f.sessionID)
	if err != nil {
		return fmt.Errorf("read session %s: %w", f.sessionID, err)
	}

	usageView, err := f.app.readUsage(grpcCtx, f.sessionID)
	if err != nil {
		return fmt.Errorf("read session usage %s: %w", f.sessionID, err)
	}

	return components.SessionUsage(sessionView, usageView).Render(ctx, w)
}
