// Operator identity resolution for this binary's own write paths: the one
// place a signed-in Keycloak session becomes the (iss, sub) pair krill
// attributes a mutation to (LB4).
//
// auth.go's mcpCallerResolver already resolves that pair for the MCP
// OAuth2 front door; the resolver below is the same resolution factored out
// so the app pages' write path (writeclient.go) can never diverge from it.
// A caller whose session, issuer, or subject does not resolve gets no
// Subject at all -- never a synthetic or dev-only one -- so a deployed UI
// has no identity to attribute a write with, and the write cannot happen.
//
// The one exception is AUTH_MODE=none, where devAuth is true and there is
// no Keycloak session to resolve in the first place: the pair is the fixed
// dev pair below, which is exactly what api resolves for its own dev token,
// so a local read and a local write attribute the same operator. That
// branch is unreachable for AUTH_MODE=oidc, so no real, misconfigured, or
// tampered OIDC session can ever reach it.
package main

import (
	"context"
	"net/http"

	"github.com/whale-net/everything/krill/apiclient"
	"github.com/whale-net/everything/krill/identity"
	"github.com/whale-net/everything/krill/mcp/server"
	"github.com/whale-net/everything/krill/store"
)

// operatorIdentity reads the request's Keycloak session and returns the
// operator's real (iss, sub) pair. ok is false for a missing, tampered, or
// expired session -- a deployed UI with no resolvable identity has nothing
// to attribute a write with, so the write must not happen.
//
// Under AUTH_MODE=none (app.devAuth) there is no Keycloak session: the
// authenticator hands back a synthetic dev user and the configured issuer is
// empty, so the encode below would always refuse and every identity-bearing
// page -- credentials above all -- would be unusable in local dev. That mode
// has exactly one pair it can honestly report, the one api's own dev token
// resolves to (authdoor.DevIssuer/DevSubject), so it is returned here
// verbatim: the same (iss, sub) a local write is already attributed to, so
// reads and writes agree. app.devAuth is set from the auth mode alone and is
// false for AUTH_MODE=oidc, so this branch can never serve a real session.
func (app *App) operatorIdentity(r *http.Request) (iss, sub string, ok bool) {
	if app.devAuth {
		return devIdentity()
	}
	user, err := app.auth.CurrentUser(r)
	if err != nil {
		return "", "", false
	}
	// identity.Encode is the pair's validation gate (non-empty halves, no
	// embedded separator) -- its own output is not needed here, only its
	// verdict.
	if _, err := identity.Encode(app.oidcIssuer, user.Sub); err != nil {
		return "", "", false
	}
	return app.oidcIssuer, user.Sub, true
}

// operatorSubject is operatorIdentity as the store.Subject every krill
// session this binary mints records as both acting and on-behalf-of (a
// signed-in operator acts for themselves; the persona a write is
// authorized against is resolved separately, never from this triple).
func (app *App) operatorSubject(r *http.Request) (store.Subject, bool) {
	iss, sub, ok := app.operatorIdentity(r)
	if !ok {
		return store.Subject{}, false
	}
	return store.Subject{Iss: iss, Sub: sub, Kind: store.SubjectKindHuman}, true
}

// operatorEncodedIdentity is operatorIdentity packed into the single opaque
// string auth's credential/auth-code stores keep as Identity.
func (app *App) operatorEncodedIdentity(r *http.Request) (string, bool) {
	iss, sub, ok := app.operatorIdentity(r)
	if !ok {
		return "", false
	}
	encoded, err := identity.Encode(iss, sub)
	if err != nil {
		return "", false
	}
	return encoded, true
}

// operatorPersona is the persona the resolved operator mints a credential
// under. It is resolved separately from the (iss, sub) pair on purpose:
// identity says who, persona says what they may do.
//
// Under AUTH_MODE=none the synthetic dev user carries htmxauth.AllRoles,
// which names no configured realm role, so ResolvePersona would refuse every
// dev mint. The dev operator is instead the swarm operator outright --
// exactly what api resolves for the same dev token (authdoor's resolve), so
// a credential minted here is authorized the same way it would be verified
// there. For AUTH_MODE=oidc this is ResolvePersona unchanged, and an
// identity holding neither configured role still resolves no persona.
func (app *App) operatorPersona(r *http.Request) (server.Persona, bool) {
	if app.devAuth {
		return server.PersonaSwarmOperator, true
	}
	roles, err := app.requestRoles(r)
	if err != nil {
		return "", false
	}
	return app.roles.ResolvePersona(roles)
}

// Identity api's dev token resolves to (authdoor.DevIssuer/DevSubject).
const (
	devIssuer  = "krill-dev"
	devSubject = "dev-operator"
)

// devIdentity is the one (iss, sub) pair AUTH_MODE=none can report. The two
// constants are api's own dev-token halves (authdoor.DevIssuer/DevSubject),
// so a local read and a local write resolve to the same identity rather than
// two half-invented ones. The encode is the same gate a real pair passes, so
// a dev pair that stopped being encodable would be reported as no identity
// at all rather than a malformed one.
func devIdentity() (iss, sub string, ok bool) {
	if _, err := identity.Encode(devIssuer, devSubject); err != nil {
		return "", "", false
	}
	return devIssuer, devSubject, true
}

// operatorSubjectContextKey is the unexported context key
// withOperatorSubject/OperatorSubjectFromContext share.
type operatorSubjectContextKey struct{}

// withOperatorSubject returns ctx carrying subject.
func withOperatorSubject(ctx context.Context, subject store.Subject) context.Context {
	return context.WithValue(ctx, operatorSubjectContextKey{}, subject)
}

// OperatorSubjectFromContext returns the signed-in operator's Subject
// requireOperator resolved onto ctx, or false when the route did not run
// through it -- in which case a write handler has no identity to attribute
// with and must not proceed.
func OperatorSubjectFromContext(ctx context.Context) (store.Subject, bool) {
	subject, ok := ctx.Value(operatorSubjectContextKey{}).(store.Subject)
	return subject, ok
}

// requireOperator is the gate this binary's own mutating app routes pass
// through, the same posture api's RequireSession gate takes for agents
// (api/handlers/gate.go): it resolves the operator's Subject once, places
// it on the request context for the handler's write client, and rejects
// with 401 -- never calling the handler -- when the identity does not
// resolve.
func (app *App) requireOperator(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if app.devAuth {
			// AUTH_MODE=none dev stack: the operator is the dev pair and the
			// token api accepts is the static one. With no dev token there is
			// nothing api would authenticate, so the write still refuses --
			// a dev identity must not become a way to write unidentifiably.
			iss, sub, ok := devIdentity()
			if !ok || app.devAPIToken == "" {
				http.Error(w, "unresolved operator identity", http.StatusUnauthorized)
				return
			}
			subject := store.Subject{Iss: iss, Sub: sub, Kind: store.SubjectKindHuman}
			ctx := apiclient.WithUserToken(withOperatorSubject(r.Context(), subject), app.devAPIToken)
			next(w, r.WithContext(ctx))
			return
		}
		subject, ok := app.operatorSubject(r)
		if !ok {
			http.Error(w, "unresolved operator identity", http.StatusUnauthorized)
			return
		}
		// The operator's access token rides every api call so api can
		// verify identity itself; an unrefreshable session must re-login.
		token, err := app.auth.GetAccessToken(r)
		if err != nil {
			http.Error(w, "access token unavailable: sign in again", http.StatusUnauthorized)
			return
		}
		ctx := apiclient.WithUserToken(withOperatorSubject(r.Context(), subject), token)
		next(w, r.WithContext(ctx))
	}
}
