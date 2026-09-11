package tools

import (
	"context"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	pb "github.com/whale-net/everything/whagent_net/protos"
)

// ReadTranscriptInput is read_transcript's argument schema (issue #2120,
// FR2). FromSeq/Limit mirror pb.ReadTranscriptRequest's pagination
// exactly: FromSeq is inclusive, 0 (the default/omitted) reads from the
// beginning; Limit may be capped server-side.
type ReadTranscriptInput struct {
	SessionID string `json:"session_id" jsonschema:"The session whose transcript to read, as a UUID string"`
	FromSeq   int64  `json:"from_seq,omitempty" jsonschema:"Inclusive resume point; 0 or omitted reads from the beginning"`
	Limit     int32  `json:"limit,omitempty" jsonschema:"Maximum events to return; 0 or omitted uses the server default"`
}

// TranscriptEventOutput is one transcript event, mirroring
// pb.TranscriptEvent's wire shape (FR2): Payload is carried verbatim
// (JSON body, per-type shape) so no consumer needs a second call to
// interpret an event.
type TranscriptEventOutput struct {
	EventID     string `json:"event_id" jsonschema:"The event's id"`
	Seq         int64  `json:"seq" jsonschema:"The event's commit-order sequence number"`
	Turn        int32  `json:"turn" jsonschema:"The turn this event belongs to"`
	Type        string `json:"type" jsonschema:"The event's type (user turn, model message, tool call, tool result, or session failure)"`
	Payload     string `json:"payload" jsonschema:"The event's JSON payload, verbatim"`
	CommittedAt string `json:"committed_at" jsonschema:"RFC3339 timestamp this event was committed"`
}

// ReadTranscriptOutput is read_transcript's structured result: events in
// commit order (FR2), plus next_from_seq to resume exactly after the
// last event returned -- no gap, no duplicate (mirrors
// pb.ReadTranscriptResponse).
type ReadTranscriptOutput struct {
	Events      []TranscriptEventOutput `json:"events" jsonschema:"Transcript events in commit (seq) order"`
	NextFromSeq int64                   `json:"next_from_seq" jsonschema:"Pass back as from_seq on the next call to resume exactly after the last event returned here"`
}

// readTranscriptTool holds the SessionService client this tool is a
// pass-through to.
//
// domainResolver/grant are FR7/FR8's dispatch-time resolution seams
// (domain.go, grant.go), injected here by issue #2430's Scaffold phase.
// call does not use them yet -- resolving in.SessionID's domain via
// DomainForSession and acquiring a token via grant.TokenSource before
// forwarding is this same issue's Implementation phase.
type readTranscriptTool struct {
	client         pb.SessionServiceClient
	domainResolver DomainResolver
	grant          GrantSource
}

// RegisterReadTranscript registers the read_transcript tool on srv.
func RegisterReadTranscript(srv *mcp.Server, client pb.SessionServiceClient, domainResolver DomainResolver, grant GrantSource) {
	t := &readTranscriptTool{client: client, domainResolver: domainResolver, grant: grant}
	mcp.AddTool(srv, &mcp.Tool{
		Name:        "read_transcript",
		Description: "Read a whagent-net session's transcript events in commit order, paginated by from_seq/limit (FR2). Works for a running or ended session.",
	}, t.call)
}

// call is a direct pass-through to t.client.ReadTranscript -- no
// business logic -- forwarding the caller's bearer token via ctx exactly
// as ../server/auth.go's AuthMiddleware placed it there. Events come
// back from api already in commit (seq) order (FR2); this method
// preserves that order rather than re-sorting.
func (t *readTranscriptTool) call(ctx context.Context, req *mcp.CallToolRequest, in ReadTranscriptInput) (*mcp.CallToolResult, ReadTranscriptOutput, error) {
	resp, err := t.client.ReadTranscript(ctx, &pb.ReadTranscriptRequest{
		SessionId: in.SessionID,
		FromSeq:   in.FromSeq,
		Limit:     in.Limit,
	})
	if err != nil {
		return nil, ReadTranscriptOutput{}, toolError("ReadTranscript", err)
	}

	events := make([]TranscriptEventOutput, 0, len(resp.GetEvents()))
	for _, ev := range resp.GetEvents() {
		var committedAt string
		if ts := ev.GetCommittedAt(); ts != nil {
			committedAt = ts.AsTime().Format(time.RFC3339)
		}
		events = append(events, TranscriptEventOutput{
			EventID:     ev.GetEventId(),
			Seq:         ev.GetSeq(),
			Turn:        ev.GetTurn(),
			Type:        ev.GetType(),
			Payload:     string(ev.GetPayload()),
			CommittedAt: committedAt,
		})
	}

	return nil, ReadTranscriptOutput{
		Events:      events,
		NextFromSeq: resp.GetNextFromSeq(),
	}, nil
}
