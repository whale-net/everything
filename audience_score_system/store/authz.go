package store

import (
	"context"

	"github.com/google/uuid"
)

// CanApprove, CanInvite, CanReconnect, CanRead, and CanWrite are the ONLY
// sanctioned way M1 code answers "is this Person allowed to do X on this
// Channel" (NFR5). Every one of them is defined purely in terms of
// RoleStore.RolesFor -- reading the open (valid_to IS NULL) channel_person
// row(s) for the (channelID, personID) pair -- never a channel.owner_id
// column (there is none) and never an assumption that a Channel has
// exactly one Creator. No handler, tool, or workflow outside this package
// may reconstruct one of these checks from raw SQL.

// CanApprove reports whether personID currently holds role=creator on
// channelID -- required to approve/finalize a schedule draft (C8).
func CanApprove(ctx context.Context, rs RoleStore, channelID, personID uuid.UUID) (bool, error) {
	return hasRole(ctx, rs, channelID, personID, RoleCreator)
}

// CanInvite reports whether personID currently holds role=creator on
// channelID -- required to generate an invite code (FR5).
func CanInvite(ctx context.Context, rs RoleStore, channelID, personID uuid.UUID) (bool, error) {
	return hasRole(ctx, rs, channelID, personID, RoleCreator)
}

// CanReconnect reports whether personID currently holds role=creator on
// channelID -- required to re-establish a needs_reauth Channel's YouTube
// connection.
func CanReconnect(ctx context.Context, rs RoleStore, channelID, personID uuid.UUID) (bool, error) {
	return hasRole(ctx, rs, channelID, personID, RoleCreator)
}

// CanRead reports whether personID currently holds role=creator or
// role=analyst on channelID.
func CanRead(ctx context.Context, rs RoleStore, channelID, personID uuid.UUID) (bool, error) {
	return hasRole(ctx, rs, channelID, personID, RoleCreator, RoleAnalyst)
}

// CanWrite reports whether personID currently holds role=creator or
// role=analyst on channelID.
func CanWrite(ctx context.Context, rs RoleStore, channelID, personID uuid.UUID) (bool, error) {
	return hasRole(ctx, rs, channelID, personID, RoleCreator, RoleAnalyst)
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
