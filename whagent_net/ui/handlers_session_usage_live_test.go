package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/whale-net/everything/whagent_net/events"
	whagentpb "github.com/whale-net/everything/whagent_net/protos"
)

// This file guards issue #2248's Testing section, second bullet ("Live
// path"): publishing a usage-relevant event triggers exactly one panel
// swap and the swapped fragment reflects the re-read figure, and a
// duplicate re-publish of the same event_id changes no number -- mirrors
// handlers_session_live_test.go's shape (fakeLiveTransport,
// newLiveTestHub, waitForLiveHandlerRegistered are all defined there and
// reused verbatim, package-private to package main).

// fakeUsageSessionServer is a real whagentpb.SessionServiceServer serving
// a mutable (session, usage) pair -- mirrors handlers_session_live_test.go's
// fakeLiveSessionServer, but for GetSession/GetSessionUsage (the two RPCs
// sessionUsageFragment.Render calls) rather than
// GetSession/ReadTranscript.
type fakeUsageSessionServer struct {
	whagentpb.UnimplementedSessionServiceServer

	mu      sync.Mutex
	session *whagentpb.Session
	usage   *whagentpb.SessionUsage
}

func (f *fakeUsageSessionServer) GetSession(ctx context.Context, req *whagentpb.GetSessionRequest) (*whagentpb.GetSessionResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return &whagentpb.GetSessionResponse{Session: f.session}, nil
}

func (f *fakeUsageSessionServer) GetSessionUsage(ctx context.Context, req *whagentpb.GetSessionUsageRequest) (*whagentpb.GetSessionUsageResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return &whagentpb.GetSessionUsageResponse{Usage: f.usage}, nil
}

func (f *fakeUsageSessionServer) setUsage(u *whagentpb.SessionUsage) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.usage = u
}

// startUsageStream connects to handleSessionUsageEvents for sessionID over
// a real *httptest.ResponseRecorder, waits for its handler to register on
// transport, and lets the connect-time fragment flush -- shared setup for
// both tests below.
func startUsageStream(t *testing.T, app *App, transport *fakeLiveTransport, sessionID uuid.UUID) (*httptest.ResponseRecorder, func()) {
	t.Helper()

	req := httptest.NewRequest(http.MethodGet, "/sessions/"+sessionID.String()+"/usage-events", nil)
	req.SetPathValue("id", sessionID.String())
	ctx, cancel := context.WithCancel(req.Context())
	req = req.WithContext(ctx)
	w := httptest.NewRecorder()

	done := make(chan struct{})
	go func() {
		app.handleSessionUsageEvents(w, req)
		close(done)
	}()

	waitForLiveHandlerRegistered(t, transport, time.Second)
	time.Sleep(30 * time.Millisecond) // let the connect-time fragment flush

	stop := func() {
		cancel()
		<-done
	}
	return w, stop
}

// TestHandleSessionUsageEvents_PublishedEventProducesExactlyOneSwap is
// issue #2248's Testing-section SSE handler test: "publishing a
// usage-relevant event triggers exactly one panel swap and the swapped
// fragment reflects the re-read figure."
func TestHandleSessionUsageEvents_PublishedEventProducesExactlyOneSwap(t *testing.T) {
	sessionID := uuid.New()
	server := &fakeUsageSessionServer{
		session: &whagentpb.Session{SessionId: sessionID.String(), State: whagentpb.SessionState_SESSION_STATE_RUNNING},
		usage:   &whagentpb.SessionUsage{TurnsUsed: 1, TurnCap: 10, CostUsd: 0.5, CostCapUsd: 5},
	}
	app := &App{
		auth:    devModeAuthenticator(t),
		session: newBufconnUISessionClient(t, server),
	}
	transport := &fakeLiveTransport{}
	app.sseHub = newLiveTestHub(transport)
	defer app.sseHub.Close()

	w, stop := startUsageStream(t, app, transport, sessionID)

	// The figure changes between connect and the delivered event -- the
	// re-read must reflect the new value, not the one baked in at connect.
	server.setUsage(&whagentpb.SessionUsage{TurnsUsed: 2, TurnCap: 10, CostUsd: 1, CostCapUsd: 5})

	topic := events.RoutingKey(sessionID, events.EventTypeAssistantMessage)
	transport.deliver(topic)
	time.Sleep(50 * time.Millisecond)

	stop()

	body := w.Body.String()
	if got := strings.Count(body, "event: "+topic+"\n"); got != 2 { // connect (1/10) + the delivered event (2/10)
		t.Fatalf("expected exactly 2 swaps for topic %s (connect + one delivered event), got %d in body %q", topic, got, body)
	}
	if !strings.Contains(body, "2 / 10") {
		t.Errorf("expected the re-read turns figure %q in the delivered swap, got %q", "2 / 10", body)
	}
	if !strings.Contains(body, "$1.00 / $5.00") {
		t.Errorf("expected the re-read cost figure %q in the delivered swap, got %q", "$1.00 / $5.00", body)
	}
}

// TestHandleSessionUsageEvents_DuplicateRepublishDoesNotChangeNumber is
// issue #2248's Testing-section "a duplicate re-publish of the same
// event_id produces ... no changed number" case, and this task's red/green
// anchor for NFR5 (see the doc comment below for the hand-verified
// red/green run).
//
// The event bus (whagent/events over RabbitMQ) is at-least-once, so the
// same committed event_id can be re-delivered on the same routing key; the
// Hub itself (libs/go/htmxsse/hub.go) has no dedup of its own, so
// transport.deliver(topic) below simulates that re-delivery directly, the
// same way a real re-published message would reach this connection twice.
// Unlike the transcript path (sessionTranscriptState's seenIDs set),
// sessionUsageFragment.Render keeps no per-connection state to dedup
// against -- NFR5 is satisfied by construction (every delivery re-reads
// GetSessionUsage's authoritative summed figure, never merges or
// accumulates from what was delivered), so the two deliveries below
// simply read the same server-side value twice and must render the same
// number both times, not a doubled one.
//
// Red/green discipline (verified by hand, then reverted): temporarily
// changing handlers_session_usage_live.go's sessionUsageFragment.Render to
// accumulate into a package-level running total
// (`usageAccumulatedCostUSD += usageView.CostUsedUSD;
// usageView.CostUsedUSD = usageAccumulatedCostUSD`) instead of using the
// freshly re-read value as-is made this test fail -- the second delivery's
// swap showed "$5.00 / $5.00" (2x $2.50, the accumulated total) instead of
// "$2.50 / $5.00" -- restoring the direct re-read made it pass again.
func TestHandleSessionUsageEvents_DuplicateRepublishDoesNotChangeNumber(t *testing.T) {
	sessionID := uuid.New()
	server := &fakeUsageSessionServer{
		session: &whagentpb.Session{SessionId: sessionID.String(), State: whagentpb.SessionState_SESSION_STATE_RUNNING},
		usage:   &whagentpb.SessionUsage{TurnsUsed: 1, TurnCap: 10, CostUsd: 0.5, CostCapUsd: 5},
	}
	app := &App{
		auth:    devModeAuthenticator(t),
		session: newBufconnUISessionClient(t, server),
	}
	transport := &fakeLiveTransport{}
	app.sseHub = newLiveTestHub(transport)
	defer app.sseHub.Close()

	w, stop := startUsageStream(t, app, transport, sessionID)

	server.setUsage(&whagentpb.SessionUsage{TurnsUsed: 5, TurnCap: 10, CostUsd: 2.5, CostCapUsd: 5})

	topic := events.RoutingKey(sessionID, events.EventTypeAssistantMessage)
	transport.deliver(topic) // first delivery of this event_id
	time.Sleep(30 * time.Millisecond)
	transport.deliver(topic) // re-published duplicate of the same event_id
	time.Sleep(30 * time.Millisecond)

	stop()

	body := w.Body.String()
	require.Equal(t, 2, strings.Count(body, "5 / 10"), "turns figure must read the same value on both the first and the duplicate delivery, body %q", body)
	require.Equal(t, 0, strings.Count(body, "10 / 10"), "turns figure must never be doubled by a duplicate re-publish, body %q", body)
	require.Equal(t, 2, strings.Count(body, "$2.50 / $5.00"), "cost figure must read the same authoritative value on both deliveries, body %q", body)
	require.Equal(t, 0, strings.Count(body, "$5.00 / $5.00"), "cost figure must never be doubled by a duplicate re-publish, body %q", body)
}
