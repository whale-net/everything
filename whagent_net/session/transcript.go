package session

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/whale-net/everything/whagent_net/events"
)

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
// The committed row, the RabbitMQ message body, and (later) the S3 jsonl
// line are all the same `events.Event` record (LB1) -- there is exactly
// one Go type for it, owned by whagent_net/events, and this store neither
// defines nor accepts a parallel DTO.
type TranscriptStore interface {
	// Append allocates the next per-session seq and inserts a new event in
	// the same transaction, returning the committed events.Event (including
	// its assigned Seq and CommittedAt). On success it also publishes the
	// event onto the `whagent/events` bus -- see the transcriptStore.Append
	// doc comment for the commit/publish ordering guarantee.
	Append(ctx context.Context, sessionID uuid.UUID, turn int, eventType string, payload json.RawMessage) (events.Event, error)
	// Read returns events for sessionID in commit order (by seq), starting
	// at fromSeq (inclusive) and returning at most limit rows -- the shape
	// ReadTranscript's pagination and resume-from-seq rely on.
	Read(ctx context.Context, sessionID uuid.UUID, fromSeq int64, limit int) ([]events.Event, error)
}

// transcriptStore is the Postgres-backed TranscriptStore implementation.
// pub may be nil (publisher disabled by config): Append then commits
// exactly as if pub were always present, it simply skips the publish step.
type transcriptStore struct {
	pool *pgxpool.Pool
	pub  events.PublisherInterface
}

var _ TranscriptStore = transcriptStore{}

// Append allocates eventID itself (UUIDv7, time-ordered per LB1) and
// serializes seq allocation for sessionID via a transaction-scoped
// advisory lock (pg_advisory_xact_lock, released automatically at
// commit/rollback) keyed on sessionID's hash -- this is what makes
// concurrent Appends to the SAME session produce strictly increasing,
// non-duplicate seq values (the `UNIQUE (session_id, seq)` constraint is
// the last line of defense, not the mechanism); Appends to DIFFERENT
// sessions never contend for this lock and proceed fully in parallel.
//
// Publish-after-commit (NFR2): the event is only published to pub once the
// Postgres transaction has committed, so a publish retry can re-send a
// duplicate but the bus can never see an event that was never durably
// committed. A publish failure is therefore never allowed to roll back or
// fail this call -- it is logged at WARNING (the append completed, but not
// exactly as expected; AGENTS.md § Logging Levels) and Append still
// returns the committed event successfully. Consumers dedup on EventID and
// order on Seq, so the resulting at-least-once delivery is safe.
func (s transcriptStore) Append(ctx context.Context, sessionID uuid.UUID, turn int, eventType string, payload json.RawMessage) (events.Event, error) {
	eventID, err := uuid.NewV7()
	if err != nil {
		return events.Event{}, fmt.Errorf("generate event id: %w", err)
	}

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return events.Event{}, fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback(ctx)

	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1::text, 0))`, sessionID); err != nil {
		return events.Event{}, fmt.Errorf("lock session for append: %w", err)
	}

	var ev events.Event
	err = tx.QueryRow(ctx, `
		INSERT INTO transcript_event (event_id, session_id, seq, turn, type, payload)
		SELECT $1, $2, COALESCE(MAX(seq), 0) + 1, $3, $4, $5
		FROM transcript_event
		WHERE session_id = $2
		RETURNING event_id, session_id, seq, turn, type, payload, committed_at
	`, eventID, sessionID, turn, eventType, payload).Scan(
		&ev.EventID, &ev.SessionID, &ev.Seq, &ev.Turn, &ev.Type, &ev.Payload, &ev.CommittedAt,
	)
	if err != nil {
		return events.Event{}, fmt.Errorf("append transcript event: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return events.Event{}, fmt.Errorf("commit: %w", err)
	}

	if s.pub != nil {
		if err := s.pub.Publish(ctx, ev); err != nil {
			slog.WarnContext(ctx, "publish transcript event failed; row already committed",
				"session_id", ev.SessionID,
				"event_id", ev.EventID,
				"seq", ev.Seq,
				"type", ev.Type,
				"error", err,
			)
		}
	}

	return ev, nil
}

// Read returns up to limit events for sessionID in seq order starting at
// fromSeq (inclusive) -- the shape both plain pagination and
// resume-from-seq rely on.
func (s transcriptStore) Read(ctx context.Context, sessionID uuid.UUID, fromSeq int64, limit int) ([]events.Event, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT event_id, session_id, seq, turn, type, payload, committed_at
		FROM transcript_event
		WHERE session_id = $1 AND seq >= $2
		ORDER BY seq
		LIMIT $3
	`, sessionID, fromSeq, limit)
	if err != nil {
		return nil, fmt.Errorf("read transcript events: %w", err)
	}
	defer rows.Close()

	var evs []events.Event
	for rows.Next() {
		var ev events.Event
		if err := rows.Scan(&ev.EventID, &ev.SessionID, &ev.Seq, &ev.Turn, &ev.Type, &ev.Payload, &ev.CommittedAt); err != nil {
			return nil, fmt.Errorf("scan transcript event: %w", err)
		}
		evs = append(evs, ev)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read transcript events: %w", err)
	}
	return evs, nil
}
