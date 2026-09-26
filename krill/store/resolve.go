// This file is resolve -- the third shape in the LB3 boundary call, and the
// only verb that can change a `kind`.
//
// amend.go closes a row and opens a successor, carrying `kind` forward.
// void.go closes a row and opens nothing. Neither can express a re-kind,
// because amend's whole contract is that it never re-kinds (FR f0f6bc18) --
// that guarantee is what lets a reader trust an amended row's `kind` to be
// the row's original kind. So PROMOTE re-implements the close-and-open
// write with exactly one difference, the changed `kind`, rather than
// widening amend and weakening the guarantee.
package store

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// ResolveOutcome is the choice a resolve operation makes. There is no
// third value: a `deferred` Non-Goal is either settled as a standing rule
// or dropped, and "decide later" is what the row's own `deferred` kind
// already means (FR d0021a0f).
type ResolveOutcome string

const (
	// ResolvePromote closes the current row and opens a successor under the
	// same id with kind `permanent`. The body and name are preserved, so a
	// citation already rendered for the deferred Non-Goal still resolves to
	// the same logical entity.
	ResolvePromote ResolveOutcome = "promote"
	// ResolveRetire closes the current row and opens nothing. The row is
	// tombstoned on the same terms as a void, and its name is freed for a
	// later create.
	ResolveRetire ResolveOutcome = "retire"
)

// NonGoalPromotion is one row of `non_goal_promotion` (migration 022): the
// audit record that a Non-Goal was promoted, by whom, and when.
//
// A promotion leaves a current row, so the SCD2 table records only that
// `kind` changed -- not that it changed by resolution rather than by a
// create that happened to reuse the id. This is the audit read for that
// fact, and the counterpart of VoidEvent on the RETIRE side.
type NonGoalPromotion struct {
	ID                  uuid.UUID
	ScopeID             uuid.UUID
	NonGoalID           uuid.UUID
	ProductID           uuid.UUID
	FromKind            NonGoalKind
	ToKind              NonGoalKind
	Reason              *string
	CreatedByActing     Subject
	CreatedByOnBehalfOf Subject
	CreatedAt           time.Time
}

const nonGoalPromotionColumns = `id, scope_id, non_goal_id, product_id, from_kind, to_kind, reason, ` +
	`created_by_acting_iss, created_by_acting_sub, created_by_acting_kind, ` +
	`created_by_on_behalf_of_iss, created_by_on_behalf_of_sub, created_by_on_behalf_of_kind, created_at`

func scanNonGoalPromotion(row pgx.Row) (NonGoalPromotion, error) {
	var (
		p                        NonGoalPromotion
		fromKind, toKind         string
		actingIss, actingSub     string
		actingKind               string
		onBehalfIss, onBehalfSub string
		onBehalfKind             string
	)
	err := row.Scan(&p.ID, &p.ScopeID, &p.NonGoalID, &p.ProductID, &fromKind, &toKind, &p.Reason,
		&actingIss, &actingSub, &actingKind, &onBehalfIss, &onBehalfSub, &onBehalfKind, &p.CreatedAt)
	p.FromKind = NonGoalKind(fromKind)
	p.ToKind = NonGoalKind(toKind)
	p.CreatedByActing = Subject{Iss: actingIss, Sub: actingSub, Kind: SubjectKind(actingKind)}
	p.CreatedByOnBehalfOf = Subject{Iss: onBehalfIss, Sub: onBehalfSub, Kind: SubjectKind(onBehalfKind)}
	return p, err
}

// ErrNotDeferred is resolve's one named refusal: the target's current kind
// is not `deferred`. A `permanent` Non-Goal is the terminal state of both
// outcomes -- there is nothing left to settle -- so a caller reaching this
// has either already resolved the row, or is trying to resolve one that
// was never deferred. Naming the kind it actually found is what lets the
// caller tell those apart.
var ErrNotDeferred = errors.New("krill/store: only a `deferred` Non-Goal is resolvable -- a `permanent` one is already settled")

// ResolveStore settles a `deferred` Non-Goal (FR d0021a0f), the
// Requirement Contributor's path for a decision that was deliberately held
// back at authoring time.
type ResolveStore interface {
	// ResolveNonGoal resolves the current Non-Goal row for id by one of the
	// two outcomes, and returns what the product's current Non-Goal read
	// then holds -- the promoted successor for PROMOTE, and the zero
	// NonGoal for RETIRE, which leaves none.
	//
	// It refuses a `permanent` Non-Goal with ErrNotDeferred, writing
	// nothing, and a RETIRE inherits void's own two refusals (a delivered
	// Non-Goal, and any reference the tombstone would orphan) by running
	// the same close rather than a second copy of it.
	ResolveNonGoal(ctx context.Context, scopeID, id uuid.UUID, outcome ResolveOutcome, reason *string, acting, onBehalfOf Subject) (NonGoal, error)

	// ListNonGoalPromotions returns every promotion in scopeID, oldest
	// first, optionally narrowed to one product. It is the audit read
	// (FR 19123858) for the PROMOTE outcome; a RETIRE is audited through
	// VoidStore.ListVoidEvents narrowed to outcome=retire instead, because
	// the two outcomes leave different shapes.
	ListNonGoalPromotions(ctx context.Context, scopeID uuid.UUID, productID *uuid.UUID) ([]NonGoalPromotion, error)
}

type resolveStore struct{ pool *pgxpool.Pool }

var _ ResolveStore = resolveStore{}

func (s resolveStore) ResolveNonGoal(
	ctx context.Context, scopeID, id uuid.UUID, outcome ResolveOutcome, reason *string, acting, onBehalfOf Subject,
) (NonGoal, error) {
	var zero NonGoal

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return zero, fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	var resolved NonGoal
	switch outcome {
	case ResolvePromote:
		resolved, err = promoteNonGoal(ctx, tx, scopeID, id, reason, acting, onBehalfOf)
	case ResolveRetire:
		err = retireNonGoal(ctx, tx, scopeID, id, reason, acting, onBehalfOf)
	default:
		return zero, fmt.Errorf("resolve outcome must be one of %v", []ResolveOutcome{ResolvePromote, ResolveRetire})
	}
	if err != nil {
		return zero, err
	}

	if err := tx.Commit(ctx); err != nil {
		return zero, fmt.Errorf("commit: %w", err)
	}
	return resolved, nil
}

// lockDeferredNonGoal is resolve's own check and the only one it adds: the
// target's current kind must be `deferred`. It runs before either outcome
// writes anything, so a `permanent` Non-Goal is refused with no UPDATE and
// no INSERT -- there is no rollback for a misordered guard to hide behind.
//
// The SELECT ... FOR UPDATE is what makes two concurrent resolves of the
// same id serialize: the second waits on the lock, then finds no current
// `deferred` row and is refused. Without it both would read `deferred`,
// and both would try to promote -- one of them into a unique-index
// violation reported as a 500 rather than a resolution.
//
// The lookup is scope-qualified (LB1) on both counts, matching voidEntity:
// another scope's id is reported as not-found rather than resolved, and the
// error names the kind actually found so a caller can tell "already
// resolved" from "never deferred".
func lockDeferredNonGoal(ctx context.Context, tx pgx.Tx, scopeID, id uuid.UUID) (NonGoal, error) {
	var zero NonGoal
	current, err := scanNonGoal(tx.QueryRow(ctx, `
		SELECT `+nonGoalColumns+`
		FROM non_goal
		WHERE id = $1 AND scope_id = $2 AND valid_to IS NULL
		FOR UPDATE
	`, id, scopeID))
	if errors.Is(err, pgx.ErrNoRows) {
		return zero, fmt.Errorf("%w: no current non_goal row for id %s in scope %s", ErrNotFound, id, scopeID)
	}
	if err != nil {
		return zero, fmt.Errorf("get current non_goal for resolve: %w", err)
	}
	if current.Kind != NonGoalKindDeferred {
		return zero, fmt.Errorf("%w: non_goal id %s is %q", ErrNotDeferred, id, current.Kind)
	}
	return current, nil
}

// promoteNonGoal is the close-and-re-kind half of FR d0021a0f: close the
// current row, open a successor under the SAME id with kind `permanent`,
// and record the promotion. It does not call AmendNonGoal, which would
// carry `kind` forward and so accomplish nothing; the re-kind is the entire
// point, and AmendStore's contract is that it never performs one.
//
// Everything else is carried forward untouched -- scope_id, product_id,
// name, body, position -- because a promote changes what the Non-Goal
// asserts, not which Non-Goal it is. In particular the name and body are
// preserved so a citation already rendered for the deferred row still
// resolves, and the id is unchanged for the same reason (LB2).
//
// The close and the insert are one transaction with the register row, so
// there is no state in which the kind has changed but nothing records it.
func promoteNonGoal(
	ctx context.Context, tx pgx.Tx,
	scopeID, id uuid.UUID, reason *string, acting, onBehalfOf Subject,
) (NonGoal, error) {
	var zero NonGoal

	current, err := lockDeferredNonGoal(ctx, tx, scopeID, id)
	if err != nil {
		return zero, err
	}

	if _, err := tx.Exec(ctx,
		`UPDATE non_goal SET valid_to = NOW() WHERE id = $1 AND scope_id = $2 AND valid_to IS NULL`, id, scopeID); err != nil {
		return zero, fmt.Errorf("close current non_goal for promote: %w", err)
	}

	promoted, err := scanNonGoal(tx.QueryRow(ctx, `
		INSERT INTO non_goal (id, scope_id, product_id, kind, name, body, position)
		VALUES ($1, $2, $3, $4, $5, $6, $7)
		RETURNING `+nonGoalColumns,
		current.ID, current.ScopeID, current.ProductID, string(NonGoalKindPermanent), current.Name, current.Body, current.Position))
	if err != nil {
		return zero, fmt.Errorf("insert promoted non_goal: %w", err)
	}

	if _, err := tx.Exec(ctx, `
		INSERT INTO non_goal_promotion (
			scope_id, non_goal_id, product_id, from_kind, to_kind, reason,
			created_by_acting_iss, created_by_acting_sub, created_by_acting_kind,
			created_by_on_behalf_of_iss, created_by_on_behalf_of_sub, created_by_on_behalf_of_kind
		) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12)
	`, scopeID, id, current.ProductID, string(NonGoalKindDeferred), string(NonGoalKindPermanent), reason,
		acting.Iss, acting.Sub, string(acting.Kind),
		onBehalfOf.Iss, onBehalfOf.Sub, string(onBehalfOf.Kind)); err != nil {
		return zero, fmt.Errorf("insert non_goal_promotion: %w", err)
	}

	return promoted, nil
}

// retireNonGoal is the close-WITHOUT-successor half of FR d0021a0f. It
// resolves by running void's own tombstone rather than a second copy of
// it, which is what keeps the two from drifting on the refusals, on the
// name-freeing, and on the register row they share -- void_event carries
// this one too, distinguished by Outcome = VoidOutcomeRetire.
//
// Void's own delivery refusal applies unchanged and is not a quirk of
// this path: retiring a Non-Goal a milestone has already delivered would
// leave that Delivers pointing at a row no current read can see, which is
// exactly the orphaning FR 2a3a8eef exists to prevent. The correct path
// there is PROMOTE, which keeps the id and so keeps the reference valid --
// which is why the delivery refusal is not lifted for the retire case even
// though the promote case never needed one.
func retireNonGoal(
	ctx context.Context, tx pgx.Tx,
	scopeID, id uuid.UUID, reason *string, acting, onBehalfOf Subject,
) error {
	if _, err := lockDeferredNonGoal(ctx, tx, scopeID, id); err != nil {
		return err
	}
	return voidEntityInTx(ctx, tx, VoidedNonGoal, "non_goal", productQueryForNonGoal, scopeID, id, nil, false, reason, acting, onBehalfOf, VoidOutcomeRetire)
}

func (s resolveStore) ListNonGoalPromotions(ctx context.Context, scopeID uuid.UUID, productID *uuid.UUID) ([]NonGoalPromotion, error) {
	query := `SELECT ` + nonGoalPromotionColumns + ` FROM non_goal_promotion WHERE scope_id = $1`
	args := []any{scopeID}
	if productID != nil {
		query += ` AND product_id = $2`
		args = append(args, *productID)
	}
	query += ` ORDER BY created_at, id`

	rows, err := s.pool.Query(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("list non_goal_promotion: %w", err)
	}
	defer rows.Close()

	var promotions []NonGoalPromotion
	for rows.Next() {
		p, err := scanNonGoalPromotion(rows)
		if err != nil {
			return nil, fmt.Errorf("scan non_goal_promotion: %w", err)
		}
		promotions = append(promotions, p)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list non_goal_promotion: %w", err)
	}
	return promotions, nil
}
