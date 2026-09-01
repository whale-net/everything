package store

import (
	"context"
	"errors"

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
//
// Scaffold only (issue #1568): every function below is a stub returning
// errors.New("not implemented"). Full implementation lands in the
// Implementation phase.

// CanApprove reports whether personID currently holds role=creator on
// channelID -- required to approve/finalize a schedule draft (C8).
func CanApprove(ctx context.Context, rs RoleStore, channelID, personID uuid.UUID) (bool, error) {
	return false, errors.New("not implemented")
}

// CanInvite reports whether personID currently holds role=creator on
// channelID -- required to generate an invite code (FR5).
func CanInvite(ctx context.Context, rs RoleStore, channelID, personID uuid.UUID) (bool, error) {
	return false, errors.New("not implemented")
}

// CanReconnect reports whether personID currently holds role=creator on
// channelID -- required to re-establish a needs_reauth Channel's YouTube
// connection.
func CanReconnect(ctx context.Context, rs RoleStore, channelID, personID uuid.UUID) (bool, error) {
	return false, errors.New("not implemented")
}

// CanRead reports whether personID currently holds role=creator or
// role=analyst on channelID.
func CanRead(ctx context.Context, rs RoleStore, channelID, personID uuid.UUID) (bool, error) {
	return false, errors.New("not implemented")
}

// CanWrite reports whether personID currently holds role=creator or
// role=analyst on channelID.
func CanWrite(ctx context.Context, rs RoleStore, channelID, personID uuid.UUID) (bool, error) {
	return false, errors.New("not implemented")
}
