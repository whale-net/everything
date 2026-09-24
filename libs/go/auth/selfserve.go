package auth

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/google/uuid"
)

// This file is the self-serve credential API: a small REST surface a
// consuming domain mounts (via MountSelfServe, opt-in — not part of
// Provider.Mount's fixed OAuth2 endpoint set) so an already-signed-in
// caller can mint, list, and revoke their own long-lived bearer
// credentials without ever running the OAuth2 authorization-code + PKCE
// dance. This is the same CredentialStore (ProviderConfig.Credentials) and
// the same CallerResolver (ProviderConfig.Resolver) /authorize already
// uses — a self-serve mint is not a second credential lifecycle, just a
// second, script-friendly way to reach the one this package already has.
//
// Unlike /authorize, an unresolved caller here gets a plain 401, never a
// SignInURL redirect — this is a JSON API a script or fetch() call hits
// directly, not a browser navigation a human is sitting in front of.

// selfServeErrorBody is the fixed JSON error shape every self-serve
// endpoint failure renders, mirroring token.go's tokenErrorBody.
type selfServeErrorBody struct {
	Error string `json:"error"`
}

// writeSelfServeError writes a fixed JSON error body at the given status,
// with Cache-Control: no-store — like /token, a self-serve response can
// carry a bearer credential and must never be cached.
func writeSelfServeError(w http.ResponseWriter, status int, code string) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Pragma", "no-cache")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(selfServeErrorBody{Error: code})
}

// credentialResponse is one credential as rendered to its own owner: never
// the raw token (that only ever appears once, in mintResponse, at mint
// time) and never the token_hash (NFR1 — a hash of a bearer credential is
// still a secret-shaped value with no reason to leave the database).
type credentialResponse struct {
	ID         string  `json:"id"`
	CreatedAt  string  `json:"created_at"`
	LastUsedAt *string `json:"last_used_at,omitempty"`
	RevokedAt  *string `json:"revoked_at,omitempty"`
}

// mintResponse is POST /credentials' success body: the raw token, shown
// exactly once (NFR1 — Mint itself never returns it again after this),
// plus the same metadata credentialResponse otherwise renders.
type mintResponse struct {
	Token string `json:"token"`
	credentialResponse
}

func toCredentialResponse(c Credential) credentialResponse {
	resp := credentialResponse{
		ID:        c.ID.String(),
		CreatedAt: c.CreatedAt.Format(timeFormat),
	}
	if c.LastUsedAt != nil {
		s := c.LastUsedAt.Format(timeFormat)
		resp.LastUsedAt = &s
	}
	if c.RevokedAt != nil {
		s := c.RevokedAt.Format(timeFormat)
		resp.RevokedAt = &s
	}
	return resp
}

// timeFormat is RFC 3339 — the one timestamp shape every field above uses.
const timeFormat = "2006-01-02T15:04:05Z07:00"

// resolveSelfServeCaller resolves the already-authenticated caller behind
// r via ProviderConfig.Resolver, writing a 401 {"error":"unauthenticated"}
// and returning ok == false if no session is established. Every self-serve
// handler starts here.
func (p *Provider) resolveSelfServeCaller(w http.ResponseWriter, r *http.Request) (identity string, ok bool) {
	identity, resolved := p.cfg.Resolver.ResolveCaller(r)
	if !resolved {
		writeSelfServeError(w, http.StatusUnauthorized, "unauthenticated")
		return "", false
	}
	return identity, true
}

// handleMintCredential serves POST /credentials: mints a fresh long-lived
// bearer credential for the resolved caller's identity and returns it
// exactly once. There is no request body — a credential carries no label
// or scope beyond the identity it was minted for (the same identity/
// mcp_credential shape /token already produces).
func (p *Provider) handleMintCredential(w http.ResponseWriter, r *http.Request) {
	identity, ok := p.resolveSelfServeCaller(w, r)
	if !ok {
		return
	}

	rawToken, cred, err := p.cfg.Credentials.Mint(r.Context(), identity)
	if err != nil {
		writeSelfServeError(w, http.StatusInternalServerError, "mint_failed")
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Pragma", "no-cache")
	w.WriteHeader(http.StatusCreated)
	_ = json.NewEncoder(w).Encode(mintResponse{
		Token:              rawToken,
		credentialResponse: toCredentialResponse(cred),
	})
}

// listCredentialsResponse is GET /credentials' success body.
type listCredentialsResponse struct {
	Credentials []credentialResponse `json:"credentials"`
}

// handleListCredentials serves GET /credentials: every credential (live
// and revoked) the resolved caller has ever minted, most recent first.
// Never renders a raw token or a token_hash — once a credential is minted,
// this is the only way to see it again, and it is metadata-only.
func (p *Provider) handleListCredentials(w http.ResponseWriter, r *http.Request) {
	identity, ok := p.resolveSelfServeCaller(w, r)
	if !ok {
		return
	}

	creds, err := p.cfg.Credentials.List(r.Context(), identity)
	if err != nil {
		writeSelfServeError(w, http.StatusInternalServerError, "list_failed")
		return
	}

	resp := listCredentialsResponse{Credentials: []credentialResponse{}}
	for _, c := range creds {
		resp.Credentials = append(resp.Credentials, toCredentialResponse(c))
	}

	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	_ = json.NewEncoder(w).Encode(resp)
}

// handleRevokeCredential serves DELETE /credentials/{id}: revokes the
// credential if it is live and owned by the resolved caller. Idempotent
// (CredentialStore.Revoke's own contract) — revoking an already-revoked,
// nonexistent, or not-owned id still reports success (204) rather than
// leaking which case occurred.
func (p *Provider) handleRevokeCredential(w http.ResponseWriter, r *http.Request) {
	identity, ok := p.resolveSelfServeCaller(w, r)
	if !ok {
		return
	}

	id, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		writeSelfServeError(w, http.StatusBadRequest, "invalid_id")
		return
	}

	if err := p.cfg.Credentials.Revoke(r.Context(), id, identity); err != nil {
		writeSelfServeError(w, http.StatusInternalServerError, "revoke_failed")
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

// selfServeCredentialsPath and selfServeCredentialPath are the fixed paths
// MountSelfServe registers. Not configurable, for the same reason
// provider.go's OAuth2 paths aren't: a fixed, predictable surface beats a
// per-deployment path a client would have to be told about separately.
const (
	selfServeCredentialsPath = "/credentials"
	selfServeCredentialPath  = "/credentials/{id}"
)

// ErrSelfServeRequiresResolver is returned by MountSelfServe when
// ProviderConfig.Resolver is nil. NewProvider already requires Resolver to
// be non-nil, so this only fires if a caller somehow got past that (kept
// as a defensive, named error rather than a panic).
var ErrSelfServeRequiresResolver = errors.New("auth: MountSelfServe requires a non-nil ProviderConfig.Resolver")

// MountSelfServe registers the self-serve credential API on mux: POST
// /credentials (mint), GET /credentials (list), and DELETE /credentials/
// {id} (revoke) — all authenticated the same way /authorize is, via
// ProviderConfig.Resolver reading whatever session the consuming domain's
// own sign-in flow already established.
//
// This is deliberately opt-in and separate from Provider.Mount: a domain
// that only wants the OAuth2 authorization-code + PKCE flow (the shape
// every MCP client already speaks) is not forced to also expose a
// script-friendly PAT-minting API just by calling Mount. A domain that
// wants both (e.g. so a human can mint a static token for a non-OAuth2
// harness once, from a browser, instead of running that harness through
// the OAuth2 dance every time) calls both Mount and MountSelfServe on the
// same mux.
func (p *Provider) MountSelfServe(mux *http.ServeMux) error {
	if p.cfg.Resolver == nil {
		return ErrSelfServeRequiresResolver
	}
	mux.HandleFunc("POST "+selfServeCredentialsPath, p.handleMintCredential)
	mux.HandleFunc("GET "+selfServeCredentialsPath, p.handleListCredentials)
	mux.HandleFunc("DELETE "+selfServeCredentialPath, p.handleRevokeCredential)
	return nil
}
