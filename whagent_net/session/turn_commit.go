package session

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/google/uuid"

	"github.com/whale-net/everything/whagent_net/events"
)

// CommitTurnParams is Store.CommitTurn's argument: the turn's model-
// response transcript event plus its usage row, committed together
// (issue #2114's CommitTurn activity, ARCHITECTURE.md "Session workflow"
// step 5). Usage.SessionID/Usage.Turn are ignored -- SessionID/Turn above
// are the single source of truth for both writes.
type CommitTurnParams struct {
	SessionID uuid.UUID
	Turn      int
	EventType string
	Payload   json.RawMessage
	Usage     TurnUsage
}

// CommitTurn atomically appends the turn's model-response transcript event
// and records its usage row, in one transaction guarded by the same
// per-session advisory lock Append/AppendIfAbsent use (transcript.go) --
// this is the durable boundary issue #2114's CommitTurn activity relies on
// for retry-safety (Testing phase: "a turn whose commit activity fails
// once and is retried commits exactly one set of events, no duplicated
// turn"). If a transcript_event row for (SessionID, Turn, EventType)
// already exists, this is a no-op: the paired turn_usage row was written
// in the same transaction that produced it, so it exists too, and neither
// write is repeated. Temporal's at-least-once activity execution can
// re-invoke the activity that calls this after a prior attempt's commit
// already succeeded but whose result was lost in transit (worker crash,
// RPC timeout) -- this is what makes that safe.
func (s *Store) CommitTurn(ctx context.Context, params CommitTurnParams) (events.Event, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return events.Event{}, fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback(ctx)

	if err := lockSessionTx(ctx, tx, params.SessionID); err != nil {
		return events.Event{}, err
	}

	if existing, found, err := findEventTx(ctx, tx, params.SessionID, params.Turn, params.EventType); err != nil {
		return events.Event{}, err
	} else if found {
		if err := tx.Commit(ctx); err != nil {
			return events.Event{}, fmt.Errorf("commit: %w", err)
		}
		return existing, nil
	}

	eventID, err := uuid.NewV7()
	if err != nil {
		return events.Event{}, fmt.Errorf("generate event id: %w", err)
	}
	ev, err := insertEventTx(ctx, tx, eventID, params.SessionID, params.Turn, params.EventType, params.Payload)
	if err != nil {
		return events.Event{}, err
	}

	if _, err := tx.Exec(ctx, `
		INSERT INTO turn_usage (session_id, turn, model, prompt_tokens, completion_tokens, cost_usd, cost_estimated, generation_id)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
	`,
		params.SessionID, params.Turn, params.Usage.Model, params.Usage.PromptTokens, params.Usage.CompletionTokens,
		params.Usage.CostUSD, params.Usage.CostEstimated, params.Usage.GenerationID,
	); err != nil {
		return events.Event{}, fmt.Errorf("record turn usage: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return events.Event{}, fmt.Errorf("commit: %w", err)
	}

	publish(ctx, s.pub, ev)
	return ev, nil
}
