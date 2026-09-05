// Package research is `web`'s read-only Loop 1 browse surface (milestone
// M4.1, FR1/FR2/FR8/FR9/FR10, issue #1899): a Channel-scoped research
// index (every Idea with its note count and verdict presence, plus a
// separate section for research notes that predate any Idea, M1 FR9) and
// a per-Idea detail page (that Idea's research notes, most-recent first,
// and its full viability-verdict history). Handlers and templ views live
// together in this one package, mirroring web/schedule.Handlers' package
// doc comment rationale: the read flow and its two views are tightly
// coupled with no reuse outside this package.
//
// This task ships no POST route and no form anywhere in this package --
// FR3/FR4/FR6/FR7's save forms land on these same pages in follow-up
// tasks, dropped into the page structure built here without
// restructuring it.
//
// Authorization (NFR2, NFR5): both routes are visible to a Channel's
// Founder, Co-Creator, AND Analyst (store.CanRead) -- mirrors
// web/schedule.Handlers.HandleList's read gate exactly, since Loop 1
// browse carries the same three-tier read visibility as schedule
// approval's read side. Every authorization decision is re-derived fresh
// from the {id} path segment on every call, never cached or trusted from
// the session alone.
//
// Routes (mounted by ../main.go's setupRoutes, behind
// web/auth.Authenticator.RequireSignedIn):
//
//   - GET /channels/{id}/research -- HandleChannelIndex (FR1).
//   - GET /channels/{id}/research/ideas/{ideaID} -- HandleIdeaDetail
//     (FR2, FR9, FR10).
package research

import (
	"context"
	"errors"
	"fmt"
	"net/http"

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
// store.CanRead and Ideas()/Research()/Verdicts()/Channels()/Roles()/
// Persons()).
type Handlers struct {
	store *store.Store
}

// New wires st into a Handlers.
func New(st *store.Store) *Handlers {
	return &Handlers{store: st}
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
	notes, notesTruncated, err := h.store.Research().ListFiltered(ctx, channelID, nil, nil, nil, nil, defaultPageLimit)
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

	title := ch.Title + " research"
	data := components.LayoutData{Title: title, User: person}
	if err := components.Render(w, r, title, ChannelIndex(data, ch, ideas, unattached, ideasTruncated, notesTruncated)); err != nil {
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

	notes, notesTruncated, err := h.store.Research().ListFiltered(ctx, channelID, &ideaID, nil, nil, nil, defaultPageLimit)
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

	title := idea.Title
	data := components.LayoutData{Title: title, User: person}
	if err := components.Render(w, r, title, IdeaDetail(data, ch, idea, notes, notesTruncated, current, history, authorNames)); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
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
