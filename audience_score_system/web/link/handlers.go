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
// # FR3 ordering and where each failure lands
//
// Both handlers run the same three checks, in this exact order, stopping
// at the first failure: h.verifier.Verify (signature, then claim shape,
// then issuer, then expiry -- see verifier.go), the return-URL origin
// check (FR10's open-redirect guard), then the replay check.
//
// A signature/claim-shape/issuer/expiry failure (rejectAssertion) still
// redirects to ui with the "rejected" outcome when possible: it
// best-effort recovers an UNVERIFIED return_url straight from the token's
// JWT payload (never trusting anything else about the token) and follows
// it only if that return_url's origin matches h.uiOrigin. This is safe
// specifically because of that origin bound -- an attacker who forges a
// token can, at worst, redirect the browser within ui's own domain
// carrying a query parameter ui already renders as a generic failure for
// any value it doesn't recognize (issue #2596), never off ui's domain
// entirely. Only when no such trustworthy-origin return_url can be
// recovered at all (no token, unparseable token, or one whose return_url
// fails the origin check) does the handler fall back to the local
// Rejected page (views.templ) instead.
//
// Once an assertion's signature has actually verified, its ReturnURL is
// fully trustworthy -- from that point on (the origin check, the replay
// check, and everything HandleConfirm does), a failing origin check
// renders the local Rejected page (an already-verified ReturnURL pointing
// off ui's domain is never followed, full stop), while every other
// failure DOES redirect to that verified ReturnURL with the "rejected"
// outcome (FR10).
//
// GET performs a READ-ONLY replay check (store.LinkAssertionStore.
// IsConsumed) -- POST /link/whagent/confirm is the only place that calls
// the consuming store.LinkAssertionStore.Consume, immediately adjacent to
// the FR6 write. This is deliberate: an Operator who opens the
// confirmation page and abandons it must still be able to retry within
// the assertion's TTL, which a consuming call from GET would foreclose.
//
// # FR10 outcome query-parameter contract
//
// Every redirect back to ui appends a single "outcome" query parameter to
// the assertion's ReturnURL, with exactly one of these four values --
// whagent_net/ui's handleLinkASSResult (issue #2596) resolves this same
// parameter into its four operator-facing messages:
//
//   - "linked" -- store.LinkCreated: a new person_oidc_identity row was
//     written.
//   - "already_linked" -- store.LinkAlreadyOwned: a no-op, the pair
//     already pointed at this same Person.
//   - "conflict" -- store.ErrLinkedToOtherPerson: the pair is already
//     linked to a DIFFERENT Person; no merge or reassignment is offered.
//   - "rejected" -- the assertion was invalid, expired, or already used.
//
// NFR5: this package must not import, extend, or hook into web/auth's
// mcpauth machinery (/authorize, /token, /register) -- "alongside, never
// on top of".
package link

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/whale-net/everything/audience_score_system/store"
	"github.com/whale-net/everything/audience_score_system/web/auth"
	"github.com/whale-net/everything/audience_score_system/web/components"
	"github.com/whale-net/everything/libs/go/logging"
)

var logger = logging.Get("audience_score_system/web/link")

// The four FR10 outcome values -- see this file's package doc comment for
// the query-parameter contract these are carried under.
const (
	outcomeLinked        = "linked"
	outcomeAlreadyLinked = "already_linked"
	outcomeConflict      = "conflict"
	outcomeRejected      = "rejected"
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
// FR5). The assertion is carried on the query string ("token") -- never a
// URL fragment (unreadable server-side), never a custom header (this is a
// plain browser redirect from ui).
func (h *Handlers) HandleShow(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	token := r.URL.Query().Get("token")
	if token == "" {
		logger.WarnContext(ctx, "link assertion rejected: no token on request")
		h.rejectAssertion(w, r, token)
		return
	}

	assertion, err := h.verifier.Verify(ctx, token)
	if err != nil {
		logger.WarnContext(ctx, "link assertion rejected: verification failed", "error", err)
		h.rejectAssertion(w, r, token)
		return
	}

	if !returnURLOriginMatches(assertion.ReturnURL, h.uiOrigin) {
		logger.WarnContext(ctx, "link assertion rejected: return url origin mismatch")
		h.renderRejected(w, r)
		return
	}

	// FR3 step 3: a READ-ONLY replay check -- see this file's package doc
	// comment for why the consuming write belongs exclusively to
	// HandleConfirm.
	consumed, err := h.store.LinkAssertions().IsConsumed(ctx, assertion.ID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if consumed {
		logger.WarnContext(ctx, "link assertion rejected: already consumed", "jti", assertion.ID)
		h.redirectOutcome(w, r, assertion.ReturnURL, outcomeRejected)
		return
	}

	person, err := h.optionalSignedInPerson(ctx, r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if person == nil {
		// FR4: no ASS Google session yet -- round-trip through the existing
		// C1 sign-in flow via the same `next=` continuation pattern
		// web/invite's HandleShow uses, then land back on this exact
		// request (path + query, including the same token -- no re-mint)
		// once signed in.
		next := r.URL.RequestURI()
		http.Redirect(w, r, "/login?next="+url.QueryEscape(next), http.StatusFound)
		return
	}

	data := ConfirmationData{
		Layout:       components.LayoutData{Title: "Link whagent-net identity", User: person},
		PersonLabel:  personLabel(person),
		SubjectLabel: subjectLabel(assertion),
		Token:        token,
	}
	if err := components.Render(w, r, "Link whagent-net identity", Confirmation(data)); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

// HandleConfirm serves POST /link/whagent/confirm -- signed-in only
// (wrapped in auth.RequireSignedIn by main.go). The assertion is carried
// on the hidden "token" form field Confirmation's form submits.
//
// This re-verifies the assertion, the return-URL origin, and the replay
// state exactly as HandleShow does -- a GET that rendered the
// confirmation page is never trusted to still hold true on this POST (the
// assertion could have expired, or been consumed by a concurrent request,
// in between). See this file's package doc comment for the full FR3
// ordering and the FR10 outcome contract.
func (h *Handlers) HandleConfirm(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	person := auth.PersonFromContext(ctx)
	if person == nil {
		// auth.RequireSignedIn (main.go) always resolves a Person before
		// this handler runs; nil here means it was called incorrectly.
		http.Error(w, "not signed in", http.StatusUnauthorized)
		return
	}

	token := r.FormValue("token")
	if token == "" {
		logger.WarnContext(ctx, "link assertion rejected on confirm: no token in form")
		h.rejectAssertion(w, r, token)
		return
	}

	assertion, err := h.verifier.Verify(ctx, token)
	if err != nil {
		logger.WarnContext(ctx, "link assertion rejected on confirm: verification failed", "error", err)
		h.rejectAssertion(w, r, token)
		return
	}

	if !returnURLOriginMatches(assertion.ReturnURL, h.uiOrigin) {
		logger.WarnContext(ctx, "link assertion rejected on confirm: return url origin mismatch")
		h.renderRejected(w, r)
		return
	}

	// The consuming write (FR3/NFR1) -- immediately adjacent to the FR6
	// write below, both inside this one request, so a replay racing this
	// confirm is rejected here rather than by HandleShow's read-only check.
	if err := h.store.LinkAssertions().Consume(ctx, assertion.ID, assertion.Expiry); err != nil {
		if errors.Is(err, store.ErrAssertionAlreadyConsumed) {
			logger.WarnContext(ctx, "link assertion rejected on confirm: already consumed", "jti", assertion.ID)
			h.redirectOutcome(w, r, assertion.ReturnURL, outcomeRejected)
			return
		}
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	outcome, err := h.store.PersonIdentities().LinkToExistingPerson(ctx, assertion.SubjectIssuer, assertion.Subject, person.ID)
	if err != nil {
		if errors.Is(err, store.ErrLinkedToOtherPerson) {
			logger.WarnContext(ctx, "link rejected: pair already linked to a different person",
				"person_id", person.ID, "iss", assertion.SubjectIssuer, "sub", assertion.Subject)
			h.redirectOutcome(w, r, assertion.ReturnURL, outcomeConflict)
			return
		}
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	switch outcome {
	case store.LinkCreated:
		logger.InfoContext(ctx, "whagent-net identity linked",
			"person_id", person.ID, "iss", assertion.SubjectIssuer, "sub", assertion.Subject)
		h.redirectOutcome(w, r, assertion.ReturnURL, outcomeLinked)
	case store.LinkAlreadyOwned:
		logger.InfoContext(ctx, "whagent-net identity already linked",
			"person_id", person.ID, "iss", assertion.SubjectIssuer, "sub", assertion.Subject)
		h.redirectOutcome(w, r, assertion.ReturnURL, outcomeAlreadyLinked)
	default:
		// store.LinkOutcome is exhaustive at exactly these two values today
		// (see person_identity.go) -- this default only ever fires if a
		// future value is added there without updating this switch.
		http.Error(w, fmt.Sprintf("unrecognized link outcome %d", outcome), http.StatusInternalServerError)
	}
}

// optionalSignedInPerson resolves the caller's session cookie to a
// store.Person WITHOUT redirecting when absent -- unlike
// web/auth.Authenticator.RequireSignedIn, which always redirects to
// /login, since HandleShow (the only caller) must render a different view
// for an anonymous caller rather than send them away from the link
// request itself. Mirrors web/invite's identical helper. Returns (nil,
// nil) for "no valid session", distinct from a real error.
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

// rejectAssertion handles a token that failed h.verifier.Verify (or was
// never present at all) -- see this file's package doc comment for why
// this still redirects to ui with the "rejected" outcome whenever
// possible, rather than always falling back to the local Rejected page:
// it best-effort recovers an UNVERIFIED return_url from token's raw JWT
// payload and follows it only if that return_url's origin matches
// h.uiOrigin (the same bound returnURLOriginMatches enforces for a fully
// verified assertion elsewhere in this file).
func (h *Handlers) rejectAssertion(w http.ResponseWriter, r *http.Request, token string) {
	if returnURL, ok := unverifiedReturnURL(token); ok && returnURLOriginMatches(returnURL, h.uiOrigin) {
		h.redirectOutcome(w, r, returnURL, outcomeRejected)
		return
	}
	h.renderRejected(w, r)
}

// unverifiedReturnURL best-effort extracts the return_url claim straight
// from token's JWT payload segment, WITHOUT checking its signature --
// used only by rejectAssertion to decide where to redirect a REJECTED
// outcome. Never used to trust anything else about the token; see
// rejectAssertion's doc comment for why following even an unverified
// return_url is safe as long as its origin independently matches
// h.uiOrigin.
func unverifiedReturnURL(token string) (string, bool) {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return "", false
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return "", false
	}
	var claims struct {
		ReturnURL string `json:"return_url"`
	}
	if err := json.Unmarshal(payload, &claims); err != nil || claims.ReturnURL == "" {
		return "", false
	}
	return claims.ReturnURL, true
}

// renderRejected renders the local terminal Rejected view (views.templ) --
// used exactly when no trustworthy, verified, origin-matching ReturnURL is
// available to redirect to (see this file's package doc comment). User is
// best-effort (nil is fine -- Rejected never reads it beyond the shared
// Layout chrome).
func (h *Handlers) renderRejected(w http.ResponseWriter, r *http.Request) {
	person, _ := h.optionalSignedInPerson(r.Context(), r)
	data := components.LayoutData{Title: "Link request rejected", User: person}
	if err := components.Render(w, r, "Link request rejected", Rejected(data)); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

// redirectOutcome appends outcome as the "outcome" query parameter to
// returnURL (see this file's package doc comment for the four recognized
// values) and redirects the browser there (FR10). Callers must have
// already validated returnURL's origin via returnURLOriginMatches before
// calling this.
func (h *Handlers) redirectOutcome(w http.ResponseWriter, r *http.Request, returnURL, outcome string) {
	u, err := url.Parse(returnURL)
	if err != nil {
		// Unreachable in practice: returnURLOriginMatches already parsed
		// this exact string successfully before any caller reaches here.
		http.Error(w, "invalid return url", http.StatusInternalServerError)
		return
	}
	q := u.Query()
	q.Set("outcome", outcome)
	u.RawQuery = q.Encode()
	http.Redirect(w, r, u.String(), http.StatusSeeOther)
}

// returnURLOriginMatches reports whether returnURL's scheme+host exactly
// matches uiOrigin's (FR10's open-redirect guard): an assertion carrying a
// ReturnURL pointing anywhere else is rejected rather than followed, even
// though its signature verified -- a compromised or malformed assertion
// must never turn this endpoint into an open redirect.
func returnURLOriginMatches(returnURL, uiOrigin string) bool {
	ru, err := url.Parse(returnURL)
	if err != nil || ru.Scheme == "" || ru.Host == "" {
		return false
	}
	origin, err := url.Parse(uiOrigin)
	if err != nil || origin.Scheme == "" || origin.Host == "" {
		return false
	}
	return ru.Scheme == origin.Scheme && ru.Host == origin.Host
}

// personLabel names the signed-in ASS Operator about to be linked (FR5) --
// display name if set, else email. Every store.Person resolved through a
// live Google session has at least one of the two (see auth.go's
// UpsertByGoogleSubject).
func personLabel(p *store.Person) string {
	if p.DisplayName != "" {
		return p.DisplayName
	}
	return p.Email
}

// subjectLabel is an operator-recognizable identifier of the whagent-net
// identity from the verified assertion (FR5) -- the Operator's Keycloak
// subject and issuer, which is all an Assertion carries (see assertion.go;
// there is no display name to show here, unlike personLabel).
func subjectLabel(a *Assertion) string {
	return fmt.Sprintf("%s (%s)", a.Subject, a.SubjectIssuer)
}
