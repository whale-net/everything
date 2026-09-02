package store

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// AppendVerdictInput is the input to VerdictStore.Append.
type AppendVerdictInput struct {
	IdeaID         uuid.UUID
	Verdict        VerdictValue
	Reasoning      string
	AuthorPersonID uuid.UUID
	IdempotencyKey string
	// CitedResearchNoteIDs populates verdict_citation (FR11) for this
	// version, in the same transaction as the insert.
	CitedResearchNoteIDs []uuid.UUID
}

// ErrVerdictNotViable is returned by ScheduleStore.SaveDraft when the
// referenced Verdict's Verdict field is not VerdictViable (FR16).
var ErrVerdictNotViable = errors.New("referenced verdict is not viable")

// VerdictStore covers `viability_verdict` and `verdict_citation`
// (migration 002, FR11-FR13). viability_verdict is an append-only version
// log (FR12) -- no method here, or anywhere else in this package, may
// UPDATE an existing viability_verdict row.
type VerdictStore interface {
	// Append allocates version = max+1 for in.IdeaID in one transaction
	// and inserts a new viability_verdict row plus its verdict_citation
	// rows. Never UPDATEs an existing row (FR12).
	Append(ctx context.Context, in AppendVerdictInput) (Verdict, error)

	// GetByID returns the Verdict for id (a specific version), or an error
	// if none exists. Backs save_viability_verdict's WriteRender step
	// (mcp/tools/verdict.go), which always re-reads from Postgres rather
	// than caching what Append returned (LB4 -- see
	// server.RegisterWrite's doc).
	GetByID(ctx context.Context, id uuid.UUID) (Verdict, error)

	// Current returns the highest-version Verdict for ideaID (i.e.
	// v_current_verdict's row for that idea), or an error if ideaID has no
	// verdict yet.
	Current(ctx context.Context, ideaID uuid.UUID) (Verdict, error)

	// History returns every Verdict for ideaID, ordered by version
	// ascending (FR13).
	History(ctx context.Context, ideaID uuid.UUID) ([]Verdict, error)
}

// verdictStore implements VerdictStore against `viability_verdict` and
// `verdict_citation` (migration 002).
type verdictStore struct{ pool *pgxpool.Pool }

var _ VerdictStore = verdictStore{}

const verdictColumns = `id, idea_id, version, verdict, reasoning, author_person_id, created_at, COALESCE(idempotency_key, '')`

func scanVerdict(row pgx.Row) (Verdict, error) {
	var v Verdict
	err := row.Scan(&v.ID, &v.IdeaID, &v.Version, &v.Verdict, &v.Reasoning, &v.AuthorPersonID, &v.CreatedAt, &v.IdempotencyKey)
	return v, err
}

// pgxQueryer is satisfied by both *pgxpool.Pool and pgx.Tx -- it lets
// citedResearchNoteIDs run either against the pool directly (Current,
// History) or as part of a caller-owned transaction (Append).
type pgxQueryer interface {
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
}

// citedResearchNoteIDs returns FR11's cited-note list for verdictID, read
// from verdict_citation.
func citedResearchNoteIDs(ctx context.Context, q pgxQueryer, verdictID uuid.UUID) ([]uuid.UUID, error) {
	rows, err := q.Query(ctx, `
		SELECT research_note_id FROM verdict_citation WHERE verdict_id = $1 ORDER BY research_note_id
	`, verdictID)
	if err != nil {
		return nil, fmt.Errorf("list verdict_citation: %w", err)
	}
	defer rows.Close()

	var ids []uuid.UUID
	for rows.Next() {
		var id uuid.UUID
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("scan verdict_citation: %w", err)
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list verdict_citation: %w", err)
	}
	return ids, nil
}

// Append honours IdempotencyKey (a replayed (idea, key) pair returns the
// original row, citations included, rather than allocating a new
// version), then locks the idea row so a racing Append for the same idea
// serializes its version allocation, computes version = max+1, and inserts
// the new viability_verdict row plus its verdict_citation rows -- all in
// one transaction. Never UPDATEs an existing viability_verdict row (FR12).
func (s verdictStore) Append(ctx context.Context, in AppendVerdictInput) (Verdict, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return Verdict{}, fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback(ctx)

	if in.IdempotencyKey != "" {
		existing, err := scanVerdict(tx.QueryRow(ctx, `
			SELECT `+verdictColumns+`
			FROM viability_verdict
			WHERE idea_id = $1 AND idempotency_key = $2
		`, in.IdeaID, in.IdempotencyKey))
		if err == nil {
			existing.CitedResearchNoteIDs, err = citedResearchNoteIDs(ctx, tx, existing.ID)
			if err != nil {
				return Verdict{}, err
			}
			if err := tx.Commit(ctx); err != nil {
				return Verdict{}, fmt.Errorf("commit: %w", err)
			}
			return existing, nil
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return Verdict{}, fmt.Errorf("lookup verdict by idempotency key: %w", err)
		}
	}

	if _, err := tx.Exec(ctx, `SELECT id FROM idea WHERE id = $1 FOR UPDATE`, in.IdeaID); err != nil {
		return Verdict{}, fmt.Errorf("lock idea: %w", err)
	}

	var nextVersion int
	if err := tx.QueryRow(ctx, `
		SELECT COALESCE(MAX(version), 0) + 1 FROM viability_verdict WHERE idea_id = $1
	`, in.IdeaID).Scan(&nextVersion); err != nil {
		return Verdict{}, fmt.Errorf("compute next verdict version: %w", err)
	}

	v, err := scanVerdict(tx.QueryRow(ctx, `
		INSERT INTO viability_verdict (idea_id, version, verdict, reasoning, author_person_id, idempotency_key)
		VALUES ($1, $2, $3, $4, $5, NULLIF($6, ''))
		RETURNING `+verdictColumns,
		in.IdeaID, nextVersion, in.Verdict, in.Reasoning, in.AuthorPersonID, in.IdempotencyKey))
	if err != nil {
		return Verdict{}, fmt.Errorf("insert viability_verdict: %w", err)
	}

	for _, noteID := range in.CitedResearchNoteIDs {
		if _, err := tx.Exec(ctx, `
			INSERT INTO verdict_citation (verdict_id, research_note_id) VALUES ($1, $2)
		`, v.ID, noteID); err != nil {
			return Verdict{}, fmt.Errorf("insert verdict_citation: %w", err)
		}
	}
	v.CitedResearchNoteIDs = in.CitedResearchNoteIDs

	if err := tx.Commit(ctx); err != nil {
		return Verdict{}, fmt.Errorf("commit: %w", err)
	}
	return v, nil
}

// GetByID returns the Verdict for id (a specific version), or an error if
// none exists.
func (s verdictStore) GetByID(ctx context.Context, id uuid.UUID) (Verdict, error) {
	v, err := scanVerdict(s.pool.QueryRow(ctx, `SELECT `+verdictColumns+` FROM viability_verdict WHERE id = $1`, id))
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return Verdict{}, pgx.ErrNoRows
		}
		return Verdict{}, fmt.Errorf("get viability_verdict by id: %w", err)
	}

	cited, err := citedResearchNoteIDs(ctx, s.pool, v.ID)
	if err != nil {
		return Verdict{}, err
	}
	v.CitedResearchNoteIDs = cited
	return v, nil
}

func (s verdictStore) Current(ctx context.Context, ideaID uuid.UUID) (Verdict, error) {
	v, err := scanVerdict(s.pool.QueryRow(ctx, `
		SELECT id, idea_id, version, verdict, reasoning, author_person_id, created_at, COALESCE(idempotency_key, '')
		FROM v_current_verdict
		WHERE idea_id = $1
	`, ideaID))
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return Verdict{}, pgx.ErrNoRows
		}
		return Verdict{}, fmt.Errorf("get current verdict: %w", err)
	}

	cited, err := citedResearchNoteIDs(ctx, s.pool, v.ID)
	if err != nil {
		return Verdict{}, err
	}
	v.CitedResearchNoteIDs = cited
	return v, nil
}

func (s verdictStore) History(ctx context.Context, ideaID uuid.UUID) ([]Verdict, error) {
	rows, err := s.pool.Query(ctx, `SELECT `+verdictColumns+` FROM viability_verdict WHERE idea_id = $1 ORDER BY version ASC`, ideaID)
	if err != nil {
		return nil, fmt.Errorf("list verdict history: %w", err)
	}
	defer rows.Close()

	var verdicts []Verdict
	for rows.Next() {
		v, err := scanVerdict(rows)
		if err != nil {
			return nil, fmt.Errorf("scan verdict: %w", err)
		}
		verdicts = append(verdicts, v)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list verdict history: %w", err)
	}

	for i := range verdicts {
		cited, err := citedResearchNoteIDs(ctx, s.pool, verdicts[i].ID)
		if err != nil {
			return nil, err
		}
		verdicts[i].CitedResearchNoteIDs = cited
	}
	return verdicts, nil
}
