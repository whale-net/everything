package main

// FR9 owner-readable activity (#1348): the two repository queries
// ListHouseholdActivity's handler (activity.go) merges into one
// keyset-paginated list. Two sources, not one, because FR76.7's
// claim-attempt detection is deliberately sourced from claim_challenge
// rather than audit_log (leaflab/api/activity's package doc comment) --
// CompleteClaim only ever writes an audit row when a claim actually
// succeeds (a never-claimed or Unadopted board), never for an attempt
// against a real household's board, so the *attempt* itself has no
// audit_log row to read at all.
//
// Merging two independently-queried sources into one correctly
// keyset-paginated list needs a total order that survives a page boundary
// landing on either source. activityTag below is that order's tie-break;
// see contract.EncodeActivityCursor's doc comment for the cursor encoding
// built on top of it.

import (
	"context"
	"fmt"
	"time"

	"github.com/whale-net/everything/leaflab/api/activity"
	"github.com/whale-net/everything/leaflab/api/audit"
)

// activitySourceAudit / activitySourceClaim tag which of the two queries
// below produced a given row -- see activityTag.
const (
	activitySourceAudit = "audit"
	activitySourceClaim = "claim"
)

// activityTag builds the per-row tag contract.EncodeActivityCursor/
// DecodeActivityCursor's tag half carries: "<source>:<20-digit
// zero-padded id>". Zero-padding keeps lexicographic string comparison of
// the tag equivalent to numeric comparison of the id within one source,
// which is what lets ListAuditActivity's SQL compare a TEXT column against
// a cursor tag with no per-source branching, and what lets
// mergeActivitySources (activity.go) compare an audit tag against a claim tag
// with a plain string "<" -- audit_id and challenge_id are independent
// sequences, so "audit:..." vs "claim:..." ties are resolved by the source
// prefix alone, deterministically (if arbitrarily) rather than by
// accident.
func activityTag(source string, id int64) string {
	return fmt.Sprintf("%s:%020d", source, id)
}

// AuditActivityRow is one audit_log row scoped to a household (FR9),
// carrying everything ListHouseholdActivity's handler needs to resolve an
// activity.RenderInput plus this row's page position (Tag, OccurredAt).
type AuditActivityRow struct {
	Tag          string
	OccurredAt   time.Time
	Action       string
	EntityKind   string
	ActorKind    audit.ActorKind
	ActorSubject string
	EntityID     *string
}

// ListAuditActivity returns up to limit of householdID's audit_log rows,
// most recent first, keyset-paginated on (occurred_at DESC, tag DESC) per
// FR61. The row-value comparison against ($2, $3) is deliberately typed
// against 'audit:' || lpad(...) -- not audit_id directly -- so it compares
// correctly against afterTag even when the previous page's last-returned
// row came from ListClaimAttemptActivity instead (see
// contract.EncodeActivityCursor's doc comment). hasAfter false requests
// the first page.
func (r *Repository) ListAuditActivity(ctx context.Context, householdID int64, afterOccurredAt time.Time, afterTag string, hasAfter bool, limit int32) ([]AuditActivityRow, error) {
	var sqlQuery string
	var args []any
	if hasAfter {
		sqlQuery = `
			SELECT audit_id, occurred_at, action, entity_kind, actor_kind, actor_subject, entity_id
			FROM audit_log
			WHERE target_household_id = $1
			  AND (occurred_at, 'audit:' || lpad(audit_id::text, 20, '0')) < ($2, $3)
			ORDER BY occurred_at DESC, audit_id DESC
			LIMIT $4
		`
		args = []any{householdID, afterOccurredAt, afterTag, limit}
	} else {
		sqlQuery = `
			SELECT audit_id, occurred_at, action, entity_kind, actor_kind, actor_subject, entity_id
			FROM audit_log
			WHERE target_household_id = $1
			ORDER BY occurred_at DESC, audit_id DESC
			LIMIT $2
		`
		args = []any{householdID, limit}
	}

	rows, err := r.db.Query(ctx, sqlQuery, args...)
	if err != nil {
		return nil, fmt.Errorf("list audit activity for household %d: %w", householdID, err)
	}
	defer rows.Close()

	var out []AuditActivityRow
	for rows.Next() {
		var auditID int64
		var actorKind string
		var row AuditActivityRow
		if err := rows.Scan(&auditID, &row.OccurredAt, &row.Action, &row.EntityKind, &actorKind, &row.ActorSubject, &row.EntityID); err != nil {
			return nil, fmt.Errorf("scan audit activity: %w", err)
		}
		row.Tag = activityTag(activitySourceAudit, auditID)
		row.ActorKind = audit.ActorKind(actorKind)
		out = append(out, row)
	}
	return out, rows.Err()
}

// ClaimAttemptActivityRow is one FR76.7 claim-attempt entry: a resolved
// claim_challenge row (state 'discharged' or 'not_discharged') that
// occurred while a board_ownership row for one of householdID's boards --
// current or historical -- covered it. Outcome is one of
// activity.ClaimAttemptNotDischarged / *DischargedRetained /
// *DischargedDeparted, computed here (not in leaflab/api/activity, which
// takes no database dependency) from claim_challenge.state plus whether
// the covering board_ownership interval has since closed.
type ClaimAttemptActivityRow struct {
	Tag        string
	OccurredAt time.Time
	DeviceID   string
	Outcome    string
}

// claimAttemptOutcome maps one claim_challenge row's raw state (plus
// whether the board_ownership interval ListClaimAttemptActivity joined it
// against has since closed) to the three-valued outcome
// leaflab/api/activity's ClaimAttempt/board Template switches on -- see
// that Registry entry's doc comment for what each value means.
func claimAttemptOutcome(state string, ownershipValidTo *time.Time) string {
	if state != "discharged" {
		return activity.ClaimAttemptNotDischarged
	}
	if ownershipValidTo != nil {
		return activity.ClaimAttemptDischargedDeparted
	}
	return activity.ClaimAttemptDischargedRetained
}

// ListClaimAttemptActivity returns every claim attempt FR76.7 entitles
// householdID to see. The join's WHERE clause is the load-bearing part:
//
//	cc.opened_at >= bo.valid_from AND (bo.valid_to IS NULL OR cc.opened_at < bo.valid_to)
//
// scopes each claim_challenge row to whichever household's board_ownership
// interval it actually fell within, rather than to whichever household
// happens to own the board *now*. Without that historical join, a board
// that genuinely left the household after a successful claim would no
// longer satisfy a naive "board.household_id = $1" filter by the time
// anyone reads this list -- exactly the case this task's Implementation
// section's "the board left your household" wording describes. When
// bo.valid_to is NULL (the household still owns the board), a discharged
// challenge renders as ClaimAttemptDischargedRetained instead: CompleteClaim
// (claim.go) refuses to move a real household's board even once a
// challenge discharges (requirement 6), so "discharged" alone never
// implies "departed".
//
// Not paginated the way ListAuditActivity is: a household's boards
// accumulate claim attempts slowly in practice (bounded by NFR10's
// per-principal rate limits and claim.Config's concurrent-challenge cap),
// so ListHouseholdActivity's handler fetches this source in full on every
// call and merges it against ListAuditActivity's keyset page in Go.
func (r *Repository) ListClaimAttemptActivity(ctx context.Context, householdID int64) ([]ClaimAttemptActivityRow, error) {
	rows, err := r.db.Query(ctx, `
		SELECT cc.challenge_id, COALESCE(cc.discharged_at, cc.opened_at), cc.device_id, cc.state, bo.valid_to
		FROM claim_challenge cc
		JOIN board b ON b.device_id = cc.device_id
		JOIN board_ownership bo ON bo.board_id = b.board_id
		WHERE bo.household_id = $1
		  AND cc.state IN ('discharged', 'not_discharged')
		  AND cc.opened_at >= bo.valid_from
		  AND (bo.valid_to IS NULL OR cc.opened_at < bo.valid_to)
	`, householdID)
	if err != nil {
		return nil, fmt.Errorf("list claim attempt activity for household %d: %w", householdID, err)
	}
	defer rows.Close()

	var out []ClaimAttemptActivityRow
	for rows.Next() {
		var challengeID int64
		var occurredAt time.Time
		var deviceID string
		var state string
		var validTo *time.Time
		if err := rows.Scan(&challengeID, &occurredAt, &deviceID, &state, &validTo); err != nil {
			return nil, fmt.Errorf("scan claim attempt activity: %w", err)
		}
		out = append(out, ClaimAttemptActivityRow{
			Tag:        activityTag(activitySourceClaim, challengeID),
			OccurredAt: occurredAt,
			DeviceID:   deviceID,
			Outcome:    claimAttemptOutcome(state, validTo),
		})
	}
	return out, rows.Err()
}
