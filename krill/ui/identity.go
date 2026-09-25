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
package main

import (
	"context"
	"net/http"

	"github.com/whale-net/everything/krill/identity"
	"github.com/whale-net/everything/krill/store"
)

// operatorIdentity reads the request's Keycloak session and returns the
// operator's real (iss, sub) pair. ok is false for a missing, tampered, or
// expired session, and for the AuthModeNone dev user (its fixed issuer
// config leaves identity.Encode with an empty iss) -- there is no real pair
// to report without a configured issuer.
func (app *App) operatorIdentity(r *http.Request) (iss, sub string, ok bool) {
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
		subject, ok := app.operatorSubject(r)
		if !ok {
			http.Error(w, "unresolved operator identity", http.StatusUnauthorized)
			return
		}
		next(w, r.WithContext(withOperatorSubject(r.Context(), subject)))
	}
}
