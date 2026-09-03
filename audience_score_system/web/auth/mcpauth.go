package auth

import (
	"net/http"

	"github.com/whale-net/everything/libs/go/mcpauth"
)

// MCPCallerResolver adapts ASS's existing signed-in-session lookup to
// mcpauth.CallerResolver (FR12, issue #1646) so mcpauth's `/authorize`
// endpoint (mounted on `web`) can resolve the already-signed-in caller
// without ever rendering a login form or collecting credentials itself
// (FR2). It performs no IdP call and no fresh token verification: the
// Google OIDC sign-in that authenticated this caller already happened in
// HandleCallback, and this resolver only reads the session state that
// established — exactly the same read RequireSignedIn performs
// (SessionManager.PersonID), just reported as (identity, ok) instead of
// redirecting or resolving a store.Person.
//
// The identity string reported on success is the Person's UUID in string
// form (person.ID.String()) — the same value PersonMiddleware
// (audience_score_system/mcp/server/auth.go, #1643) parses back into a
// uuid.UUID on the mcp side, and the same value that ends up in
// mcp_credential.person_id and mcp_auth_code.identity.
//
// Implementation note (issue #1646): this reads only a.sessions.PersonID(r)
// -- a cookie parse plus a single SQL query against web_session -- and
// never touches a.oauth2Config/a.verifier (the Google-calling half of this
// package), so it makes no IdP or other network call of any kind.
func (a *Authenticator) MCPCallerResolver() mcpauth.CallerResolverFunc {
	return func(r *http.Request) (string, bool) {
		personID, err := a.sessions.PersonID(r)
		if err != nil {
			return "", false
		}
		return personID, true
	}
}
