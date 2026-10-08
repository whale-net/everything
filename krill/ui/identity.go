// Operator identity for this binary's write paths: a Keycloak session becomes
// the (iss, sub) pair a mutation is attributed to. No session, no identity, no
// write; AUTH_MODE=none instead reports api's fixed dev pair.
package main

import (
	"context"
	"net/http"

	"github.com/whale-net/everything/krill/apiclient"
	"github.com/whale-net/everything/krill/identity"
	"github.com/whale-net/everything/krill/mcp/server"
	"github.com/whale-net/everything/krill/store"
)

// operatorIdentity returns the operator's (iss, sub) from the Keycloak session;
// ok is false for a missing, tampered, or expired session. Under AUTH_MODE=none
// it returns the dev pair api's dev token resolves to, so reads and writes agree.
func (app *App) operatorIdentity(r *http.Request) (iss, sub string, ok bool) {
	if app.devAuth {
		return devIdentity()
	}
	user, err := app.auth.CurrentUser(r)
	if err != nil {
		return "", "", false
	}
	// identity.Encode is used only as the pair's validation gate.
	if _, err := identity.Encode(app.oidcIssuer, user.Sub); err != nil {
		return "", "", false
	}
	return app.oidcIssuer, user.Sub, true
}

// operatorSubject is operatorIdentity as a store.Subject, used as both acting
// and on-behalf-of. Persona is resolved separately, never from this.
func (app *App) operatorSubject(r *http.Request) (store.Subject, bool) {
	iss, sub, ok := app.operatorIdentity(r)
	if !ok {
		return store.Subject{}, false
	}
	return store.Subject{Iss: iss, Sub: sub, Kind: store.SubjectKindHuman}, true
}

// operatorEncodedIdentity is operatorIdentity encoded as the opaque Identity
// string auth's credential and auth-code stores keep.
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

// operatorPersona is the persona the operator mints a credential under.
// Under AUTH_MODE=none the dev user is the swarm operator, matching how api
// resolves its dev token; the dev user's roles would otherwise resolve nothing.
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

// devIdentity is the (iss, sub) pair AUTH_MODE=none reports, passed through the
// same encode gate as a real pair.
func devIdentity() (iss, sub string, ok bool) {
	if _, err := identity.Encode(devIssuer, devSubject); err != nil {
		return "", "", false
	}
	return devIssuer, devSubject, true
}

type operatorSubjectContextKey struct{}

func withOperatorSubject(ctx context.Context, subject store.Subject) context.Context {
	return context.WithValue(ctx, operatorSubjectContextKey{}, subject)
}

// OperatorSubjectFromContext returns the Subject requireOperator placed on ctx;
// false means a write handler must not proceed.
func OperatorSubjectFromContext(ctx context.Context) (store.Subject, bool) {
	subject, ok := ctx.Value(operatorSubjectContextKey{}).(store.Subject)
	return subject, ok
}

// requireOperator gates this binary's mutating routes: it resolves the
// operator's Subject and access token onto the context, or responds 401.
func (app *App) requireOperator(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if app.devAuth {
			// Without a dev API token api would authenticate nothing, so refuse rather
			// than write unidentifiably.
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
		// The operator's access token rides every api call so api verifies identity
		// itself; an unrefreshable session must re-login.
		token, err := app.auth.GetAccessToken(r)
		if err != nil {
			http.Error(w, "access token unavailable: sign in again", http.StatusUnauthorized)
			return
		}
		ctx := apiclient.WithUserToken(withOperatorSubject(r.Context(), subject), token)
		next(w, r.WithContext(ctx))
	}
}
