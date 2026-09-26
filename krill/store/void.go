// This file is void -- the SCD2 close-WITHOUT-successor (LB3), the other
// half of the boundary call store/amend.go draws. Where an amend closes a
// current row and opens a successor under the same immutable id, a void
// closes the current row and opens nothing: the entity is tombstoned.
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

// VoidedEntityKind names which of the seven void-able spec-axis entity
// kinds a VoidEvent or a read is about. Milestone is deliberately absent:
// a milestone is a delivery-axis fact whose delivery records are
// append-only, and FR 39373553 gives it amend for its authoring fields
// instead.
type VoidedEntityKind string

const (
	VoidedProduct             VoidedEntityKind = "product"
	VoidedFeatureSet          VoidedEntityKind = "feature_set"
	VoidedFeature             VoidedEntityKind = "feature"
	VoidedRequirement         VoidedEntityKind = "requirement"
	VoidedPersona             VoidedEntityKind = "persona"
	VoidedNonGoal             VoidedEntityKind = "non_goal"
	VoidedLoadBearingDecision VoidedEntityKind = "load_bearing_decision"
)

// VoidEvent is one row of `void_event` (migration 021): the audit record
// that an entity was voided, by whom, when, and -- for the two kinds that
// carry one -- which display number that void retired.
//
// This is the ONLY way a caller reaches a tombstone. Every current read
// filters `valid_to IS NULL`, so a voided entity is absent from the slice
// reads and from render, and EntityID plus RetiredDisplayNumber are how an
// auditor recovers the identity a `C7`-style citation resolved to before
// the void.
type VoidEvent struct {
	ID                   uuid.UUID
	ScopeID              uuid.UUID
	EntityKind           VoidedEntityKind
	EntityID             uuid.UUID
	ProductID            uuid.UUID
	RetiredDisplayNumber *int
	Reason               *string
	CreatedByActing      Subject
	CreatedByOnBehalfOf  Subject
	CreatedAt            time.Time
}

const voidEventColumns = `id, scope_id, entity_kind, entity_id, product_id, retired_display_number, reason, ` +
	`created_by_acting_iss, created_by_acting_sub, created_by_acting_kind, ` +
	`created_by_on_behalf_of_iss, created_by_on_behalf_of_sub, created_by_on_behalf_of_kind, created_at`

func scanVoidEvent(row pgx.Row) (VoidEvent, error) {
	var (
		v                        VoidEvent
		kind                     string
		actingIss, actingSub     string
		actingKind               string
		onBehalfIss, onBehalfSub string
		onBehalfKind             string
	)
	err := row.Scan(&v.ID, &v.ScopeID, &kind, &v.EntityID, &v.ProductID, &v.RetiredDisplayNumber, &v.Reason,
		&actingIss, &actingSub, &actingKind, &onBehalfIss, &onBehalfSub, &onBehalfKind, &v.CreatedAt)
	v.EntityKind = VoidedEntityKind(kind)
	v.CreatedByActing = Subject{Iss: actingIss, Sub: actingSub, Kind: SubjectKind(actingKind)}
	v.CreatedByOnBehalfOf = Subject{Iss: onBehalfIss, Sub: onBehalfSub, Kind: SubjectKind(onBehalfKind)}
	return v, err
}

// VoidStore is the tombstone write side for every void-able spec-axis
// kind. Every method here refuses rather than orphaning a reference
// (FR 2a3a8eef) and never writes anything at all when it refuses: the
// two refusal checks run before the tombstoning UPDATE, so a refused void
// leaves the current row exactly as it found it with no rollback
// involved.
type VoidStore interface {
	// VoidProduct tombstones the current Product row for id in scopeID.
	// Returns ErrNotFound if no current row exists, ErrEntityDelivered or
	// ErrHasLiveChildren if the product is spoken for.
	VoidProduct(ctx context.Context, scopeID, id uuid.UUID, reason *string, acting, onBehalfOf Subject) error

	// VoidFeatureSet tombstones the current FeatureSet row for id. A
	// FeatureSet with a live Feature or LoadBearingDecision under it is
	// refused.
	VoidFeatureSet(ctx context.Context, scopeID, id uuid.UUID, reason *string, acting, onBehalfOf Subject) error

	// VoidFeature tombstones the current Feature row for id. A Feature
	// with a live Requirement under it is refused. The feature's
	// `display_number` is recorded as retired and is never reissued.
	VoidFeature(ctx context.Context, scopeID, id uuid.UUID, reason *string, acting, onBehalfOf Subject) error

	// VoidRequirement tombstones the current Requirement row for id.
	// Requirements carry no stored display number, so nothing is retired.
	VoidRequirement(ctx context.Context, scopeID, id uuid.UUID, reason *string, acting, onBehalfOf Subject) error

	// VoidPersona tombstones the current Persona row for id. Personas are
	// leaves, so this refuses only on delivery.
	VoidPersona(ctx context.Context, scopeID, id uuid.UUID, reason *string, acting, onBehalfOf Subject) error

	// VoidNonGoal tombstones the current NonGoal row for id, of either
	// kind. This is the same close-without-successor shape the resolve
	// verb's RETIRE outcome needs (FR d0021a0f); it does not itself check
	// that the NonGoal is `deferred` -- that is the resolve verb's rule to
	// enforce, and a permanent NonGoal may still be a mistaken create.
	VoidNonGoal(ctx context.Context, scopeID, id uuid.UUID, reason *string, acting, onBehalfOf Subject) error

	// VoidLoadBearingDecision tombstones the current
	// LoadBearingDecision row for id and retires its `display_number`.
	VoidLoadBearingDecision(ctx context.Context, scopeID, id uuid.UUID, reason *string, acting, onBehalfOf Subject) error

	// ListVoidEvents returns every void recorded in scopeID, optionally
	// narrowed to one entity kind, oldest first. It is the audit read
	// (FR d38d726e (c)): the only way to reach a tombstoned entity's
	// original id and original display number. An empty kind returns
	// every kind's voids rather than none.
	ListVoidEvents(ctx context.Context, scopeID uuid.UUID, kind VoidedEntityKind) ([]VoidEvent, error)
}

// ErrEntityDelivered is void's named, loud refusal: the entity already
// appears in some milestone or milepebble's Delivers, or has shipped. A
// delivered entity is part of a commitment someone is tracking, so the
// correct path is amend/supersession -- never a tombstone, which would
// leave that commitment pointing at a row no current read can see.
var ErrEntityDelivered = errors.New("krill/store: entity is already delivered or shipped -- amend it instead of voiding it")

// ErrHasLiveChildren is void's other named refusal: the entity is the
// parent of at least one live child row. Voiding it would orphan that
// child the way a hard delete would, so the children must be voided first
// (bottom-up) or the entity amended.
var ErrHasLiveChildren = errors.New("krill/store: entity still has live children -- void them first, or amend instead")

// childGuard names a child table and the column in it that holds the
// parent's immutable `id`. A void of a parent with any live row in one of
// these is refused.
//
// Every table listed is a spec-axis SCD2 table, so "live" means
// `valid_to IS NULL` -- a child that was itself voided does not keep its
// parent alive, which is what makes voiding bottom-up work.
type childGuard struct {
	table        string
	parentColumn string
}

var (
	// A Product keeps alive: FeatureSets, Personas, NonGoals, and every
	// milestone (milestone_ref.product_id -- a milestone is authored
	// against a product, so voiding the product would orphan the whole
	// delivery axis).
	productChildren = []childGuard{
		{"feature_set", "product_id"},
		{"persona", "product_id"},
		{"non_goal", "product_id"},
		{"milestone_ref", "product_id"},
	}
	// A FeatureSet keeps alive: Features and LoadBearingDecisions.
	featureSetChildren = []childGuard{
		{"feature", "feature_set_id"},
		{"load_bearing_decision", "feature_set_id"},
	}
	// A Feature keeps alive: Requirements.
	featureChildren = []childGuard{{"requirement", "feature_id"}}
)

// childRefusal returns the name of the first child table holding a live
// child of parentID, or "" when the parent has none. It is the shared
// body of every kind's live-children check.
//
// The scope_id comparison is load-bearing, not decorative: entity ids are
// globally unique, so a parent id alone cannot distinguish "this scope's
// parent" from "another scope's parent that happens to be cited here",
// and dropping it would let one scope void another scope's parent. The
// child query is scope-qualified for the mirror-image reason -- a child
// row belonging to another scope is not this scope's business and must
// not block its void.
//
// table/parentColumn are always this package's own constant names, never
// caller input, so fmt.Sprintf carries no injection risk (matches
// currentRowExists' same note in errors.go).
func childRefusal(ctx context.Context, q txQuerier, children []childGuard, parentID, scopeID uuid.UUID) (string, error) {
	for _, child := range children {
		var exists bool
		if err := q.QueryRow(ctx, fmt.Sprintf(`
			SELECT EXISTS (
				SELECT 1 FROM %s WHERE %s = $1 AND scope_id = $2 AND valid_to IS NULL
			)
		`, child.table, child.parentColumn), parentID, scopeID).Scan(&exists); err != nil {
			return "", fmt.Errorf("check live %s under %s: %w", child.table, child.parentColumn, err)
		}
		if exists {
			return child.table, nil
		}
	}
	return "", nil
}

// errHasLiveChildren names the child table that blocked the void, so the
// caller's error says what to void first rather than only that it failed.
func errHasLiveChildren(kind VoidedEntityKind, id uuid.UUID, childTable string) error {
	return fmt.Errorf("%w: %s id %s still has a live %s row", ErrHasLiveChildren, kind, id, childTable)
}

// deliveryRefusal reports whether entityID is already spoken for by the
// delivery axis: any `entity_milestone` association (Delivers OR
// Must-not-foreclose) or any `delivery_shipment` row.
//
// It checks BOTH tables, and does not narrow to relation='delivers',
// because the point of the refusal (FR 2a3a8eef) is that void must never
// orphan a reference the way a hard delete would. A Must-not-foreclose
// association and a shipment are both references a reader will follow, so
// both must survive a void; and a shipment is only ever recorded against
// a Delivers association, so checking entity_milestone alone would leave
// the shipped case resting on an invariant enforced in another package.
func deliveryRefusal(ctx context.Context, q txQuerier, entityID, scopeID uuid.UUID) (bool, error) {
	var referenced bool
	if err := q.QueryRow(ctx, `
		SELECT EXISTS (
			SELECT 1 FROM entity_milestone WHERE entity_id = $1 AND scope_id = $2
			UNION ALL
			SELECT 1 FROM delivery_shipment WHERE entity_id = $1 AND scope_id = $2
		)
	`, entityID, scopeID).Scan(&referenced); err != nil {
		return false, fmt.Errorf("check delivery references for entity %s: %w", entityID, err)
	}
	return referenced, nil
}

// productQuery resolves the product a voided row belongs to, taking the
// entity's id ($1) and scope ($2). void_event's number-retirement index is
// per product (migration 017's numbering scope), and only four of the
// seven void-able tables carry `product_id` directly -- a Feature,
// LoadBearingDecision, or Requirement reaches its product through the
// feature_set ancestor, so each kind supplies its own resolution here.
//
// These are this package's own constant strings, never caller input.
var (
	productQueryForProduct    = `SELECT id FROM product WHERE id = $1 AND scope_id = $2 AND valid_to IS NULL`
	productQueryForFeatureSet = `SELECT product_id FROM feature_set WHERE id = $1 AND scope_id = $2 AND valid_to IS NULL`
	productQueryForPersona    = `SELECT product_id FROM persona WHERE id = $1 AND scope_id = $2 AND valid_to IS NULL`
	productQueryForNonGoal    = `SELECT product_id FROM non_goal WHERE id = $1 AND scope_id = $2 AND valid_to IS NULL`
	productQueryForFeature    = `SELECT fs.product_id FROM feature f
		JOIN feature_set fs ON f.feature_set_id = fs.id AND fs.scope_id = f.scope_id AND fs.valid_to IS NULL
		WHERE f.id = $1 AND f.scope_id = $2 AND f.valid_to IS NULL`
	productQueryForDecision = `SELECT fs.product_id FROM load_bearing_decision d
		JOIN feature_set fs ON d.feature_set_id = fs.id AND fs.scope_id = d.scope_id AND fs.valid_to IS NULL
		WHERE d.id = $1 AND d.scope_id = $2 AND d.valid_to IS NULL`
	productQueryForRequirement = `SELECT fs.product_id FROM requirement r
		JOIN feature f ON f.id = r.feature_id AND f.scope_id = r.scope_id AND f.valid_to IS NULL
		JOIN feature_set fs ON f.feature_set_id = fs.id AND fs.scope_id = f.scope_id AND fs.valid_to IS NULL
		WHERE r.id = $1 AND r.scope_id = $2 AND r.valid_to IS NULL`
)

// voidStore is the pgx-backed VoidStore implementation.
type voidStore struct{ pool *pgxpool.Pool }

var _ VoidStore = voidStore{}

// voidEntity is the shared body of every Void* method: it row-locks the
// current row, runs BOTH refusal checks, and only then closes the row and
// records the tombstone -- all in one transaction.
//
// Order is the contract, not an accident. Both refusals are evaluated
// before the first write, so a refused void issues no UPDATE and no
// INSERT; there is nothing to roll back and no window in which the row
// could be observed half-closed. A reader of this function should keep it
// that way.
//
// The SELECT ... FOR UPDATE row-lock is what makes two concurrent voids
// of the same id serialize: without it both would find the same current
// row and both proceed. With it, the second waits, then finds no current
// row and is refused with ErrNotFound.
func voidEntity(
	ctx context.Context,
	pool *pgxpool.Pool,
	kind VoidedEntityKind,
	table string,
	productQuery string,
	scopeID, id uuid.UUID,
	children []childGuard,
	hasDisplayNumber bool,
	reason *string,
	acting, onBehalfOf Subject,
) error {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	// Lock the current row, and pick up the number this void retires for
	// the two kinds that carry one. The lookup is scope-qualified (LB1): a
	// real id belonging to another scope must be reported as not-found
	// here rather than voided.
	lockQuery := fmt.Sprintf(`SELECT 1 FROM %s WHERE id = $1 AND scope_id = $2 AND valid_to IS NULL FOR UPDATE`, table)
	var lockDest any = new(int)
	if hasDisplayNumber {
		lockQuery = fmt.Sprintf(`SELECT display_number FROM %s WHERE id = $1 AND scope_id = $2 AND valid_to IS NULL FOR UPDATE`, table)
		lockDest = new(*int)
	}
	var displayNumber *int
	if hasDisplayNumber {
		displayNumber = lockDest.(*int)
	}
	if err := tx.QueryRow(ctx, lockQuery, id, scopeID).Scan(lockDest); errors.Is(err, pgx.ErrNoRows) {
		return fmt.Errorf("%w: no current %s row for id %s in scope %s", ErrNotFound, table, id, scopeID)
	} else if err != nil {
		return fmt.Errorf("get current %s for void: %w", table, err)
	}

	// Refusal (a): the delivery axis already points at this entity.
	delivered, err := deliveryRefusal(ctx, tx, id, scopeID)
	if err != nil {
		return err
	}
	if delivered {
		return fmt.Errorf("%w: %s id %s", ErrEntityDelivered, kind, id)
	}

	// Refusal (b): live children would be orphaned.
	childTable, err := childRefusal(ctx, tx, children, id, scopeID)
	if err != nil {
		return err
	}
	if childTable != "" {
		return errHasLiveChildren(kind, id, childTable)
	}

	// Past both refusals: close the current row and open nothing. This
	// single UPDATE is the whole tombstone -- the freed name and the
	// retired number both fall out of it, the first because every name
	// index is partial on `valid_to IS NULL`, the second because
	// nextDisplayNumber counts closed rows too.
	if _, err := tx.Exec(ctx, fmt.Sprintf(
		`UPDATE %s SET valid_to = NOW() WHERE id = $1 AND scope_id = $2 AND valid_to IS NULL`, table), id, scopeID); err != nil {
		return fmt.Errorf("close current %s for void: %w", table, err)
	}

	// The product is resolved AFTER the close, from the row the lock just
	// confirmed existed. A live entity always has a live ancestor chain
	// (that is what the live-children refusal just enforced), so the
	// resolution cannot fail for a reason the caller could act on.
	var productID uuid.UUID
	if err := tx.QueryRow(ctx, productQuery, id, scopeID).Scan(&productID); err != nil {
		return fmt.Errorf("resolve product for voided %s %s: %w", kind, id, err)
	}

	if _, err := tx.Exec(ctx, `
		INSERT INTO void_event (
			scope_id, entity_kind, entity_id, product_id, retired_display_number, reason,
			created_by_acting_iss, created_by_acting_sub, created_by_acting_kind,
			created_by_on_behalf_of_iss, created_by_on_behalf_of_sub, created_by_on_behalf_of_kind
		) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12)
	`, scopeID, string(kind), id, productID, displayNumber, reason,
		acting.Iss, acting.Sub, string(acting.Kind),
		onBehalfOf.Iss, onBehalfOf.Sub, string(onBehalfOf.Kind)); err != nil {
		return fmt.Errorf("insert void_event for %s: %w", kind, err)
	}

	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit: %w", err)
	}
	return nil
}

func (s voidStore) VoidProduct(ctx context.Context, scopeID, id uuid.UUID, reason *string, acting, onBehalfOf Subject) error {
	return voidEntity(ctx, s.pool, VoidedProduct, "product", productQueryForProduct, scopeID, id, productChildren, false, reason, acting, onBehalfOf)
}

func (s voidStore) VoidFeatureSet(ctx context.Context, scopeID, id uuid.UUID, reason *string, acting, onBehalfOf Subject) error {
	return voidEntity(ctx, s.pool, VoidedFeatureSet, "feature_set", productQueryForFeatureSet, scopeID, id, featureSetChildren, false, reason, acting, onBehalfOf)
}

func (s voidStore) VoidFeature(ctx context.Context, scopeID, id uuid.UUID, reason *string, acting, onBehalfOf Subject) error {
	return voidEntity(ctx, s.pool, VoidedFeature, "feature", productQueryForFeature, scopeID, id, featureChildren, true, reason, acting, onBehalfOf)
}

func (s voidStore) VoidRequirement(ctx context.Context, scopeID, id uuid.UUID, reason *string, acting, onBehalfOf Subject) error {
	return voidEntity(ctx, s.pool, VoidedRequirement, "requirement", productQueryForRequirement, scopeID, id, nil, false, reason, acting, onBehalfOf)
}

func (s voidStore) VoidPersona(ctx context.Context, scopeID, id uuid.UUID, reason *string, acting, onBehalfOf Subject) error {
	return voidEntity(ctx, s.pool, VoidedPersona, "persona", productQueryForPersona, scopeID, id, nil, false, reason, acting, onBehalfOf)
}

func (s voidStore) VoidNonGoal(ctx context.Context, scopeID, id uuid.UUID, reason *string, acting, onBehalfOf Subject) error {
	return voidEntity(ctx, s.pool, VoidedNonGoal, "non_goal", productQueryForNonGoal, scopeID, id, nil, false, reason, acting, onBehalfOf)
}

func (s voidStore) VoidLoadBearingDecision(ctx context.Context, scopeID, id uuid.UUID, reason *string, acting, onBehalfOf Subject) error {
	return voidEntity(ctx, s.pool, VoidedLoadBearingDecision, "load_bearing_decision", productQueryForDecision, scopeID, id, nil, true, reason, acting, onBehalfOf)
}

func (s voidStore) ListVoidEvents(ctx context.Context, scopeID uuid.UUID, kind VoidedEntityKind) ([]VoidEvent, error) {
	// An empty kind is "every kind", not "no kinds" -- a caller that has
	// not narrowed should see the whole register for its scope.
	query := `SELECT ` + voidEventColumns + ` FROM void_event WHERE scope_id = $1`
	args := []any{scopeID}
	if kind != "" {
		query += ` AND entity_kind = $2`
		args = append(args, string(kind))
	}
	query += ` ORDER BY created_at, id`

	rows, err := s.pool.Query(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("list void_event: %w", err)
	}
	defer rows.Close()

	var events []VoidEvent
	for rows.Next() {
		v, err := scanVoidEvent(rows)
		if err != nil {
			return nil, fmt.Errorf("scan void_event: %w", err)
		}
		events = append(events, v)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list void_event: %w", err)
	}
	return events, nil
}
