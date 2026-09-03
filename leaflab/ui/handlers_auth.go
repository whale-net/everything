package main

import (
	"bytes"
	"context"
	"fmt"
	"net/http"

	"github.com/whale-net/everything/libs/go/htmxauth"
)

// responseCapture buffers a downstream handler's response so a wrapper can
// inspect its side effects (here: whether a session cookie was set) before
// relaying it, unchanged, to the real http.ResponseWriter.
//
// This exists because htmxauth.Authenticator.HandleCallback has no
// post-login hook (see this task's Scaffold-phase note) — capturing its
// response is how handleAuthCallback observes "did sign-in just succeed"
// without duplicating HandleCallback's OIDC code-exchange/ID-token-verify
// logic locally.
type responseCapture struct {
	header      http.Header
	statusCode  int
	body        bytes.Buffer
	wroteHeader bool
}

func newResponseCapture() *responseCapture {
	return &responseCapture{header: make(http.Header)}
}

func (c *responseCapture) Header() http.Header { return c.header }

func (c *responseCapture) Write(b []byte) (int, error) {
	if !c.wroteHeader {
		c.WriteHeader(http.StatusOK)
	}
	return c.body.Write(b)
}

func (c *responseCapture) WriteHeader(statusCode int) {
	if c.wroteHeader {
		return
	}
	c.statusCode = statusCode
	c.wroteHeader = true
}

// flush relays the captured response to the real ResponseWriter, unchanged.
func (c *responseCapture) flush(w http.ResponseWriter) {
	dst := w.Header()
	for k, vv := range c.header {
		for _, v := range vv {
			dst.Add(k, v)
		}
	}
	if c.statusCode == 0 {
		c.statusCode = http.StatusOK
	}
	w.WriteHeader(c.statusCode)
	_, _ = w.Write(c.body.Bytes())
}

// findSetCookie looks up a Set-Cookie header by name from a captured
// response's headers, reusing net/http's own Set-Cookie parser (via a
// throwaway http.Response) instead of hand-rolling cookie parsing.
func findSetCookie(header http.Header, name string) *http.Cookie {
	resp := http.Response{Header: header}
	for _, c := range resp.Cookies() {
		if c.Name == name {
			return c
		}
	}
	return nil
}

// handleAuthCallback wraps htmxauth.Authenticator.HandleCallback with the
// FR2 leaflab_user upsert-on-sign-in.
//
// HandleCallback sets the session cookie only on its success path
// (sessionStore.SetUserInfo) — every failure path (state mismatch, code
// exchange failure, ID token verification failure) calls http.Error and
// returns before ever reaching SetUserInfo. So capturing HandleCallback's
// response and checking for that cookie is an exact, non-duplicative proxy
// for "an interactive sign-in just succeeded" — the only path FR2 allows to
// mint or refresh a leaflab_user row (hard constraint LB1: no service
// caller, background job, or gRPC handler ever does this).
//
// The incoming request r does not carry the cookie yet (HandleCallback only
// just set it on the outgoing response), so a throwaway request carrying it
// is used to read the session back via htmxauth.Authenticator.CurrentUser.
func (app *App) handleAuthCallback(w http.ResponseWriter, r *http.Request) {
	capture := newResponseCapture()
	app.auth.HandleCallback(capture, r)

	if sessionCookie := findSetCookie(capture.header, sessionName); sessionCookie != nil && sessionCookie.Value != "" {
		// Start from a blank header rather than cloning r.Header: if the
		// browser's original callback request happened to carry a stale
		// cookie under this same name (e.g. an expired prior session still
		// sitting in the browser), appending the fresh value after it would
		// leave DBSessionManager.sessionID reading the *stale* one first —
		// http.Request.Cookie returns the first match. GetUserInfo only
		// needs the Cookie header, so a blank header plus the one cookie
		// HandleCallback just minted is both sufficient and unambiguous.
		withSession := r.Clone(r.Context())
		withSession.Header = make(http.Header)
		withSession.AddCookie(sessionCookie)

		user, err := app.auth.CurrentUser(withSession)
		if err != nil {
			app.log().Warn("FR2 upsert skipped — could not read back signed-in user", "err", err)
		} else if err := app.upsertLeafLabUser(r.Context(), user); err != nil {
			// Best-effort relative to the sign-in itself: a failed upsert
			// must not strand the user on an error page in the middle of
			// an OAuth redirect. The next sign-in retries the upsert.
			app.log().Warn("FR2 leaflab_user upsert failed", "sub", user.Sub, "err", err)
		} else {
			app.log().Info("leaflab_user upserted on sign-in", "sub", user.Sub, "preferred_username", user.PreferredUsername)
		}
	}

	capture.flush(w)
}

// upsertLeafLabUser implements FR2: it resolves the just-signed-in person to
// a leaflab_user row keyed on their OIDC sub, creating it on first sign-in
// and refreshing preferred_username/email/display_name plus last_seen_at on
// every later sign-in. The ON CONFLICT clause makes this idempotent so
// concurrent sign-ins from the same person cannot race two rows into
// existence — see this task's Testing-phase red/green note (remove the
// ON CONFLICT clause and the repeat-sign-in test goes red).
//
// This does not create, assign, or imply ownership of anything — it writes
// only to leaflab_user, never board_owner_history or any owner_* column
// (NFR5/NFR6: signing in claims nothing).
func (app *App) upsertLeafLabUser(ctx context.Context, user *htmxauth.UserInfo) error {
	if user == nil || user.Sub == "" {
		return fmt.Errorf("cannot upsert leaflab_user: missing OIDC sub")
	}

	_, err := app.pool.Exec(ctx, `
		INSERT INTO leaflab_user (oidc_sub, preferred_username, email, display_name, last_seen_at)
		VALUES ($1, $2, $3, $4, NOW())
		ON CONFLICT (oidc_sub) DO UPDATE SET
			preferred_username = EXCLUDED.preferred_username,
			email = EXCLUDED.email,
			display_name = EXCLUDED.display_name,
			last_seen_at = NOW()
	`, user.Sub, user.PreferredUsername, user.Email, user.Name)
	if err != nil {
		return fmt.Errorf("failed to upsert leaflab_user: %w", err)
	}
	return nil
}
