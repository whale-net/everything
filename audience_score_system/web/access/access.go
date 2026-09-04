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
// ../main.go's setupRoutes); every handler additionally re-derives
// authorization from a fresh store.Can* call every time it runs -- never
// from the session alone, a cached field, or which button the client
// happened to POST to (NFR5/NFR6, mirroring web/schedule's package doc
// comment rationale for why hiding a button is never the only line of
// defense).
//
// Page gate (HandleShow) and the coarse actor check every mutating
// handler starts with: store.CanInvite, which names the same tier set
// (Founder + Co-Creator) this whole page is scoped to -- see authz.go's
// package doc comment for why CanApprove/CanInvite/CanReconnect/CanRead/
// CanWrite/CanViewAudit all resolve to that same hasRole helper. This
// package standardizes on CanInvite rather than CanViewAudit (either
// names the identical tier set) purely for consistency with the
// pre-existing "invite Analyst" action on web/pages/channel_detail.templ.
//
// The removal matrix (FR33) is never re-implemented here: every Remove
// decision, both the row-level affordance in views.templ and
// HandleRemove's server-side check, goes through store.CanRemove
// (authz.go) -- the ONLY place that matrix exists in the codebase. Per
// that function's doc comment, CanRemove's `false, nil` return covers TWO
// distinct outcomes (a matrix-disallowed removal, and a target that
// already holds no open role -- FR33's idempotent no-op) that read
// identically from CanRemove alone; HandleRemove disambiguates them with
// one extra RoleStore.RolesFor(channelID, targetID) call, exactly as that
// doc comment describes.
package access

import (
	"errors"
	"net/http"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/whale-net/everything/audience_score_system/store"
	"github.com/whale-net/everything/audience_score_system/web/auth"
	"github.com/whale-net/everything/audience_score_system/web/components"
)

// auditTrailLimit caps the History panel (FR35) at the most recent N
// events, newest first -- the Implementation section's "documented
// default" so a Channel with years of role churn never renders
// unbounded. HandleShow fetches one extra row beyond this to detect
// truncation without a second COUNT query.
const auditTrailLimit = 50

// auditTrailView is what HandleShow hands views.templ for the History
// panel: at most auditTrailLimit entries (in AccessStore.AuditTrail's own
// newest-first order, never re-sorted or re-derived here) plus whether
// older history exists beyond that cap.
type auditTrailView struct {
	Entries   []store.AuditEvent
	Truncated bool
}

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

// rosterRow pairs one store.RosterEntry with this specific viewer's
// row-level affordances -- computed fresh on every GET, never cached or
// re-implemented in views.templ:
//
//   - CanRemove: store.CanRemove(ctx, roles, channelID, viewer.ID,
//     row.PersonID) -- FR33's matrix, the ONLY place it is evaluated.
//   - CanPromote: row.Role == store.RoleAnalyst -- FR31's "Promote to
//     Co-Creator" inline action only ever appears on an Analyst row.
type rosterRow struct {
	store.RosterEntry
	CanRemove  bool
	CanPromote bool
}

// HandleShow serves GET /channels/{id}/access (FR30/FR31/FR33's read
// side). Requires Founder or Co-Creator authority (store.CanInvite,
// naming the same tier set as store.CanViewAudit -- see package doc
// comment); an Analyst or non-member gets a flat 403, never a redirect or
// a partial page.
func (h *Handlers) HandleShow(w http.ResponseWriter, r *http.Request) {
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

	canManage, err := store.CanInvite(ctx, h.store.Roles(), channelID, person.ID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if !canManage {
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

	entries, err := h.store.Access().Roster(ctx, channelID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	rows := make([]rosterRow, 0, len(entries))
	for _, e := range entries {
		canRemove, err := store.CanRemove(ctx, h.store.Roles(), channelID, person.ID, e.PersonID)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		rows = append(rows, rosterRow{
			RosterEntry: e,
			CanRemove:   canRemove,
			CanPromote:  e.Role == store.RoleAnalyst,
		})
	}

	// Audit trail (FR35, #1727): gated by canManage above, which names
	// the identical Founder/Co-Creator tier set as store.CanViewAudit
	// (see this package's doc comment for why HandleShow standardizes on
	// CanInvite) -- an Analyst or non-member never reaches this point at
	// all, so the panel is never assembled, let alone rendered, for them.
	// One extra row beyond auditTrailLimit is fetched purely to detect
	// truncation without a second query.
	auditRows, err := h.store.Access().AuditTrail(ctx, channelID, auditTrailLimit+1)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	audit := auditTrailView{Entries: auditRows}
	if len(auditRows) > auditTrailLimit {
		audit.Truncated = true
		audit.Entries = auditRows[:auditTrailLimit]
	}

	data := components.LayoutData{Title: ch.Title + " access", User: person}
	if err := components.Render(w, r, ch.Title+" access", View(data, ch, rows, audit)); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

// HandleInviteCoCreator serves POST /channels/{id}/access/invites (FR30).
// Founder or Co-Creator only (store.CanInvite). store.InviteStore.Generate
// is idempotent per (Channel, tier) (LB4): a repeat call while a
// Co-Creator invite is already live returns that same code rather than
// minting a new one, and a live Analyst invite (if any) is entirely
// unaffected (NFR11) -- InviteResult's copy is written to be accurate in
// both the brand-new and already-live case without needing to detect
// which one happened, per this task's Implementation section ("say so
// plainly rather than implying a new code was minted"). Delivery is out
// of band: this only displays the code (NFR8 -- no email, no
// notification, anywhere in this task).
func (h *Handlers) HandleInviteCoCreator(w http.ResponseWriter, r *http.Request) {
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

	can, err := store.CanInvite(ctx, h.store.Roles(), channelID, person.ID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if !can {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}

	inv, err := h.store.Invites().Generate(ctx, channelID, person.ID, store.RoleCoCreator)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	data := components.LayoutData{Title: "Co-Creator invite", User: person}
	if err := components.Render(w, r, "Co-Creator invite", InviteResult(data, inv.Code, channelID.String())); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

// HandlePromote serves POST /channels/{id}/access/promote (FR31), form
// field "person_id". Founder or Co-Creator only (store.CanInvite). The
// target's CURRENT role on this Channel (RoleStore.RolesFor, never a
// value trusted from the form) decides the outcome:
//
//   - analyst: promotes via RoleStore.AddRole(..., RoleCoCreator,
//     grantedByPersonID=viewer.ID) -- the SCD2 close-and-open pattern,
//     attributing the grant to the viewer (FR34).
//   - co_creator already: idempotent no-op success, no error banner
//     (FR31) -- re-render/redirect exactly as a fresh promote would.
//   - creator (the Founder): 403 -- no path on this page ever promotes,
//     demotes, or otherwise touches the Founder.
//   - no open role at all: 400 -- there is nothing to promote.
func (h *Handlers) HandlePromote(w http.ResponseWriter, r *http.Request) {
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

	if err := r.ParseForm(); err != nil {
		http.Error(w, "invalid form", http.StatusBadRequest)
		return
	}
	targetID, err := uuid.Parse(r.FormValue("person_id"))
	if err != nil {
		http.Error(w, "invalid person_id", http.StatusBadRequest)
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

	targetRoles, err := h.store.Roles().RolesFor(ctx, channelID, targetID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	switch {
	case hasRole(targetRoles, store.RoleAnalyst):
		if err := h.store.Roles().AddRole(ctx, channelID, targetID, store.RoleCoCreator, person.ID); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
	case hasRole(targetRoles, store.RoleCoCreator):
		// Already promoted: idempotent no-op success (FR31), no error.
	case hasRole(targetRoles, store.RoleCreator):
		http.Error(w, "forbidden: the Founder cannot be promoted", http.StatusForbidden)
		return
	default:
		http.Error(w, "person has no open role on this channel", http.StatusBadRequest)
		return
	}

	http.Redirect(w, r, "/channels/"+channelID.String()+"/access", http.StatusSeeOther)
}

// HandleRemove serves POST /channels/{id}/access/remove (FR33), form
// field "person_id". Two checks gate every removal, in order:
//
//  1. The actor must hold Founder or Co-Creator authority on THIS Channel
//     at all (store.CanInvite) -- rejects a forged POST from an Analyst,
//     a non-member, or one aimed at a person_id scoped to a different
//     Channel outright, before store.CanRemove's per-target matrix even
//     runs. Without this, an unauthorized actor targeting a person with
//     no open role on this Channel (e.g. a person_id lifted from another
//     Channel entirely) would otherwise read identically to FR33's
//     legitimate idempotent no-op below -- this check is what keeps those
//     two cases from being conflated.
//  2. store.CanRemove(ctx, roles, channelID, viewer.ID, targetID) -- FR33's
//     matrix, the only place it is evaluated. `false` here is ambiguous
//     between "the matrix disallows this" and "the target already holds
//     no open role" (see CanRemove's doc comment); RolesFor on the target
//     disambiguates: no open role at all is the idempotent no-op success
//     FR33 requires, anything else (most notably the Founder's own row)
//     is a flat 403 with no state change.
//
// On success, RoleStore.RemoveRole closes the target's open row,
// attributing the revoke to the viewer (FR34).
func (h *Handlers) HandleRemove(w http.ResponseWriter, r *http.Request) {
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

	if err := r.ParseForm(); err != nil {
		http.Error(w, "invalid form", http.StatusBadRequest)
		return
	}
	targetID, err := uuid.Parse(r.FormValue("person_id"))
	if err != nil {
		http.Error(w, "invalid person_id", http.StatusBadRequest)
		return
	}

	actorAuthorized, err := store.CanInvite(ctx, h.store.Roles(), channelID, person.ID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if !actorAuthorized {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}

	can, err := store.CanRemove(ctx, h.store.Roles(), channelID, person.ID, targetID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if !can {
		targetRoles, err := h.store.Roles().RolesFor(ctx, channelID, targetID)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if len(targetRoles) == 0 {
			// FR33's idempotent no-op: nothing left to remove.
			http.Redirect(w, r, "/channels/"+channelID.String()+"/access", http.StatusSeeOther)
			return
		}
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}

	if _, err := h.store.Roles().RemoveRole(ctx, channelID, targetID, person.ID); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	http.Redirect(w, r, "/channels/"+channelID.String()+"/access", http.StatusSeeOther)
}

// hasRole reports whether roles contains want -- a plain set-membership
// check local to this package's Promote/Remove target-state checks, NOT a
// re-implementation of authz.go's removal matrix (that lives ONLY in
// store.CanRemove -- see package doc comment).
func hasRole(roles []store.Role, want store.Role) bool {
	for _, r := range roles {
		if r == want {
			return true
		}
	}
	return false
}
