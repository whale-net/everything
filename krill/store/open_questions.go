// This file (issue #2545, krill M2, FR6/FR7) is RevisionEventStore's
// derived open-question view -- ListOpenQuestions -- plus
// validateResolvedQuestionsOpened, the stateful half of Append's FR6
// validation.
//
// FR6 is explicit and non-negotiable: the current open-question set is a
// last-event-wins window query over revision_event's own
// open_questions_delta column (AGENTS.md § SCD2's carve-out for deriving
// an SCD2-shaped view over an append-only log -- "If a table needs a
// SCD2-shaped view over it, derive one with a window function"). There is
// NO mutable question-status table and no `resolved` boolean column
// anywhere -- do not "simplify" this into one; NFR1 restates the same
// prohibition for the current-draft/current-open-questions read paths in
// general. A later contributor who finds this query awkward should widen
// the query, not add a table.
package store

import (
	"context"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// ListOpenQuestions returns sessionID's currently open questions, ordered
// by the seq_no of the revision_event whose `opened` entry is currently
// winning for that question id (FR6).
//
// This is one SQL statement, not a fold over ListBySession's full log:
// unnest each event's open_questions_delta->'opened'/->'resolved' into
// (question_id, seq_no, action) rows, keep the last (highest seq_no) row
// per question_id, and keep only the questions whose last action is
// `opened`. An event that neither opened nor resolved anything contributes
// zero rows to the unnest, so the window only ever ranks events that
// actually touched a question -- the read never has to reconstruct or
// inspect entity_deltas, verified_against, or any other revision_event
// column for events that never mention a question at all.
func (s revisionEventStore) ListOpenQuestions(ctx context.Context, sessionID uuid.UUID) ([]OpenQuestion, error) {
	rows, err := s.pool.Query(ctx, `
		WITH touches AS (
			SELECT
				re.seq_no,
				opened.elem ->> 'question_id' AS question_id,
				COALESCE((opened.elem ->> 'blocking')::boolean, false) AS blocking,
				opened.elem ->> 'text' AS text,
				'opened' AS action
			FROM revision_event re
			CROSS JOIN LATERAL jsonb_array_elements(
				CASE WHEN jsonb_typeof(re.open_questions_delta -> 'opened') = 'array'
					THEN re.open_questions_delta -> 'opened' ELSE '[]'::jsonb END
			) AS opened(elem)
			WHERE re.session_id = $1

			UNION ALL

			SELECT
				re.seq_no,
				trim(both '"' from resolved.elem::text) AS question_id,
				false AS blocking,
				NULL AS text,
				'resolved' AS action
			FROM revision_event re
			CROSS JOIN LATERAL jsonb_array_elements(
				CASE WHEN jsonb_typeof(re.open_questions_delta -> 'resolved') = 'array'
					THEN re.open_questions_delta -> 'resolved' ELSE '[]'::jsonb END
			) AS resolved(elem)
			WHERE re.session_id = $1
		),
		ranked AS (
			SELECT
				question_id, blocking, text, seq_no, action,
				ROW_NUMBER() OVER (PARTITION BY question_id ORDER BY seq_no DESC) AS rn
			FROM touches
		)
		SELECT question_id, blocking, text, seq_no
		FROM ranked
		WHERE rn = 1 AND action = 'opened'
		ORDER BY seq_no ASC
	`, sessionID)
	if err != nil {
		return nil, fmt.Errorf("list open questions: %w", err)
	}
	defer rows.Close()

	var out []OpenQuestion
	for rows.Next() {
		var q OpenQuestion
		if err := rows.Scan(&q.QuestionID, &q.Blocking, &q.Text, &q.OpenedAtSeqNo); err != nil {
			return nil, fmt.Errorf("scan open question: %w", err)
		}
		out = append(out, q)
	}
	return out, rows.Err()
}

// validateResolvedQuestionsOpened enforces FR6's "a resolved entry naming
// a question id that was never opened in this session is a 400, not a
// silently-ignored no-op" rule. It runs inside Append's own transaction
// (after that method's row lock on the owning design_session), so it sees
// every revision_event already committed for sessionID plus delta's own
// `opened` entries -- but deliberately NOT ListOpenQuestions' currently-
// open subset, since re-resolving an already-resolved question must stay
// a no-op (Testing case 4), not a 400.
func validateResolvedQuestionsOpened(ctx context.Context, tx pgx.Tx, sessionID uuid.UUID, delta OpenQuestionsDelta) error {
	if len(delta.Resolved) == 0 {
		return nil
	}

	everOpened := make(map[string]bool, len(delta.Opened))
	for _, o := range delta.Opened {
		everOpened[o.QuestionID] = true
	}

	rows, err := tx.Query(ctx, `
		SELECT DISTINCT elem ->> 'question_id'
		FROM revision_event, jsonb_array_elements(
			CASE WHEN jsonb_typeof(open_questions_delta -> 'opened') = 'array'
				THEN open_questions_delta -> 'opened' ELSE '[]'::jsonb END
		) AS elem
		WHERE session_id = $1
	`, sessionID)
	if err != nil {
		return fmt.Errorf("check previously opened questions: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return fmt.Errorf("scan previously opened question id: %w", err)
		}
		everOpened[id] = true
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("check previously opened questions: %w", err)
	}

	for _, r := range delta.Resolved {
		if !everOpened[r] {
			return fmt.Errorf("%w: open_questions_delta.resolved names %q, which was never opened in this session", ErrInvalidRevisionEvent, r)
		}
	}
	return nil
}
