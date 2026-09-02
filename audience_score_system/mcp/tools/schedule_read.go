// get_channel_schedule -- C6's read half (issue #1576, FR14/FR15): hands
// an agent the `synced_video` read model (worker/sync.Activities.
// SyncSchedule's write target, migration 002) as planning context, so it
// can tell a scheduled draft from a published upload and see how fresh
// the data is.
package tools

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/whale-net/everything/audience_score_system/mcp/server"
	"github.com/whale-net/everything/audience_score_system/store"
)

// GetChannelScheduleInput is get_channel_schedule's argument struct.
// ChannelID is JSON-wire a string (parsed to uuid.UUID in
// ChannelScopeID), mirroring every other Channel-scoped tool input --
// jsonschema-go infers a raw uuid.UUID field as schema type "array", not
// "string" (see mcp/server's fakes_test.go for the full explanation).
type GetChannelScheduleInput struct {
	ChannelID string `json:"channel_id" jsonschema:"the Channel to read the schedule for, as a UUID string"`

	// From/To optionally bound the window by each video's effective
	// timestamp (PublishAt if still a scheduled draft, else
	// PublishedAt) -- a video with neither set (never published, no
	// scheduled publish time on file) is never excluded by From/To,
	// since there is nothing to compare against.
	From *time.Time `json:"from,omitempty" jsonschema:"optional lower bound (inclusive) on each video's effective publish time"`
	To   *time.Time `json:"to,omitempty" jsonschema:"optional upper bound (inclusive) on each video's effective publish time"`

	// IncludeDrafts defaults to true (nil == true) -- set false to omit
	// scheduled/private drafts and see only what is actually public.
	IncludeDrafts *bool `json:"include_drafts,omitempty" jsonschema:"whether to include scheduled/private drafts -- defaults to true"`
}

// ChannelScopeID implements server.ChannelScoped -- RegisterRead
// authorizes every call via store.CanRead before the handler below runs
// (NFR5). An unparseable ChannelID resolves to uuid.Nil, which
// store.CanRead will simply find no role for, rather than panicking.
func (i GetChannelScheduleInput) ChannelScopeID() uuid.UUID {
	id, _ := uuid.Parse(i.ChannelID)
	return id
}

// ScheduleVideo is one `synced_video` row as get_channel_schedule reports
// it -- everything an agent needs to tell a scheduled draft from a
// published upload and judge freshness.
type ScheduleVideo struct {
	YouTubeVideoID   string     `json:"youtube_video_id" jsonschema:"the YouTube video id"`
	Title            string     `json:"title" jsonschema:"the video's title"`
	PrivacyStatus    string     `json:"privacy_status" jsonschema:"public, private, or unlisted"`
	PublishAt        *time.Time `json:"publish_at,omitempty" jsonschema:"set for a scheduled/private draft not yet published"`
	PublishedAt      *time.Time `json:"published_at,omitempty" jsonschema:"set once the video is actually live"`
	IsScheduledDraft bool       `json:"is_scheduled_draft" jsonschema:"true for a private video with a future publish_at"`
	LastSyncedAt     time.Time  `json:"last_synced_at" jsonschema:"when this row was last confirmed present on YouTube -- a stale value relative to the Channel's other rows means this video was not seen on the most recent sync cycle"`
}

// GetChannelScheduleOutput is get_channel_schedule's structured result.
type GetChannelScheduleOutput struct {
	Videos []ScheduleVideo `json:"videos" jsonschema:"the Channel's synced schedule, ordered by effective publish time"`
}

// RegisterGetChannelSchedule registers get_channel_schedule via
// server.RegisterRead -- Channel-scoped (NFR5), Creator and Analyst both
// allowed (FR15). syncStore is the `synced_video` read source
// (st.Sync(), migration 002).
func RegisterGetChannelSchedule(reg *server.Registry, syncStore store.SyncStore) {
	server.RegisterRead(reg, &mcp.Tool{
		Name:        "get_channel_schedule",
		Description: "Read a Channel's synced YouTube upload schedule, including scheduled/private drafts not yet published.",
	}, getChannelScheduleHandler(syncStore))
}

// getChannelScheduleHandler closes over syncStore so RegisterGetChannelSchedule's
// caller supplies it once at registration time rather than every call.
func getChannelScheduleHandler(syncStore store.SyncStore) mcp.ToolHandlerFor[GetChannelScheduleInput, GetChannelScheduleOutput] {
	return func(ctx context.Context, _ *mcp.CallToolRequest, in GetChannelScheduleInput) (*mcp.CallToolResult, GetChannelScheduleOutput, error) {
		channelID := in.ChannelScopeID()

		vids, err := syncStore.ListSchedule(ctx, channelID)
		if err != nil {
			return nil, GetChannelScheduleOutput{}, fmt.Errorf("get_channel_schedule: list schedule for channel %s: %w", channelID, err)
		}

		includeDrafts := true
		if in.IncludeDrafts != nil {
			includeDrafts = *in.IncludeDrafts
		}

		out := make([]ScheduleVideo, 0, len(vids))
		for _, v := range vids {
			if v.IsScheduledDraft && !includeDrafts {
				continue
			}
			if !withinWindow(v, in.From, in.To) {
				continue
			}
			out = append(out, ScheduleVideo{
				YouTubeVideoID:   v.YouTubeVideoID,
				Title:            v.Title,
				PrivacyStatus:    string(v.PrivacyStatus),
				PublishAt:        v.PublishAt,
				PublishedAt:      v.PublishedAt,
				IsScheduledDraft: v.IsScheduledDraft,
				LastSyncedAt:     v.LastSyncedAt,
			})
		}

		return nil, GetChannelScheduleOutput{Videos: out}, nil
	}
}

// withinWindow reports whether v's effective timestamp (PublishAt if
// still a scheduled draft, else PublishedAt) falls within [from, to]
// (either bound nil-able and inclusive). A video with neither timestamp
// set (never published, no scheduled publish time on file) always
// passes -- there is nothing to compare a From/To window against, and
// silently dropping such a row would be more surprising than including
// it.
func withinWindow(v store.SyncedVideo, from, to *time.Time) bool {
	ts := v.PublishAt
	if ts == nil {
		ts = v.PublishedAt
	}
	if ts == nil {
		return true
	}
	if from != nil && ts.Before(*from) {
		return false
	}
	if to != nil && ts.After(*to) {
		return false
	}
	return true
}
