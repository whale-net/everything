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
// SyncOutcomes is #1581's real implementation (outcomes.go), same pattern.
type Activities struct {
	// Channels backs LoadChannelState's store.Channel lookup and
	// SyncSchedule's/SyncOutcomes' `channel.youtube_channel_id` lookup.
	Channels store.ChannelStore

	// Tokens resolves channelID's oauth2.TokenSource (SyncSchedule/
	// SyncOutcomes build a youtube.Client from it) and marks a Channel
	// needs-reauth when either sees youtube.ErrRevoked -- see
	// video_sync.go/outcomes.go.
	Tokens tokens.Store

	// Sync is SyncSchedule's `synced_video` write target and SyncOutcomes'
	// `synced_video` read source + `video_metrics` write target (migration
	// 002).
	Sync store.SyncStore

	// Matches is SyncOutcomes' `video_schedule_match` write target and
	// matcher-candidate read source (migration 002, issue #1581).
	Matches store.MatchStore

	// NewYouTubeClient builds SyncSchedule's/SyncOutcomes' youtube.Client
	// for a given oauth2.TokenSource -- production wiring (../main.go)
	// sets this to youtube.New; tests substitute a factory returning a
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
