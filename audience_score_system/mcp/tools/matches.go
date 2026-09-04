// C9's pending-match confirm/reject MCP tool group (issue #1581,
// FR22/FR23): list_pending_matches (read) surfaces every video_schedule_match
// worker/sync.Activities.SyncOutcomes queued for human resolution (state =
// 'pending', below worker/sync.MatchConfidenceThreshold), and
// resolve_pending_match (write) confirms or rejects one. See
// ../../ARCHITECTURE.md "Outcome matching: confidence threshold" for why
// SyncOutcomes queues rather than guesses below that threshold, and
// ../../store/match.go (MatchStore, already real from #1569's schema plus
// this task's ListCandidates/HasMatch/GetByID additions) for the storage
// this group reads and writes.
package tools

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/whale-net/everything/audience_score_system/mcp/server"
	"github.com/whale-net/everything/audience_score_system/store"
)

// -- shared rendering ---------------------------------------------------------

// MatchVideoOutput is the SyncedVideo half of a rendered match -- the
// video itself plus its latest recorded metrics snapshot (if any), so a
// human resolving a pending match can judge it (FR23).
type MatchVideoOutput struct {
	YouTubeVideoID string     `json:"youtube_video_id" jsonschema:"the YouTube video id"`
	Title          string     `json:"title" jsonschema:"the video's title"`
	PublishedAt    *time.Time `json:"published_at,omitempty" jsonschema:"when the video actually went live"`

	Views                      *int64     `json:"views,omitempty" jsonschema:"most recently recorded view count, if any metrics have been synced yet"`
	AverageViewDurationSeconds *float64   `json:"average_view_duration_seconds,omitempty" jsonschema:"most recently recorded average view duration"`
	AverageViewPercentage      *float64   `json:"average_view_percentage,omitempty" jsonschema:"most recently recorded average view percentage"`
	Impressions                *int64     `json:"impressions,omitempty" jsonschema:"most recently recorded impression count"`
	ImpressionCTR              *float64   `json:"impression_ctr,omitempty" jsonschema:"most recently recorded impression click-through rate"`
	MetricsMeasuredAt          *time.Time `json:"metrics_measured_at,omitempty" jsonschema:"when the metrics snapshot above was measured; absent if no metrics have synced yet"`
}

// renderMatchVideo resolves syncedVideoID's SyncedVideo plus its latest
// VideoMetrics (if any) via sync, rendering both as MatchVideoOutput.
func renderMatchVideo(ctx context.Context, sync store.SyncStore, syncedVideoID uuid.UUID) (MatchVideoOutput, error) {
	video, err := sync.GetByID(ctx, syncedVideoID)
	if err != nil {
		return MatchVideoOutput{}, fmt.Errorf("load synced_video %s: %w", syncedVideoID, err)
	}
	metrics, err := sync.LatestMetricsFor(ctx, syncedVideoID)
	if err != nil {
		return MatchVideoOutput{}, fmt.Errorf("load latest metrics for synced_video %s: %w", syncedVideoID, err)
	}

	out := MatchVideoOutput{
		YouTubeVideoID: video.YouTubeVideoID,
		Title:          video.Title,
		PublishedAt:    video.PublishedAt,
	}
	if metrics != nil {
		out.Views = metrics.Views
		out.AverageViewDurationSeconds = metrics.AverageViewDurationSeconds
		out.AverageViewPercentage = metrics.AverageViewPercentage
		out.Impressions = metrics.Impressions
		out.ImpressionCTR = metrics.ImpressionCTR
		measuredAt := metrics.MeasuredAt
		out.MetricsMeasuredAt = &measuredAt
	}
	return out, nil
}

// MatchEntryOutput is a resolved schedule_entry as a match's linked or
// best-guess candidate -- the idea title and verdict a human needs to
// judge whether the guess is right (FR23), since schedule_entry itself
// carries neither.
type MatchEntryOutput struct {
	ScheduleEntryID   string    `json:"schedule_entry_id" jsonschema:"the candidate schedule_entry's ID, as a UUID string"`
	IdeaTitle         string    `json:"idea_title" jsonschema:"the bound idea's title"`
	ProposedPublishAt time.Time `json:"proposed_publish_at" jsonschema:"the entry's proposed/committed publish time"`
	Verdict           string    `json:"verdict" jsonschema:"the verdict version this entry was scheduled under -- viable, not-viable, or needs-more-research"`
}

// renderMatchEntry resolves entryID's ScheduleEntry, its bound Idea's
// title, and its bound Verdict, rendering all three as MatchEntryOutput.
func renderMatchEntry(ctx context.Context, schedules store.ScheduleStore, ideas store.IdeaStore, verdicts store.VerdictStore, entryID uuid.UUID) (MatchEntryOutput, error) {
	entry, err := schedules.GetByID(ctx, entryID)
	if err != nil {
		return MatchEntryOutput{}, fmt.Errorf("load schedule_entry %s: %w", entryID, err)
	}
	idea, err := ideas.GetByID(ctx, entry.IdeaID)
	if err != nil {
		return MatchEntryOutput{}, fmt.Errorf("load idea for schedule_entry %s: %w", entryID, err)
	}
	verdict, err := verdicts.GetByID(ctx, entry.VerdictID)
	if err != nil {
		return MatchEntryOutput{}, fmt.Errorf("load verdict for schedule_entry %s: %w", entryID, err)
	}
	return MatchEntryOutput{
		ScheduleEntryID:   entry.ID.String(),
		IdeaTitle:         idea.Title,
		ProposedPublishAt: entry.ProposedPublishAt,
		Verdict:           string(verdict.Verdict),
	}, nil
}

// matchDeps bundles the read-only stores every render step in this file
// needs, so registerListPendingMatches/registerResolvePendingMatch each
// take one argument instead of four.
type matchDeps struct {
	sync      store.SyncStore
	schedules store.ScheduleStore
	ideas     store.IdeaStore
	verdicts  store.VerdictStore
}

// -- list_pending_matches -----------------------------------------------------

// defaultListPendingMatchesLimit bounds list_pending_matches' response when
// a caller supplies limit <= 0 -- without this, a Channel with enough
// pending matches exceeds the calling MCP client's response-size cap and
// the tool call fails outright with no way to retrieve the data in pages
// (issue #1808).
const defaultListPendingMatchesLimit = 50

// ListPendingMatchesInput is list_pending_matches' argument schema.
type ListPendingMatchesInput struct {
	ChannelID string `json:"channel_id" jsonschema:"Channel to list pending outcome matches for, as a UUID string"`
	// Since bounds the window by each match's created_at (inclusive).
	// Matches are returned oldest-first (the natural triage order), so a
	// caller pages forward past a truncated response by re-calling with
	// since set to the last returned match's created_at -- that match
	// reappears as the new page's first row (inclusive bound), so
	// de-duplicate on match_id across pages if needed (issue #1808).
	Since *time.Time `json:"since,omitempty" jsonschema:"Only include matches created at or after this time -- page forward past a truncated response by setting this to the last returned match's created_at (that match will reappear as this page's first row)"`
	Limit int        `json:"limit,omitempty" jsonschema:"Maximum matches to return, oldest first (default 50). The response's truncated flag is set when more matching rows exist."`
}

// ChannelScopeID implements server.ChannelScoped.
func (i ListPendingMatchesInput) ChannelScopeID() uuid.UUID {
	id, _ := uuid.Parse(i.ChannelID)
	return id
}

// PendingMatchOutput is one pending video_schedule_match as
// list_pending_matches renders it: the published video (with its latest
// metrics snapshot), the matcher's best-guess entry (nil if no plausible
// candidate existed at all), and the confidence score the matcher
// computed -- everything a human needs to confirm or reject (FR23).
type PendingMatchOutput struct {
	MatchID        string            `json:"match_id" jsonschema:"the pending match's ID, as a UUID string -- pass this to resolve_pending_match"`
	Video          MatchVideoOutput  `json:"video" jsonschema:"the published video awaiting resolution"`
	BestGuessEntry *MatchEntryOutput `json:"best_guess_entry,omitempty" jsonschema:"the matcher's best-guess schedule entry, if any plausible candidate existed; null if none did"`
	Confidence     float64           `json:"confidence" jsonschema:"the matcher's confidence score for best_guess_entry, in [0,1]; 0 when best_guess_entry is null"`
}

// renderPendingMatch renders m (a MatchStatePending row) as
// PendingMatchOutput.
func renderPendingMatch(ctx context.Context, deps matchDeps, m store.VideoScheduleMatch) (PendingMatchOutput, error) {
	video, err := renderMatchVideo(ctx, deps.sync, m.SyncedVideoID)
	if err != nil {
		return PendingMatchOutput{}, err
	}

	out := PendingMatchOutput{
		MatchID:    m.ID.String(),
		Video:      video,
		Confidence: m.Confidence,
	}
	if m.ScheduleEntryID != nil {
		entry, err := renderMatchEntry(ctx, deps.schedules, deps.ideas, deps.verdicts, *m.ScheduleEntryID)
		if err != nil {
			return PendingMatchOutput{}, err
		}
		out.BestGuessEntry = &entry
	}
	return out, nil
}

// ListPendingMatchesOutput is list_pending_matches' structured result.
type ListPendingMatchesOutput struct {
	Matches   []PendingMatchOutput `json:"matches" jsonschema:"pending matches for this Channel, oldest first, awaiting confirm/reject via resolve_pending_match"`
	Truncated bool                 `json:"truncated" jsonschema:"True if more pending matches exist beyond limit"`
}

// filterMatchesSince returns the subset of matches created at or after
// since (nil = no filter) -- the pagination bound list_pending_matches
// uses to page forward past a truncated response (issue #1808).
func filterMatchesSince(matches []store.VideoScheduleMatch, since *time.Time) []store.VideoScheduleMatch {
	if since == nil {
		return matches
	}
	out := make([]store.VideoScheduleMatch, 0, len(matches))
	for _, m := range matches {
		if !m.CreatedAt.Before(*since) {
			out = append(out, m)
		}
	}
	return out
}

// registerListPendingMatches registers list_pending_matches via
// server.RegisterRead -- Channel-scoped (NFR5), Creator and Analyst both
// allowed (store.CanRead, same as get_channel_schedule).
func registerListPendingMatches(reg *server.Registry, matches store.MatchStore, deps matchDeps) {
	server.RegisterRead(reg, &mcp.Tool{
		Name: "list_pending_matches",
		Description: "List every video<->schedule outcome match awaiting human resolution for a Channel (FR23), " +
			"oldest first: a published video the sync worker could not confidently auto-link to a committed " +
			"schedule entry. Each result includes the video's title/publish time/latest metrics, the matcher's " +
			"best-guess entry (idea title, proposed slot, verdict) if any, and the confidence score -- call " +
			"resolve_pending_match to confirm or reject. Response is capped at limit (default 50); see truncated. " +
			"Page forward past truncation by re-calling with since set to the last returned match's created_at.",
	}, listPendingMatchesHandler(matches, deps))
}

func listPendingMatchesHandler(matches store.MatchStore, deps matchDeps) mcp.ToolHandlerFor[ListPendingMatchesInput, ListPendingMatchesOutput] {
	return func(ctx context.Context, _ *mcp.CallToolRequest, in ListPendingMatchesInput) (*mcp.CallToolResult, ListPendingMatchesOutput, error) {
		channelID := in.ChannelScopeID()

		pending, err := matches.ListPending(ctx, channelID)
		if err != nil {
			return nil, ListPendingMatchesOutput{}, fmt.Errorf("list_pending_matches: list pending matches for channel %s: %w", channelID, err)
		}
		pending = filterMatchesSince(pending, in.Since)

		limit := in.Limit
		if limit <= 0 {
			limit = defaultListPendingMatchesLimit
		}
		trimmed, truncated := truncateSlice(pending, limit)

		out := ListPendingMatchesOutput{Matches: make([]PendingMatchOutput, 0, len(trimmed)), Truncated: truncated}
		for _, m := range trimmed {
			rendered, err := renderPendingMatch(ctx, deps, m)
			if err != nil {
				return nil, ListPendingMatchesOutput{}, err
			}
			out.Matches = append(out.Matches, rendered)
		}
		return nil, out, nil
	}
}

// -- resolve_pending_match -----------------------------------------------------

// ResolvePendingMatchInput is resolve_pending_match's argument schema.
type ResolvePendingMatchInput struct {
	ChannelID string `json:"channel_id" jsonschema:"Channel the match belongs to, as a UUID string"`
	MatchID   string `json:"match_id" jsonschema:"The pending match to resolve, as a UUID string (from list_pending_matches)"`
	Confirm   bool   `json:"confirm" jsonschema:"true to confirm the match (creates the outcome link, FR22), false to reject it (the video remains unmatched, FR23)"`
	// ScheduleEntryID lets a human confirm against a different entry than
	// the matcher's best guess (this task's Implementation spec) -- only
	// meaningful when Confirm is true.
	ScheduleEntryID string `json:"schedule_entry_id,omitempty" jsonschema:"Optional, confirm only: link to this schedule_entry instead of the matcher's best guess, as a UUID string"`
	// IdempotencyKeyArg backs IdempotencyKey() below -- named ...Arg
	// because a Go type cannot declare both a field and a method named
	// IdempotencyKey (mirrors verdict.go's identical pattern).
	IdempotencyKeyArg string `json:"idempotency_key,omitempty" jsonschema:"Caller-supplied idempotency key. Strongly recommended: a replay with the same key is a no-op returning the original result (NFR2); resolving an already-resolved match under a different key is rejected as a conflict, never a silent state flip."`
}

// ChannelScopeID implements server.ChannelScoped.
func (i ResolvePendingMatchInput) ChannelScopeID() uuid.UUID {
	id, _ := uuid.Parse(i.ChannelID)
	return id
}

// IdempotencyKey implements server.IdempotencyKeyed.
func (i ResolvePendingMatchInput) IdempotencyKey() string { return i.IdempotencyKeyArg }

// ResolvedMatchOutput is a resolved (confirmed or rejected)
// video_schedule_match, as resolve_pending_match renders it.
type ResolvedMatchOutput struct {
	MatchID            string            `json:"match_id" jsonschema:"the resolved match's ID, as a UUID string"`
	State              string            `json:"state" jsonschema:"confirmed or rejected"`
	Video              MatchVideoOutput  `json:"video" jsonschema:"the video this match concerns"`
	LinkedEntry        *MatchEntryOutput `json:"linked_entry,omitempty" jsonschema:"the schedule entry this match now links to; null for a rejected match (FR23: the video remains unmatched)"`
	Confidence         float64           `json:"confidence" jsonschema:"the matcher's original confidence score"`
	ResolvedByPersonID string            `json:"resolved_by_person_id" jsonschema:"the Person who resolved this match, as a UUID string"`
	ResolvedAt         *time.Time        `json:"resolved_at,omitempty" jsonschema:"when this match was resolved, RFC3339"`
}

// registerResolvePendingMatch registers resolve_pending_match via
// server.RegisterWrite, so the idempotency middleware (NFR2) applies
// automatically. Creator and Analyst may both resolve (store.CanWrite,
// applied by RegisterWrite via ChannelScoped) -- FR23 names both
// explicitly.
func registerResolvePendingMatch(reg *server.Registry, matches store.MatchStore, deps matchDeps) {
	server.RegisterWrite(reg, &mcp.Tool{
		Name: "resolve_pending_match",
		Description: "Confirm or reject a pending video<->schedule outcome match (FR22/FR23). Confirming creates the " +
			"outcome link (optionally against a schedule_entry_id other than the matcher's best guess); rejecting " +
			"leaves the video unmatched. Always supply idempotency_key: resolving an already-resolved match without " +
			"one is rejected as a conflict, not treated as a no-op replay.",
	}, resolvePendingMatchMutate(matches, deps.sync, deps.schedules), resolvePendingMatchRender(matches, deps))
}

func resolvePendingMatchMutate(matches store.MatchStore, sync store.SyncStore, schedules store.ScheduleStore) server.WriteMutate[ResolvePendingMatchInput] {
	return func(ctx context.Context, in ResolvePendingMatchInput) (uuid.UUID, error) {
		channelID, err := uuid.Parse(in.ChannelID)
		if err != nil {
			return uuid.Nil, fmt.Errorf("channel_id is not a valid UUID: %w", err)
		}

		matchID, err := uuid.Parse(in.MatchID)
		if err != nil {
			return uuid.Nil, fmt.Errorf("match_id is not a valid UUID: %w", err)
		}

		m, err := matches.GetByID(ctx, matchID)
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return uuid.Nil, fmt.Errorf("match_id does not exist")
			}
			return uuid.Nil, fmt.Errorf("load match: %w", err)
		}

		video, err := sync.GetByID(ctx, m.SyncedVideoID)
		if err != nil {
			return uuid.Nil, fmt.Errorf("load video for match: %w", err)
		}
		if video.ChannelID != channelID {
			return uuid.Nil, fmt.Errorf("match_id does not belong to channel_id")
		}

		var overrideEntryID *uuid.UUID
		if in.ScheduleEntryID != "" {
			if !in.Confirm {
				return uuid.Nil, fmt.Errorf("schedule_entry_id may only be set when confirm is true")
			}
			entryID, err := uuid.Parse(in.ScheduleEntryID)
			if err != nil {
				return uuid.Nil, fmt.Errorf("schedule_entry_id is not a valid UUID: %w", err)
			}
			entry, err := schedules.GetByID(ctx, entryID)
			if err != nil {
				if errors.Is(err, pgx.ErrNoRows) {
					return uuid.Nil, fmt.Errorf("schedule_entry_id does not exist")
				}
				return uuid.Nil, fmt.Errorf("load schedule_entry: %w", err)
			}
			if entry.ChannelID != channelID {
				return uuid.Nil, fmt.Errorf("schedule_entry_id belongs to a different Channel")
			}
			overrideEntryID = &entryID
		}

		person := server.PersonFromContext(ctx)
		if person == nil {
			return uuid.Nil, fmt.Errorf("unauthenticated: no caller credential resolved")
		}

		if err := matches.Resolve(ctx, matchID, person.ID, in.Confirm, overrideEntryID); err != nil {
			return uuid.Nil, err
		}
		return matchID, nil
	}
}

// resolvePendingMatchRender always re-reads the match (by ref, the match
// ID mutate returned) from Postgres rather than trusting anything cached
// from mutate -- see server.RegisterWrite's doc on why render runs on
// every call, replay included.
func resolvePendingMatchRender(matches store.MatchStore, deps matchDeps) server.WriteRender[ResolvedMatchOutput] {
	return func(ctx context.Context, ref uuid.UUID) (*mcp.CallToolResult, ResolvedMatchOutput, error) {
		m, err := matches.GetByID(ctx, ref)
		if err != nil {
			return nil, ResolvedMatchOutput{}, fmt.Errorf("load resolved match: %w", err)
		}

		video, err := renderMatchVideo(ctx, deps.sync, m.SyncedVideoID)
		if err != nil {
			return nil, ResolvedMatchOutput{}, err
		}

		out := ResolvedMatchOutput{
			MatchID:    m.ID.String(),
			State:      string(m.State),
			Video:      video,
			Confidence: m.Confidence,
			ResolvedAt: m.ResolvedAt,
		}
		if m.ResolvedByPersonID != nil {
			out.ResolvedByPersonID = m.ResolvedByPersonID.String()
		}
		if m.ScheduleEntryID != nil && m.State == store.MatchStateConfirmed {
			entry, err := renderMatchEntry(ctx, deps.schedules, deps.ideas, deps.verdicts, *m.ScheduleEntryID)
			if err != nil {
				return nil, ResolvedMatchOutput{}, err
			}
			out.LinkedEntry = &entry
		}
		return nil, out, nil
	}
}

// -- registration ------------------------------------------------------------

// RegisterMatches registers list_pending_matches and resolve_pending_match
// against reg (see ../server/registry.go), backed by st's
// MatchStore/SyncStore/ScheduleStore/IdeaStore/VerdictStore.
func RegisterMatches(reg *server.Registry, st *store.Store) {
	deps := matchDeps{sync: st.Sync(), schedules: st.Schedules(), ideas: st.Ideas(), verdicts: st.Verdicts()}
	registerListPendingMatches(reg, st.Matches(), deps)
	registerResolvePendingMatch(reg, st.Matches(), deps)
}
