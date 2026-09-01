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
//
// Scaffold only (issue #1572): every handler below is a stub returning
// "not implemented" except the plain field assignment in New. Real
// store.CanInvite/InviteStore wiring, the pending-code redirect, and the
// accept/decline/resume state changes are filled in during the
// Implementation phase.
package invite

import (
	"net/http"

	"github.com/whale-net/everything/audience_score_system/store"
	"github.com/whale-net/everything/audience_score_system/web/auth"
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
//
// Stub only -- filled in during the Implementation phase.
func (h *Handlers) HandleGenerate(w http.ResponseWriter, r *http.Request) {
	http.Error(w, "not implemented", http.StatusNotImplemented)
}

// HandleShow serves GET /invites/{code} -- public, no session required
// (FR6/FR7/FR8). See the package doc comment's route list for the three
// outcomes (Invalid / redirect-to-sign-in / AcceptPrompt).
//
// Stub only -- filled in during the Implementation phase.
func (h *Handlers) HandleShow(w http.ResponseWriter, r *http.Request) {
	http.Error(w, "not implemented", http.StatusNotImplemented)
}

// HandleResume serves GET /invites/{code}/resume -- signed-in only
// (wrapped in auth.RequireSignedIn by main.go). The new-visitor path's
// landing point after completing Google sign-in from HandleShow's
// redirect (FR6): consumes the code for the now-resolved Person with no
// further click required, then redirects to the Channel.
//
// Stub only -- filled in during the Implementation phase.
func (h *Handlers) HandleResume(w http.ResponseWriter, r *http.Request) {
	http.Error(w, "not implemented", http.StatusNotImplemented)
}

// HandleAccept serves POST /invites/{code}/accept -- signed-in only
// (FR7). store.InviteStore.Consume(code, person) in one transaction: sets
// consumed_at/consumed_by_person_id and grants role=analyst. Redirects to
// the Channel on success.
//
// Stub only -- filled in during the Implementation phase.
func (h *Handlers) HandleAccept(w http.ResponseWriter, r *http.Request) {
	http.Error(w, "not implemented", http.StatusNotImplemented)
}

// HandleDecline serves POST /invites/{code}/decline -- signed-in only
// (FR7). Creates no association and leaves the code unconsumed and still
// live, so it can be redeemed later or by someone else.
//
// Stub only -- filled in during the Implementation phase.
func (h *Handlers) HandleDecline(w http.ResponseWriter, r *http.Request) {
	http.Error(w, "not implemented", http.StatusNotImplemented)
}
