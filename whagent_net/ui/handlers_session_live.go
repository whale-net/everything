package main

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strconv"
	"strings"

	"github.com/a-h/templ"
	"github.com/google/uuid"

	"github.com/whale-net/everything/libs/go/grpcauth"
	"github.com/whale-net/everything/libs/go/htmxsse"
	"github.com/whale-net/everything/libs/go/htmxsse/templadapter"
	"github.com/whale-net/everything/libs/go/logging"
	"github.com/whale-net/everything/whagent_net/events"
	"github.com/whale-net/everything/whagent_net/ui/components"
)

// toolEventTopicHeadroom is how many tool_call/tool_result call indices
// beyond the highest index already observed in a session's seed transcript
// (sessionEventTopics' seed argument) an SSE connection subscribes to
// speculatively.
//
// events.RoutingKey folds a per-turn call index into the routing key
// (events.EventTypeToolCall's doc comment: the committed type is always
// "tool_call:<call_index>"/"tool_result:<call_index>", never the bare
// constant), and CallIndex resets to 0 at the start of every turn
// (whagent_net/worker/activities.go), so the same routing key recurs turn
// over turn -- but a turn that dispatches more tool calls than any turn
// seen so far mints a routing key this connection never subscribed to.
// htmxsse.Hub.Subscribe requires an exact topic match, no wildcard (see
// Hub.handleMessage's map lookup by literal routing key), so such an
// event is never pushed to this connection the instant it commits.
//
// It is not lost, though: htmxsse.Handler re-invokes the fragment
// function for every already-subscribed topic on every heartbeat tick
// (Hub's HeartbeatInterval) regardless of whether a matching message
// arrived, and sessionTranscriptFragment.Render always re-reads the
// transcript from its own cursor rather than the delivered event's body
// (see sessionTranscriptState's doc comment for why) -- so a call index
// beyond this headroom still surfaces, just on the next heartbeat instead
// of instantly. A generous headroom keeps that the rare case, not the
// common one.
const toolEventTopicHeadroom = 8

// sessionEventTopics computes the htmxsse topic set for sessionID's SSE
// stream (NFR2/LB7, "topics = this session's routing keys"): the four
// session-lifecycle event types that never carry a call index, plus
// tool_call/tool_result topics for every call index observed in seed
// extended by toolEventTopicHeadroom (see that constant's doc comment for
// why a headroom, not the exact observed set, is both necessary and
// sufficient). Shared by handleSessionDetail (the page's sse-swap
// attribute, components.SessionDetailData.Topics) and handleSessionEvents
// (the actual Subscribe calls) so the two can never drift.
func sessionEventTopics(sessionID uuid.UUID, seed []components.TranscriptEventView) []string {
	maxCallIndex := -1
	for _, ev := range seed {
		if idx, ok := toolCallIndex(ev.Type); ok && idx > maxCallIndex {
			maxCallIndex = idx
		}
	}

	topics := []string{
		events.RoutingKey(sessionID, events.EventTypeUserMessage),
		events.RoutingKey(sessionID, events.EventTypeAssistantMessage),
		events.RoutingKey(sessionID, events.EventTypeCapped),
		events.RoutingKey(sessionID, events.EventTypeFailure),
	}
	for i := 0; i <= maxCallIndex+toolEventTopicHeadroom; i++ {
		topics = append(topics,
			events.RoutingKey(sessionID, fmt.Sprintf("%s:%d", events.EventTypeToolCall, i)),
			events.RoutingKey(sessionID, fmt.Sprintf("%s:%d", events.EventTypeToolResult, i)),
		)
	}
	return topics
}

// toolCallIndex parses eventType's trailing call index for the
// "tool_call:<n>"/"tool_result:<n>" prefixes (events.go's
// EventTypeToolCall/EventTypeToolResult doc comment). ok is false for any
// other type, including the bare prefix constants themselves (never
// committed verbatim, per that doc comment) and a malformed suffix.
func toolCallIndex(eventType string) (idx int, ok bool) {
	for _, prefix := range []string{events.EventTypeToolCall, events.EventTypeToolResult} {
		rest, found := strings.CutPrefix(eventType, prefix+":")
		if !found {
			continue
		}
		n, err := strconv.Atoi(rest)
		if err != nil {
			return 0, false
		}
		return n, true
	}
	return 0, false
}

// sessionTranscriptState is one SSE connection's incrementally maintained
// transcript view (NFR4/LB1): seeded from the same ReadTranscript page
// handleSessionDetail already read for the initial full-page render, then
// advanced on every fragment invocation by reading only what is new since
// the last render (from_seq = watermark+1, app.readTranscript) and
// merging it in.
//
// htmxsse.Handler's Fragment signature (libs/go/htmxsse/handler.go) is
// not handed the delivered event's body, only the topic name that
// triggered it -- "apply this one event" is not an option, so every
// invocation re-derives the connection's current full state from the
// canonical read path. That path is itself the simplest way to guarantee
// dedup-by-event_id/order-by-seq (ReadTranscript never returns a
// duplicate row, and always in seq order); the seenIDs set and explicit
// Seq-ordered insert below are a second, defensive guarantee against a
// pathological ReadTranscript response (a retried or overlapping page),
// not something the happy path relies on -- and are what
// duplicate-event/out-of-order-seq unit tests exercise directly.
//
// Not safe for concurrent use: htmxsse.Handler invokes a given
// connection's Fragment calls sequentially from its own single event
// loop, never concurrently, so this needs no locking.
type sessionTranscriptState struct {
	events    []components.TranscriptEventView
	seenIDs   map[string]struct{}
	watermark int64 // highest seq merged so far; next read resumes at watermark+1
}

// newSessionTranscriptState seeds a state from seed (already in seq
// order, per ReadTranscript's contract).
func newSessionTranscriptState(seed []components.TranscriptEventView) *sessionTranscriptState {
	s := &sessionTranscriptState{seenIDs: make(map[string]struct{}, len(seed))}
	for _, ev := range seed {
		s.merge(ev)
	}
	return s
}

// merge inserts ev into s in Seq order if not already present (by
// EventID -- a duplicate is a no-op, never a second rendered line),
// advancing the watermark when ev.Seq exceeds it (a late-arriving lower
// Seq is inserted in place, never appended, and never regresses the
// watermark). Returns whether s.events actually changed.
func (s *sessionTranscriptState) merge(ev components.TranscriptEventView) bool {
	if _, dup := s.seenIDs[ev.EventID]; dup {
		return false
	}
	s.seenIDs[ev.EventID] = struct{}{}

	idx := sort.Search(len(s.events), func(i int) bool { return s.events[i].Seq >= ev.Seq })
	s.events = append(s.events, components.TranscriptEventView{})
	copy(s.events[idx+1:], s.events[idx:])
	s.events[idx] = ev

	if ev.Seq > s.watermark {
		s.watermark = ev.Seq
	}
	return true
}

// handleSessionEvents is the SSE endpoint backing the session detail
// page's live transcript (FR2, NFR2, NFR4, issue #2242). Mounted at
// GET /sessions/{id}/events, wrapped with RequireAuthFunc only -- never
// WithAccessToken, mirrors manmanv2/ui/handlers_sessions_live.go's
// handleDeploymentsLiveSSE: WithAccessToken redirects on a stale token,
// which would corrupt an established SSE stream, so the token is
// re-acquired per delivery inside sessionTranscriptFragment.Render
// instead.
func (app *App) handleSessionEvents(w http.ResponseWriter, r *http.Request) {
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
	// the fragment (session gone) can end the stream via cancel(), while
	// a transient failure (token refresh failed) leaves the stream open
	// -- mirrors handlers_sessions_live.go's deploymentRowFragment.
	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()
	r = r.WithContext(ctx)

	token, err := app.auth.GetAccessToken(r)
	if err != nil {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	grpcCtx := grpcauth.WithUserToken(ctx, token)

	seed, _, err := app.readTranscript(grpcCtx, sessionID, 0)
	if err != nil {
		// Degrade to an empty seed rather than failing the whole
		// connection: the transcript is still readable on the next
		// heartbeat/delivery once the transient failure clears.
		logger.Warn("failed to seed session transcript for live stream", "session_id", sessionID, "error", err)
		seed = nil
	}
	state := newSessionTranscriptState(seed)
	topics := sessionEventTopics(sessionID, seed)

	fragment := templadapter.Adapt(func(req *http.Request, topic string) templ.Component {
		return sessionTranscriptFragment{
			app:       app,
			r:         req,
			sessionID: sessionID,
			state:     state,
			cancel:    cancel,
		}
	})

	logger.Info("session live stream opened", "session_id", sessionID, "topics", len(topics))
	htmxsse.Handler(app.sseHub, topics, fragment)(w, r)
	logger.Info("session live stream closed", "session_id", sessionID)
}

// sessionTranscriptFragment renders the transcript's current full state
// for a single SSE delivery (FR2, NFR4). A fresh value is built per
// delivery (handleSessionEvents' componentFunc closure) so Render always
// re-acquires credentials, but state is shared across every delivery on
// the same connection (sessionTranscriptState's whole point).
type sessionTranscriptFragment struct {
	app       *App
	r         *http.Request
	sessionID uuid.UUID
	state     *sessionTranscriptState
	cancel    context.CancelFunc
}

// Render implements templ.Component. An error is always treated by
// htmxsse.Handler as transient (no bytes written for this delivery,
// stream stays open, per FR2/NFR4) unless f.cancel was already called to
// end the stream for a terminal (session-gone) failure -- mirrors
// manmanv2/ui/handlers_sessions_live.go's deploymentRowFragment.Render.
func (f sessionTranscriptFragment) Render(ctx context.Context, w io.Writer) error {
	token, err := f.app.auth.GetAccessToken(f.r)
	if err != nil {
		if _, checkErr := f.app.auth.CurrentUser(f.r); checkErr != nil {
			f.cancel()
			return fmt.Errorf("session lost: %w", checkErr)
		}
		return fmt.Errorf("token refresh failed: %w", err)
	}
	grpcCtx := grpcauth.WithUserToken(f.r.Context(), token)

	newEvents, _, err := f.app.readTranscript(grpcCtx, f.sessionID, f.state.watermark+1)
	if err != nil {
		return fmt.Errorf("read transcript from seq %d: %w", f.state.watermark+1, err)
	}
	for _, ev := range newEvents {
		f.state.merge(ev)
	}

	// Re-read the session's current state on every delivery, not just the
	// initial page render, so the state badge (and terminal-state banner)
	// swap alongside the transcript instead of going stale until a manual
	// refresh (Implementation: "swaps transcript fragments and the state
	// badge on each SSE event"). A failure here is transient like every
	// other error in this method -- no bytes written, previous content
	// (including the previously rendered badge) stays on screen.
	sessionView, err := f.app.readSession(grpcCtx, f.sessionID)
	if err != nil {
		return fmt.Errorf("read session %s: %w", f.sessionID, err)
	}

	return components.SessionLive(sessionView, f.state.events).Render(ctx, w)
}
