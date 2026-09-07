// Package research is `web`'s Loop 1 browse-and-save surface (milestone
// M4.1): a Channel-scoped research index (every Idea with its note count
// and verdict presence, plus a separate section for research notes that
// predate any Idea, M1 FR9) and a per-Idea detail page (that Idea's
// research notes, most-recent first, and its full viability-verdict
// history) -- both built by #1899 (FR1/FR2/FR8/FR9/FR10) -- plus #1900's
// save-note form on both pages (FR3, the research-note half of FR6/FR7,
// NFR1/NFR3) and this task's (#1901) save-verdict form on IdeaDetail
// (FR4, the verdict half of FR6/FR7, NFR1/NFR3). Handlers and templ views
// live together in this one package, mirroring web/schedule.Handlers'
// package doc comment rationale: the read/write flow and its views are
// tightly coupled with no reuse outside this package.
//
// renderChannelIndex/renderIdeaDetail also load FR3's research-thread
// discovery list (root plan #1934, issue #1937) via the identical
// store.ThreadStore.ListByChannel call and store.CanRead check
// list_research_threads makes (NFR2) -- ChannelIndex renders every thread
// on the Channel, IdeaDetail that Idea's threads only.
//
// HandleSaveNote calls store.ResearchStore.SaveNote, which as of issue
// #1938 requires every note to resolve a thread (FR4). This form has no
// thread/relation UI yet -- defaultNoteThreadTitle is a stopgap constant
// (see HandleSaveNote's own doc comment) that #1945 replaces with a real
// picker/creator calling this same SaveNote method (NFR2).
//
// This task reuses #1900's write-path plumbing verbatim rather than
// duplicating it: newIdempotencyKey (server-generated, minted once at
// render time) and authorizeWrite (parse + fresh store.CanWrite preamble
// for POST handlers) back HandleSaveVerdict exactly as they back
// HandleSaveNote -- see each's doc comment.
//
// #1943 (FR9, FR16, NFR2) adds IdeaDetail's cited-notes rendering: every
// verdict's CitedResearchNoteIDs, resolved to its store.ResearchNote by
// citedResearchNotes below in ONE batched store.ResearchStore.GetByIDs
// call across the whole page (current plus every history entry), never
// one query per citation. mcp/tools/verdict.go's resolveCitedNotes shares
// the SAME GetByIDs method and the SAME excerpt truncation
// (mcp/tools.Excerpt/CitationExcerptRunes) -- one resolution path and one
// truncation bound for both surfaces (FR16/NFR2).
//
// Authorization (NFR2, NFR3, NFR5): both GET routes are visible to a
// Channel's Founder, Co-Creator, AND Analyst (store.CanRead) -- mirrors
// web/schedule.Handlers.HandleList's read gate exactly, since Loop 1
// browse carries the same three-tier read visibility as schedule
// approval's read side. Both POST routes additionally require
// store.CanWrite (identical tier set to CanRead, see store/authz.go) --
// re-derived fresh from Postgres on every request via authorizeWrite,
// never from the session alone, a cached field, or which form the client
// was shown. IdeaDetail omits either form entirely when canWrite is
// false, but that omission is presentation only (see IdeaDetail's/
// ChannelIndex's doc comments in views.templ): the server-side check
// inside authorizeWrite is what actually rejects a forged POST from a
// non-member or a signed-out request, exactly as web/schedule's package
// doc comment describes for its own mutating routes.
//
// Routes (mounted by ../main.go's setupRoutes, behind
// web/auth.Authenticator.RequireSignedIn):
//
//   - GET /channels/{id}/research -- HandleChannelIndex (FR1).
//   - GET /channels/{id}/research/ideas/{ideaID} -- HandleIdeaDetail
//     (FR2, FR9, FR10).
//   - POST /channels/{id}/research/notes -- HandleSaveNote (FR3, FR6,
//     FR7).
//   - POST /channels/{id}/research/ideas/{ideaID}/verdicts --
//     HandleSaveVerdict (FR4, FR6, FR7).
//   - POST /channels/{id}/research/ideas/{ideaID}/video-scripts --
//     HandleProposeVideoScript (#1915, FR1-FR5, NFR1-NFR3).
package research

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/whale-net/everything/audience_score_system/store"
	"github.com/whale-net/everything/audience_score_system/web/auth"
	"github.com/whale-net/everything/audience_score_system/web/components"
)

// defaultPageLimit is NFR2's fixed 50-row default page for both
// HandleChannelIndex's Idea list and HandleIdeaDetail's research-note
// list -- no "load more" control, no client-driven paging exists anywhere
// in this package (see views.templ).
const defaultPageLimit = 50

// Handlers holds the dependencies research's routes need: the Store (for
// store.CanRead/store.CanWrite and Ideas()/Research()/Verdicts()/
// Channels()/Roles()/Persons()).
type Handlers struct {
	store *store.Store
}

// New wires st into a Handlers.
func New(st *store.Store) *Handlers {
	return &Handlers{store: st}
}

// noteFormData carries the save-note form's current values through a
// render: on a plain GET (HandleChannelIndex/HandleIdeaDetail) it holds
// nothing but a freshly minted IdempotencyKey (newIdempotencyKey below,
// FR6) and, on IdeaDetail, that page's own Idea pre-selected; on a
// validation-failure re-render from HandleSaveNote it additionally
// carries the submitted Text/SourceURL/IdeaID and an Error message, with
// the SAME IdempotencyKey the failed POST carried -- so a corrected
// resubmit is still the same logical write (FR6, FR7).
type noteFormData struct {
	IdempotencyKey string
	Text           string
	SourceURL      string
	IdeaID         string // "" means unattached / no selection.
	Error          string
}

// verdictFormData carries the save-verdict form's (#1901, FR4) current
// values through a render: on a plain GET (HandleIdeaDetail, via
// newVerdictFormData) it holds nothing but a freshly minted
// IdempotencyKey (newIdempotencyKey, FR6); on a validation-failure
// re-render from HandleSaveVerdict it additionally carries the submitted
// Verdict/Reasoning/CitedNoteIDs and an Error message, with the SAME
// IdempotencyKey the failed POST carried -- so a corrected resubmit is
// still the same logical write (FR6, FR7), mirroring noteFormData's
// contract exactly.
type verdictFormData struct {
	IdempotencyKey string
	Verdict        string // raw submitted value; "" on a plain GET.
	Reasoning      string
	CitedNoteIDs   []string // submitted cited_note_ids, as strings, for re-selecting the multi-select.
	Error          string
}

// newVerdictFormData mints a fresh verdictFormData for a plain render --
// no submitted content, just a freshly minted IdempotencyKey (FR6). Used
// by HandleIdeaDetail's GET and by HandleSaveNote's re-renders (the
// verdict form was not the form that failed, so it gets its own new key
// rather than reusing HandleSaveNote's).
func newVerdictFormData() verdictFormData {
	return verdictFormData{IdempotencyKey: newIdempotencyKey()}
}

// proposeFormData carries the propose-video-script form's (#1915, FR1-FR5)
// current values through a render: on a plain GET (HandleIdeaDetail, via
// newProposeFormData) it holds nothing but a freshly minted IdempotencyKey
// (newIdempotencyKey, FR5); on a validation-failure re-render from
// HandleProposeVideoScript it additionally carries the submitted
// StrategyID/Title/ScriptText/TargetPublishDate and an Error message, with
// the SAME IdempotencyKey the failed POST carried -- so a corrected
// resubmit is still the same logical write (FR5), mirroring
// verdictFormData's contract exactly. There is deliberately no VerdictID
// field here: the Idea's current verdict is always resolved server-side at
// submit time (LB3), never accepted as a form input.
type proposeFormData struct {
	IdempotencyKey    string
	StrategyID        string // raw submitted value; "" on a plain GET.
	Title             string
	ScriptText        string
	TargetPublishDate string // raw "YYYY-MM-DD"; "" means unset.
	Error             string
}

// newProposeFormData mints a fresh proposeFormData for a plain render --
// no submitted content, just a freshly minted IdempotencyKey (FR5). Used by
// HandleIdeaDetail's GET and by HandleSaveNote's/HandleSaveVerdict's
// re-renders (the propose form was not the form that failed, so it gets
// its own new key rather than reusing theirs), mirroring
// newVerdictFormData's identical rationale.
func newProposeFormData() proposeFormData {
	return proposeFormData{IdempotencyKey: newIdempotencyKey()}
}

// newIdempotencyKey mints a server-generated idempotency key (FR6),
// created ONCE at render time and carried as a hidden
// <input name="idempotency_key"> on both this task's save-note form and
// the verdict-form task's (#1901) save-verdict form. Never client-
// generated and never derived from the form's own content -- two
// separate GETs of the same page must always mint two different keys, so
// the browser back-button/refresh double-submit case (NFR1) is caught by
// SaveNote's (channel, author, key) dedupe, not by content hashing.
func newIdempotencyKey() string {
	return uuid.NewString()
}

// authorizeWrite is the shared authorization + parse preamble for
// research's POST handlers (HandleSaveNote here; the verdict-form task's,
// #1901, save-verdict handler reuses it verbatim): resolve the signed-in
// Person (401 if none -- RequireSignedIn should already guarantee this,
// so nil here means it was wired incorrectly), parse {id} (400 on
// malformed), load the Channel (404 via pgx.ErrNoRows), and re-derive
// store.CanWrite fresh from Postgres ON THIS REQUEST (403 when false) --
// never from session state, a hidden form field, or which button the
// client rendered (FR7, NFR3). ok is false after this has already
// written the appropriate error response; callers MUST return
// immediately when ok is false.
func (h *Handlers) authorizeWrite(w http.ResponseWriter, r *http.Request) (person *store.Person, ch store.Channel, ok bool) {
	ctx := r.Context()
	person = auth.PersonFromContext(ctx)
	if person == nil {
		http.Error(w, "not signed in", http.StatusUnauthorized)
		return nil, store.Channel{}, false
	}

	channelID, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		http.Error(w, "invalid channel id", http.StatusBadRequest)
		return nil, store.Channel{}, false
	}

	ch, err = h.store.Channels().GetByID(ctx, channelID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			http.NotFound(w, r)
			return nil, store.Channel{}, false
		}
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return nil, store.Channel{}, false
	}

	canWrite, err := store.CanWrite(ctx, h.store.Roles(), channelID, person.ID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return nil, store.Channel{}, false
	}
	if !canWrite {
		http.Error(w, "forbidden", http.StatusForbidden)
		return nil, store.Channel{}, false
	}

	return person, ch, true
}

// HandleChannelIndex serves GET /channels/{id}/research (FR1's read
// side): every Idea on the Channel with its note count and verdict
// presence, plus a separate unattached-notes section (M1 FR9). Visible to
// Founder, Co-Creator, and Analyst alike (store.CanRead).
//
// Order of operations mirrors web/schedule.Handlers.HandleList exactly
// (load-bearing, not stylistic): an unknown Channel must 404 before
// authorization can turn it into a 403.
func (h *Handlers) HandleChannelIndex(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	person := auth.PersonFromContext(ctx)
	if person == nil {
		http.Error(w, "not signed in", http.StatusUnauthorized)
		return
	}

	channelID, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		http.Error(w, "invalid channel id", http.StatusBadRequest)
		return
	}

	ch, err := h.store.Channels().GetByID(ctx, channelID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			http.NotFound(w, r)
			return
		}
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	canRead, err := store.CanRead(ctx, h.store.Roles(), channelID, person.ID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if !canRead {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}

	h.renderChannelIndex(w, r, person, ch, noteFormData{IdempotencyKey: newIdempotencyKey()}, http.StatusOK)
}

// renderChannelIndex assembles and renders /channels/{id}/research: the
// identical query set for a plain GET (HandleChannelIndex, form carrying
// only a freshly minted IdempotencyKey and status 200) and for the same
// page re-rendered after a failed POST /channels/{id}/research/notes
// whose idea_id was empty, missing, or itself invalid (HandleSaveNote,
// form carrying the submitted values plus a non-empty Error and status
// 400) -- differing only in form and status, never in what is queried.
// canWrite (store.CanWrite, a second call alongside HandleChannelIndex's
// CanRead, mirroring web/schedule.HandleList's canApprove alongside
// canRead) gates whether ChannelIndex renders the save-note form at all
// (FR7) -- presentation only, see ChannelIndex's doc comment.
func (h *Handlers) renderChannelIndex(w http.ResponseWriter, r *http.Request, person *store.Person, ch store.Channel, form noteFormData, status int) {
	ctx := r.Context()
	channelID := ch.ID

	canWrite, err := store.CanWrite(ctx, h.store.Roles(), channelID, person.ID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	ideas, ideasTruncated, err := h.store.Ideas().ListByChannelWithStats(ctx, channelID, nil, defaultPageLimit)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	// notes/notesTruncated is the identical 50-row default page
	// ListFiltered returns for the whole Channel (same call list_
	// research_notes and get_channel_overview make), filtered below to
	// just the unattached (idea_id IS NULL, M1 FR9) rows -- no new store
	// method or SQL path for the unattached section in M4.1. notesTruncated
	// therefore reports whether that CHANNEL-WIDE 50-row page was
	// truncated, not specifically whether more unattached notes exist
	// beyond it: this is deliberately the default page, not a complete
	// unattached-note listing (NFR2).
	notes, notesTruncated, err := h.store.Research().ListFiltered(ctx, channelID, nil, nil, nil, nil, nil, defaultPageLimit)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	var unattached []store.ResearchNoteWithAuthor
	for _, n := range notes {
		if n.IdeaID == nil {
			unattached = append(unattached, n)
		}
	}

	// threads is FR3's discovery list, scoped to the whole Channel
	// (ideaID nil) -- the IDENTICAL store.ThreadStore.ListByChannel call
	// and canRead check list_research_threads makes (NFR2, no second
	// query path).
	threads, err := h.store.Threads().ListByChannel(ctx, channelID, nil)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	title := ch.Title + " research"
	data := components.LayoutData{Title: title, User: person}
	if status != http.StatusOK {
		w.WriteHeader(status)
	}
	if err := components.Render(w, r, title, ChannelIndex(data, ch, ideas, unattached, threads, ideasTruncated, notesTruncated, canWrite, form)); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

// HandleIdeaDetail serves GET /channels/{id}/research/ideas/{ideaID}
// (FR2, FR9, FR10): that Idea's research notes (most-recent first) and
// its current verdict plus full version history -- the identical pair of
// store.VerdictStore calls get_viability_verdict makes, so `web` and
// `mcp` can never disagree on which version is current.
func (h *Handlers) HandleIdeaDetail(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	person := auth.PersonFromContext(ctx)
	if person == nil {
		http.Error(w, "not signed in", http.StatusUnauthorized)
		return
	}

	channelID, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		http.Error(w, "invalid channel id", http.StatusBadRequest)
		return
	}

	ch, err := h.store.Channels().GetByID(ctx, channelID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			http.NotFound(w, r)
			return
		}
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	canRead, err := store.CanRead(ctx, h.store.Roles(), channelID, person.ID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if !canRead {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}

	ideaID, err := uuid.Parse(r.PathValue("ideaID"))
	if err != nil {
		http.Error(w, "invalid idea id", http.StatusBadRequest)
		return
	}

	idea, err := h.store.Ideas().GetByID(ctx, ideaID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			http.NotFound(w, r)
			return
		}
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	// A cross-Channel Idea probe (an Idea that exists, but under a
	// different Channel than the path's {id}) 404s exactly like an unknown
	// Idea -- never 403, never rendered under the wrong Channel's URL --
	// so a caller can never distinguish "wrong channel" from "does not
	// exist".
	if idea.ChannelID != channelID {
		http.NotFound(w, r)
		return
	}

	h.renderIdeaDetail(w, r, person, ch, idea, noteFormData{IdempotencyKey: newIdempotencyKey(), IdeaID: idea.ID.String()}, newVerdictFormData(), newProposeFormData(), http.StatusOK)
}

// renderIdeaDetail assembles and renders an Idea's detail page: the
// identical query set for a plain GET (HandleIdeaDetail, both forms
// carrying only a freshly minted IdempotencyKey, note form's idea
// pre-selected, status 200), for the same page re-rendered after a
// failed POST /channels/{id}/research/notes whose idea_id resolved to
// idea (HandleSaveNote, note form carrying the submitted values plus a
// non-empty Error and status 400, verdict form freshly minted since it
// was not the form that failed), and for the same page re-rendered after
// a failed POST .../verdicts (HandleSaveVerdict, verdict form carrying
// the submitted values plus a non-empty Error and status 400, note form
// freshly minted) -- differing only in which form carries an Error and
// status. canWrite (store.CanWrite, a second call alongside
// HandleIdeaDetail's CanRead) gates whether IdeaDetail renders EITHER
// form at all (FR7) -- presentation only, see IdeaDetail's doc comment.
//
// This also loads the Channel's active Strategies (#1915, FR2) for the
// propose-video-script form's picker -- the identical
// store.StrategyStore.ListByChannel(activeOnly=true) call, so a Strategy
// that is not currently active on this Channel can never appear as an
// option.
func (h *Handlers) renderIdeaDetail(w http.ResponseWriter, r *http.Request, person *store.Person, ch store.Channel, idea store.Idea, form noteFormData, verdictForm verdictFormData, proposeForm proposeFormData, status int) {
	ctx := r.Context()
	channelID := ch.ID
	ideaID := idea.ID

	canWrite, err := store.CanWrite(ctx, h.store.Roles(), channelID, person.ID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	// activeStrategies backs the propose-video-script form's picker
	// (#1915, FR2) -- only Strategies active on THIS Channel are ever
	// offered, so the form can never present one Propose would itself
	// reject with ErrStrategyNotFound.
	activeStrategies, _, err := h.store.Strategies().ListByChannel(ctx, channelID, true, defaultPageLimit)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	notes, notesTruncated, err := h.store.Research().ListFiltered(ctx, channelID, &ideaID, nil, nil, nil, nil, defaultPageLimit)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	// threads is FR3's discovery list, scoped to THIS Idea only -- the
	// IDENTICAL store.ThreadStore.ListByChannel call and canRead check
	// list_research_threads makes (NFR2, no second query path).
	threads, err := h.store.Threads().ListByChannel(ctx, channelID, &ideaID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	// History + Current are the identical pair of store.VerdictStore calls
	// get_viability_verdict makes (mcp/tools/verdict.go), in the same
	// order, so `web` and `mcp` can never disagree on which version is
	// current.
	history, err := h.store.Verdicts().History(ctx, ideaID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	var current *store.Verdict
	cv, err := h.store.Verdicts().Current(ctx, ideaID)
	switch {
	case err == nil:
		current = &cv
	case errors.Is(err, pgx.ErrNoRows):
		// No verdict yet -- current stays nil, rendered as an empty
		// verdict section (200), never an error.
	default:
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	authorNames, err := h.verdictAuthorDisplayNames(ctx, history, current)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	citedNotes, err := h.citedResearchNotes(ctx, history, current)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	// The save-note form always attaches to this page's own Idea (FR3):
	// idea_id is pre-selected regardless of what the caller passed in
	// form.IdeaID, so a HandleSaveNote re-render can never accidentally
	// present a stale or mismatched idea_id here.
	form.IdeaID = idea.ID.String()

	title := idea.Title
	data := components.LayoutData{Title: title, User: person}
	if status != http.StatusOK {
		w.WriteHeader(status)
	}
	// notes (already loaded above for the note list, FR2) is the SAME
	// slice the save-verdict form's citation multi-select is populated
	// from -- no extra store call, and no notes from any other Idea can
	// ever appear as options (FR4).
	if err := components.Render(w, r, title, IdeaDetail(data, ch, idea, notes, notesTruncated, threads, current, history, authorNames, citedNotes, canWrite, form, verdictForm, activeStrategies, proposeForm)); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

// defaultNoteThreadTitle is HandleSaveNote's stopgap ThreadTitle (issue
// #1938) until #1945 adds this form's real thread/relation UI. It matches
// migration 016's backfill title exactly (see that migration's header
// comment), so every note this form saves converges onto the same
// per-(channel, idea) default thread the migration already created for
// pre-existing notes, rather than forking a differently-named bucket.
const defaultNoteThreadTitle = "Research"

// HandleSaveNote serves POST /channels/{id}/research/notes (FR3, FR6,
// FR7): saves a research note through store.ResearchStore.SaveNote --
// the IDENTICAL method save_research_note's mutate step calls (LB5: one
// write path, never a parallel one) -- then 303-redirects back to the
// page the form was rendered on.
//
// Field handling:
//   - text: required; empty/whitespace-only re-renders the originating
//     page (400) with an error and the submitted values preserved.
//   - source_url: optional, passed through RAW to SaveNote -- this
//     handler does not pre-validate or normalize it. #1897 moved that
//     rule inside SaveNote itself precisely so this handler carries no
//     second copy of it; a SaveNote validation error re-renders the
//     originating page (400) with the store's own message.
//   - idea_id: optional. Empty means IdeaID: nil (M1 FR9's unattached
//     note). When present, it must parse as a UUID and name an Idea that
//     belongs to THIS Channel -- otherwise this re-renders the Channel
//     index (400), since there is no valid Idea to show a detail page
//     for. A valid idea_id determines both which page is re-rendered on
//     a later validation failure and which page success redirects to.
//   - idempotency_key: read from the hidden field the rendering GET set
//     (newIdempotencyKey); if absent, treated as empty, so SaveNote
//     simply does not dedupe rather than this handler inventing a key
//     server-side per submit.
func (h *Handlers) HandleSaveNote(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	person, ch, ok := h.authorizeWrite(w, r)
	if !ok {
		return
	}
	channelID := ch.ID

	if err := r.ParseForm(); err != nil {
		http.Error(w, "invalid form", http.StatusBadRequest)
		return
	}

	form := noteFormData{
		IdempotencyKey: r.FormValue("idempotency_key"),
		Text:           r.FormValue("text"),
		SourceURL:      r.FormValue("source_url"),
		IdeaID:         r.FormValue("idea_id"),
	}
	text := strings.TrimSpace(form.Text)

	// Resolve idea_id first: its validity decides which page any
	// subsequent validation failure re-renders (Idea detail vs. Channel
	// index), mirroring which page success redirects to below.
	var ideaID *uuid.UUID
	var validIdea store.Idea
	haveValidIdea := false
	if form.IdeaID != "" {
		parsed, err := uuid.Parse(form.IdeaID)
		if err != nil {
			h.renderChannelIndex(w, r, person, ch, formWithError(form, "invalid idea selection"), http.StatusBadRequest)
			return
		}
		idea, err := h.store.Ideas().GetByID(ctx, parsed)
		if err != nil || idea.ChannelID != channelID {
			// Same cross-Channel rule as HandleIdeaDetail's, but a 400
			// here (a form field, not a URL path segment) rather than
			// that handler's 404.
			h.renderChannelIndex(w, r, person, ch, formWithError(form, "invalid idea selection"), http.StatusBadRequest)
			return
		}
		ideaID = &parsed
		validIdea = idea
		haveValidIdea = true
	}

	renderErr := func(msg string) {
		if haveValidIdea {
			h.renderIdeaDetail(w, r, person, ch, validIdea, formWithError(form, msg), newVerdictFormData(), newProposeFormData(), http.StatusBadRequest)
			return
		}
		h.renderChannelIndex(w, r, person, ch, formWithError(form, msg), http.StatusBadRequest)
	}

	if text == "" {
		renderErr("note text is required")
		return
	}

	var sourceURLPtr *string
	if form.SourceURL != "" {
		sourceURLPtr = &form.SourceURL
	}

	_, err := h.store.Research().SaveNote(ctx, store.SaveNoteInput{
		ChannelID: channelID,
		IdeaID:    ideaID,
		// ThreadTitle is a stopgap default, not a form field: SaveNote now
		// requires a resolved thread on every write (FR4, issue #1938),
		// but this form's own thread/relation UI is a separate task
		// (#1945). "Research" matches migration 016's backfill title
		// exactly, so every note this form saves converges onto the same
		// per-(channel, idea) default thread the migration already
		// created for pre-existing notes, rather than forking a
		// differently-named bucket. #1945 replaces this constant with a
		// real thread picker/creator calling this same SaveNote method
		// (NFR2).
		ThreadTitle:    defaultNoteThreadTitle,
		Text:           text,
		SourceURL:      sourceURLPtr,
		AuthorPersonID: person.ID,
		IdempotencyKey: form.IdempotencyKey,
	})
	if err != nil {
		renderErr(err.Error())
		return
	}

	if ideaID != nil {
		http.Redirect(w, r, "/channels/"+channelID.String()+"/research/ideas/"+ideaID.String(), http.StatusSeeOther)
		return
	}
	http.Redirect(w, r, "/channels/"+channelID.String()+"/research", http.StatusSeeOther)
}

// formWithError returns a copy of form with Error set to msg -- a small
// helper so HandleSaveNote's several validation-failure call sites never
// need to spell out the field-by-field copy themselves.
func formWithError(form noteFormData, msg string) noteFormData {
	form.Error = msg
	return form
}

// verdictFormWithError returns a copy of form with Error set to msg,
// mirroring formWithError above for HandleSaveVerdict's validation-failure
// call sites.
func verdictFormWithError(form verdictFormData, msg string) verdictFormData {
	form.Error = msg
	return form
}

// parseVerdictFormValue validates raw against the three values the
// viability_verdict.verdict CHECK constraint (migration 002) allows,
// mirroring mcp/tools/verdict.go's parseVerdictValue exactly (LB5: the
// same validation rule, never a second copy of the CHECK constraint's
// value set drifting independently) -- rejects anything else before
// HandleSaveVerdict ever calls Append, so an invalid selection writes
// nothing (never trusts the rendered <select> alone, per FR4's scope).
func parseVerdictFormValue(raw string) (store.VerdictValue, error) {
	switch store.VerdictValue(raw) {
	case store.VerdictViable, store.VerdictNotViable, store.VerdictNeedsMoreResearch:
		return store.VerdictValue(raw), nil
	default:
		return "", fmt.Errorf("verdict must be one of %q, %q, %q", store.VerdictViable, store.VerdictNotViable, store.VerdictNeedsMoreResearch)
	}
}

// HandleSaveVerdict serves POST
// /channels/{id}/research/ideas/{ideaID}/verdicts (FR4, FR5, FR6, FR7):
// appends a new viability_verdict version through the IDENTICAL
// store.VerdictStore.Append method save_viability_verdict's mutate step
// calls (mcp/tools/verdict.go, LB5 -- one write path, never a parallel
// one) -- Source: store.VerdictSourceHuman is the only difference from
// that MCP call site, a value on that one write, never a second write
// path -- then 303-redirects back to the Idea detail page, so a refresh
// lands on a GET (never resubmits the form).
//
// Field handling:
//   - verdict: required; must be exactly one of store.VerdictValue's
//     three constants (parseVerdictFormValue) -- anything else 400s with
//     nothing written; the rendered <select> is never trusted alone.
//   - reasoning: required, non-empty after trimming; empty 400s with the
//     form re-rendered carrying the error.
//   - cited_note_ids: zero or more repeated values (r.Form's multi-value
//     support), each parsed as a UUID and verified to belong to THIS Idea
//     (via store.ResearchStore.GetByID) before it is used -- a forged
//     note ID from another Idea or another Channel 400s with nothing
//     written, so it can never end up in verdict_citation.
//   - idempotency_key: read from the hidden field the rendering GET set
//     (newVerdictFormData); if absent, treated as empty, so Append simply
//     does not dedupe rather than this handler inventing a key
//     server-side per submit -- mirrors HandleSaveNote's identical
//     rationale.
func (h *Handlers) HandleSaveVerdict(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	person, ch, ok := h.authorizeWrite(w, r)
	if !ok {
		return
	}
	channelID := ch.ID

	ideaID, err := uuid.Parse(r.PathValue("ideaID"))
	if err != nil {
		http.Error(w, "invalid idea id", http.StatusBadRequest)
		return
	}

	idea, err := h.store.Ideas().GetByID(ctx, ideaID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			http.NotFound(w, r)
			return
		}
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	// Same cross-Channel rule as HandleIdeaDetail's guard: an Idea that
	// exists but under a different Channel than the path's {id} 404s
	// exactly like an unknown Idea.
	if idea.ChannelID != channelID {
		http.NotFound(w, r)
		return
	}

	if err := r.ParseForm(); err != nil {
		http.Error(w, "invalid form", http.StatusBadRequest)
		return
	}

	form := verdictFormData{
		IdempotencyKey: r.FormValue("idempotency_key"),
		Verdict:        r.FormValue("verdict"),
		Reasoning:      r.FormValue("reasoning"),
		CitedNoteIDs:   r.Form["cited_note_ids"],
	}

	renderErr := func(msg string) {
		// Neither the note form nor the propose form was the form that
		// failed here -- each gets its own freshly minted key rather than
		// reusing verdict's.
		h.renderIdeaDetail(w, r, person, ch, idea, noteFormData{IdempotencyKey: newIdempotencyKey(), IdeaID: idea.ID.String()}, verdictFormWithError(form, msg), newProposeFormData(), http.StatusBadRequest)
	}

	verdictValue, err := parseVerdictFormValue(form.Verdict)
	if err != nil {
		renderErr("invalid verdict selection")
		return
	}

	reasoning := strings.TrimSpace(form.Reasoning)
	if reasoning == "" {
		renderErr("reasoning is required")
		return
	}

	citedIDs := make([]uuid.UUID, 0, len(form.CitedNoteIDs))
	for _, raw := range form.CitedNoteIDs {
		noteID, err := uuid.Parse(raw)
		if err != nil {
			renderErr("invalid cited note selection")
			return
		}
		note, err := h.store.Research().GetByID(ctx, noteID)
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				renderErr("invalid cited note selection")
				return
			}
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		// A forged note ID belonging to a different Idea (or unattached,
		// or another Channel entirely) must never end up in
		// verdict_citation -- reject the whole submission (400, nothing
		// written) rather than silently dropping just that ID.
		if note.IdeaID == nil || *note.IdeaID != ideaID {
			renderErr("invalid cited note selection")
			return
		}
		citedIDs = append(citedIDs, noteID)
	}

	_, err = h.store.Verdicts().Append(ctx, store.AppendVerdictInput{
		IdeaID:               ideaID,
		Verdict:              verdictValue,
		Reasoning:            reasoning,
		AuthorPersonID:       person.ID,
		IdempotencyKey:       form.IdempotencyKey,
		CitedResearchNoteIDs: citedIDs,
		// Source is always human here -- this handler is the web
		// save-verdict form (FR5); MCP's save_viability_verdict is the
		// only other call site and always sets VerdictSourceAgent. Set
		// explicitly rather than relying on Append's empty-Source
		// normalization, so this call site can never silently drift if
		// that default ever changes.
		Source: store.VerdictSourceHuman,
	})
	if err != nil {
		renderErr(err.Error())
		return
	}

	http.Redirect(w, r, "/channels/"+channelID.String()+"/research/ideas/"+ideaID.String(), http.StatusSeeOther)
}

// proposeFormWithError returns a copy of form with Error set to msg,
// mirroring formWithError/verdictFormWithError above for
// HandleProposeVideoScript's validation-failure call sites.
func proposeFormWithError(form proposeFormData, msg string) proposeFormData {
	form.Error = msg
	return form
}

// HandleProposeVideoScript serves POST
// /channels/{id}/research/ideas/{ideaID}/video-scripts (#1915, FR1-FR5,
// NFR1-NFR3): proposes a video_script through the IDENTICAL
// store.VideoScriptStore.Propose method save_video_script's mutate step
// calls (mcp/tools/video_script.go, LB5 -- one write path, never a
// parallel one), then 303-redirects to /channels/{id}/schedule (FR3),
// where the new proposed row is immediately visible -- no separate
// propose-confirmation page.
//
// Field handling:
//   - verdict_id is NEVER a form field (LB3, one level up from Propose's
//     own "idea_id is always derived from verdict_id" rule): the Idea's
//     CURRENT verdict is always resolved server-side, via
//     h.store.Verdicts().Current, at submit time. A forged verdict_id
//     value in the POST body (there is no such field to forge, but a
//     client could send one anyway) is silently ignored -- ParseForm
//     never even reads it.
//   - No current verdict, or a current verdict that is not
//     store.VerdictViable, re-renders the Idea detail page (400) with a
//     form error -- this is FR4's actual server-side gate; FR1's
//     render-time hiding in renderIdeaDetail/views.templ is presentation
//     only.
//   - strategy_id: required, must parse as a UUID. A strategy_id naming a
//     Strategy that is not on this Channel (or was deactivated/deleted
//     between render and submit, NFR3) is rejected by Propose itself
//     (store.ErrStrategyNotFound), mapped to a form error re-render, not
//     a 500.
//   - title / script_text: required, non-empty after
//     strings.TrimSpace.
//   - target_publish_date: optional; empty string means nil. When
//     present, parsed as "2006-01-02" (the HTML date input's wire
//     format) -- not RFC3339 like the MCP tool's string input, since this
//     value comes from an <input type="date"> rather than a caller-typed
//     string.
//   - idempotency_key: read from the hidden field the rendering GET set
//     (newProposeFormData); if absent, treated as empty, so Propose
//     simply does not dedupe rather than this handler inventing a key
//     server-side per submit -- mirrors HandleSaveNote's/
//     HandleSaveVerdict's identical rationale (FR5/NFR1).
//
// Any validation failure, and any error Propose itself returns
// (ErrVerdictNotViable, ErrStrategyNotFound, or otherwise), re-renders the
// Idea detail page (400) with a form error and the submitted values
// preserved, carrying the SAME idempotency_key the failed POST carried --
// so a corrected resubmit stays one logical write.
func (h *Handlers) HandleProposeVideoScript(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	person, ch, ok := h.authorizeWrite(w, r)
	if !ok {
		return
	}
	channelID := ch.ID

	ideaID, err := uuid.Parse(r.PathValue("ideaID"))
	if err != nil {
		http.Error(w, "invalid idea id", http.StatusBadRequest)
		return
	}

	idea, err := h.store.Ideas().GetByID(ctx, ideaID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			http.NotFound(w, r)
			return
		}
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	// Same cross-Channel rule as HandleIdeaDetail's/HandleSaveVerdict's
	// guard: an Idea that exists but under a different Channel than the
	// path's {id} 404s exactly like an unknown Idea -- never 403, never
	// distinguishable from "does not exist".
	if idea.ChannelID != channelID {
		http.NotFound(w, r)
		return
	}

	if err := r.ParseForm(); err != nil {
		http.Error(w, "invalid form", http.StatusBadRequest)
		return
	}

	form := proposeFormData{
		IdempotencyKey:    r.FormValue("idempotency_key"),
		StrategyID:        r.FormValue("strategy_id"),
		Title:             r.FormValue("title"),
		ScriptText:        r.FormValue("script_text"),
		TargetPublishDate: r.FormValue("target_publish_date"),
	}

	renderErr := func(msg string) {
		// Neither the note form nor the verdict form was the form that
		// failed here -- each gets its own freshly minted key rather than
		// reusing propose's, mirroring HandleSaveVerdict's identical
		// rationale.
		h.renderIdeaDetail(w, r, person, ch, idea, noteFormData{IdempotencyKey: newIdempotencyKey(), IdeaID: idea.ID.String()}, newVerdictFormData(), proposeFormWithError(form, msg), http.StatusBadRequest)
	}

	// The Idea's current verdict is always resolved server-side (LB3) --
	// there is no verdict_id form field to trust, forged or otherwise.
	current, err := h.store.Verdicts().Current(ctx, ideaID)
	switch {
	case err == nil:
		// fall through to the viability check below.
	case errors.Is(err, pgx.ErrNoRows):
		renderErr("this idea has no viability verdict yet -- a viable verdict is required to propose a video script")
		return
	default:
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if current.Verdict != store.VerdictViable {
		renderErr("this idea's current verdict is not viable -- a viable verdict is required to propose a video script")
		return
	}

	strategyID, err := uuid.Parse(form.StrategyID)
	if err != nil {
		renderErr("invalid strategy selection")
		return
	}

	title := strings.TrimSpace(form.Title)
	if title == "" {
		renderErr("title is required")
		return
	}

	scriptText := strings.TrimSpace(form.ScriptText)
	if scriptText == "" {
		renderErr("script text is required")
		return
	}

	var targetPublishDate *time.Time
	if form.TargetPublishDate != "" {
		parsed, err := time.Parse("2006-01-02", form.TargetPublishDate)
		if err != nil {
			renderErr("invalid target publish date")
			return
		}
		targetPublishDate = &parsed
	}

	_, err = h.store.VideoScripts().Propose(ctx, store.ProposeVideoScriptInput{
		ChannelID:         channelID,
		VerdictID:         current.ID,
		StrategyID:        strategyID,
		Title:             title,
		ScriptText:        scriptText,
		TargetPublishDate: targetPublishDate,
		CreatedByPersonID: person.ID,
		IdempotencyKey:    form.IdempotencyKey,
	})
	if err != nil {
		// store.ErrVerdictNotViable (NFR3's stale-at-submit case: the
		// verdict version resolved above changed between resolution and
		// Propose's own re-check inside its transaction -- vanishingly
		// unlikely given both happen in this one request, but handled
		// identically to the up-front check for defense in depth) and
		// store.ErrStrategyNotFound (NFR3's deactivated/deleted-Strategy
		// case) both re-render with a form error, never a 500; any other
		// error re-renders with the store's own message, mirroring
		// HandleSaveVerdict's renderErr(err.Error()) convention.
		renderErr(err.Error())
		return
	}

	http.Redirect(w, r, "/channels/"+channelID.String()+"/schedule", http.StatusSeeOther)
}

// verdictAuthorDisplayNames resolves each distinct AuthorPersonID across
// history (and current, when non-nil) to its Person.DisplayName, one
// lookup per distinct author regardless of how many verdict versions they
// authored. Verdict itself carries no AuthorDisplayName field (unlike
// ResearchNoteWithAuthor, which is already joined) -- views.templ reads
// this map by AuthorPersonID rather than re-querying per row.
func (h *Handlers) verdictAuthorDisplayNames(ctx context.Context, history []store.Verdict, current *store.Verdict) (map[uuid.UUID]string, error) {
	names := make(map[uuid.UUID]string, len(history)+1)
	resolve := func(id uuid.UUID) error {
		if _, ok := names[id]; ok {
			return nil
		}
		p, err := h.store.Persons().GetByID(ctx, id)
		if err != nil {
			return fmt.Errorf("load verdict author %s: %w", id, err)
		}
		names[id] = p.DisplayName
		return nil
	}
	for _, v := range history {
		if err := resolve(v.AuthorPersonID); err != nil {
			return nil, err
		}
	}
	if current != nil {
		if err := resolve(current.AuthorPersonID); err != nil {
			return nil, err
		}
	}
	return names, nil
}

// citedResearchNotes resolves the UNION of every CitedResearchNoteIDs
// across history (and current, when non-nil) to its store.ResearchNote in
// ONE batched store.ResearchStore.GetByIDs call (FR9, FR16/NFR2) -- never
// one GetByID call per citation per verdict version, so an Idea with a
// long verdict history citing many notes still issues a single query.
// views.templ's citedNoteBody reads this map by id; an id absent from it
// (should not happen -- see store.ResearchStore.GetByIDs's doc comment)
// renders as a benign "note unavailable" marker rather than panicking.
func (h *Handlers) citedResearchNotes(ctx context.Context, history []store.Verdict, current *store.Verdict) (map[uuid.UUID]store.ResearchNote, error) {
	var ids []uuid.UUID
	for _, v := range history {
		ids = append(ids, v.CitedResearchNoteIDs...)
	}
	if current != nil {
		ids = append(ids, current.CitedResearchNoteIDs...)
	}
	notes, err := h.store.Research().GetByIDs(ctx, ids)
	if err != nil {
		return nil, fmt.Errorf("load cited research notes: %w", err)
	}
	return notes, nil
}
