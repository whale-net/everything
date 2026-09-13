//go:build integration

// This file guards issue #2600's Testing section: the full GET /link/whagent
// + POST /link/whagent/confirm flow against a real Postgres, real handlers,
// and a test JWKS server standing in for `ui` -- mirroring
// audience_score_system/web/invite/invite_integration_test.go's shape (see
// that file's package doc comment for the dbtest/embedded-migrations
// pattern this task's Implementation phase wires up here), and this
// package's own link_test.go for the local-key/JWKS-minting pattern (that
// file lives in package link and cannot be imported from here, so the
// handful of helpers it defines -- generate a key, serve a JWKS, sign raw
// claims, tamper a payload -- are duplicated here in package link_test).
// Gated behind `//go:build integration` so `bazel test //...` never
// compiles or runs it; see //libs/go/dbtest's README for why.
package link_test

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/go-jose/go-jose/v4"
	"github.com/go-jose/go-jose/v4/jwt"
	"github.com/google/uuid"
	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/whale-net/everything/audience_score_system/migrate/schema"
	"github.com/whale-net/everything/audience_score_system/store"
	"github.com/whale-net/everything/audience_score_system/web/auth"
	"github.com/whale-net/everything/audience_score_system/web/link"
	"github.com/whale-net/everything/libs/go/dbtest"
	"github.com/whale-net/everything/libs/go/migrate"
)

const (
	testCookieName = "test_ass_session"

	// testUIOrigin stands in for both ASS_WHAGENT_UI_ISSUER and the
	// verified assertion's ReturnURL origin -- main.go wires the exact
	// same configured value into both link.NewVerifier's issuer and
	// link.New's uiOrigin, so tests must too.
	testUIOrigin = "https://ui.example.test"
	testSubject  = "keycloak-sub-1"
	testSubIss   = "https://keycloak.example.test/realms/humans"
)

func testEncKey() [32]byte {
	return sha256.Sum256([]byte("link-integration-test-key"))
}

// linkTestKey is a locally generated Ed25519 key pair standing in for ui's
// signing key -- these tests never depend on a running ui, they mint their
// own assertions and serve their own JWKS (mirrors link_test.go's testKey).
type linkTestKey struct {
	priv ed25519.PrivateKey
	pub  ed25519.PublicKey
	kid  string
}

func newLinkTestKey(t *testing.T) linkTestKey {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	return linkTestKey{priv: priv, pub: pub, kid: "kid-1"}
}

func (k linkTestKey) jwk() jose.JSONWebKey {
	return jose.JSONWebKey{Key: k.pub, KeyID: k.kid, Algorithm: string(jose.EdDSA), Use: "sig"}
}

// mintAssertion signs a well-formed link.Assertion wire payload (see
// verifier.go's wireAssertion) with k, using testUIOrigin/testSubject/
// testSubIss and a fresh random jti/5-minute expiry by default -- callers
// override individual fields (a bad exp, a foreign return_url, ...) via
// overrides.
func mintAssertion(t *testing.T, k linkTestKey, overrides map[string]interface{}) string {
	t.Helper()
	now := time.Now()
	claims := map[string]interface{}{
		"iss":        testUIOrigin,
		"sub":        testSubject,
		"sub_iss":    testSubIss,
		"jti":        uuid.NewString(),
		"exp":        now.Add(5 * time.Minute).Unix(),
		"iat":        now.Unix(),
		"return_url": testUIOrigin + "/callback",
	}
	for k2, v := range overrides {
		claims[k2] = v
	}

	signer, err := jose.NewSigner(
		jose.SigningKey{Algorithm: jose.EdDSA, Key: k.priv},
		(&jose.SignerOptions{}).WithType("JWT").WithHeader("kid", k.kid),
	)
	require.NoError(t, err)
	token, err := jwt.Signed(signer).Claims(claims).Serialize()
	require.NoError(t, err)
	return token
}

// tamperLinkToken edits token's payload segment after signing (changing the
// subject) while leaving the header and signature segments untouched -- a
// genuine tampering attempt, where the signature no longer matches the (now
// different) payload it was computed over. Mirrors link_test.go's
// tamperPayload.
func tamperLinkToken(t *testing.T, token string) string {
	t.Helper()
	parts := strings.Split(token, ".")
	require.Len(t, parts, 3)
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	require.NoError(t, err)
	var claims map[string]interface{}
	require.NoError(t, json.Unmarshal(payload, &claims))
	claims["sub"] = "attacker"
	tampered, err := json.Marshal(claims)
	require.NoError(t, err)
	parts[1] = base64.RawURLEncoding.EncodeToString(tampered)
	return strings.Join(parts, ".")
}

// linkTestStack bundles everything a test in this file needs: a real
// Store/SessionManager over an isolated Postgres (via dbtest), a
// link.Verifier backed by a local key + httptest JWKS server standing in
// for ui, and a router that mirrors main.go's GET /link/whagent + POST
// /link/whagent/confirm wiring (see this file's package doc comment).
type linkTestStack struct {
	store    *store.Store
	sessions *auth.SessionManager
	router   http.Handler
	db       *dbtest.Postgres
	key      linkTestKey
}

func newLinkTestStack(t *testing.T) *linkTestStack {
	t.Helper()
	ctx := context.Background()

	db := dbtest.NewPostgres(ctx, t, dbtest.Options{})

	sqlDB, err := sql.Open("pgx", db.ConnString)
	require.NoError(t, err)
	t.Cleanup(func() { _ = sqlDB.Close() })

	runner := migrate.NewRunner(sqlDB, schema.Migrations, schema.Dir)
	require.NoError(t, runner.Up(), "apply the real embedded schema")

	key := newLinkTestKey(t)
	jwksSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(jose.JSONWebKeySet{Keys: []jose.JSONWebKey{key.jwk()}})
	}))
	t.Cleanup(jwksSrv.Close)

	verifier, err := link.NewVerifier(ctx, jwksSrv.URL, testUIOrigin)
	require.NoError(t, err)

	st := store.New(db.Pool)
	sessions := auth.NewSessionManager(db.Pool, testCookieName, "session-secret", testEncKey())
	a := auth.NewForTests(st.Persons(), sessions)
	lk := link.New(st, sessions, verifier, testUIOrigin)

	mux := http.NewServeMux()
	mux.HandleFunc("GET /link/whagent", lk.HandleShow)
	mux.HandleFunc("POST /link/whagent/confirm", a.RequireSignedIn(lk.HandleConfirm))

	return &linkTestStack{store: st, sessions: sessions, router: mux, db: db, key: key}
}

// newPerson creates a fresh, role-less, session-capable Person -- mirrors
// invite_integration_test.go's identical fixture.
func (s *linkTestStack) newPerson(t *testing.T, ctx context.Context, label string) store.Person {
	t.Helper()
	p, _, err := s.store.Persons().UpsertByGoogleSubject(ctx, "sub-"+label+"-"+uuid.NewString(), label+"@example.com", label)
	require.NoError(t, err)
	return p
}

// sessionCookie establishes a real session row for personID and returns the
// resulting cookie, standing in for a completed sign-in.
func (s *linkTestStack) sessionCookie(t *testing.T, ctx context.Context, personID uuid.UUID) *http.Cookie {
	t.Helper()
	w := httptest.NewRecorder()
	require.NoError(t, s.sessions.Establish(ctx, w, personID.String(), ""))
	return findCookie(t, w.Result().Cookies(), testCookieName)
}

func findCookie(t *testing.T, cookies []*http.Cookie, name string) *http.Cookie {
	t.Helper()
	for _, c := range cookies {
		if c.Name == name {
			return c
		}
	}
	t.Fatalf("cookie %q not found among %d cookies", name, len(cookies))
	return nil
}

// doGet issues a GET against target, optionally carrying cookie.
func (s *linkTestStack) doGet(t *testing.T, target string, cookie *http.Cookie) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, target, nil)
	if cookie != nil {
		req.AddCookie(cookie)
	}
	w := httptest.NewRecorder()
	s.router.ServeHTTP(w, req)
	return w
}

// doPostForm issues a POST with an application/x-www-form-urlencoded body,
// optionally carrying cookie -- how Confirmation's form (views.templ)
// submits the token.
func (s *linkTestStack) doPostForm(t *testing.T, target string, form url.Values, cookie *http.Cookie) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, target, strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	if cookie != nil {
		req.AddCookie(cookie)
	}
	w := httptest.NewRecorder()
	s.router.ServeHTTP(w, req)
	return w
}

// identityCount returns the total row count of person_oidc_identity --
// used to assert a rejected/GET-only request writes nothing.
func (s *linkTestStack) identityCount(t *testing.T, ctx context.Context) int {
	t.Helper()
	var n int
	require.NoError(t, s.db.Pool.QueryRow(ctx, `SELECT count(*) FROM person_oidc_identity`).Scan(&n))
	return n
}

// identityOwner returns the person_id currently linked to (iss, sub), or
// ok=false if no row exists for that pair.
func (s *linkTestStack) identityOwner(t *testing.T, ctx context.Context, iss, sub string) (uuid.UUID, bool) {
	t.Helper()
	var id uuid.UUID
	err := s.db.Pool.QueryRow(ctx, `SELECT person_id FROM person_oidc_identity WHERE iss = $1 AND sub = $2`, iss, sub).Scan(&id)
	if err != nil {
		return uuid.UUID{}, false
	}
	return id, true
}

// ── FR5: valid assertion + signed-in Operator renders confirmation naming
// both sides, with no write yet ──────────────────────────────────────────

func TestHandleShow_ValidAssertionSignedIn_RendersConfirmationBothSidesNoWrite(t *testing.T) {
	ctx := context.Background()
	s := newLinkTestStack(t)
	operator := s.newPerson(t, ctx, "operator-show")
	cookie := s.sessionCookie(t, ctx, operator.ID)

	token := mintAssertion(t, s.key, nil)
	w := s.doGet(t, "/link/whagent?token="+url.QueryEscape(token), cookie)

	require.Equal(t, http.StatusOK, w.Code, "body: %s", w.Body.String())
	body := w.Body.String()
	assert.Contains(t, body, "operator-show", "confirmation must name the signed-in Operator (FR5)")
	assert.Contains(t, body, fmt.Sprintf("%s (%s)", testSubject, testSubIss), "confirmation must name the whagent-net identity from the assertion (FR5)")
	assert.Equal(t, 0, s.identityCount(t, ctx), "GET must never write a person_oidc_identity row")
}

// ── FR6/FR10: confirming a fresh assertion creates the link and redirects
// with the "linked" outcome ────────────────────────────────────────────────

func TestHandleConfirm_Creates_RedirectsLinked(t *testing.T) {
	ctx := context.Background()
	s := newLinkTestStack(t)
	operator := s.newPerson(t, ctx, "operator-create")
	cookie := s.sessionCookie(t, ctx, operator.ID)

	token := mintAssertion(t, s.key, nil)
	w := s.doPostForm(t, "/link/whagent/confirm", url.Values{"token": {token}}, cookie)

	require.Equal(t, http.StatusSeeOther, w.Code, "body: %s", w.Body.String())
	loc, err := url.Parse(w.Header().Get("Location"))
	require.NoError(t, err)
	assert.Equal(t, testUIOrigin, loc.Scheme+"://"+loc.Host)
	assert.Equal(t, "linked", loc.Query().Get("outcome"))

	ownerID, ok := s.identityOwner(t, ctx, testSubIss, testSubject)
	require.True(t, ok, "expected a person_oidc_identity row for the linked pair")
	assert.Equal(t, operator.ID, ownerID)
	assert.Equal(t, 1, s.identityCount(t, ctx))
}

// ── FR9: confirming twice (two separate assertions for the same pair, the
// same Person) yields "already linked" the second time, no duplicate row ──

func TestHandleConfirm_Twice_SecondIsAlreadyLinkedNoDuplicate(t *testing.T) {
	ctx := context.Background()
	s := newLinkTestStack(t)
	operator := s.newPerson(t, ctx, "operator-twice")
	cookie := s.sessionCookie(t, ctx, operator.ID)

	w1 := s.doPostForm(t, "/link/whagent/confirm", url.Values{"token": {mintAssertion(t, s.key, nil)}}, cookie)
	require.Equal(t, http.StatusSeeOther, w1.Code, "body: %s", w1.Body.String())
	loc1, err := url.Parse(w1.Header().Get("Location"))
	require.NoError(t, err)
	require.Equal(t, "linked", loc1.Query().Get("outcome"))

	// A SECOND, distinct assertion (different jti -- a repeat "Link ASS
	// identity" action from whagent-net) for the identical (iss, sub)/
	// Person pair.
	w2 := s.doPostForm(t, "/link/whagent/confirm", url.Values{"token": {mintAssertion(t, s.key, nil)}}, cookie)
	require.Equal(t, http.StatusSeeOther, w2.Code, "body: %s", w2.Body.String())
	loc2, err := url.Parse(w2.Header().Get("Location"))
	require.NoError(t, err)
	assert.Equal(t, "already_linked", loc2.Query().Get("outcome"))

	assert.Equal(t, 1, s.identityCount(t, ctx), "confirming the same pair twice must not create a duplicate row")
}

// ── FR8: an assertion whose pair is already linked to a DIFFERENT Person
// yields "conflict", the existing row is left unchanged ────────────────────

func TestHandleConfirm_LinkedToOtherPerson_Conflict(t *testing.T) {
	ctx := context.Background()
	s := newLinkTestStack(t)
	owner := s.newPerson(t, ctx, "operator-owner")
	other := s.newPerson(t, ctx, "operator-other")

	ownerCookie := s.sessionCookie(t, ctx, owner.ID)
	wOwner := s.doPostForm(t, "/link/whagent/confirm", url.Values{"token": {mintAssertion(t, s.key, nil)}}, ownerCookie)
	require.Equal(t, http.StatusSeeOther, wOwner.Code, "body: %s", wOwner.Body.String())

	otherCookie := s.sessionCookie(t, ctx, other.ID)
	wOther := s.doPostForm(t, "/link/whagent/confirm", url.Values{"token": {mintAssertion(t, s.key, nil)}}, otherCookie)
	require.Equal(t, http.StatusSeeOther, wOther.Code, "body: %s", wOther.Body.String())
	locOther, err := url.Parse(wOther.Header().Get("Location"))
	require.NoError(t, err)
	assert.Equal(t, "conflict", locOther.Query().Get("outcome"))

	ownerID, ok := s.identityOwner(t, ctx, testSubIss, testSubject)
	require.True(t, ok)
	assert.Equal(t, owner.ID, ownerID, "the existing row must remain owned by the original Person -- no merge or reassignment")
	assert.Equal(t, 1, s.identityCount(t, ctx))
}

// ── FR3: tampered signature / expired / already-consumed jti are all
// rejected, writing nothing ────────────────────────────────────────────────

func TestRejectedAssertions_ZeroWrites(t *testing.T) {
	ctx := context.Background()

	t.Run("tampered signature", func(t *testing.T) {
		s := newLinkTestStack(t)
		operator := s.newPerson(t, ctx, "operator-tamper")
		cookie := s.sessionCookie(t, ctx, operator.ID)
		tampered := tamperLinkToken(t, mintAssertion(t, s.key, nil))

		w := s.doGet(t, "/link/whagent?token="+url.QueryEscape(tampered), cookie)
		require.Equal(t, http.StatusSeeOther, w.Code, "body: %s", w.Body.String())
		loc, err := url.Parse(w.Header().Get("Location"))
		require.NoError(t, err)
		assert.Equal(t, "rejected", loc.Query().Get("outcome"))
		assert.Equal(t, 0, s.identityCount(t, ctx))
	})

	t.Run("expired assertion", func(t *testing.T) {
		s := newLinkTestStack(t)
		operator := s.newPerson(t, ctx, "operator-expired")
		cookie := s.sessionCookie(t, ctx, operator.ID)
		token := mintAssertion(t, s.key, map[string]interface{}{"exp": time.Now().Add(-1 * time.Minute).Unix()})

		w := s.doGet(t, "/link/whagent?token="+url.QueryEscape(token), cookie)
		require.Equal(t, http.StatusSeeOther, w.Code, "body: %s", w.Body.String())
		loc, err := url.Parse(w.Header().Get("Location"))
		require.NoError(t, err)
		assert.Equal(t, "rejected", loc.Query().Get("outcome"))
		assert.Equal(t, 0, s.identityCount(t, ctx))
	})

	t.Run("already consumed jti", func(t *testing.T) {
		s := newLinkTestStack(t)
		operator := s.newPerson(t, ctx, "operator-replay")
		cookie := s.sessionCookie(t, ctx, operator.ID)
		jti := uuid.NewString()
		token := mintAssertion(t, s.key, map[string]interface{}{"jti": jti})
		require.NoError(t, s.store.LinkAssertions().Consume(ctx, jti, time.Now().Add(5*time.Minute)))

		w := s.doGet(t, "/link/whagent?token="+url.QueryEscape(token), cookie)
		require.Equal(t, http.StatusSeeOther, w.Code, "body: %s", w.Body.String())
		loc, err := url.Parse(w.Header().Get("Location"))
		require.NoError(t, err)
		assert.Equal(t, "rejected", loc.Query().Get("outcome"))
		assert.Equal(t, 0, s.identityCount(t, ctx))
	})
}

// ── FR4: an unauthenticated GET is routed into sign-in and resumes the
// SAME assertion afterward, no re-mint ─────────────────────────────────────

func TestHandleShow_Unauthenticated_RoutesToSignInAndResumesSameAssertion(t *testing.T) {
	ctx := context.Background()
	s := newLinkTestStack(t)
	token := mintAssertion(t, s.key, nil)
	target := "/link/whagent?token=" + url.QueryEscape(token)

	showW := s.doGet(t, target, nil)
	require.Equal(t, http.StatusFound, showW.Code)
	assert.Equal(t, "/login?next="+url.QueryEscape(target), showW.Header().Get("Location"))

	newVisitor := s.newPerson(t, ctx, "new-visitor-link")
	cookie := s.sessionCookie(t, ctx, newVisitor.ID)

	resumeW := s.doGet(t, target, cookie)
	require.Equal(t, http.StatusOK, resumeW.Code, "body: %s", resumeW.Body.String())
	assert.Contains(t, resumeW.Body.String(), fmt.Sprintf("%s (%s)", testSubject, testSubIss), "landing back must render the confirmation for the SAME assertion, not a fresh one")
	assert.Equal(t, 0, s.identityCount(t, ctx))
}

// ── FR13: an unauthenticated POST is rejected outright, no write ──────────

func TestHandleConfirm_Unauthenticated_Rejected(t *testing.T) {
	ctx := context.Background()
	s := newLinkTestStack(t)
	token := mintAssertion(t, s.key, nil)

	w := s.doPostForm(t, "/link/whagent/confirm", url.Values{"token": {token}}, nil)
	assert.Equal(t, http.StatusFound, w.Code)
	assert.Equal(t, "/login", w.Header().Get("Location"))
	assert.Equal(t, 0, s.identityCount(t, ctx))
}

// ── GET alone never writes, checked directly via row counts ──────────────

func TestHandleShow_NeverWrites(t *testing.T) {
	ctx := context.Background()
	s := newLinkTestStack(t)
	operator := s.newPerson(t, ctx, "operator-neverwrite")
	cookie := s.sessionCookie(t, ctx, operator.ID)
	before := s.identityCount(t, ctx)

	token := mintAssertion(t, s.key, nil)
	w := s.doGet(t, "/link/whagent?token="+url.QueryEscape(token), cookie)
	require.Equal(t, http.StatusOK, w.Code, "body: %s", w.Body.String())

	after := s.identityCount(t, ctx)
	assert.Equal(t, before, after)
	assert.Equal(t, 0, after)
}

// ── FR10's open-redirect guard: a return URL off ui's origin is rejected,
// never redirected to ───────────────────────────────────────────────────

func TestHandleConfirm_ReturnURLWrongOrigin_Rejected(t *testing.T) {
	ctx := context.Background()
	s := newLinkTestStack(t)
	operator := s.newPerson(t, ctx, "operator-wrongorigin")
	cookie := s.sessionCookie(t, ctx, operator.ID)

	token := mintAssertion(t, s.key, map[string]interface{}{"return_url": "https://evil.example.test/callback"})
	w := s.doGet(t, "/link/whagent?token="+url.QueryEscape(token), cookie)

	assert.Equal(t, http.StatusOK, w.Code, "a wrong-origin return URL must render the local Rejected page, not redirect anywhere")
	assert.Empty(t, w.Header().Get("Location"))
	assert.Contains(t, w.Body.String(), "rejected")
	assert.Equal(t, 0, s.identityCount(t, ctx))
}

// ── NFR3: exact log levels, no raw token ever logged ──────────────────────

func TestNFR3LogLevels_NoRawTokenLogged(t *testing.T) {
	ctx := context.Background()
	s := newLinkTestStack(t)
	operator := s.newPerson(t, ctx, "operator-nfr3")
	otherOperator := s.newPerson(t, ctx, "operator-nfr3-other")
	cookie := s.sessionCookie(t, ctx, operator.ID)
	otherCookie := s.sessionCookie(t, ctx, otherOperator.ID)

	prevDefault := slog.Default()
	t.Cleanup(func() { slog.SetDefault(prevDefault) })
	var buf bytes.Buffer
	slog.SetDefault(slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})))

	linkedToken := mintAssertion(t, s.key, nil)
	confirmW := s.doPostForm(t, "/link/whagent/confirm", url.Values{"token": {linkedToken}}, cookie)
	require.Equal(t, http.StatusSeeOther, confirmW.Code, "body: %s", confirmW.Body.String())

	rejectedToken := tamperLinkToken(t, mintAssertion(t, s.key, nil))
	rejectW := s.doGet(t, "/link/whagent?token="+url.QueryEscape(rejectedToken), cookie)
	require.Equal(t, http.StatusSeeOther, rejectW.Code, "body: %s", rejectW.Body.String())

	// A second Person confirming the SAME pair -- FR8's conflict outcome,
	// also required to log at WARNING (NFR3).
	conflictToken := mintAssertion(t, s.key, nil)
	conflictW := s.doPostForm(t, "/link/whagent/confirm", url.Values{"token": {conflictToken}}, otherCookie)
	require.Equal(t, http.StatusSeeOther, conflictW.Code, "body: %s", conflictW.Body.String())

	var infoLine, warnConflictLine string
	warnCount := 0
	for _, line := range strings.Split(strings.TrimSpace(buf.String()), "\n") {
		if line == "" {
			continue
		}
		var rec map[string]any
		require.NoError(t, json.Unmarshal([]byte(line), &rec), "line: %s", line)
		msg, _ := rec["msg"].(string)
		switch rec["level"] {
		case "INFO":
			if msg == "whagent-net identity linked" {
				infoLine = line
			}
		case "WARN":
			warnCount++
			if strings.Contains(msg, "already linked to a different person") {
				warnConflictLine = line
			}
		}
	}

	require.NotEmpty(t, infoLine, "expected an INFO log line for the successful link")
	assert.Contains(t, infoLine, operator.ID.String(), "the INFO line must carry the resolved person_id")
	assert.Contains(t, infoLine, testSubIss)
	assert.Contains(t, infoLine, testSubject)

	assert.GreaterOrEqual(t, warnCount, 2, "both the rejected assertion and the conflict must log at WARNING")
	require.NotEmpty(t, warnConflictLine, "expected a WARNING log line for the conflict outcome")
	assert.Contains(t, warnConflictLine, otherOperator.ID.String())

	full := buf.String()
	assert.NotContains(t, full, linkedToken, "the raw assertion token must never be logged")
	assert.NotContains(t, full, rejectedToken, "the raw assertion token must never be logged")
	assert.NotContains(t, full, conflictToken, "the raw assertion token must never be logged")
}

// TestConsumeMustHappenOnConfirmNotShow is this task's required red/green
// proof: an Operator who opens the confirmation page and abandons it (a
// second GET for the same assertion) must still be able to complete the
// link afterward -- proving HandleConfirm, not HandleShow, owns the
// consuming write. To confirm this test actually guards that split: move
// the h.store.LinkAssertions().Consume call from HandleConfirm into
// HandleShow, rerun
// `bazel test //audience_score_system/web/link:link_handlers_integration_test --test_filter=TestConsumeMustHappenOnConfirmNotShow --test_output=all`
// and observe the second GET (or the final POST) come back rejected
// instead of succeeding; then revert.
func TestConsumeMustHappenOnConfirmNotShow(t *testing.T) {
	ctx := context.Background()
	s := newLinkTestStack(t)
	operator := s.newPerson(t, ctx, "operator-abandon-retry")
	cookie := s.sessionCookie(t, ctx, operator.ID)

	token := mintAssertion(t, s.key, nil)
	target := "/link/whagent?token=" + url.QueryEscape(token)

	firstShow := s.doGet(t, target, cookie)
	require.Equal(t, http.StatusOK, firstShow.Code, "body: %s", firstShow.Body.String())

	// Abandon the confirmation page and retry with the SAME assertion.
	secondShow := s.doGet(t, target, cookie)
	require.Equal(t, http.StatusOK, secondShow.Code, "abandoning and retrying the same assertion must still render the confirmation page")

	confirmW := s.doPostForm(t, "/link/whagent/confirm", url.Values{"token": {token}}, cookie)
	require.Equal(t, http.StatusSeeOther, confirmW.Code, "body: %s", confirmW.Body.String())
	loc, err := url.Parse(confirmW.Header().Get("Location"))
	require.NoError(t, err)
	assert.Equal(t, "linked", loc.Query().Get("outcome"))
}
