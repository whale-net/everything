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
		row := pages.CredentialRow{
			ID:              c.ID.String(),
			Name:            c.Name,
			CreatedRelative: relativeTime(c.CreatedAt, now),
			CreatedExact:    c.CreatedAt.UTC().Format(time.RFC3339),
			RevokeAction:    credentialRevokePath(c.ID.String()),
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
	roles, err := app.requestRoles(r)
	persona, hasPersona := app.roles.ResolvePersona(roles)
	if err != nil || !hasPersona {
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

// handleRevokeCredential revokes one of the operator's credentials. Revoke
// is idempotent and owner-scoped in the store, so a stale or foreign id is a
// no-op rather than an error.
func (app *App) handleRevokeCredential(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		d := app.credentialsData(r)
		d.Error = "Invalid credential id."
		app.renderCredentials(w, r, d)
		return
	}
	identity, ok := app.operatorEncodedIdentity(r)
	if ok {
		err = app.credentials.Revoke(r.Context(), id, identity)
	}
	d := app.credentialsData(r)
	switch {
	case !ok:
		d.Error = "Could not resolve your identity; sign in again."
	case err != nil:
		slog.Error("revoke credential failed", "error", err)
		d.Error = "Could not revoke the credential."
	}
	app.renderCredentials(w, r, d)
}