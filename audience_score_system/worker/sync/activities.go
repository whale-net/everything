package sync

import (
	"context"
	"fmt"

	"github.com/google/uuid"

	"github.com/whale-net/everything/audience_score_system/store"
)

// Activities is the concrete SyncActivities implementation worker/main.go
// registers.
//
// SyncSchedule/SyncOutcomes are, by design, permanent no-op stubs in THIS
// package: their real implementations are #1576 and #1581, separate tasks
// that build on top of this workflow/worker machinery rather than editing
// it -- see the package doc comment's "Scaffold status".
type Activities struct {
	// Channels backs LoadChannelState's store.Channel lookup.
	Channels store.ChannelStore
}

var _ SyncActivities = (*Activities)(nil)

// LoadChannelState resolves channelID's current connection_state via
// a.Channels.GetByID, mapping store.Channel.ConnectionState onto
// ChannelState -- see SyncActivities' doc comment.
func (a *Activities) LoadChannelState(ctx context.Context, channelID uuid.UUID) (ChannelState, error) {
	ch, err := a.Channels.GetByID(ctx, channelID)
	if err != nil {
		return ChannelState{}, fmt.Errorf("%s: load channel %s: %w", ActivityLoadChannelState, channelID, err)
	}
	return ChannelState{ConnectionState: ch.ConnectionState}, nil
}

// SyncSchedule is a genuine no-op stub (issue #1574's Scaffold section):
// it always succeeds without doing anything, so ChannelSyncWorkflow's
// connected-Channel path is independently buildable and testable before
// #1576 lands with the real YouTube schedule-sync activity.
func (a *Activities) SyncSchedule(ctx context.Context, channelID uuid.UUID) error {
	return nil
}

// SyncOutcomes is a genuine no-op stub (issue #1574's Scaffold section):
// it always succeeds without doing anything, so ChannelSyncWorkflow's
// connected-Channel path is independently buildable and testable before
// #1581 lands with the real YouTube outcome-sync activity.
func (a *Activities) SyncOutcomes(ctx context.Context, channelID uuid.UUID) error {
	return nil
}
