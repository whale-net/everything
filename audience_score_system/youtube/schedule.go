package youtube

import (
	"context"
	"time"

	"github.com/whale-net/everything/audience_score_system/store"
)

// Video is one item ListSchedule returns -- maps 1:1 onto
// store.SyncedVideo's data columns (migration 002, FR14/FR21) so a caller
// (worker, #1574) can upsert it via store.SyncStore.UpsertVideos after
// attaching ChannelID (this package deals only in YouTube-side ids, never
// this app's internal uuids).
type Video struct {
	YouTubeVideoID string
	Title          string
	PrivacyStatus  store.PrivacyStatus

	// PublishAt is set for a scheduled/private draft not yet published --
	// nil once PublishedAt is set.
	PublishAt *time.Time

	// PublishedAt is set once the video is actually live.
	PublishedAt *time.Time

	// IsScheduledDraft is true exactly for a private video with a future
	// PublishAt -- FR18 collision detection depends on this being correct,
	// not merely present.
	IsScheduledDraft bool
}

// ListSchedule returns every public upload AND Studio scheduled/private
// draft for the authorized Channel (FR14), paginating internally so
// callers never see a partial page.
//
// Scaffold only (issue #1573): returns errNotImplemented. The real
// implementation must reach scheduled/private drafts, not just public
// uploads -- the authenticated-owner path (search.list/playlistItems over
// the uploads playlist plus videos.list with status+snippet parts,
// forMine/owner semantics), confirmed against current Google API docs. If
// the scope granted by web/channel.Scopes (#1571) turns out insufficient
// to reach drafts, that must be reported on this issue and on #1571 rather
// than silently dropping them -- drafts are load-bearing for FR18. Lands
// in the Implementation phase; the Testing section's red/green note says
// to write the scheduled-draft mapping assertions before this mapping
// code.
func (c *client) ListSchedule(ctx context.Context, channelID string) ([]Video, error) {
	return nil, errNotImplemented
}
