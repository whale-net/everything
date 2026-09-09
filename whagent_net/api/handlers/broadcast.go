package handlers

import (
	"context"
	"encoding/json"
	"sync"

	"github.com/google/uuid"

	"github.com/whale-net/everything/libs/go/logging"
	"github.com/whale-net/everything/libs/go/rmq"
	"github.com/whale-net/everything/whagent_net/events"
)

// eventBroadcastBufferDepth is the per-subscriber channel depth
// eventBroadcaster.Subscribe buffers before applying its drop-oldest
// policy (see Subscribe's doc comment).
const eventBroadcastBufferDepth = 64

// eventBroadcaster fans out whagent/events deliveries from the single
// shared per-process *rmq.Consumer (api/main.go's initializeEventsConsumer,
// issue #2239/C17) out to however many concurrent StreamEvents calls are
// currently tailing a session -- the same subscribe/dispatch shape
// //libs/go/htmxsse.Hub uses, keyed here on the decoded event's SessionID
// rather than the raw routing key: one session spans several routing keys
// (one per event_type, events.RoutingKey), and a StreamEvents call wants
// every one of them, not a single exact-match topic the way an SSE client
// does.
//
// HandleMessage is registered as the shared consumer's sole handler
// (main.go's initializeEventsConsumer), so it -- not StreamEvents itself --
// is the only thing that ever touches the broker connection; every
// StreamEvents call talks only to this in-process fan-out via Subscribe,
// never the broker (C17's whole point: no RPC caller ever needs broker
// credentials).
type eventBroadcaster struct {
	mu   sync.RWMutex
	subs map[uuid.UUID][]*broadcastSub
}

type broadcastSub struct {
	ch        chan events.Event
	closeOnce sync.Once
}

func (s *broadcastSub) close() {
	s.closeOnce.Do(func() { close(s.ch) })
}

// newEventBroadcaster returns an empty eventBroadcaster, ready for
// Subscribe calls and, once wired to a live *rmq.Consumer (main.go), for
// HandleMessage deliveries.
func newEventBroadcaster() *eventBroadcaster {
	return &eventBroadcaster{subs: make(map[uuid.UUID][]*broadcastSub)}
}

// Subscribe returns a channel of every subsequent live events.Event for
// sessionID, plus an unsubscribe function the caller must invoke exactly
// once (StreamEvents' deferred cleanup) to stop delivery and release the
// channel -- mirroring htmxsse.Hub.Subscribe. The channel is buffered
// (eventBroadcastBufferDepth) so one slow StreamEvents call can never block
// HandleMessage's dispatch to every other concurrent subscriber; a full
// channel drops the oldest buffered event to make room for the newest
// (same drop-oldest policy as htmxsse.Hub.handleMessage). Dropping a live
// delivery here is safe: StreamEvents' eventSequencer just holds the
// resulting gap, and its periodic status-poll reconciliation
// (stream.go's drainFromCursor) re-reads the transcript directly and
// fills it in.
func (b *eventBroadcaster) Subscribe(sessionID uuid.UUID) (<-chan events.Event, func()) {
	sub := &broadcastSub{ch: make(chan events.Event, eventBroadcastBufferDepth)}

	b.mu.Lock()
	b.subs[sessionID] = append(b.subs[sessionID], sub)
	b.mu.Unlock()

	unsubscribe := func() {
		b.mu.Lock()
		defer b.mu.Unlock()
		sub.close()

		remaining := b.subs[sessionID][:0]
		for _, s := range b.subs[sessionID] {
			if s != sub {
				remaining = append(remaining, s)
			}
		}
		if len(remaining) == 0 {
			delete(b.subs, sessionID)
		} else {
			b.subs[sessionID] = remaining
		}
	}
	return sub.ch, unsubscribe
}

// HandleMessage is an rmq.MessageHandler: it decodes msg.Body as an
// events.Event and dispatches it to every current Subscribe(SessionID)
// channel for that event's session. An undecodable body is logged at
// WARNING and dropped, never NACKed for retry -- a malformed message can
// never become decodable by requeuing it, so a retry loop over it would
// only wedge the shared queue.
func (b *eventBroadcaster) HandleMessage(ctx context.Context, msg rmq.Message) error {
	var ev events.Event
	if err := json.Unmarshal(msg.Body, &ev); err != nil {
		logging.Get("streamevents").WarnContext(ctx, "undecodable whagent/events delivery; dropped",
			"routing_key", msg.RoutingKey, "error", err)
		return nil
	}

	// Held for the whole dispatch loop, same as htmxsse.Hub.handleMessage:
	// sends below are always non-blocking, so an unsubscribe racing to
	// close a channel (which needs the write lock) can never do so out
	// from under a send in progress here.
	b.mu.RLock()
	defer b.mu.RUnlock()
	for _, sub := range b.subs[ev.SessionID] {
		select {
		case sub.ch <- ev:
		default:
			select {
			case <-sub.ch: // drop the oldest buffered event to make room
				sub.ch <- ev
			default:
			}
		}
	}
	return nil
}
