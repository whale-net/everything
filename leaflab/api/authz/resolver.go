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
// household_membership rows (household_membership WHERE valid_to IS NULL)
// **and** their active household_grant rows (revoked_at IS NULL AND
// expires_at > NOW()) -- this is the "every RPC handler obtains a Scope
// from the authenticated principal's current memberships ... and applies
// it" step the Implementation section requires, extended by FR7's "a
// grant confers write capability equal to a member's": scope resolution
// does not distinguish membership-derived reach from grant-derived reach,
// so a grant's household reach is indistinguishable from a member's to
// every call site downstream of this function (ListBoards, GetDeviceConfig
// authorization, and any future entity-scoped handler) without each
// re-deciding FR7's semantics itself. Handlers that need to know *which*
// (e.g. to enforce FR7's three exclusions, or FR8.1's granted-read audit
// requirement) call RoleForPrincipalInHousehold instead, scoped to one
// household.
//
// Expiry is evaluated at request time against NOW() in the query itself --
// no background job marks a grant expired (FR7, migration 018's header).
//
// FR75 permits multi-household membership, so this returns the union of
// every current membership and active grant (UnionScope) rather than
// assuming exactly one. A principal with no current membership and no
// active grant gets a Scope that permits nothing, never an error and never
// a widened/global one -- callers (e.g. ListBoards) must render that as an
// empty result, per FR5.1.
func (r *PGResolver) ScopeForPrincipal(ctx context.Context, principalSubject string) (Scope, error) {
	rows, err := r.db.Query(ctx, `
		SELECT household_id
		FROM household_membership
		WHERE principal_subject = $1
		  AND valid_to IS NULL

		UNION

		SELECT household_id
		FROM household_grant
		WHERE grantee_subject = $1
		  AND revoked_at IS NULL
		  AND expires_at > NOW()
	`, principalSubject)
	if err != nil {
		return nil, fmt.Errorf("authz: resolve scope for principal %q: %w", principalSubject, err)
	}
	defer rows.Close()

	var scopes []Scope
	for rows.Next() {
		var householdID int64
		if err := rows.Scan(&householdID); err != nil {
			return nil, fmt.Errorf("authz: scan membership/grant for principal %q: %w", principalSubject, err)
		}
		scopes = append(scopes, NewHouseholdScope(householdID))
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("authz: iterate membership/grant for principal %q: %w", principalSubject, err)
	}
	return NewUnionScope(scopes...), nil
}

// PrincipalRole is how a principal reaches a specific household, as
// returned by RoleForPrincipalInHousehold: not at all, as a current
// member, or as an active grantee. FR7's "member-or-grantee" call sites
// use this to build the household-specific Scope MemberOrGrantee requires
// (household reach plus whether that reach is a grant, for the three
// exclusions) and to decide FR8.1's "reads performed under a granted
// (non-member) identity produce an audit record" -- a read is audited only
// when this is RoleGrantee, never when it is RoleMember.
type PrincipalRole int

const (
	// RoleNone: principalSubject is neither a current member nor an
	// active grantee of the household in question.
	RoleNone PrincipalRole = iota
	// RoleMember: principalSubject holds a current household_membership
	// row for the household.
	RoleMember
	// RoleGrantee: principalSubject holds no current membership row, but
	// does hold an active (unrevoked, unexpired) household_grant row for
	// the household.
	RoleGrantee
)

// RoleForPrincipalInHousehold reports how principalSubject reaches
// householdID -- member, grantee, or neither -- in the single query NFR2's
// "resolve and check in one round trip" shape uses elsewhere in this
// package: membership and grant reach are both read here, so a caller
// never issues a second query to tell "not a member" apart from "not a
// grantee either". Membership takes precedence over a grant when
// (improbably) both are true for the same principal/household pair -- FR7
// never contemplates a member also holding a grant on their own household,
// but if it occurred, "member" is the more permissive and correct answer.
func (r *PGResolver) RoleForPrincipalInHousehold(ctx context.Context, principalSubject string, householdID int64) (PrincipalRole, error) {
	var isMember, isGrantee bool
	err := r.db.QueryRow(ctx, `
		SELECT
			EXISTS(
				SELECT 1 FROM household_membership
				WHERE principal_subject = $1 AND household_id = $2 AND valid_to IS NULL
			),
			EXISTS(
				SELECT 1 FROM household_grant
				WHERE grantee_subject = $1 AND household_id = $2
				  AND revoked_at IS NULL AND expires_at > NOW()
			)
	`, principalSubject, householdID).Scan(&isMember, &isGrantee)
	if err != nil {
		return RoleNone, fmt.Errorf("authz: resolve role for principal %q in household %d: %w", principalSubject, householdID, err)
	}
	switch {
	case isMember:
		return RoleMember, nil
	case isGrantee:
		return RoleGrantee, nil
	default:
		return RoleNone, nil
	}
}

// ErrGrantNotFound is returned by ResolveGrantRole when grantID names no
// household_grant row. Handlers must map this to the same failure as a
// principal with RoleNone reach over a real grant's household (NFR2) --
// see server.go's grantNotFoundFailure.
var ErrGrantNotFound = errors.New("authz: household grant not found")

// GrantResolution is a household_grant row's authorization-relevant state,
// as returned by ResolveGrantRole: which household it belongs to, whether
// it is already revoked, and how principalSubject (the caller attempting
// to revoke it) reaches that household.
type GrantResolution struct {
	HouseholdID int64
	// Revoked is true when the grant's revoked_at is already set --
	// RevokeHouseholdAccess is not idempotent-by-design (mirrors
	// RetireBoard's convention in leaflab/api/repository.go), so a handler
	// distinguishes this from ErrGrantNotFound to report the accurate
	// failure.
	Revoked bool
	// Role is principalSubject's reach over HouseholdID -- RoleNone,
	// RoleMember or RoleGrantee. RevokeHouseholdAccess is an "ordinary"
	// member capability (FR7 does not name it among the three exclusions),
	// so both RoleMember and RoleGrantee are permitted to call it; RoleNone
	// means the caller has no reach over this grant's household at all.
	Role PrincipalRole
}

// ResolveGrantRole resolves grantID to its household and revocation state,
// and principalSubject's Role over that household, in the single query
// NFR2 requires: grant_id is a caller-supplied, enumerable (BIGSERIAL)
// identifier, so "no such grant" and "a real grant belonging to a
// household the caller cannot reach" must be indistinguishable in status,
// body and timing -- exactly ResolveInScope's board-resolution shape,
// specialized for household_grant since a grant does not fit the
// EntityRef/Resolver pattern (it has no owning household to inherit
// through; it *names* one directly).
func (r *PGResolver) ResolveGrantRole(ctx context.Context, grantID int64, principalSubject string) (GrantResolution, error) {
	var res GrantResolution
	var isMember, isGrantee bool
	err := r.db.QueryRow(ctx, `
		SELECT
			hg.household_id,
			hg.revoked_at IS NOT NULL,
			EXISTS(
				SELECT 1 FROM household_membership hm
				WHERE hm.principal_subject = $2 AND hm.household_id = hg.household_id AND hm.valid_to IS NULL
			),
			EXISTS(
				SELECT 1 FROM household_grant g2
				WHERE g2.grantee_subject = $2 AND g2.household_id = hg.household_id
				  AND g2.revoked_at IS NULL AND g2.expires_at > NOW()
			)
		FROM household_grant hg
		WHERE hg.grant_id = $1
	`, grantID, principalSubject).Scan(&res.HouseholdID, &res.Revoked, &isMember, &isGrantee)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return GrantResolution{}, ErrGrantNotFound
		}
		return GrantResolution{}, fmt.Errorf("authz: resolve grant %d for principal %q: %w", grantID, principalSubject, err)
	}
	switch {
	case isMember:
		res.Role = RoleMember
	case isGrantee:
		res.Role = RoleGrantee
	default:
		res.Role = RoleNone
	}
	return res, nil
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
