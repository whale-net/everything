package main

import (
	"bytes"
	"context"
	"fmt"
	"net/http"

	"github.com/jackc/pgx/v5"

	"github.com/whale-net/everything/libs/go/htmxauth"
)

// adminRole is the one leaflab-local role this milestone uses (FR10). Mirrors
// leaflab/api/server.go's own adminRole constant -- leaflab-ui and
// leaflab-api are separate Go binaries/packages, so this string is
// duplicated rather than shared, same as the wire-safe adminRole surfaced
// via ListBoardsWithStateResponse.caller_is_admin.
const adminRole = "admin"

// bootstrapAdminLockKey is an arbitrary, stable pg_advisory_xact_lock key
// used to serialize concurrent first sign-ins so at most one can win FR10's
// empty-database bootstrap grant (see maybeBootstrapAdmin). Picked once and
// not reused for any other lock in this package.
const bootstrapAdminLockKey = 78341001

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
// only to leaflab_user (plus, on the one FR10 bootstrap path below,
// leaflab_user_role), never board_owner_history or any owner_* column
// (NFR5/NFR6: signing in claims nothing).
//
// FR10's empty-database bootstrap: when the upsert *creates* leaflab_user_id
// (rather than merely refreshing an existing row) and no open admin grant
// exists anywhere yet, the new user is granted 'admin' — see
// maybeBootstrapAdmin. Both the upsert and the conditional grant run inside
// one transaction so two simultaneous first sign-ins cannot both become
// admin (idx_leaflab_user_role_current only protects one user holding the
// role twice, not two different users racing to be first).
func (app *App) upsertLeafLabUser(ctx context.Context, user *htmxauth.UserInfo) error {
	if user == nil || user.Sub == "" {
		return fmt.Errorf("cannot upsert leaflab_user: missing OIDC sub")
	}

	tx, err := app.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin leaflab_user upsert transaction: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }() // no-op once Commit succeeds

	// `xmax = 0` is the standard Postgres idiom for "this row was just
	// inserted, not updated by the ON CONFLICT arm" -- a freshly inserted
	// row's xmax is 0; ON CONFLICT DO UPDATE sets it to the current
	// transaction's id on the pre-existing row instead.
	var leaflabUserID int64
	var created bool
	err = tx.QueryRow(ctx, `
		INSERT INTO leaflab_user (oidc_sub, preferred_username, email, display_name, last_seen_at)
		VALUES ($1, $2, $3, $4, NOW())
		ON CONFLICT (oidc_sub) DO UPDATE SET
			preferred_username = EXCLUDED.preferred_username,
			email = EXCLUDED.email,
			display_name = EXCLUDED.display_name,
			last_seen_at = NOW()
		RETURNING leaflab_user_id, (xmax = 0) AS created
	`, user.Sub, user.PreferredUsername, user.Email, user.Name).Scan(&leaflabUserID, &created)
	if err != nil {
		return fmt.Errorf("failed to upsert leaflab_user: %w", err)
	}

	if created {
		if err := app.maybeBootstrapAdmin(ctx, tx, leaflabUserID, user.Sub); err != nil {
			return err
		}
	}

	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit leaflab_user upsert transaction: %w", err)
	}
	return nil
}

// maybeBootstrapAdmin grants 'admin' to a newly created leaflab_user when no
// open admin grant exists anywhere (FR10's empty-database case; migration
// 016 already handles the non-empty case at migration time by granting the
// earliest existing leaflab_user_id). Must run inside the same transaction
// as the leaflab_user insert that created leaflabUserID -- see
// upsertLeafLabUser's doc comment for why a bare read-then-write is not
// safe here.
//
// pg_advisory_xact_lock is what actually serializes two concurrent first
// sign-ins: idx_leaflab_user_role_current (UNIQUE on
// (leaflab_user_id, role) WHERE valid_to IS NULL) only forbids the *same*
// user from holding the role twice, not two *different* users both winning
// this race under READ COMMITTED. Held for the rest of this transaction
// (pg_advisory_xact_lock, not the session-scoped variant), so a second
// concurrent caller blocks here until the first commits or rolls back, then
// observes its grant via the AnyOpenGrantExists-equivalent check below.
func (app *App) maybeBootstrapAdmin(ctx context.Context, tx pgx.Tx, leaflabUserID int64, sub string) error {
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock($1)`, bootstrapAdminLockKey); err != nil {
		return fmt.Errorf("acquire bootstrap admin lock: %w", err)
	}

	var anyAdminExists bool
	if err := tx.QueryRow(ctx, `
		SELECT EXISTS(
			SELECT 1 FROM leaflab_user_role WHERE role = $1 AND valid_to IS NULL
		)
	`, adminRole).Scan(&anyAdminExists); err != nil {
		return fmt.Errorf("check existing admin grants: %w", err)
	}
	if anyAdminExists {
		return nil
	}

	if _, err := tx.Exec(ctx, `
		INSERT INTO leaflab_user_role (leaflab_user_id, role) VALUES ($1, $2)
	`, leaflabUserID, adminRole); err != nil {
		return fmt.Errorf("grant bootstrap admin role: %w", err)
	}

	// INFO: a notable event that completed normally, not a deviation
	// (AGENTS.md § Logging Levels).
	app.log().Info("bootstrap admin grant fired on first sign-in — no prior admin existed",
		"leaflab_user_id", leaflabUserID, "sub", sub)
	return nil
}
