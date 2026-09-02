// Package invite is `web`'s Analyst invite generate/accept/decline flow
// (C3: FR5-FR8) -- a Creator generates a single-use code for a Channel;
// the Analyst redeems it out of band (no email delivery, permanently out
// of scope for M1, see this task's Summary). Handlers and templ views
// live together in this one package (rather than split across a
// components/pages sibling pair like web/pages, per this task's Scaffold
// section) because the flow's redirect/redemption logic and its three
// small views are tightly coupled and have no reuse outside this package.
//
// Routes (all mounted on `web` by main.go's setupRoutes):
//
//   - POST /channels/{id}/invites -- Creator only (store.CanInvite), calls
//     store.InviteStore.Generate (FR5).
//   - GET /invites/{code} -- public, no session required. Consumed or
//     invalidated code renders the terminal Invalid view with no state
//     change (FR8). A valid code with no session redirects into the C1
//     Google sign-in flow via `next`, landing back on HandleResume rather
//     than this handler on return (FR6). A valid code with a session
//     renders AcceptPrompt (FR7).
//   - GET /invites/{code}/resume -- signed-in only (RequireSignedIn). The
//     new-visitor half of FR6: the sole redirect target `next` ever points
//     at for this flow, so only a caller who just completed sign-in
//     through HandleShow's redirect reaches here -- an already-signed-in
//     Person who visits /invites/{code} directly always lands on
//     HandleShow's explicit accept/decline prompt (FR7) instead. Consumes
//     the code immediately, no extra click.
//   - POST /invites/{code}/accept -- signed-in only. store.InviteStore.
//     Consume (FR7).
//   - POST /invites/{code}/decline -- signed-in only. No state change:
//     leaves the code unconsumed and live (FR7).
package invite

import (
	"context"
	"errors"
	"net/http"
	"net/url"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/whale-net/everything/audience_score_system/store"
	"github.com/whale-net/everything/audience_score_system/web/auth"
	"github.com/whale-net/everything/audience_score_system/web/components"
)

// Handlers holds the dependencies invite's routes need: the Store (for
// store.CanInvite and Invites()/Roles()/Channels()/Persons()) and the
// SessionManager (to resolve an *optional* signed-in Person on the public
// GET /invites/{code} route, where web/auth.RequireSignedIn's unconditional
// redirect-to-/login would be wrong -- this route must render differently
// for a signed-in vs. anonymous caller, never redirect an anonymous one
// away from the invite view itself).
type Handlers struct {
	store    *store.Store
	sessions *auth.SessionManager
}

// New wires store and sessions into a Handlers.
func New(st *store.Store, sessions *auth.SessionManager) *Handlers {
	return &Handlers{store: st, sessions: sessions}
}

// HandleGenerate serves POST /channels/{id}/invites (FR5). Creator only --
// store.CanInvite(channelID, person) must hold, else 403 -- and it never
// invalidates a live code without also handing back the new one, since
// store.InviteStore.Generate does both atomically.
func (h *Handlers) HandleGenerate(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	person := auth.PersonFromContext(ctx)
	if person == nil {
		// RequireSignedIn (main.go) always resolves a Person before this
		// handler runs; nil here means it was called incorrectly.
		http.Error(w, "not signed in", http.StatusUnauthorized)
		return
	}

	channelID, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		http.Error(w, "invalid channel id", http.StatusBadRequest)
		return
	}

	can, err := store.CanInvite(ctx, h.store.Roles(), channelID, person.ID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if !can {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}

	inv, err := h.store.Invites().Generate(ctx, channelID, person.ID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	data := components.LayoutData{Title: "Invite code generated", User: person}
	if err := components.Render(w, r, "Invite code generated", GenerateResult(data, inv.Code)); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

// HandleShow serves GET /invites/{code} -- public, no session required
// (FR6/FR7/FR8). See the package doc comment's route list for the three
// outcomes (Invalid / redirect-to-sign-in / AcceptPrompt).
func (h *Handlers) HandleShow(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	code := r.PathValue("code")

	inv, err := h.store.Invites().Lookup(ctx, code)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			h.renderInvalid(w, r)
			return
		}
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if inv.ConsumedAt != nil || inv.InvalidatedAt != nil {
		// FR8: no Channel association is created and no state changes as a
		// result of rendering this view.
		h.renderInvalid(w, r)
		return
	}

	person, err := h.optionalSignedInPerson(ctx, r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if person == nil {
		// FR6: no session -- stash the code as the post-sign-in redirect
		// target (`next`, resolved by web/auth.SessionManager.SetOAuthState/
		// GetNextURL, which are themselves bound to the OAuth `state` --
		// see auth.HandleLogin/HandleCallback) and send the caller into the
		// C1 Google sign-in flow. On return, HandleResume (not this
		// handler) completes redemption.
		next := "/invites/" + url.PathEscape(code) + "/resume"
		http.Redirect(w, r, "/login?next="+url.QueryEscape(next), http.StatusFound)
		return
	}

	channel, err := h.store.Channels().GetByID(ctx, inv.ChannelID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	data := components.LayoutData{Title: "Join channel", User: person}
	if err := components.Render(w, r, "Join channel", AcceptPrompt(data, channel.Title, code)); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

// HandleResume serves GET /invites/{code}/resume -- signed-in only
// (wrapped in auth.RequireSignedIn by main.go). The new-visitor path's
// landing point after completing Google sign-in from HandleShow's
// redirect (FR6): consumes the code for the now-resolved Person with no
// further click required, then redirects to the Channel.
func (h *Handlers) HandleResume(w http.ResponseWriter, r *http.Request) {
	h.consumeAndRedirect(w, r)
}

// HandleAccept serves POST /invites/{code}/accept -- signed-in only
// (FR7). store.InviteStore.Consume(code, person) in one transaction: sets
// consumed_at/consumed_by_person_id and grants role=analyst. Redirects to
// the Channel on success.
func (h *Handlers) HandleAccept(w http.ResponseWriter, r *http.Request) {
	h.consumeAndRedirect(w, r)
}

// consumeAndRedirect is HandleResume/HandleAccept's shared body: both are
// signed-in-only routes that consume the code and land on the same
// success/failure outcomes (the new-visitor path just skips the explicit
// accept click AcceptPrompt would otherwise require).
func (h *Handlers) consumeAndRedirect(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	code := r.PathValue("code")
	person := auth.PersonFromContext(ctx)
	if person == nil {
		http.Error(w, "not signed in", http.StatusUnauthorized)
		return
	}

	err := h.store.Invites().Consume(ctx, code, person.ID)
	if err != nil {
		if errors.Is(err, store.ErrInviteConsumed) || errors.Is(err, store.ErrInviteInvalidated) || errors.Is(err, pgx.ErrNoRows) {
			// FR8: terminal error, no association created. Covers the
			// concurrent-accept loser and an already-consumed/invalidated
			// code alike. A Person who already holds a live role on the
			// Channel (e.g. its Creator) is handled inside Consume's
			// addRoleTx (SCD2 close-and-open on (channel_id, person_id)) --
			// no duplicate row, no 500.
			h.renderInvalid(w, r)
			return
		}
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	http.Redirect(w, r, "/", http.StatusSeeOther)
}

// HandleDecline serves POST /invites/{code}/decline -- signed-in only
// (FR7). Creates no association and leaves the code unconsumed and still
// live, so it can be redeemed later or by someone else.
func (h *Handlers) HandleDecline(w http.ResponseWriter, r *http.Request) {
	person := auth.PersonFromContext(r.Context())
	if person == nil {
		http.Error(w, "not signed in", http.StatusUnauthorized)
		return
	}
	// No store call at all: declining is defined entirely by the absence
	// of a state change (see package doc comment).
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

// optionalSignedInPerson resolves the caller's session cookie to a
// store.Person WITHOUT redirecting when absent -- unlike
// web/auth.Authenticator.RequireSignedIn, which always redirects to
// /login, since HandleShow (the only caller) must render a different view
// for an anonymous caller rather than send them away from the invite link.
// Returns (nil, nil) for "no valid session", distinct from a real error.
func (h *Handlers) optionalSignedInPerson(ctx context.Context, r *http.Request) (*store.Person, error) {
	personIDStr, err := h.sessions.PersonID(r)
	if err != nil {
		return nil, nil
	}

	personID, err := uuid.Parse(personIDStr)
	if err != nil {
		return nil, nil
	}

	person, err := h.store.Persons().GetByID(ctx, personID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	return &person, nil
}

// renderInvalid renders the terminal Invalid view (FR8). User is best-effort
// (nil is fine -- Invalid never reads it beyond the shared Layout chrome).
func (h *Handlers) renderInvalid(w http.ResponseWriter, r *http.Request) {
	person, _ := h.optionalSignedInPerson(r.Context(), r)
	data := components.LayoutData{Title: "Invite no longer valid", User: person}
	if err := components.Render(w, r, "Invite no longer valid", Invalid(data)); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}
