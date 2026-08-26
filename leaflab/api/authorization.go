package main

import (
	"context"
	"fmt"
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
