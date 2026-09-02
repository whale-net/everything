package youtube

import (
	"context"
	"fmt"
	"time"

	youtubev3 "google.golang.org/api/youtube/v3"

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

// searchPageSize/videosBatchSize are Google's documented per-request
// maximums for search.list and videos.list respectively.
const (
	searchPageSize  = 50
	videosBatchSize = 50
)

// ListSchedule returns every public upload AND Studio scheduled/private
// draft for the authorized Channel (FR14), paginating internally so
// callers never see a partial page.
//
// Enumeration uses search.list(forMine=true, type=video) -- the
// authenticated-owner search path -- rather than the channel's "uploads"
// playlist (playlistItems.list), because a video scheduled for future
// publishAt is not reliably present in the uploads playlist until it
// actually goes live, which would silently drop exactly the drafts FR18
// collision detection depends on. search.list(forMine=true) is scoped to
// the OAuth-authenticated channel and, per Google's docs, includes the
// owner's private/unlisted content. Only video ids come back from search;
// videos.list(part=snippet,status) hydrates each id's title, privacy
// status, and publish timestamps in batches of videosBatchSize.
//
// If a future scope audit shows search.list(forMine=true) itself omits
// drafts under the scope granted by web/channel.Scopes (#1571), that must
// be reported on this issue and on #1571 rather than silently dropping
// them -- see this task's Implementation notes.
func (c *client) ListSchedule(ctx context.Context, channelID string) ([]Video, error) {
	if c.err != nil {
		return nil, classify(c.err)
	}

	ctx, cancel := c.withTimeout(ctx)
	defer cancel()

	ids, err := c.listOwnVideoIDs(ctx)
	if err != nil {
		return nil, classify(err)
	}
	if len(ids) == 0 {
		return nil, nil
	}

	videos := make([]Video, 0, len(ids))
	for _, batch := range chunkStrings(ids, videosBatchSize) {
		resp, err := c.yt.Videos.List([]string{"snippet", "status"}).Id(batch...).Context(ctx).Do()
		if err != nil {
			return nil, classify(err)
		}
		for _, v := range resp.Items {
			mapped, err := mapVideo(v, time.Now())
			if err != nil {
				return nil, classify(err)
			}
			videos = append(videos, mapped)
		}
	}
	return videos, nil
}

// listOwnVideoIDs paginates search.list(forMine=true, type=video) to
// completion, returning the full de-duplicated set of video ids owned by
// the authorized Channel. channelID is accepted by ListSchedule for the
// interface's own documentation purposes, but forMine=true already scopes
// the search to whichever channel ts (New's oauth2.TokenSource) is
// authorized for -- there is exactly one such channel per Client.
func (c *client) listOwnVideoIDs(ctx context.Context) ([]string, error) {
	seen := make(map[string]bool)
	var ids []string

	call := c.yt.Search.List([]string{"id"}).
		ForMine(true).
		Type("video").
		Order("date").
		MaxResults(searchPageSize).
		Context(ctx)

	err := call.Pages(ctx, func(page *youtubev3.SearchListResponse) error {
		for _, item := range page.Items {
			if item.Id == nil || item.Id.VideoId == "" {
				continue
			}
			if seen[item.Id.VideoId] {
				continue
			}
			seen[item.Id.VideoId] = true
			ids = append(ids, item.Id.VideoId)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return ids, nil
}

// mapVideo maps a single youtube/v3 Video resource (fetched with
// part=snippet,status) onto Video. now is the reference time
// IsScheduledDraft's "future publishAt" test is evaluated against --
// passed in explicitly so callers (and tests) control it rather than
// mapVideo calling time.Now() itself.
func mapVideo(v *youtubev3.Video, now time.Time) (Video, error) {
	if v.Snippet == nil || v.Status == nil {
		return Video{}, fmt.Errorf("video %s: missing snippet or status part", v.Id)
	}

	out := Video{
		YouTubeVideoID: v.Id,
		Title:          v.Snippet.Title,
		PrivacyStatus:  store.PrivacyStatus(v.Status.PrivacyStatus),
	}

	var scheduledAt *time.Time
	if v.Status.PublishAt != "" {
		t, err := time.Parse(time.RFC3339, v.Status.PublishAt)
		if err != nil {
			return Video{}, fmt.Errorf("video %s: parse status.publishAt %q: %w", v.Id, v.Status.PublishAt, err)
		}
		scheduledAt = &t
	}

	out.IsScheduledDraft = out.PrivacyStatus == store.PrivacyStatusPrivate &&
		scheduledAt != nil && scheduledAt.After(now)

	if out.IsScheduledDraft {
		out.PublishAt = scheduledAt
		return out, nil
	}

	if v.Snippet.PublishedAt != "" {
		t, err := time.Parse(time.RFC3339, v.Snippet.PublishedAt)
		if err != nil {
			return Video{}, fmt.Errorf("video %s: parse snippet.publishedAt %q: %w", v.Id, v.Snippet.PublishedAt, err)
		}
		out.PublishedAt = &t
	}
	return out, nil
}

// chunkStrings splits ids into batches of at most size elements each,
// preserving order.
func chunkStrings(ids []string, size int) [][]string {
	if len(ids) == 0 {
		return nil
	}
	var out [][]string
	for len(ids) > 0 {
		n := size
		if n > len(ids) {
			n = len(ids)
		}
		out = append(out, ids[:n])
		ids = ids[n:]
	}
	return out
}
