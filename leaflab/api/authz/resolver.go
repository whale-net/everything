package authz

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// ErrNotFound is returned by Resolver.Resolve when ref names no row in the
// underlying table. Handlers must not let this leak to the wire any
// differently than ErrOutOfScope (NFR2) -- both collapse to the same
// contract.NotFound failure. It exists as a distinct Go error only so
// server-side logs can tell "doesn't exist" apart from "exists, wrong
// household" for operators, never for callers.
var ErrNotFound = errors.New("authz: entity not found")

// ErrOutOfScope is returned by ResolveInScope when ref resolves to a real
// row that scope does not Permit. See ErrNotFound's doc comment -- the two
// must be indistinguishable on the wire.
var ErrOutOfScope = errors.New("authz: entity outside caller's scope")

// ErrNoHousehold indicates ref names a real row that resolves to no
// household despite not being FR1.1's board exception (that case is
// reported as Resolution.Unclaimed instead, not this error). This should
// not occur post-backfill (migration 015) -- it signals a data integrity
// gap, not a normal authorization outcome, and handlers should treat it
// like an internal error, not a not_found/out-of-scope refusal.
var ErrNoHousehold = errors.New("authz: entity resolves to no household")

// Resolution is an entity's authorization-relevant state, as returned by
// Resolver.Resolve.
type Resolution struct {
	// HouseholdID is the household ref currently belongs to. Zero and
	// meaningless when Unclaimed is true.
	HouseholdID int64
	// Unclaimed is true only for an EntityBoard ref with no current
	// board_ownership row -- FR1.1's one exception. A board in this state
	// resolves to no household at all; it is reachable only through the
	// claim path (FR76) and the admin elevated lane, and only as the
	// minimal claimable-status projection those paths use -- never through
	// HouseholdScope, which always refuses an Unclaimed resolution (see
	// HouseholdScope.Permits).
	Unclaimed bool
}

// Queryer is the subset of *pgxpool.Pool Resolver and ScopeForPrincipal
// depend on, narrowed to an interface so tests substitute a fake without a
// live Postgres connection.
type Queryer interface {
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
}

// Resolver resolves an EntityRef to the household it currently belongs to,
// per FR1.1's inheritance rules:
//   - board: the current row (board.household_id, kept in sync with
//     board_ownership as its current-value cache -- see migration 015's
//     comment); no row at all is FR1.1's Unclaimed exception.
//   - region: the tree root's household_id -- descendants inherit.
//     Migration 015's enforce_region_household_root trigger keeps that
//     invariant true in the schema; Resolve just walks it.
//   - plant: carried directly on plant, not inherited through region.
//   - sensor: inherited through its board.
//   - reading: inherited through its sensor's board.
type Resolver interface {
	Resolve(ctx context.Context, ref EntityRef) (Resolution, error)
}

// PGResolver is the production Resolver, backed by Postgres.
type PGResolver struct {
	db Queryer
}

// NewPGResolver builds a PGResolver. Accepts *pgxpool.Pool directly (it
// satisfies Queryer); tests may pass a narrower fake instead.
func NewPGResolver(db *pgxpool.Pool) *PGResolver {
	return &PGResolver{db: db}
}

func (r *PGResolver) Resolve(ctx context.Context, ref EntityRef) (Resolution, error) {
	switch ref.Kind {
	case EntityBoard:
		return r.resolveBoard(ctx, ref.ID)
	case EntityRegion:
		return r.resolveRegion(ctx, ref.ID)
	case EntityPlant:
		return r.resolvePlant(ctx, ref.ID)
	case EntitySensor:
		return r.resolveSensor(ctx, ref.ID)
	case EntityReading:
		return r.resolveReading(ctx, ref.ID)
	default:
		return Resolution{}, fmt.Errorf("authz: unknown entity kind %q", ref.Kind)
	}
}

func (r *PGResolver) resolveBoard(ctx context.Context, boardID int64) (Resolution, error) {
	var householdID *int64
	err := r.db.QueryRow(ctx, `
		SELECT household_id
		FROM board
		WHERE board_id = $1
	`, boardID).Scan(&householdID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return Resolution{}, ErrNotFound
		}
		return Resolution{}, fmt.Errorf("authz: resolve board %d: %w", boardID, err)
	}
	if householdID == nil {
		return Resolution{Unclaimed: true}, nil
	}
	return Resolution{HouseholdID: *householdID}, nil
}

func (r *PGResolver) resolveRegion(ctx context.Context, regionID int64) (Resolution, error) {
	var householdID *int64
	err := r.db.QueryRow(ctx, `
		WITH RECURSIVE ancestors AS (
			SELECT region_id, parent_region_id, household_id
			FROM region
			WHERE region_id = $1

			UNION ALL

			SELECT r.region_id, r.parent_region_id, r.household_id
			FROM region r
			JOIN ancestors a ON r.region_id = a.parent_region_id
		)
		SELECT household_id
		FROM ancestors
		WHERE parent_region_id IS NULL
	`, regionID).Scan(&householdID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return Resolution{}, ErrNotFound
		}
		return Resolution{}, fmt.Errorf("authz: resolve region %d: %w", regionID, err)
	}
	if householdID == nil {
		return Resolution{}, ErrNoHousehold
	}
	return Resolution{HouseholdID: *householdID}, nil
}

func (r *PGResolver) resolvePlant(ctx context.Context, plantID int64) (Resolution, error) {
	var householdID int64
	err := r.db.QueryRow(ctx, `
		SELECT household_id
		FROM plant
		WHERE plant_id = $1
	`, plantID).Scan(&householdID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return Resolution{}, ErrNotFound
		}
		return Resolution{}, fmt.Errorf("authz: resolve plant %d: %w", plantID, err)
	}
	return Resolution{HouseholdID: householdID}, nil
}

func (r *PGResolver) resolveSensor(ctx context.Context, sensorID int64) (Resolution, error) {
	var householdID *int64
	err := r.db.QueryRow(ctx, `
		SELECT b.household_id
		FROM sensor s
		JOIN board b ON b.board_id = s.board_id
		WHERE s.sensor_id = $1
	`, sensorID).Scan(&householdID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return Resolution{}, ErrNotFound
		}
		return Resolution{}, fmt.Errorf("authz: resolve sensor %d: %w", sensorID, err)
	}
	if householdID == nil {
		// The sensor's board is unclaimed (FR1.1) -- the sensor inherits
		// that state rather than resolving to a household of its own.
		return Resolution{Unclaimed: true}, nil
	}
	return Resolution{HouseholdID: *householdID}, nil
}

func (r *PGResolver) resolveReading(ctx context.Context, readingID int64) (Resolution, error) {
	var householdID *int64
	err := r.db.QueryRow(ctx, `
		SELECT b.household_id
		FROM sensor_reading sr
		JOIN sensor s ON s.sensor_id = sr.sensor_id
		JOIN board b ON b.board_id = s.board_id
		WHERE sr.reading_id = $1
		LIMIT 1
	`, readingID).Scan(&householdID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return Resolution{}, ErrNotFound
		}
		return Resolution{}, fmt.Errorf("authz: resolve reading %d: %w", readingID, err)
	}
	if householdID == nil {
		// The reading's sensor's board is unclaimed (FR1.1).
		return Resolution{Unclaimed: true}, nil
	}
	return Resolution{HouseholdID: *householdID}, nil
}

// ResolveBoardByDeviceID resolves the board named by deviceID -- the key
// PushDeviceConfig/GetDeviceConfig receive on the wire, one join short of
// board_id -- to both its EntityRef (for a later Scope.Permits/Filter
// call) and its Resolution, in the single query NFR2 requires. Handlers
// must not look device_id up (e.g. via a repository existence check)
// before calling this: a separate existence probe ahead of Resolve is
// exactly the extra round trip NFR2 rules out, since "device_id doesn't
// exist" and "device_id exists, wrong household" would then take a
// different number of queries and become distinguishable by timing.
func (r *PGResolver) ResolveBoardByDeviceID(ctx context.Context, deviceID string) (EntityRef, Resolution, error) {
	var boardID int64
	var householdID *int64
	err := r.db.QueryRow(ctx, `
		SELECT board_id, household_id
		FROM board
		WHERE device_id = $1
	`, deviceID).Scan(&boardID, &householdID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return EntityRef{}, Resolution{}, ErrNotFound
		}
		return EntityRef{}, Resolution{}, fmt.Errorf("authz: resolve board by device_id %q: %w", deviceID, err)
	}
	ref := EntityRef{Kind: EntityBoard, ID: boardID}
	if householdID == nil {
		return ref, Resolution{Unclaimed: true}, nil
	}
	return ref, Resolution{HouseholdID: *householdID}, nil
}

// ScopeForPrincipal resolves principalSubject's Scope from their current
// household_membership rows (household_membership WHERE valid_to IS
// NULL) -- this is the "every RPC handler obtains a Scope from the
// authenticated principal's current memberships" step the Implementation
// section requires. FR75 permits multi-household membership, so this
// returns the union of every current membership (UnionScope) rather than
// assuming exactly one. A principal with no current membership gets a
// Scope that permits nothing, never an error and never a widened/global
// one -- callers (e.g. ListBoards) must render that as an empty result,
// per FR5.1.
func (r *PGResolver) ScopeForPrincipal(ctx context.Context, principalSubject string) (Scope, error) {
	rows, err := r.db.Query(ctx, `
		SELECT household_id
		FROM household_membership
		WHERE principal_subject = $1
		  AND valid_to IS NULL
	`, principalSubject)
	if err != nil {
		return nil, fmt.Errorf("authz: resolve scope for principal %q: %w", principalSubject, err)
	}
	defer rows.Close()

	var scopes []Scope
	for rows.Next() {
		var householdID int64
		if err := rows.Scan(&householdID); err != nil {
			return nil, fmt.Errorf("authz: scan membership for principal %q: %w", principalSubject, err)
		}
		scopes = append(scopes, NewHouseholdScope(householdID))
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("authz: iterate membership for principal %q: %w", principalSubject, err)
	}
	return NewUnionScope(scopes...), nil
}

// ResolveInScope resolves ref and checks it against scope in one motion,
// so a handler's not-found and out-of-scope paths share the same DB round
// trip (NFR2) -- the only difference between them is a cheap in-memory
// Permits check after the query returns, never a second query, and never a
// SELECT-then-branch shortcut before the query runs. Handlers must map
// both ErrNotFound and ErrOutOfScope to the same contract.NotFound
// failure, with the same reason text, so the two are indistinguishable in
// status and body as well as timing.
func ResolveInScope(ctx context.Context, resolver Resolver, ref EntityRef, scope Scope) (Resolution, error) {
	res, err := resolver.Resolve(ctx, ref)
	if err != nil {
		return Resolution{}, err
	}
	if !scope.Permits(ref, res) {
		return Resolution{}, ErrOutOfScope
	}
	return res, nil
}
