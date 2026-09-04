//go:build integration

// This file only builds under the "integration" build tag so `bazel test
// //...` (which runs on Docker-less machines too) never compiles or runs
// it. See //libs/go/dbtest's README, audience_score_system/store's
// store_integration_test.go, and audience_score_system/web/auth's
// auth_integration_test.go for the pattern this file follows: spin up a
// throwaway Postgres via dbtest, apply the domain's own real embedded
// migrations (through 004_channel_credentials, #1571), then exercise this
// package's public API (Save/TokenSource/MarkNeedsReauth) against it.
//
// These tests exercise exactly what store_test.go's pure-Go tests cannot:
// the SCD2 close-and-open write path actually hitting Postgres (Save then
// TokenSource round-trips a token; a reconnect closes the prior credential
// and opens exactly one live row), the SELECT ... FOR UPDATE single-flight
// refresh under real concurrency, and the invalid_grant -> needs-reauth /
// transient-error -> retryable mappings actually flipping (or not
// flipping) a real channel.connection_state row -- plus MarkNeedsReauth's
// FR4 data-retention guarantee against real synced_video/video_metrics/
// video_script rows.
//
// tokenStore is built directly (white-box, package tokens) with a
// stubRefresher rather than through NewStore, so no test here ever makes a
// live call to Google -- mirrors web/auth's stubExchanger pattern.
package tokens

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/oauth2"

	"github.com/whale-net/everything/audience_score_system/migrate/schema"
	"github.com/whale-net/everything/audience_score_system/store"
	"github.com/whale-net/everything/libs/go/dbtest"
	"github.com/whale-net/everything/libs/go/migrate"
)

// stubRefresher is a no-network refresher: TokenSource(...).Token() never
// leaves the test process. onToken (if set) runs synchronously inside
// Token(), before returning tok/err, so the concurrency test below can
// force two goroutines' refresh attempts to overlap.
type stubRefresher struct {
	calls   int32
	tok     *oauth2.Token
	err     error
	onToken func()
}

func (r *stubRefresher) TokenSource(_ context.Context, _ *oauth2.Token) oauth2.TokenSource {
	return stubTokenSource{r: r}
}

type stubTokenSource struct{ r *stubRefresher }

func (s stubTokenSource) Token() (*oauth2.Token, error) {
	atomic.AddInt32(&s.r.calls, 1)
	if s.r.onToken != nil {
		s.r.onToken()
	}
	if s.r.err != nil {
		return nil, s.r.err
	}
	return s.r.tok, nil
}

var _ refresher = (*stubRefresher)(nil)

func testEncKeyIntegration() [32]byte { return testEncKey() }

// newTokensTestStack provisions an isolated Postgres database via dbtest,
// applies every migration through 004_channel_credentials, and returns a
// ready *store.Store plus the underlying dbtest.Postgres.
func newTokensTestStack(t *testing.T) (*store.Store, *dbtest.Postgres) {
	t.Helper()
	ctx := context.Background()

	db := dbtest.NewPostgres(ctx, t, dbtest.Options{})

	sqlDB, err := sql.Open("pgx", db.ConnString)
	require.NoError(t, err)
	t.Cleanup(func() { _ = sqlDB.Close() })

	runner := migrate.NewRunner(sqlDB, schema.Migrations, schema.Dir)
	require.NoError(t, runner.Up(), "apply every migration, including 004_channel_credentials")

	return store.New(db.Pool), db
}

// newTokenStore builds a tokenStore against db's pool with r as its
// refresher -- white-box construction (package tokens) so tests can inject
// a no-network stub, mirroring NewStore's real wiring in store.go.
func newTokenStore(st *store.Store, db *dbtest.Postgres, r refresher) tokenStore {
	return tokenStore{
		pool:      db.Pool,
		channels:  st.Channels(),
		encKey:    testEncKeyIntegration(),
		refresher: r,
	}
}

// setupChannel creates a Person and a Channel it is the creator of
// (mirrors store package's own setupChannel fixture, duplicated here since
// it is unexported in that package).
func setupChannel(t *testing.T, ctx context.Context, st *store.Store) (store.Channel, store.Person) {
	t.Helper()

	creator, _, err := st.Persons().UpsertByGoogleSubject(ctx, "sub-"+uuid.NewString(), "creator@example.com", "Creator")
	require.NoError(t, err)

	ch, err := st.Channels().Create(ctx, "yt-"+uuid.NewString(), "Channel", creator.ID)
	require.NoError(t, err)

	return ch, creator
}

func farFutureToken() *oauth2.Token {
	return &oauth2.Token{
		AccessToken:  "initial-access-token",
		RefreshToken: "initial-refresh-token",
		Expiry:       time.Now().Add(time.Hour),
	}
}

// ── Save then TokenSource round-trips a token; ciphertext has no plaintext ──

func TestSaveThenTokenSource_RoundTripsAndCiphertextHasNoPlaintextSubstring(t *testing.T) {
	ctx := context.Background()
	st, db := newTokensTestStack(t)
	ch, creator := setupChannel(t, ctx, st)

	s := newTokenStore(st, db, &stubRefresher{})

	tok := farFutureToken()
	require.NoError(t, s.Save(ctx, ch.ID, creator.ID, tok, []string{"scope-a", "scope-b"}))

	src, err := s.TokenSource(ctx, ch.ID)
	require.NoError(t, err)
	got, err := src.Token()
	require.NoError(t, err)
	assert.Equal(t, tok.AccessToken, got.AccessToken, "TokenSource must round-trip the exact access token Save persisted (not-yet-expired, no refresh triggered)")

	var accessCiphertext, refreshCiphertext []byte
	var scopes []string
	require.NoError(t, db.Pool.QueryRow(ctx, `
		SELECT access_token_ciphertext, refresh_token_ciphertext, scopes
		FROM channel_credential WHERE channel_id = $1 AND valid_to IS NULL
	`, ch.ID).Scan(&accessCiphertext, &refreshCiphertext, &scopes))

	assert.NotContains(t, string(accessCiphertext), tok.AccessToken, "access_token_ciphertext must never contain the plaintext access token")
	assert.NotContains(t, string(refreshCiphertext), tok.RefreshToken, "refresh_token_ciphertext must never contain the plaintext refresh token")
	assert.Equal(t, []string{"scope-a", "scope-b"}, scopes)
}

// ── Reconnect: closes the prior credential, opens exactly one live row ──────

func TestSave_Reconnect_ClosesPriorCredentialAndOpensExactlyOneLiveRow(t *testing.T) {
	ctx := context.Background()
	st, db := newTokensTestStack(t)
	ch, creator := setupChannel(t, ctx, st)

	s := newTokenStore(st, db, &stubRefresher{})

	first := &oauth2.Token{AccessToken: "first-access", RefreshToken: "first-refresh", Expiry: time.Now().Add(time.Hour)}
	require.NoError(t, s.Save(ctx, ch.ID, creator.ID, first, []string{"scope-a"}))

	second := &oauth2.Token{AccessToken: "second-access", RefreshToken: "second-refresh", Expiry: time.Now().Add(time.Hour)}
	require.NoError(t, s.Save(ctx, ch.ID, creator.ID, second, []string{"scope-a"}))

	var totalRows, liveRows int
	require.NoError(t, db.Pool.QueryRow(ctx, `SELECT count(*) FROM channel_credential WHERE channel_id = $1`, ch.ID).Scan(&totalRows))
	require.NoError(t, db.Pool.QueryRow(ctx, `SELECT count(*) FROM channel_credential WHERE channel_id = $1 AND valid_to IS NULL`, ch.ID).Scan(&liveRows))
	assert.Equal(t, 2, totalRows, "a reconnect must not delete the prior credential row (audit history)")
	assert.Equal(t, 1, liveRows, "exactly one live (valid_to IS NULL) credential row must exist after a reconnect")

	src, err := s.TokenSource(ctx, ch.ID)
	require.NoError(t, err)
	got, err := src.Token()
	require.NoError(t, err)
	assert.Equal(t, "second-access", got.AccessToken, "TokenSource must resolve the newly-opened (second) credential, not the closed one")
}

// ── TokenSource refreshes an expiring token and persists the new one ────────

func TestTokenSource_NearExpiry_RefreshesAndPersistsNewToken(t *testing.T) {
	ctx := context.Background()
	st, db := newTokensTestStack(t)
	ch, creator := setupChannel(t, ctx, st)

	refresher := &stubRefresher{tok: &oauth2.Token{AccessToken: "refreshed-access", RefreshToken: "refreshed-refresh", Expiry: time.Now().Add(time.Hour)}}
	s := newTokenStore(st, db, refresher)

	nearExpiry := &oauth2.Token{AccessToken: "stale-access", RefreshToken: "stale-refresh", Expiry: time.Now().Add(time.Minute)}
	require.NoError(t, s.Save(ctx, ch.ID, creator.ID, nearExpiry, []string{"scope-a"}))

	src, err := s.TokenSource(ctx, ch.ID)
	require.NoError(t, err)
	got, err := src.Token()
	require.NoError(t, err)

	assert.Equal(t, "refreshed-access", got.AccessToken, "a token within refreshSkew of expiry must be refreshed")
	assert.Equal(t, int32(1), atomic.LoadInt32(&refresher.calls))

	var expiresAt time.Time
	require.NoError(t, db.Pool.QueryRow(ctx, `
		SELECT access_token_expires_at FROM channel_credential WHERE channel_id = $1 AND valid_to IS NULL
	`, ch.ID).Scan(&expiresAt))
	assert.True(t, expiresAt.After(time.Now().Add(30*time.Minute)), "the refreshed expiry must be persisted, not the stale one")
}

// ── invalid_grant maps to needs-reauth; credential row is retained ──────────

func TestTokenSource_InvalidGrant_MapsToNeedsReauthAndRetainsCredentialRow(t *testing.T) {
	ctx := context.Background()
	st, db := newTokensTestStack(t)
	ch, creator := setupChannel(t, ctx, st)

	refresher := &stubRefresher{err: &oauth2.RetrieveError{
		Response:  &http.Response{StatusCode: http.StatusBadRequest},
		ErrorCode: "invalid_grant",
	}}
	s := newTokenStore(st, db, refresher)

	expired := &oauth2.Token{AccessToken: "stale-access", RefreshToken: "stale-refresh", Expiry: time.Now().Add(-time.Minute)}
	require.NoError(t, s.Save(ctx, ch.ID, creator.ID, expired, []string{"scope-a"}))

	src, err := s.TokenSource(ctx, ch.ID)
	require.NoError(t, err)
	_, err = src.Token()
	require.Error(t, err, "invalid_grant must surface as an error to the caller")

	var connState store.ConnectionState
	require.NoError(t, db.Pool.QueryRow(ctx, `SELECT connection_state FROM channel WHERE id = $1`, ch.ID).Scan(&connState))
	assert.Equal(t, store.ConnectionStateNeedsReauth, connState, "invalid_grant must flip connection_state to needs_reauth (FR4)")

	var credentialRows int
	require.NoError(t, db.Pool.QueryRow(ctx, `SELECT count(*) FROM channel_credential WHERE channel_id = $1`, ch.ID).Scan(&credentialRows))
	assert.Equal(t, 1, credentialRows, "MarkNeedsReauth must NOT delete the credential row (FR4 data retention)")
}

// ── transient (retryable) errors leave connection_state untouched ───────────

func TestTokenSource_TransientNetworkError_LeavesConnectionStateConnected(t *testing.T) {
	ctx := context.Background()
	st, db := newTokensTestStack(t)
	ch, creator := setupChannel(t, ctx, st)

	refresher := &stubRefresher{err: errors.New("dial tcp: connection refused")}
	s := newTokenStore(st, db, refresher)

	expired := &oauth2.Token{AccessToken: "stale-access", RefreshToken: "stale-refresh", Expiry: time.Now().Add(-time.Minute)}
	require.NoError(t, s.Save(ctx, ch.ID, creator.ID, expired, []string{"scope-a"}))

	src, err := s.TokenSource(ctx, ch.ID)
	require.NoError(t, err)
	_, err = src.Token()
	require.Error(t, err, "a transient refresh failure must still surface as an error to the caller")

	var connState store.ConnectionState
	require.NoError(t, db.Pool.QueryRow(ctx, `SELECT connection_state FROM channel WHERE id = $1`, ch.ID).Scan(&connState))
	assert.Equal(t, store.ConnectionStateConnected, connState, "a transient/retryable error must NOT trip needs-reauth -- connection_state stays connected")
}

func TestTokenSource_5xxRetrieveError_LeavesConnectionStateConnected(t *testing.T) {
	ctx := context.Background()
	st, db := newTokensTestStack(t)
	ch, creator := setupChannel(t, ctx, st)

	refresher := &stubRefresher{err: &oauth2.RetrieveError{
		Response:  &http.Response{StatusCode: http.StatusServiceUnavailable},
		ErrorCode: "",
	}}
	s := newTokenStore(st, db, refresher)

	expired := &oauth2.Token{AccessToken: "stale-access", RefreshToken: "stale-refresh", Expiry: time.Now().Add(-time.Minute)}
	require.NoError(t, s.Save(ctx, ch.ID, creator.ID, expired, []string{"scope-a"}))

	src, err := s.TokenSource(ctx, ch.ID)
	require.NoError(t, err)
	_, err = src.Token()
	require.Error(t, err)

	var connState store.ConnectionState
	require.NoError(t, db.Pool.QueryRow(ctx, `SELECT connection_state FROM channel WHERE id = $1`, ch.ID).Scan(&connState))
	assert.Equal(t, store.ConnectionStateConnected, connState, "a 503 (no invalid_grant error code) must be treated as retryable")
}

// ── MarkNeedsReauth retains synced_video/video_metrics/video_script rows ──

func TestMarkNeedsReauth_FlipsStateAndRetainsSyncedDataRows(t *testing.T) {
	ctx := context.Background()
	st, db := newTokensTestStack(t)
	ch, creator := setupChannel(t, ctx, st)

	s := newTokenStore(st, db, &stubRefresher{})

	// Build the full LB3 chain (idea -> viable verdict -> proposed
	// video_script) plus a synced_video/video_metrics pair, so this test
	// proves MarkNeedsReauth touches none of FR4's protected tables.
	idea, err := st.Ideas().Create(ctx, ch.ID, "Idea title", creator.ID)
	require.NoError(t, err)

	verdict, err := st.Verdicts().Append(ctx, store.AppendVerdictInput{
		IdeaID:         idea.ID,
		Verdict:        store.VerdictViable,
		Reasoning:      "looks good",
		AuthorPersonID: creator.ID,
	})
	require.NoError(t, err)

	strategy, err := st.Strategies().Save(ctx, store.SaveStrategyInput{
		ChannelID:         ch.ID,
		Title:             "Idea title Strategy",
		Active:            true,
		VerdictIDs:        []uuid.UUID{verdict.ID},
		CreatedByPersonID: creator.ID,
	})
	require.NoError(t, err)

	script, err := st.VideoScripts().Propose(ctx, store.ProposeVideoScriptInput{
		ChannelID:         ch.ID,
		VerdictID:         verdict.ID,
		StrategyID:        strategy.ID,
		Title:             "Idea title",
		ScriptText:        "script text",
		CreatedByPersonID: creator.ID,
	})
	require.NoError(t, err)

	require.NoError(t, st.Sync().UpsertVideos(ctx, ch.ID, []store.SyncedVideo{{
		YouTubeVideoID: "yt-video-1",
		Title:          "A video",
		PrivacyStatus:  store.PrivacyStatusPublic,
		LastSyncedAt:   time.Now(),
	}}))
	videos, _, err := st.Sync().ListSchedule(ctx, ch.ID, nil, nil, true, 0)
	require.NoError(t, err)
	require.Len(t, videos, 1)

	require.NoError(t, st.Sync().UpsertMetrics(ctx, []store.VideoMetrics{{
		SyncedVideoID: videos[0].ID,
		MeasuredAt:    time.Now(),
	}}))

	require.NoError(t, s.MarkNeedsReauth(ctx, ch.ID, "invalid_grant"))

	var connState store.ConnectionState
	require.NoError(t, db.Pool.QueryRow(ctx, `SELECT connection_state FROM channel WHERE id = $1`, ch.ID).Scan(&connState))
	assert.Equal(t, store.ConnectionStateNeedsReauth, connState)

	var scriptCount, videoCount, metricsCount int
	require.NoError(t, db.Pool.QueryRow(ctx, `SELECT count(*) FROM video_script WHERE id = $1`, script.ID).Scan(&scriptCount))
	require.NoError(t, db.Pool.QueryRow(ctx, `SELECT count(*) FROM synced_video WHERE channel_id = $1`, ch.ID).Scan(&videoCount))
	require.NoError(t, db.Pool.QueryRow(ctx, `SELECT count(*) FROM video_metrics WHERE synced_video_id = $1`, videos[0].ID).Scan(&metricsCount))
	assert.Equal(t, 1, scriptCount, "MarkNeedsReauth must not delete/touch video_script rows (FR4)")
	assert.Equal(t, 1, videoCount, "MarkNeedsReauth must not delete/touch synced_video rows (FR4)")
	assert.Equal(t, 1, metricsCount, "MarkNeedsReauth must not delete/touch video_metrics rows (FR4)")
}

// ── Concurrency: two goroutines refreshing an expired credential perform ────
// ── exactly one refresh (single-flight via SELECT ... FOR UPDATE) ──────────

func TestTokenSource_ConcurrentCallers_ExactlyOneRefresh(t *testing.T) {
	ctx := context.Background()
	st, db := newTokensTestStack(t)
	ch, creator := setupChannel(t, ctx, st)

	// onToken sleeps briefly so, if the row lock did NOT serialize the two
	// callers, the second goroutine's refresh call would race in before the
	// first commits -- making this test a genuine guard against a
	// regression to double-refresh, not just a happy-path smoke test.
	refresher := &stubRefresher{
		tok:     &oauth2.Token{AccessToken: "refreshed-access", RefreshToken: "refreshed-refresh", Expiry: time.Now().Add(time.Hour)},
		onToken: func() { time.Sleep(50 * time.Millisecond) },
	}
	s := newTokenStore(st, db, refresher)

	expired := &oauth2.Token{AccessToken: "stale-access", RefreshToken: "stale-refresh", Expiry: time.Now().Add(-time.Minute)}
	require.NoError(t, s.Save(ctx, ch.ID, creator.ID, expired, []string{"scope-a"}))

	var wg sync.WaitGroup
	errs := make([]error, 2)
	toks := make([]*oauth2.Token, 2)
	for i := range 2 {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			src, err := s.TokenSource(ctx, ch.ID)
			if err != nil {
				errs[i] = err
				return
			}
			toks[i], errs[i] = src.Token()
		}(i)
	}
	wg.Wait()

	require.NoError(t, errs[0])
	require.NoError(t, errs[1])
	assert.Equal(t, "refreshed-access", toks[0].AccessToken)
	assert.Equal(t, "refreshed-access", toks[1].AccessToken)
	assert.Equal(t, int32(1), atomic.LoadInt32(&refresher.calls), "two concurrent callers on an expired credential must perform exactly one refresh (single-flight via SELECT ... FOR UPDATE)")
}
