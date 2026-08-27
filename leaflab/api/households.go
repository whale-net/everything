package main

// Households and membership (FR75, FR7) -- scaffold for #1341.
//
// Invitation model decision (Scaffold section, FR75): V1 implements (a)
// direct add by principal subject, not (b) an invitation row the invitee
// accepts. (b) was preferred by the requirement text -- it avoids one
// member asserting another principal's identity -- but is a bigger surface
// (an invitation table, an accept RPC, a pending-vs-member distinction
// threaded through ListHouseholdMembers) than V1's timeline supports.
// InviteMember's audit row names both the acting member and the invited
// principal (FR8), which is the condition the requirement text sets for
// (a) being acceptable. Recorded here, and in the PR, per the issue's
// "record the choice in the PR" instruction.
//
// This file is the Scaffold-phase skeleton: proto messages/RPCs exist
// (api.proto) and the read paths below are real, but no RPC handler in
// server.go calls into this file yet, and the write paths
// (CreateHousehold, InviteMember, RemoveMember, RenameHousehold) are not
// implemented -- they return ErrHouseholdOpNotImplemented until the
// Implementation phase wires in:
//   - the never-zero-members refusal (FR59.3) plus its database-level
//     guard, enforced in the same transaction as a removal;
//   - the SCD2 close-and-open write pattern on household_membership;
//   - an audit.Entry naming both actor and affected principal, written via
//     Repository.auditedWrite (see repository.go), for every membership
//     change;
//   - the member-only authorization check (FR7's membership-change
//     exclusion: neither a grantee nor an elevated admin may call these,
//     even though both may hold general read/write capability elsewhere).
//
// household and household_membership tables are #1339's ownership schema
// (leaflab/migrate/migrations/015_ownership.up.sql) -- this task adds no
// migration of its own for them. The never-zero-members database-level
// guard named in the Implementation section, if it needs new schema (e.g.
// a constraint trigger), is Implementation-phase work and will pick the
// next free migration number at that time.

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
)

// ErrHouseholdOpNotImplemented is returned by the household write paths
// that are scaffolded (signature only) but not yet implemented -- see this
// file's doc comment.
var ErrHouseholdOpNotImplemented = errors.New("household operation not implemented (Implementation phase)")

// ErrHouseholdNotFound is returned when a household_id names no row.
var ErrHouseholdNotFound = errors.New("household not found")

// HouseholdRow is one household, as returned by GetHouseholdByID.
type HouseholdRow struct {
	HouseholdID int64
	Name        string
}

// HouseholdMembershipRow is one current household_membership row, as
// returned by ListHouseholdMembers.
type HouseholdMembershipRow struct {
	HouseholdMembershipID int64
	PrincipalSubject      string
	ValidFrom             time.Time
}

// GetHouseholdByID returns a household's id and display name.
// ErrHouseholdNotFound covers both "never existed" and "no longer exists"
// -- household rows are never deleted (ON DELETE RESTRICT elsewhere), so in
// practice this is only "never existed".
func (r *Repository) GetHouseholdByID(ctx context.Context, householdID int64) (HouseholdRow, error) {
	var h HouseholdRow
	err := r.db.QueryRow(ctx, `
		SELECT household_id, name
		FROM household
		WHERE household_id = $1
	`, householdID).Scan(&h.HouseholdID, &h.Name)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return HouseholdRow{}, ErrHouseholdNotFound
		}
		return HouseholdRow{}, fmt.Errorf("get household %d: %w", householdID, err)
	}
	return h, nil
}

// ListHouseholdMembers returns up to limit current members of householdID,
// keyset-paginated on household_membership_id per FR61 -- the same
// after-id/hasAfter/limit+1 shape as Repository.ListBoards. Only current
// rows (valid_to IS NULL) are ever returned; a member's superseded rows are
// history, not membership, per the SCD2 write pattern InviteMember/
// RemoveMember will use once implemented.
func (r *Repository) ListHouseholdMembers(ctx context.Context, householdID int64, afterMembershipID int64, hasAfter bool, limit int32) ([]HouseholdMembershipRow, error) {
	var sqlQuery string
	var args []any
	if hasAfter {
		sqlQuery = `
			SELECT household_membership_id, principal_subject, valid_from
			FROM household_membership
			WHERE household_id = $1
			  AND valid_to IS NULL
			  AND household_membership_id > $2
			ORDER BY household_membership_id
			LIMIT $3
		`
		args = []any{householdID, afterMembershipID, limit}
	} else {
		sqlQuery = `
			SELECT household_membership_id, principal_subject, valid_from
			FROM household_membership
			WHERE household_id = $1
			  AND valid_to IS NULL
			ORDER BY household_membership_id
			LIMIT $2
		`
		args = []any{householdID, limit}
	}

	rows, err := r.db.Query(ctx, sqlQuery, args...)
	if err != nil {
		return nil, fmt.Errorf("list household members for household %d: %w", householdID, err)
	}
	defer rows.Close()

	var members []HouseholdMembershipRow
	for rows.Next() {
		var m HouseholdMembershipRow
		if err := rows.Scan(&m.HouseholdMembershipID, &m.PrincipalSubject, &m.ValidFrom); err != nil {
			return nil, fmt.Errorf("scan household member: %w", err)
		}
		members = append(members, m)
	}
	return members, rows.Err()
}

// IsCurrentHouseholdMember reports whether principalSubject currently holds
// a household_membership row (valid_to IS NULL) in householdID. This is the
// member-only authorization primitive the Implementation section calls
// for: InviteMember/RemoveMember/RenameHousehold must check membership
// specifically, never the general write capability a grant or elevation
// might otherwise confer (FR7).
func (r *Repository) IsCurrentHouseholdMember(ctx context.Context, householdID int64, principalSubject string) (bool, error) {
	var exists bool
	err := r.db.QueryRow(ctx, `
		SELECT EXISTS(
			SELECT 1 FROM household_membership
			WHERE household_id = $1
			  AND principal_subject = $2
			  AND valid_to IS NULL
		)
	`, householdID, principalSubject).Scan(&exists)
	if err != nil {
		return false, fmt.Errorf("check membership for %q in household %d: %w", principalSubject, householdID, err)
	}
	return exists, nil
}

// HasCurrentHousehold reports whether principalSubject currently holds any
// household_membership row. CreateHousehold's Implementation-phase guard
// ("reachable only ... for a principal with no current household", per
// api.proto's CreateHousehold doc comment) will call this before creating
// a new household.
func (r *Repository) HasCurrentHousehold(ctx context.Context, principalSubject string) (bool, error) {
	var exists bool
	err := r.db.QueryRow(ctx, `
		SELECT EXISTS(
			SELECT 1 FROM household_membership
			WHERE principal_subject = $1
			  AND valid_to IS NULL
		)
	`, principalSubject).Scan(&exists)
	if err != nil {
		return false, fmt.Errorf("check current household for %q: %w", principalSubject, err)
	}
	return exists, nil
}

// CreateHousehold is not yet implemented -- see this file's doc comment.
// Signature carries what the Implementation phase needs: the creating
// principal (becomes the sole initial member) and a display name.
func (r *Repository) CreateHousehold(ctx context.Context, principalSubject, name string) (HouseholdRow, error) {
	return HouseholdRow{}, ErrHouseholdOpNotImplemented
}

// InviteMember is not yet implemented -- see this file's doc comment.
func (r *Repository) InviteMember(ctx context.Context, householdID int64, principalSubject string) (HouseholdMembershipRow, error) {
	return HouseholdMembershipRow{}, ErrHouseholdOpNotImplemented
}

// RemoveMember is not yet implemented -- see this file's doc comment. Will
// refuse (FR59.3) rather than error when the removal would leave the
// household with zero members.
func (r *Repository) RemoveMember(ctx context.Context, householdID int64, principalSubject string) error {
	return ErrHouseholdOpNotImplemented
}

// RenameHousehold is not yet implemented -- see this file's doc comment.
func (r *Repository) RenameHousehold(ctx context.Context, householdID int64, name string) (HouseholdRow, error) {
	return HouseholdRow{}, ErrHouseholdOpNotImplemented
}
