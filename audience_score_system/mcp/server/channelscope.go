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
	"errors"
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

// linkingRequiredMessage is FR11's discoverability response: it explains
// why a Channel-scoped call (or list_channels) came back empty for a
// whagent-authenticated caller and names the fix, in prose rather than a
// hardcoded cross-domain URL -- the caller already has a `ui` session by
// construction if it got here via the whagent auth path.
const linkingRequiredMessage = `no Channel access yet: this whagent-net identity is not linked to an ` +
	`Audience Score System Person with Channel access. Open whagent-net's ui and use the ` +
	`"Link ASS identity" action to connect your identity, then retry.`

// RequireChannelAccess enforces FR11's whole-Person zero-role check: a
// whagent-authenticated (AuthPathWhagent) caller who holds zero
// channel_person rows across EVERY Channel -- not just the one a call
// targets, which is RequireChannelRole's job, left completely unaffected --
// gets linkingRequiredMessage instead of a silent empty result or an
// undifferentiated permission error (US2). Deliberately a no-op for
// AuthPathMCPCredential: an ASS-native Person can hold zero roles for an
// unrelated reason (e.g. pending an invite), and telling them to go link a
// whagent-net identity would be wrong (FR12). Called from
// RegisterRead/RegisterWrite (registry.go) for every ChannelScoped tool,
// and directly from list_channels (mcp/tools/list_channels.go) -- the one
// unscoped tool FR11 also covers.
func RequireChannelAccess(ctx context.Context, roles store.RoleStore, personID uuid.UUID) error {
	if AuthPathFromContext(ctx) != AuthPathWhagent {
		return nil
	}
	channels, err := roles.ChannelsForPerson(ctx, personID)
	if err != nil {
		return fmt.Errorf("check whole-person channel access: %w", err)
	}
	if len(channels) == 0 {
		return errors.New(linkingRequiredMessage)
	}
	return nil
}
