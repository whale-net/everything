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

// credentialsMintPath is the mint POST. All credential routes sit under the
// page's own path, not the self-serve API's "/credentials" prefix.
const credentialsMintPath = credentialsPath

// credentialsNewPath is the create blade's URL; it also serves a full page so
// it can be bookmarked and reloaded.
const credentialsNewPath = credentialsPath + "/new"

func credentialRevokePath(id string) string {
	return credentialsPath + "/" + id + "/revoke"
}

// credentialRowPath is one credential's URL, answering with the row in its
// default state. It is the confirm-dismiss target.
func credentialRowPath(id string) string {
	return credentialsPath + "/" + id
}

// credentialsData lists the caller's live credentials, newest first. Revoked
// ones are dropped because the table has no status column; list errors render
// inline.
func (app *App) credentialsData(r *http.Request) pages.CredentialsData {
	return app.credentialsDataConfirming(r, "")
}

// credentialsDataConfirming is credentialsData with the row whose id is
// confirming in its confirmation state. An id the caller does not hold live
// confirms nothing.
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

// renderCredentials answers htmx with the results block and a plain request
// with the full page; both are 200.
func (app *App) renderCredentials(w http.ResponseWriter, r *http.Request, d pages.CredentialsData) {
	w.Header().Set("Cache-Control", "no-store")
	if r.Header.Get("HX-Request") != "" {
		renderFragment(w, r, pages.CredentialsResults(d))
		return
	}
	r, _ = app.rememberUnprefixedProduct(w, r)
	app.renderShell(w, r, "Credentials", credentialsPath, pages.CredentialsPage(d))
}

func (app *App) handleCredentials(w http.ResponseWriter, r *http.Request) {
	app.renderCredentials(w, r, app.credentialsData(r))
}

// handleNewCredentialBlade serves the create blade: a bare fragment for htmx,
// the full page with the blade open otherwise, so links and reloads land on it.
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

// handleMintCredential mints a credential under the caller's own session
// identity and persona and shows the raw token once. Refusals answer 200 with
// the blade and typed name kept, since htmx does not swap error statuses.
func (app *App) handleMintCredential(w http.ResponseWriter, r *http.Request) {
	// A refusal echoes the raw typed value; the trimmed one is judged and stored.
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

// handleRevokeConfirm serves one row's revoke confirmation: the row region
// for htmx, the full page with that row confirming otherwise. The fragment
// must be rooted at the row region's id or later swaps lose their target.
func (app *App) handleRevokeConfirm(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	id := r.PathValue("id")
	if !isHtmxRequest(r) {
		app.renderCredentials(w, r, app.credentialsDataConfirming(r, id))
		return
	}
	row, ok := app.credentialRow(r, id)
	if !ok {
		// A row the caller does not hold gets its default state, so this route does
		// not reveal whether someone else's credential exists.
		row = pages.CredentialRow{ID: id}
		app.renderCredentialRow(w, r, row, false)
		return
	}
	app.renderCredentialRow(w, r, row, true)
}

// handleCredentialRow serves a credential's URL with the row in its default
// state; it is the dismiss target.
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

// credentialRow reads one of the caller's live credentials by id. Revoked and
// foreign ids both miss, so nothing is disclosed about either.
func (app *App) credentialRow(r *http.Request, id string) (pages.CredentialRow, bool) {
	d := app.credentialsData(r)
	for _, row := range d.Rows {
		if row.ID == id {
			return row, true
		}
	}
	return pages.CredentialRow{}, false
}

// handleRevokeCredential revokes one of the caller's credentials. Revoke is
// idempotent and owner-scoped, so a stale or foreign id is a no-op.
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
	// Read the name before the write removes the row; a foreign or revoked id
	// yields no name and so no confirmation.
	name := app.credentialRowName(r, id.String())
	err = app.credentials.Revoke(r.Context(), id, identity)
	if err != nil {
		slog.Error("revoke credential failed", "error", err)
		app.renderRevokeOutcome(w, r, "Could not revoke the credential.", "")
		return
	}
	app.renderRevokeOutcome(w, r, "", name)
}

// credentialRowName returns the credential's name if the caller holds it live,
// else "".
func (app *App) credentialRowName(r *http.Request, id string) string {
	row, ok := app.credentialRow(r, id)
	if !ok {
		return ""
	}
	return row.Name
}

// renderRevokeOutcome turns a revoke outcome into a response for both submit
// paths: success as a toast or flash, refusal inline. An empty revoked name
// shows nothing, revealing nothing about a foreign id.
func (app *App) renderRevokeOutcome(w http.ResponseWriter, r *http.Request, reason, revoked string) {
	d := app.credentialsData(r)
	// Only set the reason, never clear it: credentialsData may carry its own read
	// failure, which must not render as an empty list.
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
	// A 303 has no body, so the message rides the flash cookie.
	flashSuccess(w, message)
	http.Redirect(w, r, credentialsPath, http.StatusSeeOther)
}
