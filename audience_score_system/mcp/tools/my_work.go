// get_my_work (issue #1719, FR27/FR28): the cross-Channel "what's going on
// across everywhere I have access" aggregate -- the same four content
// areas get_channel_overview (browse.go, C10/FR24) surfaces for ONE
// Channel a caller already picked, assembled here across EVERY Channel
// the caller currently holds an open role on. Output naming deliberately
// mirrors browse.go's vocabulary (channel identity, verdict, video
// scripts, prediction-vs-outcome) so an agent sees one vocabulary across
// the whole MCP read surface.
//
// get_my_work takes no channel_id, so, like whoami/list_channels, it does
// not implement server.ChannelScoped -- it reports the caller's own
// cross-Channel state. Authorization is implicit and complete:
// store.MyWorkStore.SummariesForPerson only ever returns Channels with a
// currently-open role for the caller (re-derived fresh on every call,
// FR28), so there is nothing left to gate here.
package tools

import (
	"context"
	"fmt"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/whale-net/everything/audience_score_system/mcp/server"
	"github.com/whale-net/everything/audience_score_system/store"
)

// defaultMyWorkNotesPerChannel bounds get_my_work's per-Channel research
// notes when notes_per_channel is omitted or <= 0 -- mirrors browse.go's
// "limit <= 0 means default" convention (e.g. GetPredictionVsOutcomeInput.
// Limit) rather than distinguishing "explicitly zero" from "omitted".
const defaultMyWorkNotesPerChannel = 3

// defaultMyWorkChannelsLimit caps the number of Channel summaries
// get_my_work returns in one call -- the "bounded by construction"
// contract get_channel_overview (browse.go) established: a caller on an
// unusually large number of Channels must never see the response silently
// balloon past what an MCP client's context window can hold.
const defaultMyWorkChannelsLimit = 50

// myWorkChannelsSection is the one entry GetMyWorkOutput.Truncated can
// ever contain -- get_my_work has exactly one top-level section
// (channels) that can be cut, unlike get_channel_overview's several.
const myWorkChannelsSection = "channels"

// GetMyWorkInput is get_my_work's argument schema. No channel_id -- see
// package doc comment.
type GetMyWorkInput struct {
	NotesPerChannel int `json:"notes_per_channel,omitempty" jsonschema:"Maximum research notes to include per Channel, most-recent first (default 3). Call list_research_notes on a specific Channel for the full list."`
}

// MyWorkVerdictOutput is one Channel's single most-recently-recorded
// viability verdict across ALL of its Ideas -- get_my_work's
// latest_verdict field (FR27). Deliberately cross-Idea (unlike browse.go's
// CurrentVerdictOutput, which is scoped to one Idea already known to the
// caller), so IdeaID/IdeaTitle identify which Idea it belongs to.
type MyWorkVerdictOutput struct {
	IdeaID    string `json:"idea_id" jsonschema:"The Idea this verdict concerns, as a UUID string"`
	IdeaTitle string `json:"idea_title" jsonschema:"The Idea's title"`
	VerdictID string `json:"verdict_id" jsonschema:"This verdict version's ID, as a UUID string"`
	Version   int    `json:"version" jsonschema:"This verdict's version number for its Idea (FR12)"`
	Verdict   string `json:"verdict" jsonschema:"viable, not-viable, or needs-more-research"`
	Reasoning string `json:"reasoning" jsonschema:"Why this verdict was reached"`
	CreatedAt string `json:"created_at" jsonschema:"When this verdict version was recorded, RFC3339"`
}

func toMyWorkVerdictOutput(v store.IdeaVerdictSummary) MyWorkVerdictOutput {
	return MyWorkVerdictOutput{
		IdeaID:    v.IdeaID.String(),
		IdeaTitle: v.IdeaTitle,
		VerdictID: v.VerdictID.String(),
		Version:   v.Version,
		Verdict:   string(v.Verdict),
		Reasoning: v.Reasoning,
		CreatedAt: v.CreatedAt.Format(time.RFC3339),
	}
}

// MyWorkVideoScriptStateOutput is one Channel's video_script counts by
// status -- get_my_work's video_script_state field (FR27), mirroring
// store.VideoScriptState (retargeted from schedule_entry's draft/
// committed pair by issue #1835's retirement task). denied_count and
// archived_count are deliberately separate fields, never collapsed into
// one bucket -- FR38/FR39 make `denied` and `archived` distinct terminal
// states.
type MyWorkVideoScriptStateOutput struct {
	ProposedCount int `json:"proposed_count" jsonschema:"How many video_script rows on this Channel are proposed"`
	GreenlitCount int `json:"greenlit_count" jsonschema:"How many video_script rows on this Channel are greenlit"`
	DeniedCount   int `json:"denied_count" jsonschema:"How many video_script rows on this Channel are denied"`
	ArchivedCount int `json:"archived_count" jsonschema:"How many video_script rows on this Channel are archived"`
}

// ChannelWorkSummaryOutput is one Channel's cross-section summary, as
// get_my_work renders it (FR27) -- present for every Channel returned even
// when a section has no data yet (empty ResearchNotes, nil LatestVerdict/
// LatestOutcome, zero-valued VideoScriptState), never a missing entry.
type ChannelWorkSummaryOutput struct {
	Channel          ChannelIdentityOutput         `json:"channel"`
	Role             string                        `json:"role" jsonschema:"The caller's own currently-held role on this Channel: creator, co_creator, or analyst"`
	ResearchNotes    []ResearchNoteOutput          `json:"research_notes" jsonschema:"This Channel's most recent research notes, most-recent first, capped at notes_per_channel"`
	LatestVerdict    *MyWorkVerdictOutput          `json:"latest_verdict,omitempty" jsonschema:"This Channel's most-recently-recorded viability verdict across all its Ideas; omitted if none has been recorded yet"`
	VideoScriptState MyWorkVideoScriptStateOutput  `json:"video_script_state" jsonschema:"A compact summary of this Channel's video_script counts by status"`
	LatestOutcome    *PredictionVsOutcomeRowOutput `json:"latest_outcome,omitempty" jsonschema:"This Channel's most-recently-published prediction-vs-outcome comparison; omitted if none qualifies yet -- see get_prediction_vs_outcome's qualifying-row rule"`
}

func toChannelWorkSummaryOutput(s store.ChannelWorkSummary) ChannelWorkSummaryOutput {
	out := ChannelWorkSummaryOutput{
		Channel: ChannelIdentityOutput{
			ChannelID:                s.Channel.ID.String(),
			YouTubeChannelID:         s.Channel.YouTubeChannelID,
			Title:                    s.Channel.Title,
			ConnectionState:          string(s.Channel.ConnectionState),
			ConnectionStateChangedAt: s.Channel.ConnectionStateChangedAt.Format(time.RFC3339),
		},
		Role:          string(s.Role),
		ResearchNotes: make([]ResearchNoteOutput, 0, len(s.LatestNotes)),
		VideoScriptState: MyWorkVideoScriptStateOutput{
			ProposedCount: s.ScriptState.ProposedCount,
			GreenlitCount: s.ScriptState.GreenlitCount,
			DeniedCount:   s.ScriptState.DeniedCount,
			ArchivedCount: s.ScriptState.ArchivedCount,
		},
	}
	for _, n := range s.LatestNotes {
		// "" for author display name: MyWorkStore.SummariesForPerson's
		// notes query (NFR9's 5-statement budget) does not join to
		// person for authorship the way BrowseStore's does -- only
		// AuthorPersonID is available here.
		out.ResearchNotes = append(out.ResearchNotes, toResearchNoteOutput(n, ""))
	}
	if s.LatestVerdict != nil {
		v := toMyWorkVerdictOutput(*s.LatestVerdict)
		out.LatestVerdict = &v
	}
	if s.LatestOutcome != nil {
		o := toPredictionVsOutcomeRowOutput(*s.LatestOutcome)
		out.LatestOutcome = &o
	}
	return out
}

// GetMyWorkOutput is get_my_work's structured result.
type GetMyWorkOutput struct {
	Channels []ChannelWorkSummaryOutput `json:"channels" jsonschema:"Every Channel the caller currently holds an open role on, each with its own cross-section summary; empty for a caller with no roles anywhere, never an error"`
	// Truncated follows browse.go's GetChannelOverviewOutput.Truncated
	// convention: section names whose result was capped. get_my_work has
	// exactly one such section (channels).
	Truncated []string `json:"truncated" jsonschema:"Section names whose result was capped at its documented default limit; empty if nothing was cut"`
}

// RegisterMyWork registers get_my_work via server.RegisterRead.
func RegisterMyWork(reg *server.Registry, myWork store.MyWorkStore) {
	server.RegisterRead(reg, &mcp.Tool{
		Name: "get_my_work",
		Description: "Report the calling Person's own cross-Channel work in one call (FR27): for every Channel the " +
			"caller currently holds an open role on, their role there, recent research notes, the Channel's most " +
			"recently recorded viability verdict, its video_script counts by status, and its most recent prediction-vs-outcome " +
			"comparison. Re-derived fresh on every call from the caller's currently-held roles (FR28) -- a role " +
			"revoked between two calls drops that Channel out of the very next call's result, with no re-auth or " +
			"reconnect required. A caller with no roles anywhere gets channels: [], not an error. The channel list is " +
			"capped at a documented default; see truncated.",
	}, getMyWork(myWork))
}

func getMyWork(myWork store.MyWorkStore) mcp.ToolHandlerFor[GetMyWorkInput, GetMyWorkOutput] {
	return func(ctx context.Context, _ *mcp.CallToolRequest, in GetMyWorkInput) (*mcp.CallToolResult, GetMyWorkOutput, error) {
		person := server.PersonFromContext(ctx)
		if person == nil {
			return nil, GetMyWorkOutput{}, fmt.Errorf("unauthenticated: no caller credential resolved")
		}

		notesPerChannel := in.NotesPerChannel
		if notesPerChannel <= 0 {
			notesPerChannel = defaultMyWorkNotesPerChannel
		}

		summaries, err := myWork.SummariesForPerson(ctx, person.ID, notesPerChannel)
		if err != nil {
			return nil, GetMyWorkOutput{}, fmt.Errorf("get_my_work: %w", err)
		}

		trimmed, truncated := truncateSlice(summaries, defaultMyWorkChannelsLimit)

		out := GetMyWorkOutput{Channels: make([]ChannelWorkSummaryOutput, 0, len(trimmed)), Truncated: []string{}}
		if truncated {
			out.Truncated = append(out.Truncated, myWorkChannelsSection)
		}
		for _, s := range trimmed {
			out.Channels = append(out.Channels, toChannelWorkSummaryOutput(s))
		}
		return nil, out, nil
	}
}
