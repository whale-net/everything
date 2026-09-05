// C5's viability-verdict MCP tool group (issue #1578, FR11-FR13):
// save_viability_verdict, the append-only write-back tool that records a
// new Verdict *version* for an Idea (never UPDATEs a prior one), and
// get_viability_verdict, the read tool that returns an Idea's current
// verdict together with its full version history in one call. The
// Verdict version this write tool allocates is LB3's spine -- schedule_
// entry.verdict_id (issue #1569) and, transitively, every outcome record
// downstream of it -- reference a specific version, never "the current
// verdict" as a moving target. See ../../ARCHITECTURE.md and
// ../../store/verdict.go (VerdictStore, already real from #1569).
package tools

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/whale-net/everything/audience_score_system/mcp/server"
	"github.com/whale-net/everything/audience_score_system/store"
)

// citationExcerptRunes bounds CitedNoteOutput.TextExcerpt -- long enough
// for an agent to recognize the note, short enough that a verdict with
// many citations doesn't balloon get_viability_verdict's response. The
// full note text remains available via list_research_notes (#1577).
const citationExcerptRunes = 200

// -- shared rendering ---------------------------------------------------------

// CitedNoteOutput is one verdict_citation's resolved research_note, as
// both save_viability_verdict and get_viability_verdict render it (FR11).
type CitedNoteOutput struct {
	ID          string  `json:"id" jsonschema:"The cited research note's ID, as a UUID string"`
	TextExcerpt string  `json:"text_excerpt" jsonschema:"The cited note's text, truncated to 200 runes; see list_research_notes for the full text"`
	SourceURL   *string `json:"source_url,omitempty" jsonschema:"The cited note's own source URL, if any"`
	Cited       bool    `json:"cited" jsonschema:"True if the research note itself has a source_url (FR10) -- distinct from the fact that a verdict cites this note"`
}

// excerpt truncates s to at most citationExcerptRunes runes, appending
// "..." when truncated, so a note's excerpt is never silently mistaken
// for its full text.
func excerpt(s string) string {
	runes := []rune(s)
	if len(runes) <= citationExcerptRunes {
		return s
	}
	return string(runes[:citationExcerptRunes]) + "..."
}

// resolveCitedNotes loads noteIDs (a Verdict's CitedResearchNoteIDs) from
// research and renders each as CitedNoteOutput, in the same order. An id
// that no longer resolves (should not happen -- verdict_citation FKs
// research_note and nothing in this package ever deletes a research_note)
// is surfaced as an error rather than silently dropped, so a caller never
// mistakes a broken reference for "not cited".
func resolveCitedNotes(ctx context.Context, research store.ResearchStore, noteIDs []uuid.UUID) ([]CitedNoteOutput, error) {
	out := make([]CitedNoteOutput, 0, len(noteIDs))
	for _, id := range noteIDs {
		note, err := research.GetByID(ctx, id)
		if err != nil {
			return nil, fmt.Errorf("load cited research note %s: %w", id, err)
		}
		out = append(out, CitedNoteOutput{
			ID:          note.ID.String(),
			TextExcerpt: excerpt(note.Text),
			SourceURL:   note.SourceURL,
			Cited:       note.Cited(),
		})
	}
	return out, nil
}

// VerdictOutput is one viability_verdict version, as both
// save_viability_verdict and get_viability_verdict render it.
type VerdictOutput struct {
	ID                 string            `json:"id" jsonschema:"This Verdict version's ID, as a UUID string -- reference this from save_schedule_draft's verdict_id (FR16)"`
	IdeaID             string            `json:"idea_id" jsonschema:"The Idea this Verdict judges, as a UUID string"`
	Version            int               `json:"version" jsonschema:"This Verdict's version number for its Idea, starting at 1 and incrementing by one per save_viability_verdict call -- never reused, never decreasing (FR12)"`
	Verdict            string            `json:"verdict" jsonschema:"viable, not-viable, or needs-more-research"`
	Reasoning          string            `json:"reasoning" jsonschema:"Why this verdict was reached"`
	AuthorPersonID     string            `json:"author_person_id" jsonschema:"The Person who recorded this Verdict version, as a UUID string"`
	AuthorDisplayName  string            `json:"author_display_name" jsonschema:"The author's display name"`
	CreatedAt          string            `json:"created_at" jsonschema:"When this Verdict version was recorded, RFC3339"`
	CitedResearchNotes []CitedNoteOutput `json:"cited_research_notes" jsonschema:"The research notes this Verdict version cites (FR11), resolved and in citation order"`
}

// toVerdictOutput renders v (plus its already-resolved author display
// name and cited notes) as VerdictOutput.
func toVerdictOutput(v store.Verdict, authorDisplayName string, citedNotes []CitedNoteOutput) VerdictOutput {
	return VerdictOutput{
		ID:                 v.ID.String(),
		IdeaID:             v.IdeaID.String(),
		Version:            v.Version,
		Verdict:            string(v.Verdict),
		Reasoning:          v.Reasoning,
		AuthorPersonID:     v.AuthorPersonID.String(),
		AuthorDisplayName:  authorDisplayName,
		CreatedAt:          v.CreatedAt.Format(time.RFC3339),
		CitedResearchNotes: citedNotes,
	}
}

// renderVerdict resolves v's author display name and cited notes and
// renders the result as VerdictOutput -- the single call site both
// save_viability_verdict's render step and get_viability_verdict share,
// so they can never disagree on shape.
func renderVerdict(ctx context.Context, persons store.PersonStore, research store.ResearchStore, v store.Verdict) (VerdictOutput, error) {
	author, err := persons.GetByID(ctx, v.AuthorPersonID)
	if err != nil {
		return VerdictOutput{}, fmt.Errorf("load verdict author: %w", err)
	}
	cited, err := resolveCitedNotes(ctx, research, v.CitedResearchNoteIDs)
	if err != nil {
		return VerdictOutput{}, err
	}
	return toVerdictOutput(v, author.DisplayName, cited), nil
}

// parseVerdictValue validates raw against the three values the
// viability_verdict.verdict CHECK constraint (migration 002) allows,
// rejecting anything else before a write tool ever opens a transaction.
func parseVerdictValue(raw string) (store.VerdictValue, error) {
	switch store.VerdictValue(raw) {
	case store.VerdictViable, store.VerdictNotViable, store.VerdictNeedsMoreResearch:
		return store.VerdictValue(raw), nil
	default:
		return "", fmt.Errorf("verdict must be one of %q, %q, %q", store.VerdictViable, store.VerdictNotViable, store.VerdictNeedsMoreResearch)
	}
}

// -- save_viability_verdict --------------------------------------------------

// SaveViabilityVerdictInput is save_viability_verdict's argument schema.
// ChannelID/IdeaID/CitedResearchNoteIDs are JSON-wire strings, not
// uuid.UUID fields directly -- see ../server/fakes_test.go's scopedInput
// doc for why (jsonschema-go infers uuid.UUID as schema type "array",
// which rejects the string a real MCP client sends).
type SaveViabilityVerdictInput struct {
	ChannelID            string   `json:"channel_id" jsonschema:"Channel this verdict belongs to, as a UUID string"`
	IdeaID               string   `json:"idea_id" jsonschema:"The Idea this verdict judges, as a UUID string"`
	Verdict              string   `json:"verdict" jsonschema:"viable, not-viable, or needs-more-research"`
	Reasoning            string   `json:"reasoning" jsonschema:"Why this verdict was reached; must not be empty"`
	CitedResearchNoteIDs []string `json:"cited_research_note_ids,omitempty" jsonschema:"Research notes (as UUID strings) this verdict relies on, from this same Channel (FR11); may be empty"`
	// IdempotencyKeyArg backs IdempotencyKey() below -- named ...Arg
	// because a Go type cannot declare both a field and a method named
	// IdempotencyKey (mirrors ../server/fakes_test.go's writeInput.Key).
	IdempotencyKeyArg string `json:"idempotency_key,omitempty" jsonschema:"Caller-supplied idempotency key. Strongly recommended: a retry without one may append a spurious new verdict version (NFR2), corrupting the FR13 version history."`
}

// ChannelScopeID implements server.ChannelScoped.
func (i SaveViabilityVerdictInput) ChannelScopeID() uuid.UUID {
	id, _ := uuid.Parse(i.ChannelID)
	return id
}

// IdempotencyKey implements server.IdempotencyKeyed.
func (i SaveViabilityVerdictInput) IdempotencyKey() string { return i.IdempotencyKeyArg }

// registerSaveViabilityVerdict registers save_viability_verdict via
// server.RegisterWrite, so the idempotency middleware (NFR2) applies
// automatically -- see ../server/idempotency.go. Creator and Analyst may
// both save verdicts (store.CanWrite, applied by RegisterWrite via
// ChannelScoped).
func registerSaveViabilityVerdict(reg *server.Registry, verdicts store.VerdictStore, ideas store.IdeaStore, research store.ResearchStore, persons store.PersonStore) {
	server.RegisterWrite(reg, &mcp.Tool{
		Name: "save_viability_verdict",
		Description: "Record a new viability verdict version for an Idea (viable, not-viable, or needs-more-research), " +
			"citing the research notes it relies on. Never overwrites a prior version -- every call appends (FR12). " +
			"Always supply idempotency_key: a retry without one may append a spurious duplicate version.",
	}, saveViabilityVerdictMutate(verdicts, ideas, research), saveViabilityVerdictRender(verdicts, persons, research))
}

func saveViabilityVerdictMutate(verdicts store.VerdictStore, ideas store.IdeaStore, research store.ResearchStore) server.WriteMutate[SaveViabilityVerdictInput] {
	return func(ctx context.Context, in SaveViabilityVerdictInput) (uuid.UUID, error) {
		reasoning := strings.TrimSpace(in.Reasoning)
		if reasoning == "" {
			return uuid.Nil, fmt.Errorf("reasoning must not be empty")
		}

		verdictValue, err := parseVerdictValue(in.Verdict)
		if err != nil {
			return uuid.Nil, err
		}

		channelID, err := uuid.Parse(in.ChannelID)
		if err != nil {
			return uuid.Nil, fmt.Errorf("channel_id is not a valid UUID: %w", err)
		}

		ideaID, err := uuid.Parse(in.IdeaID)
		if err != nil {
			return uuid.Nil, fmt.Errorf("idea_id is not a valid UUID: %w", err)
		}

		idea, err := ideas.GetByID(ctx, ideaID)
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return uuid.Nil, fmt.Errorf("idea_id does not exist")
			}
			return uuid.Nil, fmt.Errorf("load idea: %w", err)
		}
		if idea.ChannelID != channelID {
			return uuid.Nil, fmt.Errorf("idea_id does not belong to channel_id")
		}

		// Validate every citation -- exists AND same-Channel -- before
		// calling Append, so an invalid citation leaves nothing written at
		// all (no verdict, no citation rows), not just a rolled-back tx.
		citedIDs := make([]uuid.UUID, 0, len(in.CitedResearchNoteIDs))
		for _, raw := range in.CitedResearchNoteIDs {
			noteID, err := uuid.Parse(raw)
			if err != nil {
				return uuid.Nil, fmt.Errorf("cited_research_note_ids contains an invalid UUID %q: %w", raw, err)
			}
			note, err := research.GetByID(ctx, noteID)
			if err != nil {
				if errors.Is(err, pgx.ErrNoRows) {
					return uuid.Nil, fmt.Errorf("cited research note %s does not exist", raw)
				}
				return uuid.Nil, fmt.Errorf("load cited research note %s: %w", raw, err)
			}
			if note.ChannelID != channelID {
				return uuid.Nil, fmt.Errorf("cited research note %s belongs to a different Channel", raw)
			}
			citedIDs = append(citedIDs, noteID)
		}

		person := server.PersonFromContext(ctx)
		if person == nil {
			return uuid.Nil, fmt.Errorf("unauthenticated: no caller credential resolved")
		}

		v, err := verdicts.Append(ctx, store.AppendVerdictInput{
			IdeaID:               ideaID,
			Verdict:              verdictValue,
			Reasoning:            reasoning,
			AuthorPersonID:       person.ID, // the calling Person, never the Channel's Creator.
			IdempotencyKey:       in.IdempotencyKeyArg,
			CitedResearchNoteIDs: citedIDs,
		})
		if err != nil {
			return uuid.Nil, err
		}
		return v.ID, nil
	}
}

// saveViabilityVerdictRender always re-reads the verdict (by ref, the
// verdict.ID mutate returned) from Postgres rather than trusting anything
// cached from mutate -- see server.RegisterWrite's doc on why render runs
// on every call, replay included.
func saveViabilityVerdictRender(verdicts store.VerdictStore, persons store.PersonStore, research store.ResearchStore) server.WriteRender[VerdictOutput] {
	return func(ctx context.Context, ref uuid.UUID) (*mcp.CallToolResult, VerdictOutput, error) {
		v, err := verdicts.GetByID(ctx, ref)
		if err != nil {
			return nil, VerdictOutput{}, fmt.Errorf("load saved verdict: %w", err)
		}
		out, err := renderVerdict(ctx, persons, research, v)
		if err != nil {
			return nil, VerdictOutput{}, err
		}
		return nil, out, nil
	}
}

// -- get_viability_verdict ---------------------------------------------------

// GetViabilityVerdictInput is get_viability_verdict's argument schema.
type GetViabilityVerdictInput struct {
	ChannelID string `json:"channel_id" jsonschema:"Channel the Idea belongs to, as a UUID string"`
	IdeaID    string `json:"idea_id" jsonschema:"The Idea to read verdicts for, as a UUID string"`
}

// ChannelScopeID implements server.ChannelScoped.
func (i GetViabilityVerdictInput) ChannelScopeID() uuid.UUID {
	id, _ := uuid.Parse(i.ChannelID)
	return id
}

// GetViabilityVerdictOutput is get_viability_verdict's structured result
// (FR13): Current is the highest-version Verdict (nil if the Idea has no
// verdict yet), History is every version ordered ascending -- a client
// never has to page or reconstruct the history from repeated calls.
type GetViabilityVerdictOutput struct {
	Current *VerdictOutput  `json:"current" jsonschema:"The highest-version Verdict for this Idea, or null if none has been recorded yet"`
	History []VerdictOutput `json:"history" jsonschema:"Every Verdict version for this Idea, ordered by version ascending; empty if none has been recorded yet"`
}

func registerGetViabilityVerdict(reg *server.Registry, verdicts store.VerdictStore, ideas store.IdeaStore, persons store.PersonStore, research store.ResearchStore) {
	server.RegisterRead(reg, &mcp.Tool{
		Name: "get_viability_verdict",
		Description: "Read an Idea's current viability verdict together with its full version history (FR13) in one " +
			"call. Returns current: null and an empty history for an Idea with no verdict yet -- not an error.",
	}, getViabilityVerdict(verdicts, ideas, persons, research))
}

func getViabilityVerdict(verdicts store.VerdictStore, ideas store.IdeaStore, persons store.PersonStore, research store.ResearchStore) mcp.ToolHandlerFor[GetViabilityVerdictInput, GetViabilityVerdictOutput] {
	return func(ctx context.Context, _ *mcp.CallToolRequest, in GetViabilityVerdictInput) (*mcp.CallToolResult, GetViabilityVerdictOutput, error) {
		channelID, err := uuid.Parse(in.ChannelID)
		if err != nil {
			return nil, GetViabilityVerdictOutput{}, fmt.Errorf("channel_id is not a valid UUID: %w", err)
		}

		ideaID, err := uuid.Parse(in.IdeaID)
		if err != nil {
			return nil, GetViabilityVerdictOutput{}, fmt.Errorf("idea_id is not a valid UUID: %w", err)
		}

		idea, err := ideas.GetByID(ctx, ideaID)
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return nil, GetViabilityVerdictOutput{}, fmt.Errorf("idea_id does not exist")
			}
			return nil, GetViabilityVerdictOutput{}, fmt.Errorf("load idea: %w", err)
		}
		if idea.ChannelID != channelID {
			return nil, GetViabilityVerdictOutput{}, fmt.Errorf("idea_id does not belong to channel_id")
		}

		history, err := verdicts.History(ctx, ideaID)
		if err != nil {
			return nil, GetViabilityVerdictOutput{}, fmt.Errorf("load verdict history: %w", err)
		}

		out := GetViabilityVerdictOutput{History: make([]VerdictOutput, 0, len(history))}
		for _, v := range history {
			rendered, err := renderVerdict(ctx, persons, research, v)
			if err != nil {
				return nil, GetViabilityVerdictOutput{}, err
			}
			out.History = append(out.History, rendered)
		}

		// current is v_current_verdict's row for this idea (via
		// VerdictStore.Current), not merely "the last History element" --
		// same underlying query the issue's FR13 spec names explicitly, so
		// current and history can never disagree on which version is
		// current even if that ever stops being "highest version number".
		cv, err := verdicts.Current(ctx, ideaID)
		switch {
		case err == nil:
			rendered, err := renderVerdict(ctx, persons, research, cv)
			if err != nil {
				return nil, GetViabilityVerdictOutput{}, err
			}
			out.Current = &rendered
		case errors.Is(err, pgx.ErrNoRows):
			// No verdict yet -- Current stays nil, History stays empty.
		default:
			return nil, GetViabilityVerdictOutput{}, fmt.Errorf("load current verdict: %w", err)
		}

		return nil, out, nil
	}
}

// -- registration ------------------------------------------------------------

// RegisterVerdict registers save_viability_verdict and
// get_viability_verdict against reg (see ../server/registry.go), backed
// by st's VerdictStore/IdeaStore/ResearchStore/PersonStore.
func RegisterVerdict(reg *server.Registry, st *store.Store) {
	registerSaveViabilityVerdict(reg, st.Verdicts(), st.Ideas(), st.Research(), st.Persons())
	registerGetViabilityVerdict(reg, st.Verdicts(), st.Ideas(), st.Persons(), st.Research())
}
