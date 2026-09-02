package sync

import (
	"context"
	"fmt"

	"github.com/google/uuid"
	"golang.org/x/oauth2"

	"github.com/whale-net/everything/audience_score_system/store"
	"github.com/whale-net/everything/audience_score_system/tokens"
	"github.com/whale-net/everything/audience_score_system/youtube"
)

// Activities is the concrete SyncActivities implementation worker/main.go
// registers.
//
// SyncSchedule is #1576's real implementation (video_sync.go), built on
// top of this workflow/worker machinery rather than editing it.
// SyncOutcomes is #1581's, and remains a permanent no-op stub in THIS
// package until that task lands -- see the package doc comment's
// "Scaffold status".
type Activities struct {
	// Channels backs LoadChannelState's store.Channel lookup and
	// SyncSchedule's `channel.youtube_channel_id` lookup.
	Channels store.ChannelStore

	// Tokens resolves channelID's oauth2.TokenSource (SyncSchedule builds
	// a youtube.Client from it) and marks a Channel needs-reauth when
	// SyncSchedule sees youtube.ErrRevoked -- see video_sync.go.
	Tokens tokens.Store

	// Sync is SyncSchedule's `synced_video` write target (migration 002).
	Sync store.SyncStore

	// NewYouTubeClient builds SyncSchedule's youtube.Client for a given
	// oauth2.TokenSource -- production wiring (../main.go) sets this to
	// youtube.New; tests substitute a factory returning a
	// youtube/fake.Client so no test here ever makes a live network call.
	NewYouTubeClient func(ts oauth2.TokenSource) youtube.Client
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

// SyncOutcomes is a genuine no-op stub (issue #1574's Scaffold section):
// it always succeeds without doing anything, so ChannelSyncWorkflow's
// connected-Channel path is independently buildable and testable before
// #1581 lands with the real YouTube outcome-sync activity.
func (a *Activities) SyncOutcomes(ctx context.Context, channelID uuid.UUID) error {
	return nil
}
