// Pure-Go tests for the YouTube Channel-connect OAuth grant (C2: FR3, FR4,
// NFR1, NFR5) -- no Docker, no live Google/YouTube calls, runs as part of
// `bazel test //...`.
//
// HandleCallback's success paths (ChannelStore.Create / tokens.Store.Save
// actually persisting) are exercised against fakes here, not a real
// Postgres -- see store's and tokens' own Postgres-backed integration
// tests for the real SQL-backed guarantees (unique partial index, SCD2
// close-and-open, etc). What THIS file proves is everything
// Postgres-independent: the consent URL's exact scope set/access_type/
// state binding (NFR1), the CanReconnect gate rejecting an Analyst with no
// state change (FR4, NFR5) both on HandleReconnect and (defense in depth)
// on HandleCallback's reconnect branch, the fresh-connect vs. reconnect
// branch selection, and the documented Create-then-Save compensation path
// (MarkNeedsReauth on a Save failure right after a fresh Create -- see
// channel.go's HandleCallback doc comment on why Create and Save are not
// one literal SQL transaction).
package channel

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/oauth2"
	"golang.org/x/oauth2/google"

	"github.com/whale-net/everything/audience_score_system/store"
	"github.com/whale-net/everything/audience_score_system/tokens"
	"github.com/whale-net/everything/audience_score_system/web/auth"
)

const (
	testClientID   = "test-client-id"
	testCookieName = "test_session"
)

func testSessions() *auth.SessionManager {
	return auth.NewSessionManager(nil, testCookieName, "session-secret", [32]byte{})
}

// ── stub oauth2Exchanger / channelResolver ─────────────────────────────────

type stubExchanger struct {
	authURL string
	token   *oauth2.Token
	err     error

	gotState string
	gotCode  string
	calls    int
}

func (s *stubExchanger) AuthCodeURL(state string, _ ...oauth2.AuthCodeOption) string {
	s.calls++
	s.gotState = state
	return s.authURL
}

func (s *stubExchanger) Exchange(_ context.Context, code string, _ ...oauth2.AuthCodeOption) (*oauth2.Token, error) {
	s.gotCode = code
	return s.token, s.err
}

var _ oauth2Exchanger = (*stubExchanger)(nil)

type stubResolver struct {
	youtubeChannelID, title string
	err                     error
}

func (s stubResolver) ResolveOwnChannel(_ context.Context, _ *oauth2.Token) (string, string, error) {
	return s.youtubeChannelID, s.title, s.err
}

var _ channelResolver = stubResolver{}

// ── fake store.ChannelStore ─────────────────────────────────────────────

type fakeChannelStore struct {
	createCalled                        bool
	createYoutubeChannelID, createTitle string
	createCreatorPersonID               uuid.UUID
	createResult                        store.Channel
	createErr                           error

	getByYouTubeChannelIDCalled bool
	getByYouTubeChannelIDResult store.Channel
	getByYouTubeChannelIDErr    error

	setConnectionStateCalled    bool
	setConnectionStateChannelID uuid.UUID
	setConnectionStateValue     store.ConnectionState
	setConnectionStateErr       error
}

func (f *fakeChannelStore) Create(_ context.Context, youtubeChannelID, title string, creatorPersonID uuid.UUID) (store.Channel, error) {
	f.createCalled = true
	f.createYoutubeChannelID = youtubeChannelID
	f.createTitle = title
	f.createCreatorPersonID = creatorPersonID
	return f.createResult, f.createErr
}

func (f *fakeChannelStore) GetByID(_ context.Context, _ uuid.UUID) (store.Channel, error) {
	return store.Channel{}, nil
}

func (f *fakeChannelStore) GetByYouTubeChannelID(_ context.Context, _ string) (store.Channel, error) {
	f.getByYouTubeChannelIDCalled = true
	return f.getByYouTubeChannelIDResult, f.getByYouTubeChannelIDErr
}

func (f *fakeChannelStore) SetConnectionState(_ context.Context, channelID uuid.UUID, state store.ConnectionState) error {
	f.setConnectionStateCalled = true
	f.setConnectionStateChannelID = channelID
	f.setConnectionStateValue = state
	return f.setConnectionStateErr
}

func (f *fakeChannelStore) ListConnected(_ context.Context) ([]store.Channel, error) {
	return nil, nil
}

var _ store.ChannelStore = (*fakeChannelStore)(nil)

// ── fake store.RoleStore ─────────────────────────────────────────────────

type fakeRoleStore struct {
	roles []store.Role
	err   error
}

func (f *fakeRoleStore) RolesFor(_ context.Context, _, _ uuid.UUID) ([]store.Role, error) {
	return f.roles, f.err
}

func (f *fakeRoleStore) AddRole(_ context.Context, _, _ uuid.UUID, _ store.Role, _ uuid.UUID) error {
	return nil
}

func (f *fakeRoleStore) RemoveRole(_ context.Context, _, _, _ uuid.UUID) (bool, error) {
	return false, nil
}

func (f *fakeRoleStore) ChannelsForPerson(_ context.Context, _ uuid.UUID) ([]store.Channel, error) {
	return nil, nil
}

var _ store.RoleStore = (*fakeRoleStore)(nil)

// ── fake tokens.Store ────────────────────────────────────────────────────

type fakeTokenStore struct {
	saveCalled                    bool
	saveChannelID, saveByPersonID uuid.UUID
	saveTok                       *oauth2.Token
	saveScopes                    []string
	saveErr                       error

	markNeedsReauthCalled    bool
	markNeedsReauthChannelID uuid.UUID
	markNeedsReauthReason    string
	markNeedsReauthErr       error
}

func (f *fakeTokenStore) TokenSource(_ context.Context, _ uuid.UUID) (oauth2.TokenSource, error) {
	return nil, nil
}

func (f *fakeTokenStore) Save(_ context.Context, channelID, byPersonID uuid.UUID, tok *oauth2.Token, scopes []string) error {
	f.saveCalled = true
	f.saveChannelID = channelID
	f.saveByPersonID = byPersonID
	f.saveTok = tok
	f.saveScopes = scopes
	return f.saveErr
}

func (f *fakeTokenStore) MarkNeedsReauth(_ context.Context, channelID uuid.UUID, reason string) error {
	f.markNeedsReauthCalled = true
	f.markNeedsReauthChannelID = channelID
	f.markNeedsReauthReason = reason
	return f.markNeedsReauthErr
}

var _ tokens.Store = (*fakeTokenStore)(nil)

// ── stub scheduleManager ─────────────────────────────────────────────────

// stubScheduleManager mirrors stubExchanger/stubResolver above -- a
// substitute for the real sync.NewScheduleManager-backed implementation
// ../main.go wires in, so no test here makes a live Temporal call (issue
// #1614, FR14/NFR4).
type stubScheduleManager struct {
	calls []uuid.UUID
	err   error
}

func (s *stubScheduleManager) EnsureSchedule(_ context.Context, channelID uuid.UUID) error {
	s.calls = append(s.calls, channelID)
	return s.err
}

var _ scheduleManager = (*stubScheduleManager)(nil)

// ── test helpers ─────────────────────────────────────────────────────────

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

func withPerson(r *http.Request, p *store.Person) *http.Request {
	return r.WithContext(auth.ContextWithPerson(r.Context(), p))
}

// ── HandleConnect: consent URL (NFR1) ───────────────────────────────────

func TestHandleConnect_AuthURLHasExactScopesAccessTypeOfflinePromptConsentAndBoundState(t *testing.T) {
	sessions := testSessions()
	h := &Handler{
		config:   Config{ClientID: testClientID},
		sessions: sessions,
		oauth2Config: &oauth2.Config{
			ClientID:    testClientID,
			RedirectURL: "https://example.com/oauth/youtube/callback",
			Endpoint:    google.Endpoint,
			Scopes:      Scopes,
		},
	}

	req := httptest.NewRequest(http.MethodGet, "/channels/connect", nil)
	w := httptest.NewRecorder()
	h.HandleConnect(w, req)

	require.Equal(t, http.StatusFound, w.Code)
	loc := w.Header().Get("Location")
	require.NotEmpty(t, loc)

	u, err := url.Parse(loc)
	require.NoError(t, err)
	q := u.Query()

	assert.Equal(t, "offline", q.Get("access_type"), "must request access_type=offline to reliably get a refresh token")
	assert.Equal(t, "consent", q.Get("prompt"), "must force prompt=consent so a repeat consent still yields a refresh token")

	gotScopes := strings.Fields(q.Get("scope"))
	assert.ElementsMatch(t, Scopes, gotScopes, "the consent URL's scope set must be EXACTLY the documented NFR1/LB1 scopes -- no more, no less")

	state := q.Get("state")
	require.NotEmpty(t, state, "the consent URL must carry a CSRF state nonce")

	cookie := findCookie(t, w.Result().Cookies(), testCookieName+"_oauth")
	verifyReq := httptest.NewRequest(http.MethodGet, "/oauth/youtube/callback?state="+state, nil)
	verifyReq.AddCookie(cookie)
	valid, err := sessions.VerifyOAuthState(verifyReq, state)
	require.NoError(t, err)
	assert.True(t, valid, "the state nonce in the consent URL must be the same one bound to the session cookie")
}

func TestHandleConnect_TwoCalls_ProduceDifferentStateNonces(t *testing.T) {
	h := &Handler{
		config:   Config{ClientID: testClientID},
		sessions: testSessions(),
		oauth2Config: &oauth2.Config{
			ClientID: testClientID,
			Endpoint: google.Endpoint,
			Scopes:   Scopes,
		},
	}

	w1 := httptest.NewRecorder()
	h.HandleConnect(w1, httptest.NewRequest(http.MethodGet, "/channels/connect", nil))
	u1, err := url.Parse(w1.Header().Get("Location"))
	require.NoError(t, err)

	w2 := httptest.NewRecorder()
	h.HandleConnect(w2, httptest.NewRequest(http.MethodGet, "/channels/connect", nil))
	u2, err := url.Parse(w2.Header().Get("Location"))
	require.NoError(t, err)

	assert.NotEqual(t, u1.Query().Get("state"), u2.Query().Get("state"), "each consent round trip must get a fresh CSRF nonce")
}

// ── HandleReconnect: CanReconnect gate (FR4, NFR5) ──────────────────────

func TestHandleReconnect_AnalystNoLiveCreatorRow_Forbidden_NoStateChange(t *testing.T) {
	roles := &fakeRoleStore{roles: []store.Role{store.RoleAnalyst}}
	exch := &stubExchanger{authURL: "https://accounts.google.com/o/oauth2/v2/auth"}
	h := &Handler{
		roles:        roles,
		sessions:     testSessions(),
		oauth2Config: exch,
	}

	person := &store.Person{ID: uuid.New()}
	channelID := uuid.New()
	req := withPerson(httptest.NewRequest(http.MethodPost, "/channels/"+channelID.String()+"/reconnect", nil), person)
	req.SetPathValue("id", channelID.String())
	w := httptest.NewRecorder()
	h.HandleReconnect(w, req)

	assert.Equal(t, http.StatusForbidden, w.Code, "an Analyst (no live creator row) must get 403")
	assert.Equal(t, 0, exch.calls, "must never reach AuthCodeURL for a forbidden reconnect")
	assert.Empty(t, w.Result().Cookies(), "no oauth-state cookie may be set for a forbidden reconnect (no state change)")
}

func TestHandleReconnect_Creator_RedirectsToConsent(t *testing.T) {
	roles := &fakeRoleStore{roles: []store.Role{store.RoleCreator}}
	exch := &stubExchanger{authURL: "https://accounts.google.com/o/oauth2/v2/auth?scope=x"}
	h := &Handler{
		roles:        roles,
		sessions:     testSessions(),
		oauth2Config: exch,
	}

	person := &store.Person{ID: uuid.New()}
	channelID := uuid.New()
	req := withPerson(httptest.NewRequest(http.MethodPost, "/channels/"+channelID.String()+"/reconnect", nil), person)
	req.SetPathValue("id", channelID.String())
	w := httptest.NewRecorder()
	h.HandleReconnect(w, req)

	assert.Equal(t, http.StatusFound, w.Code, "a live Creator must be redirected to consent")
	assert.Equal(t, exch.authURL, w.Header().Get("Location"))
	assert.Equal(t, 1, exch.calls)
}

func TestHandleReconnect_NotSignedIn_Unauthorized(t *testing.T) {
	h := &Handler{roles: &fakeRoleStore{}, sessions: testSessions()}

	channelID := uuid.New()
	req := httptest.NewRequest(http.MethodPost, "/channels/"+channelID.String()+"/reconnect", nil)
	req.SetPathValue("id", channelID.String())
	w := httptest.NewRecorder()
	h.HandleReconnect(w, req)

	assert.Equal(t, http.StatusUnauthorized, w.Code)
}

func TestHandleReconnect_InvalidChannelID_BadRequest(t *testing.T) {
	h := &Handler{roles: &fakeRoleStore{}, sessions: testSessions()}

	person := &store.Person{ID: uuid.New()}
	req := withPerson(httptest.NewRequest(http.MethodPost, "/channels/not-a-uuid/reconnect", nil), person)
	req.SetPathValue("id", "not-a-uuid")
	w := httptest.NewRecorder()
	h.HandleReconnect(w, req)

	assert.Equal(t, http.StatusBadRequest, w.Code)
}

// ── HandleCallback: CSRF state ───────────────────────────────────────────

func callbackWithValidState(t *testing.T, sessions *auth.SessionManager, code string) *http.Request {
	t.Helper()
	setupW := httptest.NewRecorder()
	require.NoError(t, sessions.SetOAuthState(setupW, httptest.NewRequest(http.MethodGet, "/channels/connect", nil), "known-state", "/"))
	cookie := findCookie(t, setupW.Result().Cookies(), testCookieName+"_oauth")

	req := httptest.NewRequest(http.MethodGet, "/oauth/youtube/callback?state=known-state&code="+code, nil)
	req.AddCookie(cookie)
	return req
}

func TestHandleCallback_StateMismatch_Rejected(t *testing.T) {
	sessions := testSessions()
	sched := &stubScheduleManager{}
	h := &Handler{sessions: sessions, oauth2Config: &stubExchanger{}, schedules: sched}

	person := &store.Person{ID: uuid.New()}
	req := withPerson(callbackWithValidState(t, sessions, "abc"), person)
	req.URL.RawQuery = "state=wrong-state&code=abc"
	w := httptest.NewRecorder()
	h.HandleCallback(w, req)

	assert.Equal(t, http.StatusBadRequest, w.Code)
	assert.Empty(t, sched.calls, "a bad-state callback must never reach EnsureSchedule")
}

func TestHandleCallback_NotSignedIn_Unauthorized(t *testing.T) {
	sessions := testSessions()
	sched := &stubScheduleManager{}
	h := &Handler{sessions: sessions, oauth2Config: &stubExchanger{}, schedules: sched}

	req := callbackWithValidState(t, sessions, "abc")
	w := httptest.NewRecorder()
	h.HandleCallback(w, req)

	assert.Equal(t, http.StatusUnauthorized, w.Code)
	assert.Empty(t, sched.calls, "an unsigned-in callback must never reach EnsureSchedule")
}

func TestHandleCallback_NoRefreshTokenFromGoogle_Rejected(t *testing.T) {
	sessions := testSessions()
	exch := &stubExchanger{token: &oauth2.Token{AccessToken: "at"}} // no RefreshToken
	channels := &fakeChannelStore{}
	sched := &stubScheduleManager{}
	h := &Handler{
		sessions:     sessions,
		oauth2Config: exch,
		channels:     channels,
		roles:        &fakeRoleStore{},
		tokens:       &fakeTokenStore{},
		resolver:     stubResolver{},
		schedules:    sched,
	}

	person := &store.Person{ID: uuid.New()}
	req := withPerson(callbackWithValidState(t, sessions, "abc"), person)
	w := httptest.NewRecorder()
	h.HandleCallback(w, req)

	assert.Equal(t, http.StatusInternalServerError, w.Code)
	assert.False(t, channels.getByYouTubeChannelIDCalled, "must fail before ever resolving/looking up the Channel when no refresh token comes back")
	assert.Empty(t, sched.calls, "an exchange failure (no refresh token) must never reach EnsureSchedule")
}

// ── HandleCallback: fresh connect (FR3) ─────────────────────────────────

func TestHandleCallback_FreshConnect_CreatesChannelAndSavesCredential(t *testing.T) {
	sessions := testSessions()
	exch := &stubExchanger{token: &oauth2.Token{AccessToken: "at", RefreshToken: "rt"}}
	newChannelID := uuid.New()
	channels := &fakeChannelStore{
		getByYouTubeChannelIDErr: pgx.ErrNoRows,
		createResult:             store.Channel{ID: newChannelID, YouTubeChannelID: "yt-123", Title: "My Channel"},
	}
	tok := &fakeTokenStore{}
	sched := &stubScheduleManager{}
	h := &Handler{
		sessions:     sessions,
		oauth2Config: exch,
		channels:     channels,
		roles:        &fakeRoleStore{},
		tokens:       tok,
		resolver:     stubResolver{youtubeChannelID: "yt-123", title: "My Channel"},
		schedules:    sched,
	}

	person := &store.Person{ID: uuid.New()}
	req := withPerson(callbackWithValidState(t, sessions, "abc"), person)
	w := httptest.NewRecorder()
	h.HandleCallback(w, req)

	require.Equal(t, http.StatusSeeOther, w.Code, "body: %s", w.Body.String())
	require.True(t, channels.createCalled)
	assert.Equal(t, "yt-123", channels.createYoutubeChannelID)
	assert.Equal(t, "My Channel", channels.createTitle)
	assert.Equal(t, person.ID, channels.createCreatorPersonID, "FR3: the connecting Person must become the Channel's creator")

	require.True(t, tok.saveCalled)
	assert.Equal(t, newChannelID, tok.saveChannelID)
	assert.Equal(t, person.ID, tok.saveByPersonID)
	assert.ElementsMatch(t, Scopes, tok.saveScopes)
	assert.False(t, tok.markNeedsReauthCalled, "MarkNeedsReauth must not run on the happy path")

	require.Len(t, sched.calls, 1, "FR14/NFR4 (issue #1614): a fresh connect must call EnsureSchedule exactly once")
	assert.Equal(t, newChannelID, sched.calls[0], "EnsureSchedule must be called with the newly-created Channel's ID")
}

// TestHandleCallback_FreshConnect_EnsureScheduleErrors_StillRedirectsNoServerError
// covers issue #1614's non-fatal contract: a Channel is already correctly
// connected in Postgres by the time EnsureSchedule runs, so a Temporal
// hiccup here must degrade to "worker's next startup Reconcile will pick
// it up" rather than turn an otherwise-successful connect into a 500.
func TestHandleCallback_FreshConnect_EnsureScheduleErrors_StillRedirectsNoServerError(t *testing.T) {
	sessions := testSessions()
	exch := &stubExchanger{token: &oauth2.Token{AccessToken: "at", RefreshToken: "rt"}}
	newChannelID := uuid.New()
	channels := &fakeChannelStore{
		getByYouTubeChannelIDErr: pgx.ErrNoRows,
		createResult:             store.Channel{ID: newChannelID, YouTubeChannelID: "yt-123", Title: "My Channel"},
	}
	sched := &stubScheduleManager{err: assertErr}
	h := &Handler{
		sessions:     sessions,
		oauth2Config: exch,
		channels:     channels,
		roles:        &fakeRoleStore{},
		tokens:       &fakeTokenStore{},
		resolver:     stubResolver{youtubeChannelID: "yt-123", title: "My Channel"},
		schedules:    sched,
	}

	person := &store.Person{ID: uuid.New()}
	req := withPerson(callbackWithValidState(t, sessions, "abc"), person)
	w := httptest.NewRecorder()
	h.HandleCallback(w, req)

	assert.Equal(t, http.StatusSeeOther, w.Code, "an EnsureSchedule failure must not turn an otherwise-successful connect into a 500")
	require.Len(t, sched.calls, 1, "EnsureSchedule must still have been attempted")
}

// TestHandleCallback_SaveFailsAfterCreate_CompensatesWithMarkNeedsReauth
// covers this task's documented Implementation-phase deviation: Create and
// Save each own their own transaction (not one literal SQL transaction), so
// a Save failure right after a successful fresh Create must be compensated
// with MarkNeedsReauth rather than silently leaving a Channel row that
// exists (with its role=creator join row) but has no live credential.
func TestHandleCallback_SaveFailsAfterCreate_CompensatesWithMarkNeedsReauth(t *testing.T) {
	sessions := testSessions()
	exch := &stubExchanger{token: &oauth2.Token{AccessToken: "at", RefreshToken: "rt"}}
	newChannelID := uuid.New()
	channels := &fakeChannelStore{
		getByYouTubeChannelIDErr: pgx.ErrNoRows,
		createResult:             store.Channel{ID: newChannelID, YouTubeChannelID: "yt-123"},
	}
	tok := &fakeTokenStore{saveErr: assertErr}
	sched := &stubScheduleManager{}
	h := &Handler{
		sessions:     sessions,
		oauth2Config: exch,
		channels:     channels,
		roles:        &fakeRoleStore{},
		tokens:       tok,
		resolver:     stubResolver{youtubeChannelID: "yt-123"},
		schedules:    sched,
	}

	person := &store.Person{ID: uuid.New()}
	req := withPerson(callbackWithValidState(t, sessions, "abc"), person)
	w := httptest.NewRecorder()
	h.HandleCallback(w, req)

	assert.Equal(t, http.StatusInternalServerError, w.Code)
	require.True(t, tok.markNeedsReauthCalled, "a Save failure right after a fresh Create must be compensated with MarkNeedsReauth, since Create+Save are two separate transactions")
	assert.Equal(t, newChannelID, tok.markNeedsReauthChannelID, "MarkNeedsReauth must target the Channel Create just created, never leaving it claiming a live credential it doesn't have")
	assert.Empty(t, sched.calls, "the Channel never reached connected here (Save failed), so EnsureSchedule must never be called")
}

func TestHandleCallback_ResolveOwnChannelFails_NeverCreatesOrSaves(t *testing.T) {
	sessions := testSessions()
	exch := &stubExchanger{token: &oauth2.Token{AccessToken: "at", RefreshToken: "rt"}}
	channels := &fakeChannelStore{}
	tok := &fakeTokenStore{}
	sched := &stubScheduleManager{}
	h := &Handler{
		sessions:     sessions,
		oauth2Config: exch,
		channels:     channels,
		roles:        &fakeRoleStore{},
		tokens:       tok,
		resolver:     stubResolver{err: assertErr},
		schedules:    sched,
	}

	person := &store.Person{ID: uuid.New()}
	req := withPerson(callbackWithValidState(t, sessions, "abc"), person)
	w := httptest.NewRecorder()
	h.HandleCallback(w, req)

	assert.Equal(t, http.StatusInternalServerError, w.Code)
	assert.False(t, channels.createCalled)
	assert.False(t, tok.saveCalled)
	assert.Empty(t, sched.calls, "a resolver failure must never reach EnsureSchedule")
}

// ── HandleCallback: reconnect path (FR4, NFR5 defense in depth) ─────────

func TestHandleCallback_ExistingChannel_AnalystReconnect_ForbiddenNoSave(t *testing.T) {
	sessions := testSessions()
	exch := &stubExchanger{token: &oauth2.Token{AccessToken: "at", RefreshToken: "rt"}}
	existing := store.Channel{ID: uuid.New(), YouTubeChannelID: "yt-existing"}
	channels := &fakeChannelStore{getByYouTubeChannelIDResult: existing}
	tok := &fakeTokenStore{}
	sched := &stubScheduleManager{}
	h := &Handler{
		sessions:     sessions,
		oauth2Config: exch,
		channels:     channels,
		roles:        &fakeRoleStore{roles: []store.Role{store.RoleAnalyst}},
		tokens:       tok,
		resolver:     stubResolver{youtubeChannelID: "yt-existing"},
		schedules:    sched,
	}

	person := &store.Person{ID: uuid.New()}
	req := withPerson(callbackWithValidState(t, sessions, "abc"), person)
	w := httptest.NewRecorder()
	h.HandleCallback(w, req)

	assert.Equal(t, http.StatusForbidden, w.Code, "a forged-but-otherwise-valid callback for an existing Channel must still be gated by CanReconnect (defense in depth)")
	assert.False(t, tok.saveCalled, "no credential row may be written for a forbidden reconnect")
	assert.False(t, channels.setConnectionStateCalled)
	assert.Empty(t, sched.calls, "a forbidden reconnect (CanReconnect false) must never reach EnsureSchedule")
}

func TestHandleCallback_ExistingChannel_CreatorReconnect_SavesAndSetsConnected(t *testing.T) {
	sessions := testSessions()
	exch := &stubExchanger{token: &oauth2.Token{AccessToken: "at", RefreshToken: "rt"}}
	existing := store.Channel{ID: uuid.New(), YouTubeChannelID: "yt-existing"}
	channels := &fakeChannelStore{getByYouTubeChannelIDResult: existing}
	tok := &fakeTokenStore{}
	sched := &stubScheduleManager{}
	h := &Handler{
		sessions:     sessions,
		oauth2Config: exch,
		channels:     channels,
		roles:        &fakeRoleStore{roles: []store.Role{store.RoleCreator}},
		tokens:       tok,
		resolver:     stubResolver{youtubeChannelID: "yt-existing"},
		schedules:    sched,
	}

	person := &store.Person{ID: uuid.New()}
	req := withPerson(callbackWithValidState(t, sessions, "abc"), person)
	w := httptest.NewRecorder()
	h.HandleCallback(w, req)

	require.Equal(t, http.StatusSeeOther, w.Code, "body: %s", w.Body.String())
	require.True(t, tok.saveCalled)
	assert.Equal(t, existing.ID, tok.saveChannelID)
	require.True(t, channels.setConnectionStateCalled)
	assert.Equal(t, existing.ID, channels.setConnectionStateChannelID)
	assert.Equal(t, store.ConnectionStateConnected, channels.setConnectionStateValue, "a successful reconnect must return connection_state to connected")
	assert.False(t, channels.createCalled, "an existing Channel must never be re-Created")

	require.Len(t, sched.calls, 1, "FR14/NFR4 (issue #1614): a successful reconnect must call EnsureSchedule exactly once")
	assert.Equal(t, existing.ID, sched.calls[0], "EnsureSchedule must be called with the existing Channel's ID")
}

// assertErr is a fixed sentinel error used by tests that only need a store
// method to fail, not any specific error value.
var assertErr = &sentinelError{"stub error"}

type sentinelError struct{ msg string }

func (e *sentinelError) Error() string { return e.msg }
