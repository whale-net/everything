package session

import (
	"context"
	"encoding/json"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Event is a `transcript_event` row (LB1): append-only, explicitly not
// SCD2. EventID is time-ordered (UUIDv7 or equivalent), globally unique
// and stable across re-publish onto the `whagent/events` bus. Seq is a
// per-session monotonic sequence Append allocates inside the same
// transaction as the insert.
type Event struct {
	EventID     uuid.UUID
	SessionID   uuid.UUID
	Seq         int64
	Turn        int
	Type        string
	Payload     json.RawMessage
	CommittedAt time.Time
}

// TurnContext is a `turn_context` row (LB1): the ordered event-ID list a
// turn's context was built from, so a debug GetTurnContext (C24) needs no
// schema change. No dedicated store interface yet -- the worker writes
// this directly once context assembly lands; kept here as the row shape
// the schema commits to.
type TurnContext struct {
	SessionID uuid.UUID
	Turn      int
	EventIDs  []uuid.UUID
}

// TranscriptStore is the `transcript_event` table's repository interface.
type TranscriptStore interface {
	// Append allocates the next per-session seq and inserts a new event in
	// the same transaction, returning the committed Event (including its
	// assigned Seq and CommittedAt).
	Append(ctx context.Context, sessionID uuid.UUID, turn int, eventType string, payload json.RawMessage) (Event, error)
	// Read returns events for sessionID in commit order (by seq), starting
	// at fromSeq (inclusive) and returning at most limit rows -- the shape
	// ReadTranscript's pagination and resume-from-seq rely on.
	Read(ctx context.Context, sessionID uuid.UUID, fromSeq int64, limit int) ([]Event, error)
}

// transcriptStore is the Postgres-backed TranscriptStore implementation.
type transcriptStore struct{ pool *pgxpool.Pool }

var _ TranscriptStore = transcriptStore{}

func (s transcriptStore) Append(ctx context.Context, sessionID uuid.UUID, turn int, eventType string, payload json.RawMessage) (Event, error) {
	return Event{}, errNotImplemented
}

func (s transcriptStore) Read(ctx context.Context, sessionID uuid.UUID, fromSeq int64, limit int) ([]Event, error) {
	return nil, errNotImplemented
}
