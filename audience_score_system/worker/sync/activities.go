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
// LoadChannelState is unimplemented at Scaffold time -- issue #1574's own
// Implementation phase adds the Channels field (store.ChannelStore) and
// the real GetByID-backed lookup. SyncSchedule/SyncOutcomes are, by
// design, permanent no-op stubs in THIS package: their real
// implementations are #1576 and #1581, separate tasks that build on top
// of this workflow/worker machinery rather than editing it -- see the
// package doc comment's "Scaffold status".
type Activities struct {
	// Channels is unset at Scaffold time -- see LoadChannelState.
	Channels store.ChannelStore
}

var _ SyncActivities = (*Activities)(nil)

// LoadChannelState is unimplemented at scaffold time. See SyncActivities'
// doc comment and issue #1574's Implementation scope: the real
// implementation calls a.Channels.GetByID(ctx, channelID) and maps
// store.Channel.ConnectionState onto ChannelState.
func (a *Activities) LoadChannelState(ctx context.Context, channelID uuid.UUID) (ChannelState, error) {
	return ChannelState{}, unimplemented(ActivityLoadChannelState)
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

// unimplemented builds the scaffold-time error every not-yet-implemented
// SyncActivities method returns. Not wrapped in
// temporal.NewApplicationError as non-retryable: Temporal's default retry
// policy retrying an unimplemented activity is harmless (it will keep
// failing until the real implementation lands) and keeping it a plain
// error avoids importing go.temporal.io/sdk/temporal here just for that
// one call -- mirrors
// tools/app_registry/worker/release/activities.go's identical helper.
func unimplemented(activityName string) error {
	return fmt.Errorf("sync.%s: not implemented yet (scaffold only -- see issue #1574's Implementation scope)", activityName)
}
