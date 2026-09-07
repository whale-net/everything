// Package schedule is `web`'s Creator-tier video_script approval surface
// (milestone video-script-model, FR48/FR49, issue #1834) -- GET
// /channels/{id}/scripts, POST /scripts/{scriptID}/approve, POST
// /scripts/{scriptID}/deny, and POST /scripts/{scriptID}/archive (FR20/
// FR22, issue #2030 -- renamed from /schedule to match the video_script
// model; package name and handler names are unchanged), all
// mounted behind web/auth.Authenticator.RequireSignedIn (see ../main.go's
// setupRoutes). Handlers and templ views live together in this one
// package, mirroring web/invite's package doc comment rationale: the
// mutate-then-redirect flow and its one list view are tightly coupled with
// no reuse outside this package.
//
// This is a rebuild in place of C8's original schedule_entry-backed
// surface (FR19/FR20) against `video_script` (migration 010, #1824):
// route paths and the package name are unchanged (FR49's route-and-
// package-naming note -- {scriptID} describes the path parameter's new
// referent, not a route rename), but the handlers, store calls, and
// rendered affordances now speak video_script's propose/greenlit/denied/
// archived lifecycle (FR36-FR40) instead of schedule_entry's draft/
// committed pair. HandleEdit and HandleUnapprove have no analog and are
// retired outright (FR49): a video_script's target date is set once at
// propose time (FR36, no web edit surface), and FR40 defines no
// greenlit->proposed transition.
//
// Authorization (NFR12, NFR13, NFR5): GET is visible to a Channel's
// Founder, Co-Creator, AND Analyst (store.CanRead); the three mutating
// POST routes require Creator-tier authority -- Founder or Co-Creator,
// symmetrically (FR32) -- each re-deriving authority from a fresh
// store.CanApprove call -- never from the session alone, a cached field,
// or the client's choice of which button to render. The Analyst list view
// renders with no greenlight/deny/archive affordances at all (see
// views.templ's List/entryActions), but that omission is presentation
// only -- the server-side CanApprove check in mutate below is what
// actually rejects a forged POST from an Analyst, so hiding the button is
// never the only line of defense.
//
// The published freeze (FR39): store.VideoScriptStore.IsPublished is the
// single, reusable "recorded as published" predicate (see
// store/video_script.go's package doc comment), consulted -- atomically
// with the mutation itself -- inside Archive. mutate below translates
// store.ErrVideoScriptPublished to 409 with no state change; views.templ
// additionally calls IsPublished (via VideoScriptDetail.Published) to
// omit the archive affordance from the rendered page once true, so a
// Founder or Co-Creator is never shown a button that would just error.
//
// #2036 (FR13-FR15, FR21, plus the create half of FR18/FR19) adds a
// video-script AUTHORING surface to this SAME package rather than a new
// sibling: GET /channels/{id}/scripts/new (create form), POST
// /channels/{id}/scripts (create submit), and GET
// /channels/{id}/scripts/{scriptID} (detail/authoring page) -- see
// create.go and markdown.go. This package already owns
// /channels/{id}/scripts (HandleList above), and the issue is explicit
// that the create form and the list must not be split across two
// packages that both own that path family. Authoring reuses
// store.VideoScriptStore.Propose and store.CanWrite as-is (LB5/NFR1,
// identical to save_video_script's MCP path and
// web/research.Handlers.HandleProposeVideoScript's existing Idea-page
// entry point) -- no new store method. store.CanWrite (Creator-or-
// Analyst) gates the create POST; store.CanApprove (Creator-tier only,
// used by mutate below) is untouched and unrelated -- proposing content
// and greenlighting/denying/archiving it remain two different authority
// tiers. Script editing (FR16/FR17) is a deliberately separate follow-on
// task; create.go's routes are create/detail only.
package schedule

import (
	"context"
	"errors"
	"net/http"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/whale-net/everything/audience_score_system/store"
	"github.com/whale-net/everything/audience_score_system/web/auth"
	"github.com/whale-net/everything/audience_score_system/web/components"
)

// Handlers holds the dependencies schedule's routes need: the Store (for
// store.CanRead/store.CanApprove and VideoScripts()/Roles()/Channels()).
type Handlers struct {
	store *store.Store
}

// New wires st into a Handlers.
func New(st *store.Store) *Handlers {
	return &Handlers{store: st}
}

// HandleList serves GET /channels/{id}/scripts (FR48's read side).
// Visible to Founder, Co-Creator, and Analyst (store.CanRead) -- canApprove additionally
// gates whether the rendered view includes the greenlight/deny/archive
// affordances (views.templ's entryActions), never whether the scripts list
// itself is visible.
func (h *Handlers) HandleList(w http.ResponseWriter, r *http.Request) {
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

	// Re-authorize strictly by this Channel's ID (store.CanRead) only after
	// confirming it exists -- mirrors handleChannelDetail (web/main.go):
	// GetByID first (404 for an unknown Channel), CanRead second (403 for a
	// known Channel the caller can't read), so an unknown ID never leaks as
	// 403 before the existence check runs.
	canRead, err := store.CanRead(ctx, h.store.Roles(), channelID, person.ID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if !canRead {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}

	// canApprove is Creator-tier (store.CanApprove -- Founder or
	// Co-Creator, symmetrically per FR32, NFR5) -- it gates the rendered
	// affordances, not read access to the page itself (canRead above
	// already covers Founder, Co-Creator, and Analyst).
	canApprove, err := store.CanApprove(ctx, h.store.Roles(), channelID, person.ID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	scripts, err := h.store.VideoScripts().ListDetailByChannel(ctx, channelID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	title := ch.Title + " scripts"
	data := components.LayoutData{Title: title, User: person}
	if err := components.Render(w, r, title, List(data, ch, scripts, canApprove)); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

// HandleGreenlight serves POST /scripts/{scriptID}/approve (FR49, FR37).
// The route path keeps its pre-existing "approve" spelling (FR49's
// route-and-package-naming note); the store transition it drives is
// video_script's proposed->greenlit.
func (h *Handlers) HandleGreenlight(w http.ResponseWriter, r *http.Request) {
	h.mutate(w, r, func(ctx context.Context, scriptID, personID uuid.UUID) error {
		return h.store.VideoScripts().Greenlight(ctx, scriptID, personID)
	})
}

// HandleDeny serves POST /scripts/{scriptID}/deny (FR49, FR38):
// video_script's proposed->denied transition.
func (h *Handlers) HandleDeny(w http.ResponseWriter, r *http.Request) {
	h.mutate(w, r, func(ctx context.Context, scriptID, personID uuid.UUID) error {
		return h.store.VideoScripts().Deny(ctx, scriptID, personID)
	})
}

// HandleArchive serves POST /scripts/{scriptID}/archive (FR49, FR39):
// video_script's greenlit->archived transition, frozen once the script's
// matched video has published (see mutate's ErrVideoScriptPublished
// handling below).
func (h *Handlers) HandleArchive(w http.ResponseWriter, r *http.Request) {
	h.mutate(w, r, func(ctx context.Context, scriptID, personID uuid.UUID) error {
		return h.store.VideoScripts().Archive(ctx, scriptID, personID)
	})
}

// mutate is HandleGreenlight/HandleDeny/HandleArchive's shared body: parse
// scriptID from the path, load the script (to learn its ChannelID and
// confirm it exists), require store.CanApprove for that Channel
// (Creator-tier -- Founder or Co-Creator, symmetrically per FR32, NFR5 --
// re-derived fresh on every call, never trusted from the session or the
// client), run fn, and translate the result: fn succeeding redirects back
// to the scripts page (303); store.ErrVideoScriptPublished (FR39's
// freeze) maps to 409 specifically, and any other error from fn (an
// invalid transition per FR40 -- e.g. greenlighting an already-decided
// script, or archiving one that is not greenlit) also 409s with no
// further state change, since scriptID is already confirmed to exist by
// the GetByID call above.
func (h *Handlers) mutate(w http.ResponseWriter, r *http.Request, fn func(ctx context.Context, scriptID, personID uuid.UUID) error) {
	ctx := r.Context()
	person := auth.PersonFromContext(ctx)
	if person == nil {
		http.Error(w, "not signed in", http.StatusUnauthorized)
		return
	}

	scriptID, err := uuid.Parse(r.PathValue("scriptID"))
	if err != nil {
		http.Error(w, "invalid video script id", http.StatusBadRequest)
		return
	}

	script, err := h.store.VideoScripts().GetByID(ctx, scriptID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			http.NotFound(w, r)
			return
		}
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	canApprove, err := store.CanApprove(ctx, h.store.Roles(), script.ChannelID, person.ID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if !canApprove {
		http.Error(w, "forbidden: only a Channel's Founder or Co-Creator may change its scripts", http.StatusForbidden)
		return
	}

	if err := fn(ctx, scriptID, person.ID); err != nil {
		if errors.Is(err, store.ErrVideoScriptPublished) {
			http.Error(w, "cannot change: the corresponding video has already been published", http.StatusConflict)
			return
		}
		// Any other error here means fn's own state precondition failed
		// (store.ErrVideoScriptTransition, FR40 -- e.g. greenlighting or
		// denying a script that is not proposed, or archiving one that is
		// not greenlit) -- scriptID itself is already confirmed to exist
		// by GetByID above, so this is always a state conflict, not a
		// missing resource.
		http.Error(w, err.Error(), http.StatusConflict)
		return
	}

	http.Redirect(w, r, "/channels/"+script.ChannelID.String()+"/scripts", http.StatusSeeOther)
}
