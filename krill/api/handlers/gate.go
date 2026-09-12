// This file (issue #2489, FR3) is the write gate every mutating endpoint in
// this milestone passes through: entity creates (FR1, FR2), LB attach
// (FR4), amend (FR12), import (FR16), and pointer-issue create (FR20) --
// and, per FR3's issue body, no other endpoint. It applies to no read path
// in this milestone -- not FR5-FR9, not FR11, and explicitly not FR21's
// live C3 query (see gate_test.go's red/green coverage of a representative
// ungated read handler once the Testing phase adds it).
package handlers

import (
	"context"
	"errors"
	"net/http"

	"github.com/google/uuid"

	"github.com/whale-net/everything/krill/store"
)

// sessionHeader is the HTTP header a write caller presents its krill
// session id (minted by InitSessionHandler) on. Chosen over a request-body
// field so RequireSession can reject before a handler even parses its own
// body, and so the same header works uniformly across every write
// endpoint's differently-shaped request body.
const sessionHeader = "X-Krill-Session-Id"

// GatedSession is what RequireSession resolves a caller-presented session
// id into and places on the request context -- the two subjects (LB4) and
// the scope (LB1) a write handler attributes its mutation to, so a handler
// never has to re-authenticate or re-derive them itself (mirrors #2490's
// scope description: "resolves the session's acting / on-behalf-of
// subjects, and writes scope_id from the session's scope").
type GatedSession struct {
	SessionID  store.SessionID
	ScopeID    uuid.UUID
	Acting     store.Subject
	OnBehalfOf store.Subject
}

// gatedSessionContextKey is the unexported context key SessionFromContext /
// RequireSession use to carry a resolved GatedSession.
type gatedSessionContextKey struct{}

// SessionFromContext returns the GatedSession RequireSession resolved and
// placed on ctx, or false if none -- e.g. called from a handler not wrapped
// by RequireSession. Every write handler (a later task's #2490/#2492/#2493/
// #2496) calls this to get its attribution subjects and scope; a read
// handler never wraps with RequireSession, so this reports false for it.
func SessionFromContext(ctx context.Context) (GatedSession, bool) {
	sess, ok := ctx.Value(gatedSessionContextKey{}).(GatedSession)
	return sess, ok
}

func withGatedSession(ctx context.Context, sess GatedSession) context.Context {
	return context.WithValue(ctx, gatedSessionContextKey{}, sess)
}

// RequireSession is the write-only gate (FR3): wrap exactly the six write
// endpoints named in this file's doc comment with it, and no read endpoint.
// It requires sessionHeader to name a session InitSessionHandler actually
// minted (via sessions.GetSession); on success it resolves that session's
// two subjects and scope onto the request context (SessionFromContext) and
// calls next. On any failure -- missing header, malformed UUID, or unknown
// session id -- it rejects with 401 and never calls next.
func RequireSession(sessions store.SessionStore) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			raw := r.Header.Get(sessionHeader)
			if raw == "" {
				writeJSONError(w, http.StatusUnauthorized, "missing "+sessionHeader+" header")
				return
			}

			id, err := uuid.Parse(raw)
			if err != nil {
				writeJSONError(w, http.StatusUnauthorized, "invalid "+sessionHeader+" header")
				return
			}

			sess, err := sessions.GetSession(r.Context(), store.SessionID(id))
			if errors.Is(err, store.ErrSessionNotFound) {
				writeJSONError(w, http.StatusUnauthorized, "unknown krill session")
				return
			}
			if err != nil {
				writeJSONError(w, http.StatusInternalServerError, "failed to resolve krill session")
				return
			}

			gated := GatedSession{
				SessionID:  sess.ID,
				ScopeID:    sess.ScopeID,
				Acting:     sess.Acting,
				OnBehalfOf: sess.OnBehalfOf,
			}
			next.ServeHTTP(w, r.WithContext(withGatedSession(r.Context(), gated)))
		})
	}
}
