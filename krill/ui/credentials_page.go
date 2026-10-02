package main

import (
	"log/slog"
	"net/http"

	"github.com/google/uuid"

	"github.com/whale-net/everything/krill/ui/pages"
	"github.com/whale-net/everything/libs/go/auth"
)

// credentialsMintPath is the POST that mints a credential; revoke lives at
// credentialsPath + "/{id}/revoke". Both sit under the page's own path, not
// under the self-serve API's "/credentials" prefix (see credentialsPath).
const credentialsMintPath = credentialsPath

func credentialRevokePath(id string) string {
	return credentialsPath + "/" + id + "/revoke"
}

// credentialsData lists the caller's credentials into a view model. A list
// failure is reported inline rather than failing the page.
func (app *App) credentialsData(r *http.Request) pages.CredentialsData {
	d := pages.CredentialsData{MintAction: credentialsMintPath}
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
		id := c.ID.String()
		d.Rows = append(d.Rows, pages.CredentialRow{
			ID:           id,
			CreatedAt:    c.CreatedAt.Format("2006-01-02 15:04:05 MST"),
			Revoked:      c.RevokedAt != nil,
			RevokeAction: credentialRevokePath(id),
		})
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

// handleCredentials is the credentials page.
func (app *App) handleCredentials(w http.ResponseWriter, r *http.Request) {
	app.renderCredentials(w, r, app.credentialsData(r))
}

// handleMintCredential mints a credential for the signed-in operator and
// re-renders the block with the raw token, shown exactly once.
func (app *App) handleMintCredential(w http.ResponseWriter, r *http.Request) {
	fail := func(msg string) {
		d := app.credentialsData(r)
		d.Error = msg
		app.renderCredentials(w, r, d)
	}
	identity, ok := app.operatorEncodedIdentity(r)
	if !ok {
		fail("Could not resolve your identity; sign in again.")
		return
	}
	roles, err := app.requestRoles(r)
	persona, ok := app.roles.ResolvePersona(roles)
	if err != nil || !ok {
		fail("Your account holds no role that may mint a credential.")
		return
	}
	token, _, err := app.credentials.Mint(auth.WithPersona(r.Context(), string(persona)), identity)
	if err != nil {
		slog.Error("mint credential failed", "error", err)
		fail("Could not generate a token.")
		return
	}
	d := app.credentialsData(r)
	d.NewToken = token
	app.renderCredentials(w, r, d)
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
