// Channel-scoping middleware -- Scaffold-only skeleton for issue #1575's
// Implementation phase. Every product tool takes a channel_id argument;
// once wired into RegisterRead/RegisterWrite (registry.go), a call whose
// caller (PersonFromContext, auth.go) holds no live channel_person row for
// that Channel must be rejected with a permission error before the tool
// handler runs -- store.CanRead for read tools, store.CanWrite for write
// tools (NFR5). No handler, tool, or workflow may reconstruct this check
// from raw SQL; RequireChannelRole is the only sanctioned entry point once
// wired.
package server

import (
	"context"
	"fmt"

	"github.com/google/uuid"

	"github.com/whale-net/everything/audience_score_system/store"
)

// ChannelRoleCheck is the store.CanRead/store.CanWrite function shape
// (NFR5) -- RequireChannelRole is generic over which one a tool needs.
type ChannelRoleCheck func(ctx context.Context, rs store.RoleStore, channelID, personID uuid.UUID) (bool, error)

// RequireChannelRole enforces check for personID against channelID,
// returning a permission-denied error if it does not hold, and nil if it
// does. Scaffold only -- not yet called from RegisterRead/RegisterWrite;
// issue #1575's Implementation phase threads it through every product
// tool's channel_id argument before the handler runs.
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
