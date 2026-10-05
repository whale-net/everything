package main

import (
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/whale-net/everything/krill/ui/pages"
	"github.com/whale-net/everything/libs/go/auth"
)

// credentialsMintPath is the POST that mints a credential; revoke lives at
// credentialsPath + "/{id}/revoke" and the create blade's GET at
// credentialsNewPath. All three sit under the page's own path, not under
// the self-serve API's "/credentials" prefix (see credentialsPath).
const credentialsMintPath = credentialsPath

// credentialsNewPath is the create-credential blade's own URL. It is a
// real page as well as an htmx fragment: the blade goes here whether it
// was swapped in or the operator arrived by link, so the URL can be
// bookmarked and reloaded directly.
const credentialsNewPath = credentialsPath + "/new"

func credentialRevokePath(id string) string {
	return credentialsPath + "/" + id + "/revoke"
}

// credentialRowPath is one credential's own URL: the row, in whatever state
// it is in. It is what Confirm's dismiss points at, so a dismissal is a
// GET that answers with the row back in its default state rather than a
// state flip that exists only in JavaScript.
func credentialRowPath(id string) string {
	return credentialsPath + "/" + id
}

// credentialsData lists the caller's LIVE credentials into a view model. A
// list failure is reported inline rather than failing the page.
//
// Revoked credentials are dropped here, not in the template: auth's List
// answers live and revoked alike, and this table has no status column, so a
// revoked row left in it would be a credential that reads as usable. The
// store's own order (most recent first) is preserved -- this table is read
// as a register of what an operator has in the field, not re-sorted into a
// notion of activity the store does not hold.
func (app *App) credentialsData(r *http.Request) pages.CredentialsData {
	return app.credentialsDataConfirming(r, "")
}

// credentialsDataConfirming lists the caller's LIVE credentials, with the
// row whose id is confirming rendered in its confirmation state.
//
// An empty confirming renders every row in its default state, which is
// what every page load and every post wants. A non-empty one renders
// exactly that row confirming, which is what the row's confirm URL wants
// -- and it is scoped to a single row on purpose: a confirmation that
// appeared in every row, or in a row other than the one asked about, would
// be an answer to a question nobody asked.
//
// A confirming id naming a row that is not in the caller's live list
// (already revoked, or never theirs) leaves every row in its default
// state, so the confirmation never names a credential the caller does not
// hold.
func (app *App) credentialsDataConfirming(r *http.Request, confirming string) pages.CredentialsData {
	now := app.clock()
	d := pages.CredentialsData{
		MintAction: credentialsMintPath,
		NewHref:    credentialsNewPath,
		ListHref:   credentialsPath,
	}
	identity, ok := app.operatorEncodedIdentity(r)
	if !ok {
		d.Error = "Could not resolve your identity; sign in again."
		return d
	}
	creds, err := app.credentials.List(r.Context(), identity)
	if err != nil {
		slog.Error("list credentials failed", "error", err)
		d.Error = "Could not load credentials."
		return d
	}
	for _, c := range creds {
		if c.RevokedAt != nil {
			continue
		}
		id := c.ID.String()
		row := pages.CredentialRow{
			ID:                id,
			Name:              c.Name,
			CreatedRelative:   relativeTime(c.CreatedAt, now),
			CreatedExact:      c.CreatedAt.UTC().Format(time.RFC3339),
			RevokeAction:      credentialRevokePath(id),
			RevokeConfirmHref: credentialRevokePath(id),
			RevokeDismissHref: credentialRowPath(id),
			Confirming:        confirming != "" && confirming == id,
		}
		if c.LastUsedAt != nil {
			row.LastUsedRelative = relativeTime(*c.LastUsedAt, now)
			row.LastUsedExact = c.LastUsedAt.UTC().Format(time.RFC3339)
		}
		d.Rows = append(d.Rows, row)
	}
	return d
}

// renderCredentials answers an htmx request with the results block and a
// plain request with the full page; both are 200 (see the 200-re-render
// error rule).
func (app *App) renderCredentials(w http.ResponseWriter, r *http.Request, d pages.CredentialsData) {
	w.Header().Set("Cache-Control", "no-store")
	if r.Header.Get("HX-Request") != "" {
		renderFragment(w, r, pages.CredentialsResults(d))
		return
	}
	r, _ = app.rememberUnprefixedProduct(w, r)
	app.renderShell(w, r, "Credentials", credentialsPath, pages.CredentialsPage(d))
}

// handleCredentials is the credentials list.
func (app *App) handleCredentials(w http.ResponseWriter, r *http.Request) {
	app.renderCredentials(w, r, app.credentialsData(r))
}

// handleNewCredentialBlade serves the create-credential blade.
//
// The two modes are one view, not two: an htmx request gets the bare blade
// fragment whose root IS the blade region's swap target, and a plain
// request gets the whole page in-shell with the blade open. So a copied
// link, a bookmark and a reload all land on the blade rather than on a 404
// or on a list the operator then has to click through again.
func (app *App) handleNewCredentialBlade(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	d := app.credentialsData(r)
	d.BladeOpen = true
	if r.Header.Get("HX-Request") != "" {
		renderFragment(w, r, pages.CredentialsBladeSlot(d))
		return
	}
	r, _ = app.rememberUnprefixedProduct(w, r)
	app.renderShell(w, r, "Credentials", credentialsPath, pages.CredentialsPage(d))
}

// handleMintCredential mints a named credential for the signed-in operator
// and re-renders the results block with the raw token, shown exactly once.
//
// No identity is ever posted or rendered (LB4): the credential is filed
// under the caller's OWN encoded identity, resolved from their session, and
// the persona under which it may act is resolved from that same request's
// roles. Nothing about either is asked for in the form.
//
// A refusal answers 200 with the blade re-rendered and the typed name kept,
// never a redirect: htmx does not swap on an error status, so an error
// status would drop the operator's typing on the floor (see the
// 200-re-render rule).
func (app *App) handleMintCredential(w http.ResponseWriter, r *http.Request) {
	// The raw form value is what a refusal puts back in the field; the
	// trimmed one is what is judged and stored. Keeping them apart means a
	// refusal shows the operator their own keystrokes, spaces included.
	typed := r.FormValue("name")
	name := strings.TrimSpace(typed)
	fail := func(msg string) {
		d := app.credentialsData(r)
		d.Error = msg
		d.BladeOpen = true
		d.BladeName = typed
		app.renderCredentials(w, r, d)
	}

	identity, ok := app.operatorEncodedIdentity(r)
	if !ok {
		fail("Could not resolve your identity; sign in again.")
		return
	}
	if name == "" {
		fail("Enter a name for this credential.")
		return
	}
	persona, hasPersona := app.operatorPersona(r)
	if !hasPersona {
		fail("Your account holds no role that may mint a credential.")
		return
	}

	token, cred, err := app.credentials.MintNamed(auth.WithPersona(r.Context(), string(persona)), identity, name)
	switch {
	case errors.Is(err, auth.ErrCredentialNameRequired):
		fail("Enter a name for this credential.")
	case errors.Is(err, auth.ErrCredentialNameTaken):
		fail("A live credential is already named \"" + name + "\". Revoke it first, or pick another name.")
	case err != nil:
		slog.Error("mint named credential failed", "error", err)
		fail("Could not create the credential.")
	default:
		d := app.credentialsData(r)
		d.NewToken = token
		d.NewTokenName = cred.Name
		app.renderCredentials(w, r, d)
	}
}

// handleRevokeConfirm serves one row's revoke step at its own URL: the
// confirmation an htmx browser swaps into the row, and the whole page with
// that one row confirming for a browser with no JavaScript.
//
// Two modes of one view, the same shape the create blade uses and for the
// same reason: the question is a real address, so a reload, a bookmark and
// a no-JS click all land on it.
//
// The response is the row's OWN region and nothing else. Returning the
// whole results block here would work for the no-JS page but break the
// htmx swap, whose target is the row region: the fragment has to be rooted
// at the id being replaced, or htmx replaces the row region with markup
// that carries a different id and the next Revoke has no target left.
func (app *App) handleRevokeConfirm(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	id := r.PathValue("id")
	if !isHtmxRequest(r) {
		app.renderCredentials(w, r, app.credentialsDataConfirming(r, id))
		return
	}
	row, ok := app.credentialRow(r, id)
	if !ok {
		// A row the caller does not hold gets its default state back, not
		// an error and not a confirmation: this route must not become a way
		// to ask whether a credential exists that is not the caller's.
		// Returning the row's default state is exactly what asking for the
		// row's URL asks for, so the two are the same answer.
		row = pages.CredentialRow{ID: id}
		app.renderCredentialRow(w, r, row, false)
		return
	}
	app.renderCredentialRow(w, r, row, true)
}

// handleCredentialRow serves one credential's own URL, which answers with
// the row in its DEFAULT state. It is the dismiss target: confirming asks
// at the revoke URL, dismissing un-asks here, and both are GETs that render
// the row.
func (app *App) handleCredentialRow(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	id := r.PathValue("id")
	row, ok := app.credentialRow(r, id)
	if !ok {
		row = pages.CredentialRow{ID: id}
	}
	if isHtmxRequest(r) {
		app.renderCredentialRow(w, r, row, false)
		return
	}
	app.renderCredentials(w, r, app.credentialsData(r))
}

// renderCredentialRow writes one row's revoke region, confirming or not.
//
// The confirming flag is passed rather than read off the row so this one
// renderer serves all three callers -- the confirm route, the row route,
// and the not-yours case -- from one template invocation.
func (app *App) renderCredentialRow(w http.ResponseWriter, r *http.Request, row pages.CredentialRow, confirming bool) {
	row.Confirming = confirming
	if row.RevokeConfirmHref == "" {
		row.RevokeConfirmHref = credentialRevokePath(row.ID)
	}
	if row.RevokeDismissHref == "" {
		row.RevokeDismissHref = credentialRowPath(row.ID)
	}
	if row.RevokeAction == "" {
		row.RevokeAction = credentialRevokePath(row.ID)
	}
	renderFragment(w, r, pages.CredentialRevokeRegion(row))
}

// credentialRow reads one of the caller's LIVE credentials by id, out of
// the same read the list is built from.
//
// A miss is the store's own answer for an id that is already revoked or is
// not the caller's at all, and the two are deliberately the same answer:
// this page discloses nothing about a credential the caller does not hold.
func (app *App) credentialRow(r *http.Request, id string) (pages.CredentialRow, bool) {
	d := app.credentialsData(r)
	for _, row := range d.Rows {
		if row.ID == id {
			return row, true
		}
	}
	return pages.CredentialRow{}, false
}

// handleRevokeCredential revokes one of the operator's credentials on the
// strength of the confirmation, and answers the two ways that confirmation
// can be submitted.
//
// Revoke is idempotent and owner-scoped in the store, so a stale or
// foreign id is a no-op rather than an error, and a revocation is filed
// under the caller's OWN identity resolved from their session (LB4) --
// nothing about the caller is posted or rendered.
//
// The two success paths are the same success: an htmx request gets the
// list re-rendered in place at 200 plus one out-of-band toast, and a
// no-JS browser gets a 303 back to the list carrying the message in the
// one-shot flash cookie. A refusal is neither: it is a 200 with the list
// re-rendered and the reason inline, because a swap target never sees a
// status code and a bare error page would tell the operator nothing about
// what to do next.
func (app *App) handleRevokeCredential(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		app.renderRevokeOutcome(w, r, "Invalid credential id.", "")
		return
	}
	identity, ok := app.operatorEncodedIdentity(r)
	if !ok {
		app.renderRevokeOutcome(w, r, "Could not resolve your identity; sign in again.", "")
		return
	}
	// The name is read BEFORE the write, because the write is what takes
	// the row out of the list. An id that is not the caller's resolves to
	// no name here, and a revocation that names nothing confirms nothing
	// -- which is the answer a foreign or already-revoked id gets, the
	// same as it gets in the list itself.
	name := app.credentialRowName(r, id.String())
	err = app.credentials.Revoke(r.Context(), id, identity)
	if err != nil {
		slog.Error("revoke credential failed", "error", err)
		app.renderRevokeOutcome(w, r, "Could not revoke the credential.", "")
		return
	}
	app.renderRevokeOutcome(w, r, "", name)
}

// credentialRowName is one credential's name if the caller holds it live,
// and "" otherwise -- the caller-side answer to "is this id mine?", asked
// before the write so the write can name what it removed.
func (app *App) credentialRowName(r *http.Request, id string) string {
	row, ok := app.credentialRow(r, id)
	if !ok {
		return ""
	}
	return row.Name
}

// renderRevokeOutcome is the one place a revoke's outcome is turned into a
// response, so the two submit paths cannot drift apart: a confirmation
// answers with the toast (or the flash cookie) on both, and a refusal
// answers with the reason inline on both.
//
// revoked names the credential that was actually removed, and is empty
// when the id named nothing the caller holds. An empty name is not a
// failure and not a special case to be reported separately: withToast and
// flashSuccess both render nothing for an empty message, so a stale or
// foreign id gets a list that is unchanged and says nothing -- the same
// answer in both, and one that reveals nothing about whether the credential
// exists.
func (app *App) renderRevokeOutcome(w http.ResponseWriter, r *http.Request, reason, revoked string) {
	d := app.credentialsData(r)
	// Only ever SET the reason, never clear one: credentialsData may already
	// carry the re-read's own failure, and overwriting it with an empty
	// string would render a list that failed to load as an empty one --
	// a confident, wrong "No credentials yet." (the 200-re-render rule's
	// "never render a read failure as an empty view").
	if reason != "" {
		d.Error = reason
	}
	message := ""
	if reason == "" && revoked != "" {
		message = "Revoked " + revoked + "."
	}
	if message == "" {
		app.renderCredentials(w, r, d)
		return
	}
	if isHtmxRequest(r) {
		w.Header().Set("Cache-Control", "no-store")
		renderFragment(w, r, withToast(message, pages.CredentialsResults(d)))
		return
	}
	// A 303 has no body to carry the confirmation in, so the message rides
	// the one-shot cookie the landing page renders as a success alert.
	flashSuccess(w, message)
	http.Redirect(w, r, credentialsPath, http.StatusSeeOther)
}
