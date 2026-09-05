// C10's browsing MCP read tools (issue #1582, FR24): get_channel_overview,
// the pick-up-context entry point a Creator or Analyst uses to see a
// Channel's whole state without asking the other to repeat it, and
// get_prediction_vs_outcome, the comparison read nothing else owns. Both
// are server.RegisterRead and authorize via store.CanRead, so Creator and
// Analyst see byte-identical data -- mutual visibility is the point of
// C10. See ../../store/browse.go (store.BrowseStore) for the underlying
// reads, in particular why get_prediction_vs_outcome does not select from
// migration 002's v_prediction_vs_outcome view.
package tools

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/whale-net/everything/audience_score_system/mcp/server"
	"github.com/whale-net/everything/audience_score_system/store"
)

// truncateSlice returns items capped at limit (all of them, untruncated,
// if limit <= 0 or len(items) <= limit) plus whether truncation occurred --
// the one place both browsing tools' "bounded by construction" behavior is
// implemented, so they can never disagree on what "truncated" means.
func truncateSlice[T any](items []T, limit int) ([]T, bool) {
	if limit <= 0 || len(items) <= limit {
		return items, false
	}
	return items[:limit], true
}

// -- shared rendering ---------------------------------------------------------

// PredictionVsOutcomeVerdictOutput is the verdict half of one
// get_prediction_vs_outcome row: the SPECIFIC version bound to the
// schedule entry (LB3), never the idea's current verdict -- see
// store.BrowseStore.PredictionVsOutcome's doc for why those can differ.
type PredictionVsOutcomeVerdictOutput struct {
	VerdictID         string `json:"verdict_id" jsonschema:"This verdict version's ID, as a UUID string"`
	Version           int    `json:"version" jsonschema:"This verdict's version number for its Idea (FR12)"`
	Verdict           string `json:"verdict" jsonschema:"viable, not-viable, or needs-more-research"`
	Reasoning         string `json:"reasoning" jsonschema:"Why this verdict was reached"`
	AuthorPersonID    string `json:"author_person_id" jsonschema:"The Person who recorded this verdict version, as a UUID string"`
	AuthorDisplayName string `json:"author_display_name" jsonschema:"The author's display name"`
	CreatedAt         string `json:"created_at" jsonschema:"When this verdict version was recorded, RFC3339"`
}

func toPredictionVsOutcomeVerdictOutput(r store.PredictionOutcome) PredictionVsOutcomeVerdictOutput {
	return PredictionVsOutcomeVerdictOutput{
		VerdictID:         r.VerdictID.String(),
		Version:           r.VerdictVersion,
		Verdict:           string(r.Verdict),
		Reasoning:         r.VerdictReasoning,
		AuthorPersonID:    r.VerdictAuthorPersonID.String(),
		AuthorDisplayName: r.VerdictAuthorDisplayName,
		CreatedAt:         r.VerdictCreatedAt.Format(time.RFC3339),
	}
}

// PredictionVsOutcomeScheduleOutput is the committed schedule_entry half
// of one get_prediction_vs_outcome row.
type PredictionVsOutcomeScheduleOutput struct {
	ScheduleEntryID   string     `json:"schedule_entry_id" jsonschema:"The committed schedule_entry's ID, as a UUID string"`
	ProposedPublishAt time.Time  `json:"proposed_publish_at" jsonschema:"The entry's proposed/committed publish time"`
	ApprovedAt        *time.Time `json:"approved_at,omitempty" jsonschema:"When this entry was committed (approved)"`
}

// PredictionVsOutcomeVideoOutput is the published-video half of one
// get_prediction_vs_outcome row.
type PredictionVsOutcomeVideoOutput struct {
	YouTubeVideoID string     `json:"youtube_video_id" jsonschema:"The YouTube video id"`
	Title          string     `json:"title" jsonschema:"The video's title"`
	PublishedAt    *time.Time `json:"published_at,omitempty" jsonschema:"When the video actually went live"`
}

// PredictionVsOutcomeMetricsOutput is the latest recorded video_metrics
// snapshot for one get_prediction_vs_outcome row's video.
type PredictionVsOutcomeMetricsOutput struct {
	Views                      *int64    `json:"views,omitempty" jsonschema:"Most recently recorded view count"`
	AverageViewDurationSeconds *float64  `json:"average_view_duration_seconds,omitempty" jsonschema:"Most recently recorded average view duration"`
	AverageViewPercentage      *float64  `json:"average_view_percentage,omitempty" jsonschema:"Most recently recorded average view percentage"`
	Impressions                *int64    `json:"impressions,omitempty" jsonschema:"Most recently recorded impression count"`
	ImpressionCTR              *float64  `json:"impression_ctr,omitempty" jsonschema:"Most recently recorded impression click-through rate"`
	MeasuredAt                 time.Time `json:"measured_at" jsonschema:"When this metrics snapshot was measured"`
}

// PredictionVsOutcomeRowOutput is one get_prediction_vs_outcome row: an
// idea, the verdict version that predicted it, the committed schedule
// entry, the published video, its latest metrics, and the match
// provenance (FR22/FR23) -- whether the video<->schedule link was
// auto-inferred by the sync worker or confirmed by a human. Provenance is
// not decoration: a Creator judging whether their call was right needs to
// know which.
type PredictionVsOutcomeRowOutput struct {
	IdeaID    string                            `json:"idea_id" jsonschema:"The Idea this row concerns, as a UUID string"`
	IdeaTitle string                            `json:"idea_title" jsonschema:"The Idea's title"`
	Verdict   PredictionVsOutcomeVerdictOutput  `json:"verdict" jsonschema:"The verdict version bound to the schedule entry below -- never the Idea's current verdict, if it has since moved on"`
	Schedule  PredictionVsOutcomeScheduleOutput `json:"schedule" jsonschema:"The committed schedule entry this video fulfilled"`
	Video     PredictionVsOutcomeVideoOutput    `json:"video" jsonschema:"The published video"`
	Metrics   PredictionVsOutcomeMetricsOutput  `json:"metrics" jsonschema:"The video's latest recorded metrics snapshot"`
	// MatchProvenance is exactly store.PredictionOutcome.MatchState's
	// string value ("auto" or "confirmed") -- rows with any other match
	// state never reach this output (see
	// store.BrowseStore.PredictionVsOutcome's qualifying-row rule).
	MatchProvenance string  `json:"match_provenance" jsonschema:"auto (sync-worker-inferred) or confirmed (human-confirmed via resolve_pending_match) -- FR22/FR23"`
	MatchConfidence float64 `json:"match_confidence" jsonschema:"The matcher's confidence score for this link, in [0,1]"`
}

func toPredictionVsOutcomeRowOutput(r store.PredictionOutcome) PredictionVsOutcomeRowOutput {
	return PredictionVsOutcomeRowOutput{
		IdeaID:    r.IdeaID.String(),
		IdeaTitle: r.IdeaTitle,
		Verdict:   toPredictionVsOutcomeVerdictOutput(r),
		Schedule: PredictionVsOutcomeScheduleOutput{
			ScheduleEntryID:   r.ScheduleEntryID.String(),
			ProposedPublishAt: r.ProposedPublishAt,
			ApprovedAt:        r.ApprovedAt,
		},
		Video: PredictionVsOutcomeVideoOutput{
			YouTubeVideoID: r.YouTubeVideoID,
			Title:          r.VideoTitle,
			PublishedAt:    r.PublishedAt,
		},
		Metrics: PredictionVsOutcomeMetricsOutput{
			Views:                      r.Views,
			AverageViewDurationSeconds: r.AverageViewDurationSeconds,
			AverageViewPercentage:      r.AverageViewPercentage,
			Impressions:                r.Impressions,
			ImpressionCTR:              r.ImpressionCTR,
			MeasuredAt:                 r.MetricsMeasuredAt,
		},
		MatchProvenance: string(r.MatchState),
		MatchConfidence: r.MatchConfidence,
	}
}

// parseOptionalUUID parses raw (trimmed) as a UUID, returning nil (not an
// error) for an empty/whitespace-only raw -- the shared "optional
// caller-supplied UUID filter" idiom both tools in this file use.
func parseOptionalUUID(raw string) (*uuid.UUID, error) {
	if strings.TrimSpace(raw) == "" {
		return nil, nil
	}
	id, err := uuid.Parse(raw)
	if err != nil {
		return nil, err
	}
	return &id, nil
}

// -- get_prediction_vs_outcome ------------------------------------------------

// defaultPredictionVsOutcomeLimit bounds get_prediction_vs_outcome's
// response when a caller supplies limit <= 0.
const defaultPredictionVsOutcomeLimit = 25

// GetPredictionVsOutcomeInput is get_prediction_vs_outcome's argument
// schema.
type GetPredictionVsOutcomeInput struct {
	ChannelID string `json:"channel_id" jsonschema:"Channel to compare predictions against outcomes for, as a UUID string"`
	IdeaID    string `json:"idea_id,omitempty" jsonschema:"Restrict to this Idea, as a UUID string"`
	// Since/Before bound the window by each video's published_at.
	// Together they let a caller page backward past truncated: request
	// the newest window, then re-request with before set to the oldest
	// row's published_at from the previous response (issue #1808).
	Since  *time.Time `json:"since,omitempty" jsonschema:"Only include videos published at or after this time"`
	Before *time.Time `json:"before,omitempty" jsonschema:"Only include videos published strictly before this time -- pair with since to page backward past a truncated response"`
	Limit  int        `json:"limit,omitempty" jsonschema:"Maximum rows to return, most-recently-published first (default 25). The response's truncated flag is set when more matching rows exist."`
}

// ChannelScopeID implements server.ChannelScoped.
func (i GetPredictionVsOutcomeInput) ChannelScopeID() uuid.UUID {
	id, _ := uuid.Parse(i.ChannelID)
	return id
}

// GetPredictionVsOutcomeOutput is get_prediction_vs_outcome's structured
// result.
type GetPredictionVsOutcomeOutput struct {
	Rows      []PredictionVsOutcomeRowOutput `json:"rows" jsonschema:"Matching prediction-vs-outcome rows, most-recently-published first"`
	Truncated bool                           `json:"truncated" jsonschema:"True if more matching rows exist beyond limit"`
	// PendingMatchCount tells a browsing agent the picture above is
	// incomplete -- a video awaiting resolution carries no verdict
	// comparison yet (it is excluded from Rows entirely, per
	// store.BrowseStore.PredictionVsOutcome's qualifying-row rule) --
	// and points it at list_pending_matches (#1581) to see them.
	PendingMatchCount int `json:"pending_match_count" jsonschema:"Videos on this Channel with a match still awaiting human resolution (list_pending_matches) -- not reflected in rows above, since a pending/rejected match never qualifies for this comparison"`
}

// registerGetPredictionVsOutcome registers get_prediction_vs_outcome via
// server.RegisterRead -- Channel-scoped (NFR5), Creator and Analyst both
// see identical data (store.CanRead, C10's mutual-visibility point).
func registerGetPredictionVsOutcome(reg *server.Registry, browse store.BrowseStore, matches store.MatchStore) {
	server.RegisterRead(reg, &mcp.Tool{
		Name: "get_prediction_vs_outcome",
		Description: "Compare a Channel's viability predictions against what actually happened (FR24): for every " +
			"published video with a resolved (auto or human-confirmed) link to a committed schedule entry, returns " +
			"the idea, the SPECIFIC verdict version that predicted it (never a possibly-newer current verdict), the " +
			"committed slot, the video, its latest metrics, and the match's provenance (auto vs confirmed) and " +
			"confidence. Videos with a match still pending human resolution are excluded but counted in " +
			"pending_match_count -- call list_pending_matches to resolve them. Response is capped at limit (default " +
			"25); see truncated. Page backward past truncation by re-calling with before set to the oldest returned " +
			"row's published_at.",
	}, getPredictionVsOutcome(browse, matches))
}

func getPredictionVsOutcome(browse store.BrowseStore, matches store.MatchStore) mcp.ToolHandlerFor[GetPredictionVsOutcomeInput, GetPredictionVsOutcomeOutput] {
	return func(ctx context.Context, _ *mcp.CallToolRequest, in GetPredictionVsOutcomeInput) (*mcp.CallToolResult, GetPredictionVsOutcomeOutput, error) {
		channelID := in.ChannelScopeID()

		ideaID, err := parseOptionalUUID(in.IdeaID)
		if err != nil {
			return nil, GetPredictionVsOutcomeOutput{}, fmt.Errorf("idea_id is not a valid UUID: %w", err)
		}

		limit := in.Limit
		if limit <= 0 {
			limit = defaultPredictionVsOutcomeLimit
		}
		rows, truncated, err := browse.PredictionVsOutcome(ctx, channelID, ideaID, in.Since, in.Before, limit)
		if err != nil {
			return nil, GetPredictionVsOutcomeOutput{}, fmt.Errorf("get_prediction_vs_outcome: %w", err)
		}

		// Unbounded (since=nil, limit=0): PendingMatchCount must be the
		// TRUE total, not however many list_pending_matches' own default
		// page happens to return.
		pending, _, err := matches.ListPending(ctx, channelID, nil, 0)
		if err != nil {
			return nil, GetPredictionVsOutcomeOutput{}, fmt.Errorf("get_prediction_vs_outcome: list pending matches: %w", err)
		}

		out := GetPredictionVsOutcomeOutput{
			Rows:              make([]PredictionVsOutcomeRowOutput, 0, len(rows)),
			Truncated:         truncated,
			PendingMatchCount: len(pending),
		}
		for _, r := range rows {
			out.Rows = append(out.Rows, toPredictionVsOutcomeRowOutput(r))
		}
		return nil, out, nil
	}
}

// -- get_channel_overview ------------------------------------------------------

// Overview section names -- GetChannelOverviewInput.Sections values and
// GetChannelOverviewOutput.Truncated entries both draw from this set.
const (
	overviewSectionIdeas        = "ideas"
	overviewSectionNotes        = "research_notes"
	overviewSectionVideoScripts = "video_scripts"
	overviewSectionOutcomes     = "prediction_vs_outcome"
)

// allOverviewSections is every section resolveOverviewSections accepts, in
// a stable order -- used both to build the "everything" default set and to
// render an unknown-section error message.
var allOverviewSections = []string{overviewSectionIdeas, overviewSectionNotes, overviewSectionVideoScripts, overviewSectionOutcomes}

// Per-section default caps -- get_channel_overview's "bounded by
// construction" contract (issue #1582's spec): an overview that dumps a
// Channel's entire history is unusable to an MCP client's context window,
// so every section is capped, and the response says what was cut
// (GetChannelOverviewOutput.Truncated) rather than silently shrinking.
const (
	defaultIdeasOverviewLimit        = 50
	defaultNotesOverviewLimit        = 20
	defaultVideoScriptsOverviewLimit = 50
	defaultOutcomesOverviewLimit     = 20
)

// resolveOverviewSections turns requested (GetChannelOverviewInput.
// Sections) into the set of sections to render -- every section when
// requested is empty, exactly the named ones otherwise. Rejects an unknown
// name outright rather than silently ignoring it, so a typo'd section
// filter never comes back looking like "everything was empty".
func resolveOverviewSections(requested []string) (map[string]bool, error) {
	if len(requested) == 0 {
		want := make(map[string]bool, len(allOverviewSections))
		for _, s := range allOverviewSections {
			want[s] = true
		}
		return want, nil
	}

	want := make(map[string]bool, len(requested))
	for _, s := range requested {
		valid := false
		for _, known := range allOverviewSections {
			if s == known {
				valid = true
				break
			}
		}
		if !valid {
			return nil, fmt.Errorf("unknown section %q -- must be one of %v", s, allOverviewSections)
		}
		want[s] = true
	}
	return want, nil
}

// GetChannelOverviewInput is get_channel_overview's argument schema.
type GetChannelOverviewInput struct {
	ChannelID string `json:"channel_id" jsonschema:"Channel to read an overview of, as a UUID string"`
	// Since/Before optionally bound research_notes (by created_at) and
	// prediction_vs_outcome (by the video's published_at) -- ideas and
	// video_scripts are unaffected, since an Idea or a video_script's
	// relevance does not expire the way a note or an outcome's freshness
	// does. Together they let a caller page backward past either
	// section's fixed default limit (issue #1808): request the newest
	// window, then re-request with before set to the oldest item's
	// timestamp from the previous response.
	Since  *time.Time `json:"since,omitempty" jsonschema:"Only include research notes and prediction-vs-outcome rows at/after this time; ideas and video_scripts are unaffected"`
	Before *time.Time `json:"before,omitempty" jsonschema:"Only include research notes and prediction-vs-outcome rows strictly before this time -- pair with since to page backward past a section's default limit; ideas and video_scripts are unaffected"`
	// Sections restricts the response to a subset -- see
	// allOverviewSections for the accepted names.
	Sections []string `json:"sections,omitempty" jsonschema:"Restrict the response to these sections: ideas, research_notes, video_scripts, prediction_vs_outcome. Omit or leave empty for every section."`
}

// ChannelScopeID implements server.ChannelScoped.
func (i GetChannelOverviewInput) ChannelScopeID() uuid.UUID {
	id, _ := uuid.Parse(i.ChannelID)
	return id
}

// ChannelIdentityOutput is get_channel_overview's identity section.
type ChannelIdentityOutput struct {
	ChannelID                string `json:"channel_id" jsonschema:"This Channel's ID, as a UUID string"`
	YouTubeChannelID         string `json:"youtube_channel_id" jsonschema:"The connected YouTube channel's id"`
	Title                    string `json:"title" jsonschema:"The Channel's title"`
	ConnectionState          string `json:"connection_state" jsonschema:"connected or needs_reauth (FR4) -- an agent should explain a stale schedule when this is needs_reauth rather than assume the sync is simply slow"`
	ConnectionStateChangedAt string `json:"connection_state_changed_at" jsonschema:"When connection_state last changed, RFC3339"`
}

// CurrentVerdictOutput is an Idea's current verdict summary, as
// get_channel_overview's ideas section renders it -- deliberately leaner
// than PredictionVsOutcomeVerdictOutput (no author fields):
// store.IdeaOverview does not resolve the author, since
// get_viability_verdict (#1578) is the tool that renders a verdict's full
// detail including authorship and citations.
type CurrentVerdictOutput struct {
	VerdictID string `json:"verdict_id" jsonschema:"This verdict version's ID, as a UUID string"`
	Version   int    `json:"version" jsonschema:"This verdict's version number for its Idea (FR12)"`
	Verdict   string `json:"verdict" jsonschema:"viable, not-viable, or needs-more-research"`
	Reasoning string `json:"reasoning" jsonschema:"Why this verdict was reached"`
}

// IdeaOverviewOutput is one Idea, as get_channel_overview's ideas section
// renders it.
type IdeaOverviewOutput struct {
	IdeaID         string                `json:"idea_id" jsonschema:"This Idea's ID, as a UUID string"`
	Title          string                `json:"title" jsonschema:"The Idea's title"`
	CreatedAt      string                `json:"created_at" jsonschema:"When this Idea was first created, RFC3339"`
	CurrentVerdict *CurrentVerdictOutput `json:"current_verdict,omitempty" jsonschema:"This Idea's current (highest-version) verdict; null if none has been recorded yet -- call get_viability_verdict for full history and citations"`
}

func toIdeaOverviewOutput(i store.IdeaOverview) IdeaOverviewOutput {
	out := IdeaOverviewOutput{
		IdeaID:    i.ID.String(),
		Title:     i.Title,
		CreatedAt: i.CreatedAt.Format(time.RFC3339),
	}
	if i.CurrentVerdictID != nil {
		out.CurrentVerdict = &CurrentVerdictOutput{
			VerdictID: i.CurrentVerdictID.String(),
			Version:   *i.CurrentVerdictVersion,
			Verdict:   string(*i.CurrentVerdict),
			Reasoning: *i.CurrentVerdictReasoning,
		}
	}
	return out
}

// VideoScriptOverviewOutput is one video_script, as get_channel_overview's
// video_scripts section renders it (FR42): the script's own title, its
// status, its target publish date (if set -- undated is normal, FR36),
// and the bound verdict (version + value) -- the LB3-style bound version,
// never the idea's current one, mirroring store.VideoScriptDetail's own
// contract (video_script.go's ListDetailByChannel).
type VideoScriptOverviewOutput struct {
	VideoScriptID     string     `json:"video_script_id" jsonschema:"This video_script's ID, as a UUID string"`
	Title             string     `json:"title" jsonschema:"The video script's own title"`
	Status            string     `json:"status" jsonschema:"proposed, greenlit, denied, or archived (FR36-FR40)"`
	TargetPublishDate *time.Time `json:"target_publish_date,omitempty" jsonschema:"The script's target publish date, if set; absent when undated -- undated is normal per FR36"`
	VerdictVersion    int        `json:"verdict_version" jsonschema:"The verdict version this script was proposed under -- the bound version (LB3), not necessarily the Idea's current one"`
	Verdict           string     `json:"verdict" jsonschema:"viable, not-viable, or needs-more-research, for the bound verdict version above"`
}

func toVideoScriptOverviewOutput(d store.VideoScriptDetail) VideoScriptOverviewOutput {
	return VideoScriptOverviewOutput{
		VideoScriptID:     d.Script.ID.String(),
		Title:             d.Script.Title,
		Status:            string(d.Script.Status),
		TargetPublishDate: d.Script.TargetPublishDate,
		VerdictVersion:    d.VerdictVersion,
		Verdict:           string(d.Verdict),
	}
}

// filterNotesRange returns the subset of notes created at/after since (nil
// = no lower bound) and strictly before before (nil = no upper bound) --
// the shared helper get_channel_overview's research_notes section and
// list_research_notes both use to page backward past their default/caller
// limit (issue #1808): request the newest window, then re-request with
// before set to the oldest item's created_at from the previous response.
func filterNotesRange(notes []store.ResearchNoteWithAuthor, since, before *time.Time) []store.ResearchNoteWithAuthor {
	if since == nil && before == nil {
		return notes
	}
	out := make([]store.ResearchNoteWithAuthor, 0, len(notes))
	for _, n := range notes {
		if since != nil && n.CreatedAt.Before(*since) {
			continue
		}
		if before != nil && !n.CreatedAt.Before(*before) {
			continue
		}
		out = append(out, n)
	}
	return out
}

// GetChannelOverviewOutput is get_channel_overview's structured result --
// every section present (empty slice, never null) even for a freshly
// connected Channel with no data yet.
type GetChannelOverviewOutput struct {
	Channel             ChannelIdentityOutput          `json:"channel"`
	Ideas               []IdeaOverviewOutput           `json:"ideas" jsonschema:"Ideas on this Channel with their current verdict, most-recently-created first"`
	ResearchNotes       []ResearchNoteOutput           `json:"research_notes" jsonschema:"Recent research notes, most-recent first, each carrying the explicit cited boolean (FR10)"`
	VideoScripts        []VideoScriptOverviewOutput    `json:"video_scripts" jsonschema:"This Channel's video scripts, each with its title, status, target publish date if set, and bound verdict version (LB3, FR42)"`
	PendingMatchCount   int                            `json:"pending_match_count" jsonschema:"Videos on this Channel awaiting human match resolution -- call list_pending_matches to resolve them"`
	PredictionVsOutcome []PredictionVsOutcomeRowOutput `json:"prediction_vs_outcome" jsonschema:"Recent prediction-vs-outcome comparisons -- call get_prediction_vs_outcome for the full, independently-boundable read"`
	Truncated           []string                       `json:"truncated" jsonschema:"Section names whose result was capped at its documented default limit; empty if nothing was cut"`
}

// overviewDeps bundles the read-only stores get_channel_overview needs.
type overviewDeps struct {
	channels     store.ChannelStore
	research     store.ResearchStore
	videoScripts store.VideoScriptStore
	matches      store.MatchStore
	browse       store.BrowseStore
}

// registerGetChannelOverview registers get_channel_overview via
// server.RegisterRead -- Channel-scoped (NFR5), Creator and Analyst both
// see identical data (store.CanRead, C10's mutual-visibility point).
func registerGetChannelOverview(reg *server.Registry, deps overviewDeps) {
	server.RegisterRead(reg, &mcp.Tool{
		Name: "get_channel_overview",
		Description: "Read a Channel's full C10 browsing context in one call: identity + connection_state (needs_reauth " +
			"explains a stale YouTube sync), Ideas with their current verdict, recent research notes " +
			"(cited/uncited, FR10), video scripts with their title/status/target publish date/bound verdict version " +
			"(FR42), the pending-match count, and recent prediction-vs-outcome rows. Every section is " +
			"capped at a documented default and the response's truncated field says which ones were cut -- this tool " +
			"will never dump a Channel's entire history even if asked, since no MCP client's context window can hold " +
			"it. Optionally restrict to a subset via sections, and/or bound research_notes/prediction_vs_outcome by " +
			"since/before -- pair both to page backward past a section's default limit. Empty sections come back as " +
			"empty lists, never null or an error, for a freshly connected Channel.",
	}, getChannelOverview(deps))
}

func getChannelOverview(deps overviewDeps) mcp.ToolHandlerFor[GetChannelOverviewInput, GetChannelOverviewOutput] {
	return func(ctx context.Context, _ *mcp.CallToolRequest, in GetChannelOverviewInput) (*mcp.CallToolResult, GetChannelOverviewOutput, error) {
		channelID := in.ChannelScopeID()

		wantSections, err := resolveOverviewSections(in.Sections)
		if err != nil {
			return nil, GetChannelOverviewOutput{}, err
		}

		ch, err := deps.channels.GetByID(ctx, channelID)
		if err != nil {
			return nil, GetChannelOverviewOutput{}, fmt.Errorf("get_channel_overview: load channel: %w", err)
		}

		out := GetChannelOverviewOutput{
			Channel: ChannelIdentityOutput{
				ChannelID:                ch.ID.String(),
				YouTubeChannelID:         ch.YouTubeChannelID,
				Title:                    ch.Title,
				ConnectionState:          string(ch.ConnectionState),
				ConnectionStateChangedAt: ch.ConnectionStateChangedAt.Format(time.RFC3339),
			},
			Ideas:               []IdeaOverviewOutput{},
			ResearchNotes:       []ResearchNoteOutput{},
			VideoScripts:        []VideoScriptOverviewOutput{},
			PredictionVsOutcome: []PredictionVsOutcomeRowOutput{},
			Truncated:           []string{},
		}

		// Unbounded (since=nil, limit=0): PendingMatchCount must be the
		// TRUE total, not however many list_pending_matches' own default
		// page happens to return.
		pending, _, err := deps.matches.ListPending(ctx, channelID, nil, 0)
		if err != nil {
			return nil, GetChannelOverviewOutput{}, fmt.Errorf("get_channel_overview: list pending matches: %w", err)
		}
		out.PendingMatchCount = len(pending)

		if wantSections[overviewSectionIdeas] {
			ideas, truncated, err := deps.browse.IdeasWithCurrentVerdict(ctx, channelID, defaultIdeasOverviewLimit)
			if err != nil {
				return nil, GetChannelOverviewOutput{}, fmt.Errorf("get_channel_overview: list ideas: %w", err)
			}
			if truncated {
				out.Truncated = append(out.Truncated, overviewSectionIdeas)
			}
			for _, i := range ideas {
				out.Ideas = append(out.Ideas, toIdeaOverviewOutput(i))
			}
		}

		if wantSections[overviewSectionNotes] {
			notes, truncated, err := deps.research.ListFiltered(ctx, channelID, nil, nil, in.Since, in.Before, defaultNotesOverviewLimit)
			if err != nil {
				return nil, GetChannelOverviewOutput{}, fmt.Errorf("get_channel_overview: list research notes: %w", err)
			}
			if truncated {
				out.Truncated = append(out.Truncated, overviewSectionNotes)
			}
			for _, n := range notes {
				out.ResearchNotes = append(out.ResearchNotes, toResearchNoteOutput(n.ResearchNote, n.AuthorDisplayName))
			}
		}

		if wantSections[overviewSectionVideoScripts] {
			// VideoScriptStore.ListDetailByChannel is unbounded (unlike
			// ScheduleStore.ListDetailByChannel, it takes no limit) --
			// truncateSlice caps it here so this section's "bounded by
			// construction" contract holds regardless.
			details, err := deps.videoScripts.ListDetailByChannel(ctx, channelID)
			if err != nil {
				return nil, GetChannelOverviewOutput{}, fmt.Errorf("get_channel_overview: list video scripts: %w", err)
			}
			details, truncated := truncateSlice(details, defaultVideoScriptsOverviewLimit)
			if truncated {
				out.Truncated = append(out.Truncated, overviewSectionVideoScripts)
			}
			for _, d := range details {
				out.VideoScripts = append(out.VideoScripts, toVideoScriptOverviewOutput(d))
			}
		}

		if wantSections[overviewSectionOutcomes] {
			rows, truncated, err := deps.browse.PredictionVsOutcome(ctx, channelID, nil, in.Since, in.Before, defaultOutcomesOverviewLimit)
			if err != nil {
				return nil, GetChannelOverviewOutput{}, fmt.Errorf("get_channel_overview: list prediction vs outcome: %w", err)
			}
			if truncated {
				out.Truncated = append(out.Truncated, overviewSectionOutcomes)
			}
			for _, r := range rows {
				out.PredictionVsOutcome = append(out.PredictionVsOutcome, toPredictionVsOutcomeRowOutput(r))
			}
		}

		return nil, out, nil
	}
}

// -- registration ------------------------------------------------------------

// RegisterBrowse registers get_channel_overview and get_prediction_vs_outcome
// against reg (see ../server/registry.go), backed by st's
// ChannelStore/ResearchStore/VideoScriptStore/MatchStore/BrowseStore.
func RegisterBrowse(reg *server.Registry, st *store.Store) {
	registerGetChannelOverview(reg, overviewDeps{
		channels:     st.Channels(),
		research:     st.Research(),
		videoScripts: st.VideoScripts(),
		matches:      st.Matches(),
		browse:       st.Browse(),
	})
	registerGetPredictionVsOutcome(reg, st.Browse(), st.Matches())
}
