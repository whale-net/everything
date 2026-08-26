package main

import (
	"context"
	"fmt"
	"sort"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/whale-net/everything/libs/go/grpcauth"
)

// AuthorizationPredicates encapsulates the authorization checks for household operations.
type AuthorizationPredicates struct {
	db *pgxpool.Pool
}

func NewAuthorizationPredicates(db *pgxpool.Pool) *AuthorizationPredicates {
	return &AuthorizationPredicates{db: db}
}

// MemberOrGrantee checks if the subject is either a member or an active grantee of the household.
// Returns (true, nil) if authorized, (false, nil) if not authorized, or (false, error) on DB error.
func (ap *AuthorizationPredicates) MemberOrGrantee(ctx context.Context, householdID int64, subject string) (bool, error) {
	// First check if subject is a current member (SCD2: valid_to IS NULL).
	var isMember bool
	err := ap.db.QueryRow(ctx, `
		SELECT EXISTS(
			SELECT 1 FROM household_member
			WHERE household_id = $1
			  AND principal_id = $2
			  AND valid_to IS NULL
		)
	`, householdID, subject).Scan(&isMember)
	if err != nil {
		return false, fmt.Errorf("check member: %w", err)
	}
	if isMember {
		return true, nil
	}

	// Check if subject has an active grant (expires_at > NOW and revoked_at IS NULL).
	var isGrantee bool
	err = ap.db.QueryRow(ctx, `
		SELECT EXISTS(
			SELECT 1 FROM household_grant
			WHERE household_id = $1
			  AND grantee = $2
			  AND expires_at > NOW()
			  AND revoked_at IS NULL
		)
	`, householdID, subject).Scan(&isGrantee)
	if err != nil {
		return false, fmt.Errorf("check grantee: %w", err)
	}

	return isGrantee, nil
}

// MemberOnly checks if the subject is a current member of the household (not a grantee).
// Returns (true, nil) if authorized, (false, nil) if not authorized, or (false, error) on DB error.
// MemberOnly operations: grant further access, change membership, claim/transfer/release boards.
func (ap *AuthorizationPredicates) MemberOnly(ctx context.Context, householdID int64, subject string) (bool, error) {
	var isMember bool
	err := ap.db.QueryRow(ctx, `
		SELECT EXISTS(
			SELECT 1 FROM household_member
			WHERE household_id = $1
			  AND principal_id = $2
			  AND valid_to IS NULL
		)
	`, householdID, subject).Scan(&isMember)
	if err != nil {
		return false, fmt.Errorf("check member: %w", err)
	}
	return isMember, nil
}

// GetCallerHousehold retrieves the household for the authenticated caller.
// Returns (householdID, nil) on success.
// Per FR75: every principal must have a household; unclaimed principals get one on first claim (FR76).
func (ap *AuthorizationPredicates) GetCallerHousehold(ctx context.Context) (int64, error) {
	claims, ok := grpcauth.ClaimsFromContext(ctx)
	if !ok {
		return 0, fmt.Errorf("not authenticated")
	}

	var householdID int64
	err := ap.db.QueryRow(ctx, `
		SELECT DISTINCT hm.household_id
		FROM household_member hm
		WHERE hm.principal_id = $1
		  AND hm.valid_to IS NULL
		LIMIT 1
	`, claims.Subject).Scan(&householdID)
	if err != nil {
		if err == pgx.ErrNoRows {
			return 0, fmt.Errorf("principal %s has no household", claims.Subject)
		}
		return 0, fmt.Errorf("get household: %w", err)
	}
	return householdID, nil
}

// GrantIsActive checks if a grant has not expired and has not been revoked.
// Returns true if the grant is currently valid.
func (ap *AuthorizationPredicates) GrantIsActive(ctx context.Context, grantID int64) (bool, error) {
	var isActive bool
	err := ap.db.QueryRow(ctx, `
		SELECT EXISTS(
			SELECT 1 FROM household_grant
			WHERE grant_id = $1
			  AND expires_at > NOW()
			  AND revoked_at IS NULL
		)
	`, grantID).Scan(&isActive)
	if err != nil {
		return false, fmt.Errorf("check grant active: %w", err)
	}
	return isActive, nil
}

// GetGrantDetails retrieves grant details for display (grantee, expiry).
func (ap *AuthorizationPredicates) GetGrantDetails(ctx context.Context, grantID int64) (grantee string, expiresAt time.Time, err error) {
	err = ap.db.QueryRow(ctx, `
		SELECT grantee, expires_at
		FROM household_grant
		WHERE grant_id = $1
	`, grantID).Scan(&grantee, &expiresAt)
	if err != nil {
		return "", time.Time{}, fmt.Errorf("get grant details: %w", err)
	}
	return grantee, expiresAt, nil
}

// Scope represents an authorization scope that a principal may hold.
// A scope defines a set of entities that a principal can access.
// V1 creates household scopes only, but the interface allows narrower scopes
// (e.g., region-scoped, plant-scoped) to be added without rewriting call sites.
// FR4.3: Type the authorization decision so a narrower scope is expressible later.
type Scope interface {
	// ScopeID returns a unique identifier for this scope.
	// Used for deduplication and comparison.
	ScopeID() string
}

// HouseholdScope represents access to a single household and all its entities.
type HouseholdScope struct {
	householdID int64
}

// NewHouseholdScope creates a new scope for a household.
func NewHouseholdScope(householdID int64) *HouseholdScope {
	return &HouseholdScope{householdID: householdID}
}

// ScopeID returns the unique identifier for this household scope.
func (s *HouseholdScope) ScopeID() string {
	return fmt.Sprintf("household:%d", s.householdID)
}

// HouseholdID returns the household ID this scope grants access to.
func (s *HouseholdScope) HouseholdID() int64 {
	return s.householdID
}

// AuthorizationDecision holds the authorization decision for a principal:
// which scopes (sets of entities) the principal can reach.
// FR4: Authorization is per entity, not per household.
// The decision is made based on the principal's reach; an entity is accessible
// only if it falls within one of the scopes in the reach set.
type AuthorizationDecision struct {
	// principalID identifies the principal making the request (e.g., from JWT claims).
	principalID string
	// reach is the set of scopes this principal can access.
	// A principal may hold multiple scopes (e.g., member of multiple households).
	reach map[string]Scope
}

// NewAuthorizationDecision creates a new authorization decision for a principal.
func NewAuthorizationDecision(principalID string, scopes ...Scope) *AuthorizationDecision {
	decision := &AuthorizationDecision{
		principalID: principalID,
		reach:       make(map[string]Scope),
	}
	for _, scope := range scopes {
		decision.reach[scope.ScopeID()] = scope
	}
	return decision
}

// PrincipalID returns the principal making this request.
func (d *AuthorizationDecision) PrincipalID() string {
	return d.principalID
}

// HasReach checks if this decision grants access to any scope.
func (d *AuthorizationDecision) HasReach() bool {
	return len(d.reach) > 0
}

// ContainsHousehold checks if the principal can reach a specific household.
// Returns true if the decision includes a household scope for this household ID.
// Used to quickly filter requests before per-entity checks.
func (d *AuthorizationDecision) ContainsHousehold(householdID int64) bool {
	scopeID := fmt.Sprintf("household:%d", householdID)
	_, ok := d.reach[scopeID]
	return ok
}

// Scopes returns all scopes in the reach set.
// Used for aggregate queries that must only include entities in the reach.
func (d *AuthorizationDecision) Scopes() []Scope {
	scopes := make([]Scope, 0, len(d.reach))
	for _, scope := range d.reach {
		scopes = append(scopes, scope)
	}
	return scopes
}

// HouseholdScopes returns all household scopes in the reach set.
// Filters the reach to only return scopes that are household scopes.
// Used for queries that need the concrete set of accessible households.
func (d *AuthorizationDecision) HouseholdScopes() []*HouseholdScope {
	var households []*HouseholdScope
	for _, scope := range d.reach {
		if hs, ok := scope.(*HouseholdScope); ok {
			households = append(households, hs)
		}
	}
	sort.Slice(households, func(i, j int) bool {
		return households[i].HouseholdID() < households[j].HouseholdID()
	})
	return households
}

// ── Per-entity authorization resolution ──────────────────────────────────────
// These functions resolve entities to their owning household and check if the
// principal can reach them. They enforce FR4: authorization is per entity.

// CanAccessBoard checks if the principal can access a board.
// Returns true if the board's household is in the principal's reach.
// FR4: Authorization is per entity. Returns true only if board's household
// is reachable.
func (d *AuthorizationDecision) CanAccessBoard(ctx context.Context, repo *Repository, boardID int64) (bool, error) {
	householdID, err := repo.GetBoardHousehold(ctx, boardID)
	if err != nil {
		if err == pgx.ErrNoRows {
			// Board not found or not yet claimed.
			return false, nil
		}
		return false, fmt.Errorf("resolve board %d household: %w", boardID, err)
	}
	return d.ContainsHousehold(householdID), nil
}

// CanAccessRegion checks if the principal can access a region.
// Returns true if the region's household is in the principal's reach.
// FR4: Authorization is per entity. Returns true only if region's household
// is reachable.
func (d *AuthorizationDecision) CanAccessRegion(ctx context.Context, repo *Repository, regionID int64) (bool, error) {
	householdID, err := repo.GetRegionHousehold(ctx, regionID)
	if err != nil {
		if err == pgx.ErrNoRows {
			// Region not found.
			return false, nil
		}
		return false, fmt.Errorf("resolve region %d household: %w", regionID, err)
	}
	return d.ContainsHousehold(householdID), nil
}

// CanAccessPlant checks if the principal can access a plant.
// Returns true if the plant's household is in the principal's reach.
// FR4: Authorization is per entity. Returns true only if plant's household
// is reachable.
func (d *AuthorizationDecision) CanAccessPlant(ctx context.Context, repo *Repository, plantID int64) (bool, error) {
	householdID, err := repo.GetPlantHousehold(ctx, plantID)
	if err != nil {
		if err == pgx.ErrNoRows {
			// Plant not found.
			return false, nil
		}
		return false, fmt.Errorf("resolve plant %d household: %w", plantID, err)
	}
	return d.ContainsHousehold(householdID), nil
}

// CanAccessSensor checks if the principal can access a sensor.
// Returns true if the sensor's household (via board) is in the principal's reach.
// FR4: Authorization is per entity. Returns true only if sensor's household
// is reachable.
func (d *AuthorizationDecision) CanAccessSensor(ctx context.Context, repo *Repository, sensorID int64) (bool, error) {
	householdID, err := repo.GetSensorHousehold(ctx, sensorID)
	if err != nil {
		if err == pgx.ErrNoRows {
			// Sensor not found.
			return false, nil
		}
		return false, fmt.Errorf("resolve sensor %d household: %w", sensorID, err)
	}
	return d.ContainsHousehold(householdID), nil
}
