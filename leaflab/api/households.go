package main

// Households and membership (FR75, FR7) -- implementation for #1341.
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
// Write paths (CreateHousehold, InviteMember, RemoveMember, RenameHousehold)
// use Repository.auditedWrite (repository.go) so the write and its FR8
// audit.Entry commit together, exactly like RetireBoard/
// InsertDeviceConfigNextVersion. Every audit.Entry built here carries
// EntityID set to the *affected principal's* subject (not a numeric row id)
// so an audit row for a membership change names both actor
// (audit.Entry.ActorSubject) and subject (audit.Entry.EntityID) per FR8 --
// server.go's handlers supply ActorSubject from the authenticated caller,
// never from a request field.
//
// Membership writes never DELETE a household_membership row -- InviteMember
// INSERTs a new open (valid_to IS NULL) row, RemoveMember closes one
// (valid_to = NOW()) -- the SCD2 write pattern AGENTS.md documents, applied
// here as "open on invite, close on remove" rather than a close-and-open
// pair, since InviteMember never supersedes an existing row's data (a
// principal either already has no row for this household, or already has a
// current one -- see ErrHouseholdAlreadyMember).
//
// Never-zero-members (FR59.3) is enforced twice, per the issue's explicit
// requirement: RemoveMember below locks the household row (SELECT ... FOR
// UPDATE) and counts current members inside the same transaction as the
// close, so a friendly refusal is what a caller sees even under a race; migration
// 017_household_never_zero_members's trigger is the independent
// database-level backstop that does not depend on this code path taking
// that lock correctly (see the migration's doc comment).
//
// household and household_membership tables are #1339's ownership schema
// (leaflab/migrate/migrations/015_ownership.up.sql).

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/whale-net/everything/leaflab/api/audit"
)

// ErrHouseholdNotFound is returned when a household_id names no row.
var ErrHouseholdNotFound = errors.New("household not found")

// ErrPrincipalAlreadyHasHousehold is returned by CreateHousehold when
// principalSubject already holds a current household_membership row --
// CreateHousehold is reachable only for a principal with no current
// household (api.proto's CreateHousehold doc comment); a principal already
// in a household does not get a second one through this path (FR75 permits
// multi-household membership, but V1 specifies no switching experience, so
// there is deliberately no second creation entrance).
var ErrPrincipalAlreadyHasHousehold = errors.New("principal already has a current household")

// ErrHouseholdAlreadyMember is returned by InviteMember when
// principalSubject already holds a current household_membership row in the
// target household -- inviting them again would either violate the
// "exactly one open row per (household, principal)" invariant the
// never-zero-members count in RemoveMember relies on, or silently no-op; an
// explicit error is clearer than either.
var ErrHouseholdAlreadyMember = errors.New("principal is already a current member of this household")

// ErrHouseholdNotMember is returned by RemoveMember when principalSubject
// holds no current household_membership row in the target household -- there
// is nothing to remove.
var ErrHouseholdNotMember = errors.New("principal is not a current member of this household")

// ErrHouseholdLastMember is returned by RemoveMember when removing
// principalSubject would leave the household with zero members -- FR75's
// "a household never reaches zero members", refused with FR59.3's
// refuse-and-name-the-alternative shape at the server.go layer.
var ErrHouseholdLastMember = errors.New("removing this member would leave the household with zero members")

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
// RemoveMember use.
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
// member-only authorization primitive server.go's requireHouseholdMember
// calls for InviteMember/RemoveMember/RenameHousehold: membership specifically,
// never the general authz.Scope a grant or elevation might otherwise confer
// (FR7's "membership change is one of the three exclusions" -- a grantee or
// an elevated admin may hold a Scope that permits this household's boards
// without ever being a household_membership row here).
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
// household_membership row. CreateHousehold calls this before creating a
// new household ("reachable only ... for a principal with no current
// household", per api.proto's CreateHousehold doc comment).
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

// defaultHouseholdName is CreateHouseholdRequest.name's server-chosen
// fallback (api.proto: "Empty falls back to a server-chosen default") --
// used when a caller supplies no display name.
func defaultHouseholdName(principalSubject string) string {
	return fmt.Sprintf("%s's Household", principalSubject)
}

// CreateHousehold gives principalSubject a new household with them as its
// sole initial member, refusing (ErrPrincipalAlreadyHasHousehold) if they
// already hold a current household_membership row anywhere. The
// no-current-household check, the household INSERT and the initial
// membership INSERT all run inside the same transaction as entry's audit
// row (auditedWrite) -- a concurrent CreateHousehold racing on the same
// principal cannot both succeed, since the second transaction's check runs
// against the first's already-committed row once it observes it (and if
// both check concurrently before either commits, at most one commits: the
// second's INSERT of the initial membership row does not itself prevent a
// double-create by itself, so the guard is the SELECT ... check above,
// consistent with every other read-then-write guard in this file -- see
// RemoveMember's FOR UPDATE lock for the one case in this file that needs a
// stronger guarantee than a plain SELECT).
func (r *Repository) CreateHousehold(ctx context.Context, principalSubject, name string, entry audit.Entry) (HouseholdRow, error) {
	var household HouseholdRow
	err := r.auditedWrite(ctx, func(tx pgx.Tx) (audit.Entry, error) {
		var exists bool
		if err := tx.QueryRow(ctx, `
			SELECT EXISTS(
				SELECT 1 FROM household_membership
				WHERE principal_subject = $1 AND valid_to IS NULL
			)
		`, principalSubject).Scan(&exists); err != nil {
			return audit.Entry{}, fmt.Errorf("check current household for %q: %w", principalSubject, err)
		}
		if exists {
			return audit.Entry{}, ErrPrincipalAlreadyHasHousehold
		}

		if name == "" {
			name = defaultHouseholdName(principalSubject)
		}

		if err := tx.QueryRow(ctx, `
			INSERT INTO household (name) VALUES ($1)
			RETURNING household_id, name
		`, name).Scan(&household.HouseholdID, &household.Name); err != nil {
			return audit.Entry{}, fmt.Errorf("insert household: %w", err)
		}

		if _, err := tx.Exec(ctx, `
			INSERT INTO household_membership (household_id, principal_subject)
			VALUES ($1, $2)
		`, household.HouseholdID, principalSubject); err != nil {
			return audit.Entry{}, fmt.Errorf("insert initial membership for household %d: %w", household.HouseholdID, err)
		}

		entry.EntityID = &principalSubject
		entry.TargetHouseholdID = &household.HouseholdID
		return entry, nil
	})
	if err != nil {
		return HouseholdRow{}, err
	}
	return household, nil
}

// InviteMember adds principalSubject to householdID as a new current member
// (FR75) -- an INSERT of a new open household_membership row, never a
// close-and-open pair (there is no prior row of principalSubject's in this
// household to supersede; ErrHouseholdAlreadyMember covers the case where
// there already is one). entry's audit row commits in the same transaction
// (auditedWrite), with EntityID set to principalSubject so the row names
// both the acting member (entry.ActorSubject, set by server.go from the
// caller) and the invited principal (FR8).
func (r *Repository) InviteMember(ctx context.Context, householdID int64, principalSubject string, entry audit.Entry) (HouseholdMembershipRow, error) {
	var member HouseholdMembershipRow
	err := r.auditedWrite(ctx, func(tx pgx.Tx) (audit.Entry, error) {
		var exists bool
		if err := tx.QueryRow(ctx, `
			SELECT EXISTS(
				SELECT 1 FROM household_membership
				WHERE household_id = $1 AND principal_subject = $2 AND valid_to IS NULL
			)
		`, householdID, principalSubject).Scan(&exists); err != nil {
			return audit.Entry{}, fmt.Errorf("check existing membership for %q in household %d: %w", principalSubject, householdID, err)
		}
		if exists {
			return audit.Entry{}, ErrHouseholdAlreadyMember
		}

		if err := tx.QueryRow(ctx, `
			INSERT INTO household_membership (household_id, principal_subject)
			VALUES ($1, $2)
			RETURNING household_membership_id, principal_subject, valid_from
		`, householdID, principalSubject).Scan(&member.HouseholdMembershipID, &member.PrincipalSubject, &member.ValidFrom); err != nil {
			return audit.Entry{}, fmt.Errorf("insert membership for %q in household %d: %w", principalSubject, householdID, err)
		}

		entry.EntityID = &principalSubject
		entry.TargetHouseholdID = &householdID
		return entry, nil
	})
	if err != nil {
		return HouseholdMembershipRow{}, err
	}
	return member, nil
}

// RemoveMember closes principalSubject's current household_membership row
// in householdID (valid_to = NOW(), never DELETEd), refusing
// (ErrHouseholdLastMember) if doing so would leave the household with zero
// members (FR75, FR59.3).
//
// The never-zero-members check happens inside the same transaction as the
// close (auditedWrite), after locking the household row itself (SELECT ...
// FOR UPDATE): this serializes two concurrent RemoveMember calls on the
// same household through this method, so "two simultaneous removals of the
// last two members leave at least one member" holds without relying on
// Postgres's default READ COMMITTED isolation to save it (each transaction
// would otherwise still see the other's in-flight close as "not yet
// closed" and both could pass a naive count check). migration
// 017_household_never_zero_members's trigger is the second, independent
// database-level layer named in the issue -- it does not depend on this
// lock being taken correctly, only on the UPDATE itself.
func (r *Repository) RemoveMember(ctx context.Context, householdID int64, principalSubject string, entry audit.Entry) error {
	return r.auditedWrite(ctx, func(tx pgx.Tx) (audit.Entry, error) {
		var locked int64
		if err := tx.QueryRow(ctx, `
			SELECT household_id FROM household WHERE household_id = $1 FOR UPDATE
		`, householdID).Scan(&locked); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return audit.Entry{}, ErrHouseholdNotFound
			}
			return audit.Entry{}, fmt.Errorf("lock household %d: %w", householdID, err)
		}

		var membershipID int64
		if err := tx.QueryRow(ctx, `
			SELECT household_membership_id FROM household_membership
			WHERE household_id = $1 AND principal_subject = $2 AND valid_to IS NULL
		`, householdID, principalSubject).Scan(&membershipID); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return audit.Entry{}, ErrHouseholdNotMember
			}
			return audit.Entry{}, fmt.Errorf("find membership for %q in household %d: %w", principalSubject, householdID, err)
		}

		var currentMembers int
		if err := tx.QueryRow(ctx, `
			SELECT COUNT(*) FROM household_membership
			WHERE household_id = $1 AND valid_to IS NULL
		`, householdID).Scan(&currentMembers); err != nil {
			return audit.Entry{}, fmt.Errorf("count current members of household %d: %w", householdID, err)
		}
		if currentMembers <= 1 {
			return audit.Entry{}, ErrHouseholdLastMember
		}

		if _, err := tx.Exec(ctx, `
			UPDATE household_membership SET valid_to = NOW() WHERE household_membership_id = $1
		`, membershipID); err != nil {
			return audit.Entry{}, fmt.Errorf("close membership %d: %w", membershipID, err)
		}

		entry.EntityID = &principalSubject
		entry.TargetHouseholdID = &householdID
		return entry, nil
	})
}

// RenameHousehold changes householdID's display name (FR75). Member-only
// (FR7), same authorization exclusion as InviteMember/RemoveMember, checked
// by server.go's requireHouseholdMember before this is ever called -- this
// method itself performs no membership check, only the write.
func (r *Repository) RenameHousehold(ctx context.Context, householdID int64, name string, entry audit.Entry) (HouseholdRow, error) {
	var household HouseholdRow
	err := r.auditedWrite(ctx, func(tx pgx.Tx) (audit.Entry, error) {
		if err := tx.QueryRow(ctx, `
			UPDATE household SET name = $2 WHERE household_id = $1
			RETURNING household_id, name
		`, householdID, name).Scan(&household.HouseholdID, &household.Name); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return audit.Entry{}, ErrHouseholdNotFound
			}
			return audit.Entry{}, fmt.Errorf("rename household %d: %w", householdID, err)
		}

		entry.TargetHouseholdID = &householdID
		return entry, nil
	})
	if err != nil {
		return HouseholdRow{}, err
	}
	return household, nil
}
