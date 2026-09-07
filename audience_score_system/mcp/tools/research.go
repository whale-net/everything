// C4's research-notes MCP tool group (issue #1577, FR9/FR10): the
// save_research_note write-back tool and the list_research_notes read tool
// that visibly distinguishes cited from uncited notes, plus create_idea/
// list_ideas since a note may attach to an Idea and every downstream LB3
// record hangs off Idea identity (see ../../ARCHITECTURE.md and issue
// #1569); plus list_research_threads (root plan #1934, issue #1937,
// FR3/FR4/FR16/NFR2) -- discovery over research_thread (migration 016) so
// a caller can pick a thread_id before calling save_research_note (the
// save path itself is issue #1938).
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
	// IdeaID is thread-resolution input only (issue #1938/#1940, FR2 Stage
	// 2): it is NOT the note's own Idea attachment -- that comes from the
	// resolved thread (thread_id/thread_title below), never from this
	// field directly. It matters only when thread_title is used (the
	// Idea component of that natural key's find-or-create) or as a
	// consistency check when thread_id is supplied: a thread_id whose
	// resolved thread belongs to a DIFFERENT Idea than idea_id is
	// rejected outright, never silently reconciled.
	IdeaID string `json:"idea_id,omitempty" jsonschema:"Thread-resolution input, NOT the note's own Idea (the note's Idea comes from its resolved thread). With thread_title: the Idea component of the find-or-create natural key. With thread_id: must agree with that thread's Idea, or the call is rejected. Omit if the note predates an Idea."`
	// ThreadID/ThreadTitle are JSON-wire strings (see ChannelID/IdeaID's
	// doc comment above for why UUID fields are declared as string). FR4:
	// supply exactly one, never both and never neither -- use
	// list_research_threads to discover an existing thread_id, or supply
	// thread_title to find-or-create one on (channel_id, idea_id, title).
	ThreadID    string `json:"thread_id,omitempty" jsonschema:"Attach this note to an existing research thread, as a UUID string. Supply exactly one of thread_id/thread_title -- use list_research_threads to discover an existing thread_id."`
	ThreadTitle string `json:"thread_title,omitempty" jsonschema:"Find-or-create a research thread by title (case/whitespace-insensitive, scoped to this channel_id and idea_id). Supply exactly one of thread_id/thread_title."`
	// Relations are FR5's typed edges from this note to prior notes in the
	// SAME resolved thread, written atomically with this note. The five
	// relation_type values: 'supersedes' and 'excludes' retire the target
	// note (it drops out of v_current_research_note); 'caveats',
	// 'follows_up', and 'summarizes' do not.
	Relations []SaveResearchNoteRelationInput `json:"relations,omitempty" jsonschema:"Typed edges to prior notes in the same resolved thread. relation_type must be one of: supersedes, excludes (both retire the target note), caveats, follows_up, summarizes (none of which retire the target)."`
	// IdempotencyKeyArg backs IdempotencyKey() below -- named ...Arg because
	// a Go type cannot declare both a field and a method named
	// IdempotencyKey (mirrors ../server/fakes_test.go's writeInput.Key).
	IdempotencyKeyArg string `json:"idempotency_key,omitempty" jsonschema:"Caller-supplied idempotency key. Strongly recommended: a retry without one may create a duplicate note (NFR2)."`
}

// SaveResearchNoteRelationInput is one entry of SaveResearchNoteInput.
// Relations (FR5) -- the MCP-wire shape of store.SaveNoteRelationInput.
type SaveResearchNoteRelationInput struct {
	RelatedNoteID string `json:"related_note_id" jsonschema:"The prior research note this relation points to, as a UUID string; must be in the same resolved thread as the note being saved"`
	RelationType  string `json:"relation_type" jsonschema:"One of: supersedes, excludes, caveats, follows_up, summarizes"`
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
	ID        string `json:"id" jsonschema:"Research note ID, as a UUID string"`
	ChannelID string `json:"channel_id" jsonschema:"Channel this note belongs to, as a UUID string"`
	// IdeaID is derived from this note's resolved thread (issue #1940,
	// FR2 Stage 2b) -- not a column on the note itself. nil when the
	// resolved thread has no Idea (FR9).
	IdeaID *string `json:"idea_id,omitempty" jsonschema:"Idea this note is attached to, as a UUID string, if any -- derived from this note's resolved thread, not stored on the note itself"`
	// ThreadID/ThreadTitle let a caller see and reuse this note's thread
	// (e.g. to attach a follow-up note or a relation) without a separate
	// list_research_threads call.
	ThreadID    *string `json:"thread_id,omitempty" jsonschema:"This note's resolved research thread, as a UUID string -- reference from a later save_research_note's thread_id"`
	ThreadTitle *string `json:"thread_title,omitempty" jsonschema:"The resolved thread's title"`
	Text        string  `json:"text" jsonschema:"The research note's body text"`
	SourceURL         *string `json:"source_url,omitempty" jsonschema:"The cited source URL, if any"`
	Cited             bool    `json:"cited" jsonschema:"True if this note has a source_url. Explicit so a client cannot mistake a missing source_url for a truncated response (FR10)."`
	AuthorPersonID    string  `json:"author_person_id" jsonschema:"The Person who authored this note (the calling credential, not the Channel's Creator), as a UUID string"`
	AuthorDisplayName string  `json:"author_display_name" jsonschema:"The author's display name"`
	// CreatedAt is formatted with sub-second precision (RFC3339Nano, still
	// a valid RFC3339 string) rather than plain RFC3339 -- issue #1808's
	// since/before pagination round-trips this value back as a cursor, and
	// two notes saved within the same second (a plausible batch-research
	// pattern) would otherwise render identical timestamps and make that
	// cursor ambiguous.
	CreatedAt string `json:"created_at" jsonschema:"When this note was created, RFC3339 (with sub-second precision -- use verbatim as a since/before pagination cursor)"`
}

// toResearchNoteOutput renders n (plus its already-resolved author display
// name) as ResearchNoteOutput -- the SINGLE conversion save_research_note,
// list_research_notes, get_channel_overview (browse.go), and my_work
// (my_work.go) all route through (FR16/NFR2), so none of them can render
// a note's Idea/thread differently. Cited is derived by n.Cited() (FR10,
// FR12 -- store.ResearchNote.Cited, models.go) at this single call site so
// callers can never disagree on that rule either. IdeaID/ThreadID/
// ThreadTitle are all derived from n's already-resolved thread (issue
// #1940, FR2 Stage 2b) -- ThreadTitle is nil exactly when ThreadID is nil
// (store.ResearchNote's own invariant), never independently.
func toResearchNoteOutput(n store.ResearchNote, authorDisplayName string) ResearchNoteOutput {
	out := ResearchNoteOutput{
		ID:                n.ID.String(),
		ChannelID:         n.ChannelID.String(),
		Text:              n.Text,
		SourceURL:         n.SourceURL,
		Cited:             n.Cited(),
		AuthorPersonID:    n.AuthorPersonID.String(),
		AuthorDisplayName: authorDisplayName,
		CreatedAt:         n.CreatedAt.Format(time.RFC3339Nano),
	}
	if n.IdeaID != nil {
		s := n.IdeaID.String()
		out.IdeaID = &s
	}
	if n.ThreadID != nil {
		s := n.ThreadID.String()
		out.ThreadID = &s
	}
	if n.ThreadTitle != nil {
		out.ThreadTitle = n.ThreadTitle
	}
	return out
}

// registerSaveResearchNote registers save_research_note via
// server.RegisterWrite, so the idempotency middleware (NFR2) applies
// automatically -- see ../server/idempotency.go.
func registerSaveResearchNote(reg *server.Registry, research store.ResearchStore, persons store.PersonStore) {
	server.RegisterWrite(reg, &mcp.Tool{
		Name: "save_research_note",
		Description: "Save a research note for a Channel, attached to a research thread and citing a source " +
			"(source_url). Omit source_url for an uncited note -- it is never coerced from an empty string. " +
			"Supply exactly one of thread_id (an existing thread, from list_research_threads) or thread_title " +
			"(find-or-create by title) -- neither or both is rejected. The note's Idea comes from the resolved " +
			"thread, not from idea_id directly -- idea_id is thread-resolution input (see its own description) and " +
			"is rejected if it conflicts with thread_id's resolved Idea. Optionally add relations to prior notes in " +
			"the same resolved thread: supersedes/excludes retire the target note, caveats/follows_up/summarizes do " +
			"not. Always supply idempotency_key: a retry without one may create a duplicate note.",
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

		var threadID *uuid.UUID
		if strings.TrimSpace(in.ThreadID) != "" {
			id, err := uuid.Parse(in.ThreadID)
			if err != nil {
				return uuid.Nil, fmt.Errorf("thread_id is not a valid UUID: %w", err)
			}
			threadID = &id
		}

		relations := make([]store.SaveNoteRelationInput, 0, len(in.Relations))
		for _, r := range in.Relations {
			relatedNoteID, err := uuid.Parse(r.RelatedNoteID)
			if err != nil {
				return uuid.Nil, fmt.Errorf("related_note_id %q is not a valid UUID: %w", r.RelatedNoteID, err)
			}
			relationType := store.RelationType(r.RelationType)
			if !relationType.Valid() {
				return uuid.Nil, fmt.Errorf("relation_type %q is not a recognized relation type", r.RelationType)
			}
			relations = append(relations, store.SaveNoteRelationInput{RelatedNoteID: relatedNoteID, RelationType: relationType})
		}

		person := server.PersonFromContext(ctx)
		if person == nil {
			return uuid.Nil, fmt.Errorf("unauthenticated: no caller credential resolved")
		}

		// SourceURL is passed through raw -- SaveNote itself owns the
		// trim/normalize and the reject (FR12, store/research.go), so this
		// tool never validates it twice. Same for thread_id/thread_title's
		// exactly-one-of rule (FR4) -- SaveNote itself enforces it.
		note, err := research.SaveNote(ctx, store.SaveNoteInput{
			ChannelID:      channelID,
			IdeaID:         ideaID,
			ThreadID:       threadID,
			ThreadTitle:    in.ThreadTitle,
			Relations:      relations,
			Text:           text,
			SourceURL:      &in.SourceURL,
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

// defaultListResearchNotesLimit bounds list_research_notes' response when a
// caller supplies limit <= 0 -- without this, a Channel/Idea with enough
// notes exceeds the calling MCP client's response-size cap and the tool
// call fails outright with no way to retrieve the data in pages (issue
// #1808).
const defaultListResearchNotesLimit = 50

// ListResearchNotesInput is list_research_notes's argument schema.
type ListResearchNotesInput struct {
	ChannelID string `json:"channel_id" jsonschema:"Channel to list research notes for, as a UUID string"`
	// IdeaID restricts by the note's resolved THREAD's Idea (issue #1940,
	// FR2 Stage 2b), not a column on the note itself -- composes with
	// ThreadID (both may be supplied together) rather than replacing it.
	IdeaID string `json:"idea_id,omitempty" jsonschema:"Restrict to notes whose resolved thread belongs to this Idea, as a UUID string"`
	// ThreadID is FR8's thread filter -- composes with idea_id and every
	// other filter below (cited_only/uncited_only/since/before/limit),
	// never bypasses them.
	ThreadID    string `json:"thread_id,omitempty" jsonschema:"Restrict to notes attached to this research thread, as a UUID string"`
	CitedOnly   bool   `json:"cited_only,omitempty" jsonschema:"Return only notes with a source_url (cited, FR10). Mutually exclusive with uncited_only."`
	UncitedOnly bool   `json:"uncited_only,omitempty" jsonschema:"Return only notes with no source_url (uncited, FR10). Mutually exclusive with cited_only."`
	// CurrentOnly (FR8) defaults to false, preserving today's behaviour
	// (every note, including retired ones) when omitted. It composes with
	// every other filter on this input (idea_id, cited_only/uncited_only,
	// since/before, limit) rather than replacing any of them.
	CurrentOnly bool `json:"current_only,omitempty" jsonschema:"Exclude notes superseded or excluded by a later note (i.e. any note that is the target of a 'supersedes' or 'excludes' relation, FR8). Does NOT exclude notes targeted only by 'caveats', 'follows_up', or 'summarizes' relations -- those remain current. Composes with every other filter (idea_id, cited_only/uncited_only, since/before, limit); default false returns every note including retired ones."`
	// Since/Before bound the window by each note's created_at. Together
	// they let a caller page backward past a truncated response (issue
	// #1808): request the newest window, then re-request with before set
	// to the oldest note's created_at from the previous response.
	Since  *time.Time `json:"since,omitempty" jsonschema:"Only include notes created at or after this time"`
	Before *time.Time `json:"before,omitempty" jsonschema:"Only include notes created strictly before this time -- pair with since to page backward past a truncated response"`
	Limit  int        `json:"limit,omitempty" jsonschema:"Maximum notes to return, most-recent first (default 50). The response's truncated flag is set when more matching notes exist."`
}

// ChannelScopeID implements server.ChannelScoped.
func (i ListResearchNotesInput) ChannelScopeID() uuid.UUID {
	id, _ := uuid.Parse(i.ChannelID)
	return id
}

// ListResearchNotesOutput is list_research_notes's structured result.
type ListResearchNotesOutput struct {
	Notes     []ResearchNoteOutput `json:"notes" jsonschema:"Matching research notes, most-recent first"`
	Truncated bool                 `json:"truncated" jsonschema:"True if more matching notes exist beyond limit"`
}

func registerListResearchNotes(reg *server.Registry, research store.ResearchStore) {
	server.RegisterRead(reg, &mcp.Tool{
		Name: "list_research_notes",
		Description: "List research notes for a Channel, most-recent first, each carrying an explicit cited boolean " +
			"(FR10) and its resolved thread_id/thread_title. Optionally restrict to one Idea (idea_id, matched via " +
			"each note's resolved thread), one thread (thread_id), and/or partition into cited_only vs uncited_only " +
			"-- filters compose. Set current_only to exclude notes superseded or excluded by a later note (FR8). " +
			"Response is capped at limit (default 50); see truncated. Page backward past truncation by re-calling " +
			"with before set to the oldest returned note's created_at.",
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

		var threadID *uuid.UUID
		if strings.TrimSpace(in.ThreadID) != "" {
			id, err := uuid.Parse(in.ThreadID)
			if err != nil {
				return nil, ListResearchNotesOutput{}, fmt.Errorf("thread_id is not a valid UUID: %w", err)
			}
			threadID = &id
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

		limit := in.Limit
		if limit <= 0 {
			limit = defaultListResearchNotesLimit
		}
		notes, truncated, err := research.ListFiltered(ctx, channelID, ideaID, threadID, cited, in.CurrentOnly, in.Since, in.Before, limit)
		if err != nil {
			return nil, ListResearchNotesOutput{}, err
		}

		out := ListResearchNotesOutput{Notes: make([]ResearchNoteOutput, 0, len(notes)), Truncated: truncated}
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

// defaultListIdeasLimit bounds list_ideas' response when a caller supplies
// limit <= 0 -- without this, a Channel with enough Ideas exceeds the
// calling MCP client's response-size cap and the tool call fails outright
// with no way to retrieve the data in pages (issue #1813, following
// #1808's pattern). Ideas are a medium-risk list (plausible to reach
// hundreds over sustained Loop-1 usage), so this matches the
// research-notes/pending-matches default rather than a smaller one.
const defaultListIdeasLimit = 50

// ListIdeasInput is list_ideas's argument schema.
type ListIdeasInput struct {
	ChannelID string `json:"channel_id" jsonschema:"Channel to list Ideas for, as a UUID string"`
	// Since bounds the window by each Idea's created_at (inclusive).
	// ListByChannelWithStats returns Ideas oldest-first, so a caller
	// pages forward past a truncated response by re-calling with since
	// set to the last returned Idea's created_at -- that Idea reappears
	// as the new page's first row (inclusive bound), so de-duplicate on
	// idea_id across pages if needed (issue #1813, mirrors
	// list_pending_matches' since semantics from #1808).
	Since *time.Time `json:"since,omitempty" jsonschema:"Only include Ideas created at or after this time -- page forward past a truncated response by setting this to the last returned Idea's created_at (that Idea will reappear as this page's first row)"`
	Limit int        `json:"limit,omitempty" jsonschema:"Maximum Ideas to return, oldest first (default 50). The response's truncated flag is set when more matching Ideas exist."`
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
	// CreatedAt is formatted with sub-second precision (RFC3339Nano,
	// still a valid RFC3339 string) rather than plain RFC3339 -- issue
	// #1813's since pagination round-trips this value back as a cursor,
	// and two Ideas created within the same second (a plausible batch
	// research pattern) would otherwise render identical timestamps and
	// make that cursor ambiguous (mirrors ResearchNoteOutput.CreatedAt's
	// #1808 fix).
	CreatedAt string `json:"created_at" jsonschema:"When this Idea was first created, RFC3339 (with sub-second precision -- use verbatim as a since pagination cursor)"`
}

// ListIdeasOutput is list_ideas's structured result.
type ListIdeasOutput struct {
	Ideas     []IdeaSummaryOutput `json:"ideas" jsonschema:"Ideas on this Channel, oldest first"`
	Truncated bool                `json:"truncated" jsonschema:"True if more matching Ideas exist beyond limit"`
}

func registerListIdeas(reg *server.Registry, ideas store.IdeaStore) {
	server.RegisterRead(reg, &mcp.Tool{
		Name: "list_ideas",
		Description: "List Ideas on a Channel with their research-note counts and whether a viability verdict exists " +
			"yet, oldest first. Response is capped at limit (default 50); see truncated. Page forward past truncation " +
			"by re-calling with since set to the last returned Idea's created_at.",
	}, listIdeas(ideas))
}

func listIdeas(ideas store.IdeaStore) mcp.ToolHandlerFor[ListIdeasInput, ListIdeasOutput] {
	return func(ctx context.Context, _ *mcp.CallToolRequest, in ListIdeasInput) (*mcp.CallToolResult, ListIdeasOutput, error) {
		channelID, err := uuid.Parse(in.ChannelID)
		if err != nil {
			return nil, ListIdeasOutput{}, fmt.Errorf("channel_id is not a valid UUID: %w", err)
		}

		limit := in.Limit
		if limit <= 0 {
			limit = defaultListIdeasLimit
		}
		summaries, truncated, err := ideas.ListByChannelWithStats(ctx, channelID, in.Since, limit)
		if err != nil {
			return nil, ListIdeasOutput{}, err
		}

		out := ListIdeasOutput{Ideas: make([]IdeaSummaryOutput, 0, len(summaries)), Truncated: truncated}
		for _, s := range summaries {
			out.Ideas = append(out.Ideas, IdeaSummaryOutput{
				IdeaID:     s.ID.String(),
				Title:      s.Title,
				NoteCount:  s.NoteCount,
				HasVerdict: s.HasVerdict,
				CreatedAt:  s.CreatedAt.Format(time.RFC3339Nano),
			})
		}
		return nil, out, nil
	}
}

// -- list_research_threads --------------------------------------------------

// ListResearchThreadsInput is list_research_threads's argument schema.
// ChannelID and IdeaID are JSON-wire strings, not uuid.UUID fields
// directly -- see SaveResearchNoteInput's doc comment above for why.
type ListResearchThreadsInput struct {
	ChannelID string `json:"channel_id" jsonschema:"Channel to list research threads for, as a UUID string"`
	IdeaID    string `json:"idea_id,omitempty" jsonschema:"Restrict to threads attached to this Idea, as a UUID string; omit to list every thread on the Channel, including ones that predate an Idea"`
}

// ChannelScopeID implements server.ChannelScoped.
func (i ListResearchThreadsInput) ChannelScopeID() uuid.UUID {
	id, _ := uuid.Parse(i.ChannelID)
	return id
}

// ResearchThreadOutput is one research_thread row's list_research_threads
// shape: FR3's discovery fields, no more -- the save path itself (thread
// content) is a separate task (#1938).
type ResearchThreadOutput struct {
	ID        string  `json:"id" jsonschema:"This thread's ID, as a UUID string -- reference this from save_research_note's thread_id"`
	Title     string  `json:"title" jsonschema:"The thread's title"`
	IdeaID    *string `json:"idea_id,omitempty" jsonschema:"Idea this thread is attached to, as a UUID string, if any"`
	NoteCount int     `json:"note_count" jsonschema:"How many research notes are attached to this thread"`
	// LatestNoteAt is RFC3339Nano (sub-second precision), matching
	// ResearchNoteOutput.CreatedAt's cursor-safety rationale -- omitted
	// entirely (not an empty string) when the thread has no notes yet.
	LatestNoteAt *string `json:"latest_note_at,omitempty" jsonschema:"The most recent note's created_at on this thread, RFC3339 (sub-second precision); omitted if the thread has no notes yet"`
}

// toResearchThreadOutput renders t as ResearchThreadOutput.
func toResearchThreadOutput(t store.ThreadSummary) ResearchThreadOutput {
	out := ResearchThreadOutput{
		ID:        t.ID.String(),
		Title:     t.Title,
		NoteCount: t.NoteCount,
	}
	if t.IdeaID != nil {
		s := t.IdeaID.String()
		out.IdeaID = &s
	}
	if t.LatestNoteAt != nil {
		s := t.LatestNoteAt.Format(time.RFC3339Nano)
		out.LatestNoteAt = &s
	}
	return out
}

// ListResearchThreadsOutput is list_research_threads's structured result.
type ListResearchThreadsOutput struct {
	Threads []ResearchThreadOutput `json:"threads" jsonschema:"Matching research threads, most-recent activity first (threads with no notes yet last)"`
}

func registerListResearchThreads(reg *server.Registry, threads store.ThreadStore) {
	server.RegisterRead(reg, &mcp.Tool{
		Name: "list_research_threads",
		Description: "List research threads on a Channel, most-recent activity first (threads with no notes yet " +
			"last), each with its note count and most-recent note time. Optionally restrict to one Idea (idea_id). " +
			"Use this to pick a thread_id before calling save_research_note.",
	}, listResearchThreads(threads))
}

func listResearchThreads(threads store.ThreadStore) mcp.ToolHandlerFor[ListResearchThreadsInput, ListResearchThreadsOutput] {
	return func(ctx context.Context, _ *mcp.CallToolRequest, in ListResearchThreadsInput) (*mcp.CallToolResult, ListResearchThreadsOutput, error) {
		channelID, err := uuid.Parse(in.ChannelID)
		if err != nil {
			return nil, ListResearchThreadsOutput{}, fmt.Errorf("channel_id is not a valid UUID: %w", err)
		}

		var ideaID *uuid.UUID
		if strings.TrimSpace(in.IdeaID) != "" {
			id, err := uuid.Parse(in.IdeaID)
			if err != nil {
				return nil, ListResearchThreadsOutput{}, fmt.Errorf("idea_id is not a valid UUID: %w", err)
			}
			ideaID = &id
		}

		summaries, err := threads.ListByChannel(ctx, channelID, ideaID)
		if err != nil {
			return nil, ListResearchThreadsOutput{}, err
		}

		out := ListResearchThreadsOutput{Threads: make([]ResearchThreadOutput, 0, len(summaries))}
		for _, s := range summaries {
			out.Threads = append(out.Threads, toResearchThreadOutput(s))
		}
		return nil, out, nil
	}
}

// -- registration ------------------------------------------------------------

// RegisterResearch registers save_research_note, list_research_notes,
// create_idea, list_ideas, and list_research_threads against reg (see
// ../server/registry.go), backed by st's IdeaStore/ResearchStore/
// ThreadStore/PersonStore.
func RegisterResearch(reg *server.Registry, st *store.Store) {
	registerSaveResearchNote(reg, st.Research(), st.Persons())
	registerListResearchNotes(reg, st.Research())
	registerCreateIdea(reg, st.Ideas())
	registerListIdeas(reg, st.Ideas())
	registerListResearchThreads(reg, st.Threads())
}
