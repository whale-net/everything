// Package schedule is `web`'s Creator-only schedule approval surface (C8:
// FR19, FR20) -- GET /channels/{id}/schedule, POST /schedule/{entryID}/
// approve, POST /schedule/{entryID}/unapprove, and POST /schedule/{entryID}/
// edit, all mounted behind web/auth.Authenticator.RequireSignedIn (see
// ../main.go's setupRoutes). Handlers and templ views live together in
// this one package, mirroring web/invite's package doc comment rationale:
// the mutate-then-redirect flow and its one list view are tightly coupled
// with no reuse outside this package.
//
// Authorization (NFR5, LB2): GET is visible to a Channel's Creator AND
// Analyst (store.CanRead); the three mutating POST routes are Creator
// only, each re-deriving authority from a fresh store.CanApprove call --
// never from the session alone, a cached field, or the client's choice of
// which button to render. The Analyst list view renders with no approve/
// un-approve/edit affordances at all (see views.templ's List/entryActions),
// but that omission is presentation only -- the server-side CanApprove
// check in mutate below is what actually rejects a forged POST from an
// Analyst, so hiding the button is never the only line of defense.
//
// The published freeze (FR20): store.ScheduleStore.IsPublished is the
// single, reusable "recorded as published" predicate (see
// store/schedule.go's package doc comment), consulted -- atomically with
// the mutation itself -- inside Approve/Unapprove/Update. mutate below
// translates store.ErrScheduleEntryPublished to 409 with no state change;
// views.templ additionally calls IsPublished (via ScheduleEntryDetail.
// Published) to omit the un-approve/edit affordances from the rendered
// page once true, so a Creator is never shown a button that would just
// error.
package schedule

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/whale-net/everything/audience_score_system/store"
	"github.com/whale-net/everything/audience_score_system/web/auth"
	"github.com/whale-net/everything/audience_score_system/web/components"
)

// proposedPublishAtInputFormat matches HTML's <input type="datetime-local">
// value format ("2006-01-02T15:04"), interpreted as UTC -- mirrors
// tools/app_registry/ui/pages/deployments.go's asOfInputFormat convention.
const proposedPublishAtInputFormat = "2006-01-02T15:04"

// Handlers holds the dependencies schedule's routes need: the Store (for
// store.CanRead/store.CanApprove and Schedules()/Roles()/Channels()).
type Handlers struct {
	store *store.Store
}

// New wires st into a Handlers.
func New(st *store.Store) *Handlers {
	return &Handlers{store: st}
}

// HandleList serves GET /channels/{id}/schedule (FR19/FR20's read side).
// Visible to Creator and Analyst (store.CanRead) -- canApprove additionally
// gates whether the rendered view includes the approve/un-approve/edit
// affordances (views.templ's entryActions), never whether the schedule
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

	canRead, err := store.CanRead(ctx, h.store.Roles(), channelID, person.ID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if !canRead {
		http.Error(w, "forbidden", http.StatusForbidden)
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

	// canApprove is Creator-only (store.CanApprove, NFR5) -- it gates the
	// rendered affordances, not read access to the page itself (canRead
	// above already covers both Creator and Analyst).
	canApprove, err := store.CanApprove(ctx, h.store.Roles(), channelID, person.ID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	entries, err := h.store.Schedules().ListDetailByChannel(ctx, channelID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	title := ch.Title + " schedule"
	data := components.LayoutData{Title: title, User: person}
	if err := components.Render(w, r, title, List(data, ch, entries, canApprove)); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

// HandleApprove serves POST /schedule/{entryID}/approve (FR19).
func (h *Handlers) HandleApprove(w http.ResponseWriter, r *http.Request) {
	h.mutate(w, r, func(ctx context.Context, entryID, personID uuid.UUID) error {
		return h.store.Schedules().Approve(ctx, entryID, personID)
	})
}

// HandleUnapprove serves POST /schedule/{entryID}/unapprove (FR20).
func (h *Handlers) HandleUnapprove(w http.ResponseWriter, r *http.Request) {
	h.mutate(w, r, func(ctx context.Context, entryID, personID uuid.UUID) error {
		return h.store.Schedules().Unapprove(ctx, entryID, personID)
	})
}

// HandleEdit serves POST /schedule/{entryID}/edit (FR20): edits a draft
// entry's proposed_publish_at. The new value is read from the
// "proposed_publish_at" form field before mutate's authorization check
// runs, so a malformed value 400s without ever touching the store --
// mutate's CanApprove gate still runs first for a well-formed one, same as
// HandleApprove/HandleUnapprove.
func (h *Handlers) HandleEdit(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "invalid form", http.StatusBadRequest)
		return
	}
	proposedAt, err := parseProposedPublishAt(r.FormValue("proposed_publish_at"))
	if err != nil {
		http.Error(w, "invalid proposed_publish_at: "+err.Error(), http.StatusBadRequest)
		return
	}

	h.mutate(w, r, func(ctx context.Context, entryID, _ uuid.UUID) error {
		return h.store.Schedules().Update(ctx, entryID, proposedAt)
	})
}

// mutate is HandleApprove/HandleUnapprove/HandleEdit's shared body: parse
// entryID from the path, load the entry (to learn its ChannelID and
// confirm it exists), require store.CanApprove for that Channel (Creator
// only, NFR5 -- re-derived fresh on every call, never trusted from the
// session or the client), run fn, and translate the result: fn succeeding
// redirects back to the schedule page (303); store.ErrScheduleEntryPublished
// (FR20's freeze) and any other error from fn (entry not in the state fn
// requires, e.g. approving an already-committed entry or un-approving a
// still-draft one) both 409 with no further state change, since entryID
// is already confirmed to exist by the GetByID call above.
func (h *Handlers) mutate(w http.ResponseWriter, r *http.Request, fn func(ctx context.Context, entryID, personID uuid.UUID) error) {
	ctx := r.Context()
	person := auth.PersonFromContext(ctx)
	if person == nil {
		http.Error(w, "not signed in", http.StatusUnauthorized)
		return
	}

	entryID, err := uuid.Parse(r.PathValue("entryID"))
	if err != nil {
		http.Error(w, "invalid schedule entry id", http.StatusBadRequest)
		return
	}

	entry, err := h.store.Schedules().GetByID(ctx, entryID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			http.NotFound(w, r)
			return
		}
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	canApprove, err := store.CanApprove(ctx, h.store.Roles(), entry.ChannelID, person.ID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if !canApprove {
		http.Error(w, "forbidden: only a Channel's creator may change its schedule", http.StatusForbidden)
		return
	}

	if err := fn(ctx, entryID, person.ID); err != nil {
		if errors.Is(err, store.ErrScheduleEntryPublished) {
			http.Error(w, "cannot change: the corresponding video has already been published", http.StatusConflict)
			return
		}
		// Any other error here means fn's own state precondition failed
		// (e.g. Approve requires draft, Unapprove requires committed,
		// Update requires draft) -- entryID itself is already confirmed to
		// exist by GetByID above, so this is always a state conflict, not
		// a missing resource.
		http.Error(w, err.Error(), http.StatusConflict)
		return
	}

	http.Redirect(w, r, "/channels/"+entry.ChannelID.String()+"/schedule", http.StatusSeeOther)
}

// parseProposedPublishAt parses the "proposed_publish_at" form field.
// Accepts RFC3339 (in case a future non-HTML caller sends a
// timezone-qualified value) and falls back to HTML's
// <input type="datetime-local"> layout, interpreted as UTC (see
// proposedPublishAtInputFormat's doc comment) -- the layout List's edit
// form (views.templ) actually submits.
func parseProposedPublishAt(raw string) (time.Time, error) {
	if raw == "" {
		return time.Time{}, fmt.Errorf("proposed_publish_at is required")
	}
	if t, err := time.Parse(time.RFC3339, raw); err == nil {
		return t, nil
	}
	if t, err := time.Parse(proposedPublishAtInputFormat, raw); err == nil {
		return t.UTC(), nil
	}
	return time.Time{}, fmt.Errorf("unrecognized time format %q", raw)
}
