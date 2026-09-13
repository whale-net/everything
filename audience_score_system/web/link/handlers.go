// This file is the handler half of package link (issue #2600, FR4-FR6,
// FR8-FR10, FR13, NFR3, NFR5): GET /link/whagent verifies a ui-minted
// Assertion (FR3, #2598) and requires an ASS Google session (FR4, routing
// through web/auth's existing sign-in continuation on demand); a
// signed-in caller reaches FR5's explicit confirmation page. POST
// /link/whagent/confirm (signed-in only, wrapped by
// web/auth.RequireSignedIn in main.go) writes the link (#2597, #2599) and
// redirects back to ui (FR10). See assertion.go/verifier.go/errors.go for
// the verification half this package already had (#2598).
//
// Scaffold phase: both handlers answer 501 -- see each handler's TODO for
// what Implementation phase fills in. views.templ's Confirmation is a
// placeholder that Implementation phase replaces with FR5's full
// both-sides-named layout.
package link

import (
	"net/http"

	"github.com/whale-net/everything/audience_score_system/store"
	"github.com/whale-net/everything/audience_score_system/web/auth"
)

// Handlers holds the dependencies GET /link/whagent and POST
// /link/whagent/confirm need: verifier checks the assertion (#2598);
// store gives access to LinkAssertions() (#2597, replay/consumption) and
// PersonIdentities() (#2599, the FR6 write); sessions resolves the
// signed-in Operator the same way web/invite's optionalSignedInPerson
// does, so the GET can render FR5's confirmation for a signed-in caller
// without web/auth.RequireSignedIn's unconditional /login redirect (that
// middleware only wraps the confirm POST -- see main.go's route
// registrations). uiOrigin is ASS_WHAGENT_UI_ISSUER, used verbatim as the
// origin FR10's return-URL validation checks a redirect target against
// (open-redirect guard).
type Handlers struct {
	store    *store.Store
	sessions *auth.SessionManager
	verifier *Verifier
	uiOrigin string
}

// New wires store, sessions, verifier, and the configured ui issuer
// (ASS_WHAGENT_UI_ISSUER, passed as uiOrigin -- see Handlers' doc
// comment) into a Handlers.
func New(st *store.Store, sessions *auth.SessionManager, verifier *Verifier, uiIssuer string) *Handlers {
	return &Handlers{store: st, sessions: sessions, verifier: verifier, uiOrigin: uiIssuer}
}

// HandleShow serves GET /link/whagent (FR3's verification order, FR4,
// FR5). Scaffold phase: always answers 501.
//
// TODO(Implementation phase): parse the assertion from the query
// parameter carrying it (never a URL fragment, never a custom header --
// see this task's Routes section) and run, in order, h.verifier.Verify,
// then the FR3 expiry/replay ordering this task's "FR3 handler-side
// ordering (fail closed)" section documents -- Consume here is a
// READ-only replay check; the consuming write happens in HandleConfirm
// (see that TODO for why). Any failure redirects to the assertion's
// ReturnURL (once parsed; fall back to a local error page if parsing
// itself failed) with the "rejected assertion" outcome (FR10) and writes
// nothing. On success, resolve the caller via h.sessions (mirroring
// web/invite's optionalSignedInPerson -- NOT auth.RequireSignedIn's
// unconditional redirect, since this route must round-trip the assertion
// through the existing `next=` sign-in continuation per FR4 rather than
// lose it to a bare /login redirect) and render Confirmation naming both
// sides (FR5). Log a rejection at WARNING (NFR3) -- never the raw token.
func (h *Handlers) HandleShow(w http.ResponseWriter, r *http.Request) {
	http.Error(w, "not implemented", http.StatusNotImplemented)
}

// HandleConfirm serves POST /link/whagent/confirm -- signed-in only
// (wrapped in auth.RequireSignedIn by main.go). Scaffold phase: always
// answers 501.
//
// TODO(Implementation phase): re-verify the assertion exactly as
// HandleShow does -- a GET that rendered the confirmation page must never
// be trusted to still hold true on this POST. Then call
// h.store.LinkAssertions().Consume immediately adjacent to
// h.store.PersonIdentities().LinkToExistingPerson(ctx,
// assertion.SubjectIssuer, assertion.Subject, signedInPersonID) (#2599) --
// see this task's "Where consumption is recorded" note for why Consume
// belongs here and not in HandleShow. Map the store.LinkOutcome/error to
// FR10's four outcomes per this task's table, log success at INFO with
// person_id and (iss, sub) or rejection/conflict at WARNING (NFR3, never
// the raw token or credential material), validate the return URL's
// origin against h.uiOrigin before redirecting (open-redirect guard), and
// redirect on both success and failure.
func (h *Handlers) HandleConfirm(w http.ResponseWriter, r *http.Request) {
	http.Error(w, "not implemented", http.StatusNotImplemented)
}
