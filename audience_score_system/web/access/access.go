// Package access is `web`'s Founder/Co-Creator-only access-management
// surface (M2, FR30/FR31/FR33) -- GET /channels/{id}/access (the roster)
// and the three POSTs that mutate it: /invites (invite a Co-Creator,
// FR30), /promote (promote an Analyst to Co-Creator, FR31), and /remove
// (revoke an open role, FR33). Handlers and templ views live together in
// this one package, mirroring web/invite and web/schedule's package doc
// comment rationale: the mutate-then-redirect flow and its one roster view
// are tightly coupled with no reuse outside this package.
//
// Every route sits behind web/auth.Authenticator.RequireSignedIn (see
// ../main.go's setupRoutes); every handler will re-derive authorization
// from a fresh store.Can* call every time it runs -- never from the
// session alone, a cached field, or which button the client happened to
// POST to (NFR5/NFR6) -- once Implementation wires in the real logic.
// Every handler below is a scaffold stub (StatusNotImplemented) until
// then, same scaffold/feat split store/access.go itself followed
// (ErrAccessNotImplemented).
package access

import (
	"net/http"

	"github.com/whale-net/everything/audience_score_system/store"
)

// Handlers holds the dependency access's routes need: the Store (for
// store.CanInvite/store.CanRemove and Access()/Roles()/Channels()/
// Invites()).
type Handlers struct {
	store *store.Store
}

// New wires st into a Handlers.
func New(st *store.Store) *Handlers {
	return &Handlers{store: st}
}

// HandleShow will serve GET /channels/{id}/access (FR30/FR31/FR33's read
// side) -- roster + row-level Remove/Promote affordances. Scaffold stub.
func (h *Handlers) HandleShow(w http.ResponseWriter, r *http.Request) {
	http.Error(w, "not implemented", http.StatusNotImplemented)
}

// HandleInviteCoCreator will serve POST /channels/{id}/access/invites
// (FR30) -- generates (or returns the existing live) Co-Creator invite
// code. Scaffold stub.
func (h *Handlers) HandleInviteCoCreator(w http.ResponseWriter, r *http.Request) {
	http.Error(w, "not implemented", http.StatusNotImplemented)
}

// HandlePromote will serve POST /channels/{id}/access/promote (FR31),
// form field "person_id" -- promotes an Analyst to Co-Creator. Scaffold
// stub.
func (h *Handlers) HandlePromote(w http.ResponseWriter, r *http.Request) {
	http.Error(w, "not implemented", http.StatusNotImplemented)
}

// HandleRemove will serve POST /channels/{id}/access/remove (FR33), form
// field "person_id" -- revokes an open role per store.CanRemove's matrix.
// Scaffold stub.
func (h *Handlers) HandleRemove(w http.ResponseWriter, r *http.Request) {
	http.Error(w, "not implemented", http.StatusNotImplemented)
}
