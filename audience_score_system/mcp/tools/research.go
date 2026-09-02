// C4's research-notes MCP tool group (issue #1577, FR9/FR10): the
// save_research_note write-back tool and the list_research_notes read tool
// that visibly distinguishes cited from uncited notes, plus create_idea/
// list_ideas since a note may attach to an Idea and every downstream LB3
// record hangs off Idea identity (see ../../ARCHITECTURE.md and issue
// #1569).
package tools

import (
	"context"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/whale-net/everything/audience_score_system/mcp/server"
	"github.com/whale-net/everything/audience_score_system/store"
)

// -- save_research_note ------------------------------------------------------

// SaveResearchNoteInput is save_research_note's argument schema. ChannelID
// and IdeaID are JSON-wire strings, not uuid.UUID fields directly -- see
// ../server/fakes_test.go's scopedInput doc for why (jsonschema-go infers
// uuid.UUID as schema type "array", which rejects the string a real MCP
// client sends).
type SaveResearchNoteInput struct {
	ChannelID string `json:"channel_id" jsonschema:"Channel to attach this research note to, as a UUID string"`
	Text      string `json:"text" jsonschema:"The research note's body text; must not be empty"`
	SourceURL string `json:"source_url,omitempty" jsonschema:"Absolute http(s) URL this note cites; omit or leave empty for an uncited note (FR10) -- never coerced from missing to an empty-string citation"`
	IdeaID    string `json:"idea_id,omitempty" jsonschema:"Idea this note is attached to, as a UUID string; omit if the note predates an Idea"`
	// IdempotencyKeyArg backs IdempotencyKey() below -- named ...Arg because
	// a Go type cannot declare both a field and a method named
	// IdempotencyKey (mirrors ../server/fakes_test.go's writeInput.Key).
	IdempotencyKeyArg string `json:"idempotency_key,omitempty" jsonschema:"Caller-supplied idempotency key. Strongly recommended: a retry without one may create a duplicate note (NFR2)."`
}

// ChannelScopeID implements server.ChannelScoped.
func (i SaveResearchNoteInput) ChannelScopeID() uuid.UUID {
	id, _ := uuid.Parse(i.ChannelID)
	return id
}

// IdempotencyKey implements server.IdempotencyKeyed.
func (i SaveResearchNoteInput) IdempotencyKey() string { return i.IdempotencyKeyArg }

// ResearchNoteOutput is the shape both save_research_note and
// list_research_notes render for a single research note.
type ResearchNoteOutput struct {
	ID                string  `json:"id" jsonschema:"Research note ID, as a UUID string"`
	ChannelID         string  `json:"channel_id" jsonschema:"Channel this note belongs to, as a UUID string"`
	IdeaID            *string `json:"idea_id,omitempty" jsonschema:"Idea this note is attached to, as a UUID string, if any"`
	Text              string  `json:"text" jsonschema:"The research note's body text"`
	SourceURL         *string `json:"source_url,omitempty" jsonschema:"The cited source URL, if any"`
	Cited             bool    `json:"cited" jsonschema:"True if this note has a source_url. Explicit so a client cannot mistake a missing source_url for a truncated response (FR10)."`
	AuthorPersonID    string  `json:"author_person_id" jsonschema:"The Person who authored this note (the calling credential, not the Channel's Creator), as a UUID string"`
	AuthorDisplayName string  `json:"author_display_name" jsonschema:"The author's display name"`
	CreatedAt         string  `json:"created_at" jsonschema:"When this note was created, RFC3339"`
}

// toResearchNoteOutput renders n (plus its already-resolved author display
// name) as ResearchNoteOutput. Cited is derived from n.SourceURL == nil
// (FR10) at this single call site so save_research_note and
// list_research_notes can never disagree on the rule.
func toResearchNoteOutput(n store.ResearchNote, authorDisplayName string) ResearchNoteOutput {
	out := ResearchNoteOutput{
		ID:                n.ID.String(),
		ChannelID:         n.ChannelID.String(),
		Text:              n.Text,
		SourceURL:         n.SourceURL,
		Cited:             n.SourceURL != nil,
		AuthorPersonID:    n.AuthorPersonID.String(),
		AuthorDisplayName: authorDisplayName,
		CreatedAt:         n.CreatedAt.Format(time.RFC3339),
	}
	if n.IdeaID != nil {
		s := n.IdeaID.String()
		out.IdeaID = &s
	}
	return out
}

// validateSourceURL trims raw and, if non-empty, requires it to be a
// well-formed absolute http(s) URL -- rejecting rather than silently
// storing junk (e.g. "not a url", "javascript:...") that would later be
// rendered as "cited". Returns nil (never a pointer to an empty string)
// for an absent/empty/whitespace-only raw, which is what SaveNoteInput
// persists as SQL NULL -- the FR10 uncited case.
func validateSourceURL(raw string) (*string, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return nil, nil
	}
	u, err := url.Parse(trimmed)
	if err != nil {
		return nil, fmt.Errorf("source_url is not a well-formed URL: %w", err)
	}
	if !u.IsAbs() || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return nil, fmt.Errorf("source_url must be an absolute http or https URL")
	}
	return &trimmed, nil
}

// registerSaveResearchNote registers save_research_note via
// server.RegisterWrite, so the idempotency middleware (NFR2) applies
// automatically -- see ../server/idempotency.go.
func registerSaveResearchNote(reg *server.Registry, research store.ResearchStore, persons store.PersonStore) {
	server.RegisterWrite(reg, &mcp.Tool{
		Name: "save_research_note",
		Description: "Save a research note for a Channel, optionally attached to an Idea (idea_id) and citing a source " +
			"(source_url). Omit source_url for an uncited note -- it is never coerced from an empty string. " +
			"Always supply idempotency_key: a retry without one may create a duplicate note.",
	}, saveResearchNoteMutate(research), saveResearchNoteRender(research, persons))
}

func saveResearchNoteMutate(research store.ResearchStore) server.WriteMutate[SaveResearchNoteInput] {
	return func(ctx context.Context, in SaveResearchNoteInput) (uuid.UUID, error) {
		text := strings.TrimSpace(in.Text)
		if text == "" {
			return uuid.Nil, fmt.Errorf("text must not be empty")
		}

		channelID, err := uuid.Parse(in.ChannelID)
		if err != nil {
			return uuid.Nil, fmt.Errorf("channel_id is not a valid UUID: %w", err)
		}

		var ideaID *uuid.UUID
		if strings.TrimSpace(in.IdeaID) != "" {
			id, err := uuid.Parse(in.IdeaID)
			if err != nil {
				return uuid.Nil, fmt.Errorf("idea_id is not a valid UUID: %w", err)
			}
			ideaID = &id
		}

		sourceURL, err := validateSourceURL(in.SourceURL)
		if err != nil {
			return uuid.Nil, err
		}

		person := server.PersonFromContext(ctx)
		if person == nil {
			return uuid.Nil, fmt.Errorf("unauthenticated: no caller credential resolved")
		}

		note, err := research.SaveNote(ctx, store.SaveNoteInput{
			ChannelID:      channelID,
			IdeaID:         ideaID,
			Text:           text,
			SourceURL:      sourceURL,
			AuthorPersonID: person.ID, // the calling Person, never the Channel's Creator.
			IdempotencyKey: in.IdempotencyKeyArg,
		})
		if err != nil {
			return uuid.Nil, err
		}
		return note.ID, nil
	}
}

// saveResearchNoteRender always re-reads the note (and its author's
// current display name) from Postgres rather than trusting anything
// cached from mutate -- see server.RegisterWrite's doc on why render runs
// on every call, replay included.
func saveResearchNoteRender(research store.ResearchStore, persons store.PersonStore) server.WriteRender[ResearchNoteOutput] {
	return func(ctx context.Context, ref uuid.UUID) (*mcp.CallToolResult, ResearchNoteOutput, error) {
		note, err := research.GetByID(ctx, ref)
		if err != nil {
			return nil, ResearchNoteOutput{}, fmt.Errorf("load saved research note: %w", err)
		}
		author, err := persons.GetByID(ctx, note.AuthorPersonID)
		if err != nil {
			return nil, ResearchNoteOutput{}, fmt.Errorf("load research note author: %w", err)
		}
		return nil, toResearchNoteOutput(note, author.DisplayName), nil
	}
}

// -- list_research_notes ------------------------------------------------------

// ListResearchNotesInput is list_research_notes's argument schema.
type ListResearchNotesInput struct {
	ChannelID   string `json:"channel_id" jsonschema:"Channel to list research notes for, as a UUID string"`
	IdeaID      string `json:"idea_id,omitempty" jsonschema:"Restrict to notes attached to this Idea, as a UUID string"`
	CitedOnly   bool   `json:"cited_only,omitempty" jsonschema:"Return only notes with a source_url (cited, FR10). Mutually exclusive with uncited_only."`
	UncitedOnly bool   `json:"uncited_only,omitempty" jsonschema:"Return only notes with no source_url (uncited, FR10). Mutually exclusive with cited_only."`
}

// ChannelScopeID implements server.ChannelScoped.
func (i ListResearchNotesInput) ChannelScopeID() uuid.UUID {
	id, _ := uuid.Parse(i.ChannelID)
	return id
}

// ListResearchNotesOutput is list_research_notes's structured result.
type ListResearchNotesOutput struct {
	Notes []ResearchNoteOutput `json:"notes" jsonschema:"Matching research notes, most-recent first"`
}

func registerListResearchNotes(reg *server.Registry, research store.ResearchStore) {
	server.RegisterRead(reg, &mcp.Tool{
		Name: "list_research_notes",
		Description: "List research notes for a Channel, most-recent first, each carrying an explicit cited boolean " +
			"(FR10). Optionally restrict to one Idea (idea_id) and/or partition into cited_only vs uncited_only.",
	}, listResearchNotes(research))
}

func listResearchNotes(research store.ResearchStore) mcp.ToolHandlerFor[ListResearchNotesInput, ListResearchNotesOutput] {
	return func(ctx context.Context, _ *mcp.CallToolRequest, in ListResearchNotesInput) (*mcp.CallToolResult, ListResearchNotesOutput, error) {
		if in.CitedOnly && in.UncitedOnly {
			return nil, ListResearchNotesOutput{}, fmt.Errorf("cited_only and uncited_only are mutually exclusive")
		}

		channelID, err := uuid.Parse(in.ChannelID)
		if err != nil {
			return nil, ListResearchNotesOutput{}, fmt.Errorf("channel_id is not a valid UUID: %w", err)
		}

		var ideaID *uuid.UUID
		if strings.TrimSpace(in.IdeaID) != "" {
			id, err := uuid.Parse(in.IdeaID)
			if err != nil {
				return nil, ListResearchNotesOutput{}, fmt.Errorf("idea_id is not a valid UUID: %w", err)
			}
			ideaID = &id
		}

		var cited *bool
		switch {
		case in.CitedOnly:
			v := true
			cited = &v
		case in.UncitedOnly:
			v := false
			cited = &v
		}

		notes, err := research.ListFiltered(ctx, channelID, ideaID, cited)
		if err != nil {
			return nil, ListResearchNotesOutput{}, err
		}

		out := ListResearchNotesOutput{Notes: make([]ResearchNoteOutput, 0, len(notes))}
		for _, n := range notes {
			out.Notes = append(out.Notes, toResearchNoteOutput(n.ResearchNote, n.AuthorDisplayName))
		}
		return nil, out, nil
	}
}

// -- create_idea ---------------------------------------------------------------

// CreateIdeaInput is create_idea's argument schema. No idempotency_key --
// FindOrCreate's natural-key upsert on (channel_id, lower(title)) makes
// this safe under replay by construction (NFR2), matching
// ../server/idempotency.go's doc: a write tool whose input doesn't
// implement IdempotencyKeyed must be safe via natural-key upsert.
type CreateIdeaInput struct {
	ChannelID string `json:"channel_id" jsonschema:"Channel this Idea belongs to, as a UUID string"`
	Title     string `json:"title" jsonschema:"The Idea's title; must not be empty. Repeated calls with the same channel_id and a case/whitespace-insensitively equal title converge on the same Idea."`
}

// ChannelScopeID implements server.ChannelScoped.
func (i CreateIdeaInput) ChannelScopeID() uuid.UUID {
	id, _ := uuid.Parse(i.ChannelID)
	return id
}

// IdeaOutput is create_idea's structured result.
type IdeaOutput struct {
	IdeaID            string `json:"idea_id" jsonschema:"This Idea's ID, as a UUID string -- reference this from every later research/verdict/schedule tool call"`
	ChannelID         string `json:"channel_id" jsonschema:"Channel this Idea belongs to, as a UUID string"`
	Title             string `json:"title" jsonschema:"The Idea's title"`
	CreatedByPersonID string `json:"created_by_person_id" jsonschema:"The Person who first created this Idea, as a UUID string"`
	CreatedAt         string `json:"created_at" jsonschema:"When this Idea was first created, RFC3339"`
}

func toIdeaOutput(i store.Idea) IdeaOutput {
	return IdeaOutput{
		IdeaID:            i.ID.String(),
		ChannelID:         i.ChannelID.String(),
		Title:             i.Title,
		CreatedByPersonID: i.CreatedByPersonID.String(),
		CreatedAt:         i.CreatedAt.Format(time.RFC3339),
	}
}

func registerCreateIdea(reg *server.Registry, ideas store.IdeaStore) {
	server.RegisterWrite(reg, &mcp.Tool{
		Name: "create_idea",
		Description: "Create an Idea on a Channel, or return the existing one if a case/whitespace-insensitively " +
			"matching title already exists on that Channel (natural-key upsert -- no idempotency_key needed). Returns " +
			"the idea_id every later research/verdict/schedule tool call references.",
	}, createIdeaMutate(ideas), createIdeaRender(ideas))
}

func createIdeaMutate(ideas store.IdeaStore) server.WriteMutate[CreateIdeaInput] {
	return func(ctx context.Context, in CreateIdeaInput) (uuid.UUID, error) {
		title := strings.TrimSpace(in.Title)
		if title == "" {
			return uuid.Nil, fmt.Errorf("title must not be empty")
		}

		channelID, err := uuid.Parse(in.ChannelID)
		if err != nil {
			return uuid.Nil, fmt.Errorf("channel_id is not a valid UUID: %w", err)
		}

		person := server.PersonFromContext(ctx)
		if person == nil {
			return uuid.Nil, fmt.Errorf("unauthenticated: no caller credential resolved")
		}

		idea, err := ideas.FindOrCreate(ctx, channelID, title, person.ID)
		if err != nil {
			return uuid.Nil, err
		}
		return idea.ID, nil
	}
}

func createIdeaRender(ideas store.IdeaStore) server.WriteRender[IdeaOutput] {
	return func(ctx context.Context, ref uuid.UUID) (*mcp.CallToolResult, IdeaOutput, error) {
		idea, err := ideas.GetByID(ctx, ref)
		if err != nil {
			return nil, IdeaOutput{}, fmt.Errorf("load idea: %w", err)
		}
		return nil, toIdeaOutput(idea), nil
	}
}

// -- list_ideas ------------------------------------------------------------

// ListIdeasInput is list_ideas's argument schema.
type ListIdeasInput struct {
	ChannelID string `json:"channel_id" jsonschema:"Channel to list Ideas for, as a UUID string"`
}

// ChannelScopeID implements server.ChannelScoped.
func (i ListIdeasInput) ChannelScopeID() uuid.UUID {
	id, _ := uuid.Parse(i.ChannelID)
	return id
}

// IdeaSummaryOutput is one Idea's list_ideas row: its research-note count
// and whether a viability verdict exists yet.
type IdeaSummaryOutput struct {
	IdeaID     string `json:"idea_id" jsonschema:"This Idea's ID, as a UUID string"`
	Title      string `json:"title" jsonschema:"The Idea's title"`
	NoteCount  int    `json:"note_count" jsonschema:"How many research notes are attached to this Idea"`
	HasVerdict bool   `json:"has_verdict" jsonschema:"Whether at least one viability verdict has been recorded for this Idea"`
	CreatedAt  string `json:"created_at" jsonschema:"When this Idea was first created, RFC3339"`
}

// ListIdeasOutput is list_ideas's structured result.
type ListIdeasOutput struct {
	Ideas []IdeaSummaryOutput `json:"ideas" jsonschema:"Every Idea on this Channel"`
}

func registerListIdeas(reg *server.Registry, ideas store.IdeaStore) {
	server.RegisterRead(reg, &mcp.Tool{
		Name:        "list_ideas",
		Description: "List Ideas on a Channel with their research-note counts and whether a viability verdict exists yet.",
	}, listIdeas(ideas))
}

func listIdeas(ideas store.IdeaStore) mcp.ToolHandlerFor[ListIdeasInput, ListIdeasOutput] {
	return func(ctx context.Context, _ *mcp.CallToolRequest, in ListIdeasInput) (*mcp.CallToolResult, ListIdeasOutput, error) {
		channelID, err := uuid.Parse(in.ChannelID)
		if err != nil {
			return nil, ListIdeasOutput{}, fmt.Errorf("channel_id is not a valid UUID: %w", err)
		}

		summaries, err := ideas.ListByChannelWithStats(ctx, channelID)
		if err != nil {
			return nil, ListIdeasOutput{}, err
		}

		out := ListIdeasOutput{Ideas: make([]IdeaSummaryOutput, 0, len(summaries))}
		for _, s := range summaries {
			out.Ideas = append(out.Ideas, IdeaSummaryOutput{
				IdeaID:     s.ID.String(),
				Title:      s.Title,
				NoteCount:  s.NoteCount,
				HasVerdict: s.HasVerdict,
				CreatedAt:  s.CreatedAt.Format(time.RFC3339),
			})
		}
		return nil, out, nil
	}
}

// -- registration ------------------------------------------------------------

// RegisterResearch registers save_research_note, list_research_notes,
// create_idea, and list_ideas against reg (see ../server/registry.go),
// backed by st's IdeaStore/ResearchStore/PersonStore.
func RegisterResearch(reg *server.Registry, st *store.Store) {
	registerSaveResearchNote(reg, st.Research(), st.Persons())
	registerListResearchNotes(reg, st.Research())
	registerCreateIdea(reg, st.Ideas())
	registerListIdeas(reg, st.Ideas())
}
