package handlers

import (
	pb "github.com/whale-net/everything/whagent_net/protos"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// StreamEvents is FR5/C17's server-streaming bridge over the
// `whagent/events` exchange (NFR4, LB7, issue #2239, ARCHITECTURE.md
// "Event bus"): a programmatic client follows a session's events live
// while holding only a Keycloak token, never RabbitMQ credentials. This
// is the first streaming RPC on SessionService --
// RequireClaimsStreamInterceptor (auth.go) already authenticates it the
// same unconditional way RequireClaimsUnaryInterceptor authenticates
// every unary RPC, so no interceptor wiring is needed here.
//
// Scaffolded as UNIMPLEMENTED; the Implementation phase fills in:
//  1. resolve the session (NOT_FOUND unknown; same "any authenticated
//     caller" read-authorization rule as ReadTranscript -- no second
//     rule),
//  2. backfill from session.TranscriptStore.Read starting at
//     req.GetFromSeq(),
//  3. live tail from s.eventsConsumer's shared exchange subscription,
//     filtered to this session's routing keys (events.RoutingKey),
//  4. emit strictly in seq order, deduplicating on event_id (NFR4/LB1)
//     with a small reorder window,
//  5. terminate cleanly on a terminal transcript event, client
//     cancellation, or api shutdown -- no goroutine/queue leak per
//     stream.
func (s *SessionServer) StreamEvents(req *pb.StreamEventsRequest, stream pb.SessionService_StreamEventsServer) error {
	return status.Error(codes.Unimplemented, "StreamEvents: not implemented yet (issue #2239)")
}
