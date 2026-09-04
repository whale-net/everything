package store

import (
	"context"

	"github.com/google/uuid"
)

// CanApprove, CanInvite, CanReconnect, CanRead, CanWrite, CanRemove, and
// CanViewAudit are the ONLY sanctioned way M1/M2 code answers "is this
// Person allowed to do X on this Channel" (NFR5, NFR6). Every one of them
// is defined purely in terms of RoleStore.RolesFor -- reading the open
// (valid_to IS NULL) channel_person row(s) for the (channelID, personID)
// pair -- never a channel.owner_id column (there is none) and never an
// assumption that a Channel has exactly one Creator. No handler, tool, or
// workflow outside this package may reconstruct one of these checks from
// raw SQL.

// CanApprove reports whether personID currently holds role=creator or
// role=co_creator on channelID -- required to approve/finalize a schedule
// draft (C8). Founder and Co-Creator hold symmetric authority here
// (FR32); there is no tiebreak or consensus logic between them (NFR6's
// explicit non-goal).
func CanApprove(ctx context.Context, rs RoleStore, channelID, personID uuid.UUID) (bool, error) {
	return hasRole(ctx, rs, channelID, personID, RoleCreator, RoleCoCreator)
}

// CanInvite reports whether personID currently holds role=creator or
// role=co_creator on channelID -- required to generate an invite code
// (FR5, FR32). Founder and Co-Creator hold symmetric authority here.
func CanInvite(ctx context.Context, rs RoleStore, channelID, personID uuid.UUID) (bool, error) {
	return hasRole(ctx, rs, channelID, personID, RoleCreator, RoleCoCreator)
}

// CanReconnect reports whether personID currently holds role=creator or
// role=co_creator on channelID -- required to re-establish a
// needs_reauth Channel's YouTube connection (FR32). Founder and
// Co-Creator hold symmetric authority here.
func CanReconnect(ctx context.Context, rs RoleStore, channelID, personID uuid.UUID) (bool, error) {
	return hasRole(ctx, rs, channelID, personID, RoleCreator, RoleCoCreator)
}

// CanRead reports whether personID currently holds role=creator,
// role=co_creator, or role=analyst on channelID.
func CanRead(ctx context.Context, rs RoleStore, channelID, personID uuid.UUID) (bool, error) {
	return hasRole(ctx, rs, channelID, personID, RoleCreator, RoleCoCreator, RoleAnalyst)
}

// CanWrite reports whether personID currently holds role=creator,
// role=co_creator, or role=analyst on channelID.
func CanWrite(ctx context.Context, rs RoleStore, channelID, personID uuid.UUID) (bool, error) {
	return hasRole(ctx, rs, channelID, personID, RoleCreator, RoleCoCreator, RoleAnalyst)
}

// CanViewAudit reports whether personID currently holds role=creator or
// role=co_creator on channelID -- required to view v_channel_person_audit
// (FR35). Analysts are excluded.
func CanViewAudit(ctx context.Context, rs RoleStore, channelID, personID uuid.UUID) (bool, error) {
	return hasRole(ctx, rs, channelID, personID, RoleCreator, RoleCoCreator)
}

// CanRemove reports whether actorPersonID may remove targetPersonID's
// role on channelID (FR33), per this matrix of actor's role (rows) vs.
// target's role (columns) -- every cell resolved from each Person's own
// currently-held role(s) via RoleStore.RolesFor, never a static field:
//
//	actor \ target   Founder   Co-Creator   Analyst
//	Founder          false     true         true
//	Co-Creator       false     false        true
//	Analyst          false     false        false
//
// No cell ever authorizes removing a Founder -- FR33's "no action removes
// a Founder" and the milestone's explicit ownership-transfer non-goal.
// Self-removal is false in every cell too: a Founder removing themselves
// hits the Founder column, a Co-Creator removing themselves hits the
// Co-Creator column, both of which are already false.
//
// CanRemove returns false, nil (not an error) when targetPersonID holds
// no open role at all on channelID. Callers MUST treat that as the
// idempotent no-op success FR33 requires (there is nothing left to
// remove), not as an authorization failure -- this is the one place
// those two outcomes are easy to conflate, since both read as "false".
// Distinguishing them, if a caller needs to, requires a separate
// RolesFor(ctx, channelID, targetPersonID) call.
func CanRemove(ctx context.Context, rs RoleStore, channelID, actorPersonID, targetPersonID uuid.UUID) (bool, error) {
	actorRoles, err := rs.RolesFor(ctx, channelID, actorPersonID)
	if err != nil {
		return false, err
	}
	targetRoles, err := rs.RolesFor(ctx, channelID, targetPersonID)
	if err != nil {
		return false, err
	}

	actorIsFounder := containsRole(actorRoles, RoleCreator)
	actorIsCoCreator := containsRole(actorRoles, RoleCoCreator)
	targetIsCoCreator := containsRole(targetRoles, RoleCoCreator)
	targetIsAnalyst := containsRole(targetRoles, RoleAnalyst)

	switch {
	case actorIsFounder:
		return targetIsCoCreator || targetIsAnalyst, nil
	case actorIsCoCreator:
		return targetIsAnalyst, nil
	default:
		return false, nil
	}
}

// containsRole reports whether roles contains want -- a set-membership
// check only, never a tier-order comparison (NFR7).
func containsRole(roles []Role, want Role) bool {
	for _, r := range roles {
		if r == want {
			return true
		}
	}
	return false
}

// hasRole reports whether personID's currently-held roles on channelID
// (per RoleStore.RolesFor, i.e. the open channel_person row(s)) intersect
// any of want. A Person whose only row on the Channel has been closed
// (valid_to set) holds no current roles, so this reads false -- proving
// every Can* check above reads live join-table state, never a static
// field (NFR5).
func hasRole(ctx context.Context, rs RoleStore, channelID, personID uuid.UUID, want ...Role) (bool, error) {
	roles, err := rs.RolesFor(ctx, channelID, personID)
	if err != nil {
		return false, err
	}
	for _, held := range roles {
		for _, w := range want {
			if held == w {
				return true, nil
			}
		}
	}
	return false, nil
}
