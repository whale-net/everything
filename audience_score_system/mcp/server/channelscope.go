// Channel-scoping middleware. Every product tool's input type takes a
// channel_id argument by implementing ChannelScoped; RegisterRead/
// RegisterWrite (registry.go) type-assert each call's decoded input
// against that interface and, when it matches, reject a caller
// (PersonFromContext, auth.go) with no live channel_person row for that
// Channel before the tool handler runs -- store.CanRead for read tools,
// store.CanWrite for write tools (NFR5). A tool whose input does not
// implement ChannelScoped (e.g. whoami, which has no channel_id) is not
// scoped -- this is deliberate, not an oversight: NFR5 only applies to
// Channel-scoped data. No handler, tool, or workflow may reconstruct this
// check from raw SQL; RequireChannelRole is the only sanctioned entry
// point.
package server

import (
	"context"
	"fmt"

	"github.com/google/uuid"

	"github.com/whale-net/everything/audience_score_system/store"
)

// ChannelScoped is implemented by a product tool's input type when its
// schema carries a channel_id argument. RegisterRead/RegisterWrite
// (registry.go) type-assert each call's decoded input against this
// interface at call time (not registration time, since the generic input
// type is only ever a concrete struct at the point it's been unmarshaled)
// to decide whether Channel-scope authorization applies.
type ChannelScoped interface {
	// ChannelScopeID returns the Channel this call is scoped to.
	ChannelScopeID() uuid.UUID
}

// ChannelRoleCheck is the store.CanRead/store.CanWrite function shape
// (NFR5) -- RequireChannelRole is generic over which one a tool needs.
type ChannelRoleCheck func(ctx context.Context, rs store.RoleStore, channelID, personID uuid.UUID) (bool, error)

// RequireChannelRole enforces check for personID against channelID,
// returning a permission-denied error if it does not hold, and nil if it
// does. Called from RegisterRead/RegisterWrite (registry.go) for every
// tool whose input implements ChannelScoped.
func RequireChannelRole(ctx context.Context, roles store.RoleStore, check ChannelRoleCheck, channelID, personID uuid.UUID) error {
	ok, err := check(ctx, roles, channelID, personID)
	if err != nil {
		return fmt.Errorf("channel scope check: %w", err)
	}
	if !ok {
		return fmt.Errorf("permission denied: no live role on this Channel")
	}
	return nil
}
