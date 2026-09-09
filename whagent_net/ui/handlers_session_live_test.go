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

	"github.com/whale-net/everything/libs/go/htmxsse"
	"github.com/whale-net/everything/libs/go/rmq"
	"github.com/whale-net/everything/whagent_net/events"
	whagentpb "github.com/whale-net/everything/whagent_net/protos"
	"github.com/whale-net/everything/whagent_net/ui/components"
)

// This file guards issue #2242's NFR4/LB1 dedup-and-order contract
// (sessionTranscriptState) and NFR2's SSE delivery pipeline
// (handleSessionEvents), mirroring
// manmanv2/ui/handlers_sessions_live_test.go's shape: a hand-rolled
// htmxsse.Transport double (fakeLiveTransport below) stands in for a real
// broker since htmxsse's own fakeTransport isn't exported for reuse
// outside package htmxsse.

// --- sessionTranscriptState (NFR4/LB1) ---------------------------------

func ev(id string, seq int64) components.TranscriptEventView {
	return components.TranscriptEventView{EventID: id, Seq: seq, Type: events.EventTypeUserMessage}
}

// TestSessionTranscriptState_DuplicateEventIDRendersOnce is the red/green
// anchor for NFR4/LB1's dedup requirement. Red/green discipline (verified
// by hand, then reverted): commenting out this method's
// `if _, dup := s.seenIDs[ev.EventID]; dup { return false }` guard (so
// merge always inserted) made this test fail with len(s.events) == 2
// instead of 1 -- restoring the guard made it pass again.
func TestSessionTranscriptState_DuplicateEventIDRendersOnce(t *testing.T) {
	s := newSessionTranscriptState([]components.TranscriptEventView{ev("e1", 1)})

	changed := s.merge(ev("e1", 1))

	require.False(t, changed, "a duplicate event_id must be a no-op")
	require.Len(t, s.events, 1, "a re-published event must never produce a duplicate transcript line")
	require.Equal(t, int64(1), s.watermark)
}

// TestSessionTranscriptState_OutOfOrderSeqInsertedNotAppended proves a
// late-arriving lower seq is inserted in seq order rather than appended
// at the tail, per LB1's "order on seq" requirement.
func TestSessionTranscriptState_OutOfOrderSeqInsertedNotAppended(t *testing.T) {
	s := newSessionTranscriptState([]components.TranscriptEventView{ev("e1", 1), ev("e3", 3)})

	changed := s.merge(ev("e2", 2))

	require.True(t, changed)
	require.Len(t, s.events, 3)
	gotSeqs := []int64{s.events[0].Seq, s.events[1].Seq, s.events[2].Seq}
	require.Equal(t, []int64{1, 2, 3}, gotSeqs, "a late-arriving lower seq must not reorder already-rendered output")
}

// TestSessionTranscriptState_WatermarkTracksHighestSeqOnly proves the
// watermark advances to the highest seq merged so far, and a
// subsequently-merged lower seq never regresses it.
func TestSessionTranscriptState_WatermarkTracksHighestSeqOnly(t *testing.T) {
	s := newSessionTranscriptState(nil)

	s.merge(ev("e1", 1))
	s.merge(ev("e3", 3))
	require.Equal(t, int64(3), s.watermark, "watermark must track the highest seq merged so far")

	s.merge(ev("e2", 2))
	require.Equal(t, int64(3), s.watermark, "a late-arriving lower seq must not regress the watermark")
}

// --- handleSessionEvents (NFR2) -----------------------------------------

// fakeLiveSessionServer is a real whagentpb.SessionServiceServer serving a
// mutable transcript/session pair -- mirrors handlers_session_test.go's
// fakeUISessionServer, duplicated rather than shared because this file
// additionally needs to mutate f.events between an initial connect and a
// simulated broker delivery (below), guarded by its own mutex.
type fakeLiveSessionServer struct {
	whagentpb.UnimplementedSessionServiceServer

	mu      sync.Mutex
	session *whagentpb.Session
	events  []*whagentpb.TranscriptEvent
}

func (f *fakeLiveSessionServer) GetSession(ctx context.Context, req *whagentpb.GetSessionRequest) (*whagentpb.GetSessionResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return &whagentpb.GetSessionResponse{Session: f.session}, nil
}

func (f *fakeLiveSessionServer) ReadTranscript(ctx context.Context, req *whagentpb.ReadTranscriptRequest) (*whagentpb.ReadTranscriptResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []*whagentpb.TranscriptEvent
	next := req.GetFromSeq()
	for _, e := range f.events {
		if e.GetSeq() < req.GetFromSeq() {
			continue
		}
		out = append(out, e)
		if e.GetSeq()+1 > next {
			next = e.GetSeq() + 1
		}
	}
	return &whagentpb.ReadTranscriptResponse{Events: out, NextFromSeq: next}, nil
}

func (f *fakeLiveSessionServer) appendEvent(e *whagentpb.TranscriptEvent) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.events = append(f.events, e)
}

// fakeLiveTransport is a hand-rolled htmxsse.Transport double: it captures
// the handler htmxsse.Hub registers and lets a test invoke it directly to
// simulate a broker delivery, without a real RabbitMQ connection. Mirrors
// manmanv2/ui/handlers_sessions_live_test.go's identically-named type.
type fakeLiveTransport struct {
	mu      sync.Mutex
	handler rmq.MessageHandler
}

func (f *fakeLiveTransport) BindExchange(exchange string, routingKeys []string) error {
	return nil
}

func (f *fakeLiveTransport) RegisterHandler(queue string, handler rmq.MessageHandler) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.handler = handler
}

func (f *fakeLiveTransport) Start(ctx context.Context) error {
	return nil
}

func (f *fakeLiveTransport) Close() error {
	return nil
}

func (f *fakeLiveTransport) deliver(routingKey string) {
	f.mu.Lock()
	h := f.handler
	f.mu.Unlock()
	if h == nil {
		return
	}
	_ = h(context.Background(), rmq.Message{RoutingKey: routingKey})
}

func waitForLiveHandlerRegistered(t *testing.T, transport *fakeLiveTransport, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		transport.mu.Lock()
		ready := transport.handler != nil
		transport.mu.Unlock()
		if ready {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatalf("transport handler was never registered (hub failed to attach)")
}

func newLiveTestHub(transport *fakeLiveTransport) *htmxsse.Hub {
	config := htmxsse.DefaultConfig()
	config.ExchangeName = events.ExchangeName
	config.SubscriberBufferDepth = 10
	// Large so real-time heartbeat noise doesn't interleave with this
	// test's assertions.
	config.HeartbeatInterval = time.Hour
	return htmxsse.NewHub(func(ctx context.Context) (htmxsse.Transport, error) {
		return transport, nil
	}, config)
}

// TestHandleSessionEvents_PublishedEventProducesExactlyOneSwap is issue
// #2242's Testing-section SSE handler test: "a published event produces
// exactly one swap for a subscribed topic".
func TestHandleSessionEvents_PublishedEventProducesExactlyOneSwap(t *testing.T) {
	sessionID := uuid.New()
	server := &fakeLiveSessionServer{
		session: &whagentpb.Session{SessionId: sessionID.String(), State: whagentpb.SessionState_SESSION_STATE_RUNNING},
	}
	app := &App{
		auth:    devModeAuthenticator(t),
		session: newBufconnUISessionClient(t, server),
	}
	transport := &fakeLiveTransport{}
	app.sseHub = newLiveTestHub(transport)
	defer app.sseHub.Close()

	req := httptest.NewRequest(http.MethodGet, "/sessions/"+sessionID.String()+"/events", nil)
	req.SetPathValue("id", sessionID.String())
	ctx, cancel := context.WithCancel(req.Context())
	req = req.WithContext(ctx)
	w := httptest.NewRecorder()

	done := make(chan struct{})
	go func() {
		app.handleSessionEvents(w, req)
		close(done)
	}()

	waitForLiveHandlerRegistered(t, transport, time.Second)
	time.Sleep(30 * time.Millisecond) // let the connect-time fragment flush

	topic := events.RoutingKey(sessionID, events.EventTypeUserMessage)
	server.appendEvent(&whagentpb.TranscriptEvent{
		EventId: "e1",
		Seq:     1,
		Type:    events.EventTypeUserMessage,
		Payload: []byte(`{"role":"user","content":"hi"}`),
	})
	transport.deliver(topic)
	time.Sleep(50 * time.Millisecond)

	cancel()
	<-done

	body := w.Body.String()
	if got := strings.Count(body, "event: "+topic+"\n"); got != 2 { // connect (empty transcript) + the delivered event
		t.Errorf("expected exactly 2 swaps for topic %s (connect + one delivered event), got %d in body %q", topic, got, body)
	}
}
