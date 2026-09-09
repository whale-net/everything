package handlers

import (
	"context"
	"time"

	pb "github.com/whale-net/everything/whagent_net/protos"
	"github.com/whale-net/everything/whagent_net/session"

	"github.com/google/uuid"
	"github.com/whale-net/everything/whagent_net/events"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// streamEventsStatusPollInterval is how often StreamEvents' live tail
// (below) re-checks the session's status and re-reads the transcript from
// its current cursor while waiting on the shared broadcaster. Two
// independent reasons need it, not one:
//
//  1. Terminal detection for a "done"/"stopped" session: unlike
//     "capped"/"failed" (events.EventTypeCapped/EventTypeFailure), those
//     two statuses commit no dedicated terminal transcript event of their
//     own (worker/workflow.go's SessionWorkflow writes the status
//     directly) -- the `sessions` row's Status is the only signal
//     available, so it must be polled.
//  2. Self-healing a gap left by the shared eventBroadcaster's
//     drop-oldest policy under a delivery burst (broadcast.go's Subscribe
//     doc comment): drainFromCursor's direct transcript.Read always fills
//     any such gap deterministically, since seq allocation is gapless per
//     session (session/transcript.go's insertEventTx).
const streamEventsStatusPollInterval = 250 * time.Millisecond

// isTerminalEventType reports whether eventType is one of the two
// dedicated terminal transcript event types (events.go's EventTypeCapped/
// EventTypeFailure doc comments) -- StreamEvents ends the stream the
// instant it emits one of these, without waiting for the next status poll.
func isTerminalEventType(eventType string) bool {
	return eventType == events.EventTypeCapped || eventType == events.EventTypeFailure
}

// StreamEvents is FR5/C17's server-streaming bridge over the
// `whagent/events` exchange (NFR4, LB7, issue #2239, ARCHITECTURE.md
// "Event bus"): a programmatic client follows a session's events live
// while holding only a Keycloak token, never RabbitMQ credentials --
// RequireClaimsStreamInterceptor (auth.go) already authenticates it the
// same unconditional way RequireClaimsUnaryInterceptor authenticates every
// unary RPC.
//
// Read access is unconditional for any authenticated caller, same as
// GetSession/ReadTranscript (session.go's GetSession doc comment,
// FR2/C14) -- there is no ownership check here either.
//
// Sequence:
//
//  1. Subscribe to the shared eventBroadcaster for this session BEFORE
//     backfilling -- never after -- so no event committed and published
//     between the last backfill Read and Subscribe attaching is ever
//     missed. A duplicate delivered through both backfill and the live
//     subscription is expected and harmless: eventSequencer dedups it.
//  2. Backfill: page through everything already committed at or after
//     from_seq (session.TranscriptStore.Read, same shape ReadTranscript's
//     own pagination uses), emitting each through the sequencer.
//  3. If the session is already terminal once backfill catches up (e.g. a
//     reconnect after the session already ended), end the stream
//     immediately.
//  4. Live tail: read from the broadcaster's channel and the periodic
//     status-poll reconciliation described on streamEventsStatusPollInterval,
//     emitting through the same sequencer, until a terminal transcript
//     event is emitted, the session's status goes terminal, the client
//     cancels (stream.Context().Done()), or eventsConsumer/broadcaster are
//     unavailable.
//
// No goroutine or queue is created per call: StreamEvents only ever reads
// from the single shared, per-process eventsConsumer/broadcaster pair
// (main.go's initializeEventsConsumer, NewSessionServer) and the deferred
// unsubscribe releases this call's slot in that fan-out on every return
// path.
func (s *SessionServer) StreamEvents(req *pb.StreamEventsRequest, stream pb.SessionService_StreamEventsServer) error {
	ctx := stream.Context()

	id, err := uuid.Parse(req.GetSessionId())
	if err != nil {
		return status.Errorf(codes.InvalidArgument, "invalid session_id: %v", err)
	}

	sess, err := s.store.Sessions().GetByID(ctx, id)
	if err != nil {
		return status.Errorf(codes.Internal, "get session: %v", err)
	}
	if sess == nil {
		return status.Error(codes.NotFound, "session not found")
	}

	if s.eventsConsumer == nil || s.broadcaster == nil {
		return status.Error(codes.Unavailable, "StreamEvents unavailable: events consumer not connected")
	}

	live, unsubscribe := s.broadcaster.Subscribe(id)
	defer unsubscribe()

	seq := newEventSequencer(req.GetFromSeq())

	// emitOne feeds ev through seq and sends every event it releases, in
	// order. Returns true once it has sent a terminal transcript event
	// (isTerminalEventType) -- the caller must stop and return nil
	// immediately, never emit anything after it.
	emitOne := func(ev events.Event) (bool, error) {
		terminal := false
		for _, ready := range seq.Feed(ev) {
			if sendErr := stream.Send(transcriptEventToProto(ready)); sendErr != nil {
				return false, sendErr
			}
			if isTerminalEventType(ready.Type) {
				terminal = true
			}
		}
		return terminal, nil
	}

	// drainFromCursor reads and emits everything already committed at or
	// after seq's current cursor, paging until caught up. Used for the
	// initial backfill and, again, every time the live-tail loop's status
	// poll fires (streamEventsStatusPollInterval's doc comment).
	drainFromCursor := func() (bool, error) {
		for {
			batch, readErr := s.store.Transcript().Read(ctx, id, seq.next, defaultTranscriptLimit)
			if readErr != nil {
				return false, status.Errorf(codes.Internal, "read transcript: %v", readErr)
			}
			if len(batch) == 0 {
				return false, nil
			}
			for _, ev := range batch {
				terminal, emitErr := emitOne(ev)
				if emitErr != nil {
					return false, emitErr
				}
				if terminal {
					return true, nil
				}
			}
			if len(batch) < defaultTranscriptLimit {
				return false, nil
			}
		}
	}

	if terminal, err := drainFromCursor(); err != nil {
		return err
	} else if terminal {
		return nil
	}

	// A session already terminal by the time backfill catches up ends the
	// stream immediately -- e.g. a reconnect after the session finished
	// with a status (done/stopped) that committed no terminal transcript
	// event of its own for drainFromCursor to have already caught above.
	if terminal, err := sessionIsTerminal(ctx, s.store.Sessions(), id); err != nil {
		return err
	} else if terminal {
		return nil
	}

	ticker := time.NewTicker(streamEventsStatusPollInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case ev, ok := <-live:
			if !ok {
				return status.Error(codes.Unavailable, "StreamEvents: events consumer disconnected")
			}
			terminal, err := emitOne(ev)
			if err != nil {
				return err
			}
			if terminal {
				return nil
			}
		case <-ticker.C:
			if terminal, err := drainFromCursor(); err != nil {
				return err
			} else if terminal {
				return nil
			}
			if terminal, err := sessionIsTerminal(ctx, s.store.Sessions(), id); err != nil {
				return err
			} else if terminal {
				return nil
			}
		}
	}
}

// sessionIsTerminal re-reads id's session row and reports whether its
// status is terminal -- a nil row (deleted) is treated as "not terminal"
// (there is no session-deletion path in M1, so this is unreachable in
// practice) rather than an error, matching GetByID's own
// nil-means-not-found contract.
func sessionIsTerminal(ctx context.Context, sessions session.SessionStore, id uuid.UUID) (bool, error) {
	sess, err := sessions.GetByID(ctx, id)
	if err != nil {
		return false, status.Errorf(codes.Internal, "get session: %v", err)
	}
	if sess == nil {
		return false, nil
	}
	return sess.Status.IsTerminal(), nil
}
