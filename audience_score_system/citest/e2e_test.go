//go:build integration

// Package citest is Audience Score System's milestone-acceptance test
// (issue #1583): the outcome sentence itself -- "a single Creator and
// Analyst can run one idea through all three loops end-to-end, on one
// Channel" -- driven through the real embedded migrations (real
// Postgres, via //libs/go/dbtest), the real MCP server in-process (a real
// `mcp.NewClient` + `StreamableClientTransport` against an
// `httptest.Server` wrapping `mcp/server.NewHTTPHandler`, exactly like
// `mcp/server/server_integration_test.go`), the real `web` HTTP handlers
// (a router mirroring `web/main.go`'s route table, exactly like
// `web/schedule/schedule_integration_test.go` and
// `web/invite/invite_integration_test.go`), and the real
// `worker/sync.ChannelSyncWorkflow` + its real `Activities` implementation
// run inside Temporal's `testsuite.TestWorkflowEnvironment` -- with
// `youtube/fake.Client` standing in for Google/YouTube so no test in this
// package ever makes a live network call or drives a real OAuth consent
// screen. Every other task's own test file already proves its own slice
// in isolation; this file is the one that proves the sentence -- that
// every slice actually wires together into one continuous loop.
//
// Modelled on //tools/app_registry/citest's role in that domain (the
// task-spanning seam test), adapted to this domain's own dbtest+MCP+web
// +Temporal-testsuite conventions rather than app_registry's CI-config
// seam.
//
// # FR coverage inventory
//
// Every FR1-FR24 is either exercised directly in this file or has an
// explicit pointer below to the task-level test that already covers it in
// isolation -- re-proving a requirement's full edge-case surface here
// would just duplicate that file; this file's job is proving the STEPS
// CONNECT, not re-litigating each one's edge cases.
//
//   - FR1  -- exercised here (step 1: UpsertByGoogleSubject creates a
//     Person). Full OAuth mechanics (HandleCallback, ID token
//     verification): web/auth/auth_integration_test.go.
//   - FR2  -- pointer: web/auth/auth_integration_test.go
//     (TestHandleCallback_SameSub_ChangedProfile_ReusesPersonNoDuplicate).
//   - FR3  -- exercised here (step 1: Channel + role=creator row via the
//     same store.ChannelStore.Create call web/channel.Handler.
//     HandleCallback makes). OAuth consent mechanics (scope set, state
//     binding, channel resolver): web/channel/channel_test.go.
//   - FR4  -- exercised here (step 9: ErrRevoked -> needs_reauth, data
//     retained, quota-free skip cycle, reconnect resumes syncing).
//     Handler-level 403/state-transition detail: web/channel/channel_test.go;
//     SyncSchedule's own ErrRevoked/ErrQuotaExceeded/ErrTransient
//     classification: worker/sync/video_sync_test.go.
//   - FR5  -- exercised here (step 2: Invites().Generate). Regenerate-
//     returns-existing-code idempotency (FR30): store/store_integration_test.go.
//   - FR6  -- exercised here (step 2: brand-new Analyst redeems via
//     GET /invites/{code}/resume).
//   - FR7  -- pointer: web/invite/invite_integration_test.go (existing-Person
//     accept/decline prompt).
//   - FR8  -- pointer: web/invite/invite_integration_test.go (consumed/
//     invalidated code rejection).
//   - FR9  -- exercised here (step 3: save_research_note, cited + uncited).
//   - FR10 -- exercised here (step 3: Cited bool on both save and
//     list_research_notes).
//   - FR11 -- exercised here (step 4: save_viability_verdict with
//     citations).
//   - FR12 -- exercised here (step 4: a second verdict version leaves the
//     first byte-for-byte unchanged in History).
//   - FR13 -- exercised here (step 4: get_viability_verdict's
//     Current+History in one call).
//   - FR14 -- exercised here (steps 5, 7, 9: ChannelSyncWorkflow run via
//     Temporal's testsuite, real SyncSchedule activity, needs_reauth skip
//     and resume).
//   - FR15 -- retired outright by FR46 (issue #1831, milestone
//     video-script-model): get_channel_schedule, FR15's MCP surface, no
//     longer exists (C6 cut, not replaced). Step 5 now reads the synced
//     schedule directly from store.SyncStore to keep proving FR14's sync
//     population without depending on the retired tool.
//   - FR16-FR18 -- retired outright by FR41 (issue #1832, milestone
//     video-script-model): schedule_entry drafting, and the Channel pacing
//     policy FR18's collision check depended on, no longer exist (C6/C7/C8
//     cut, not replaced). Step 5 proposes a video_script instead (FR36),
//     bound to the viable verdict version via verdict_id, FK-checked by
//     re-reading the row back from the store -- the closest surviving
//     analog of FR16's binding check.
//   - FR19/FR37 -- exercised here (step 6: Analyst 403, Creator greenlights
//     the proposed video_script -- FR19's approve gate, rebuilt against
//     video_script by FR37/FR49).
//   - FR20/FR39 -- exercised here (step 8: greenlighting/archiving rejected
//     409 once published -- FR20's freeze, rebuilt against video_script's
//     publish-freeze predicate by FR39/FR49). FR20's un-approve/edit
//     affordances have no video_script analog (FR49's HandleUnapprove/
//     HandleEdit retirement note) and are not exercised here.
//   - FR21 -- exercised here (step 7: SyncOutcomes' real metrics upsert).
//   - FR22 -- exercised here (step 7: an exact title/date match auto-links
//     at/above the confidence threshold).
//   - FR23 -- exercised here (step 7: an unmatched video queues pending
//     and is confirmed via resolve_pending_match).
//   - FR24 -- exercised here (step 7: get_prediction_vs_outcome surfaces
//     the verdict that predicted each matched video).
//
// NFR spot-check pointers for the validator (per this task's Validation
// section): NFR1 (both OAuth scopes requested) --
// web/channel/channel_test.go; NFR2 (write-tool replay safety) -- each
// tool group's own *_integration_test.go (research/verdict/video_script/
// matches); NFR3 (web hosts only C1/C2/C3/C8) -- structural,
// see ARCHITECTURE.md's "NFR3 interface allocation"; NFR4 (sync cadence,
// one schedule per Channel) -- worker/sync/schedule_test.go; NFR5
// (authorization via the join table) -- exercised throughout this file
// (every MCP/web call in this loop goes through store.CanRead/CanWrite/
// CanApprove/CanReconnect/CanInvite, never a hand-rolled check).
//
// Run it explicitly (requires a working Docker daemon):
//
//	bazel test //audience_score_system/citest:e2e_test --test_output=all
package citest_test

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.temporal.io/sdk/activity"
	"go.temporal.io/sdk/testsuite"
	"golang.org/x/oauth2"

	mcpserver "github.com/whale-net/everything/audience_score_system/mcp/server"
	mcptools "github.com/whale-net/everything/audience_score_system/mcp/tools"
	"github.com/whale-net/everything/audience_score_system/migrate/schema"
	"github.com/whale-net/everything/audience_score_system/store"
	"github.com/whale-net/everything/audience_score_system/tokens"
	"github.com/whale-net/everything/audience_score_system/web/access"
	"github.com/whale-net/everything/audience_score_system/web/auth"
	"github.com/whale-net/everything/audience_score_system/web/invite"
	"github.com/whale-net/everything/audience_score_system/web/schedule"
	"github.com/whale-net/everything/audience_score_system/worker/sync"
	"github.com/whale-net/everything/audience_score_system/youtube"
	"github.com/whale-net/everything/audience_score_system/youtube/fake"
	"github.com/whale-net/everything/libs/go/dbtest"
	"github.com/whale-net/everything/libs/go/mcpauth"
	"github.com/whale-net/everything/libs/go/migrate"
)

const testCookieName = "e2e_ass_session"

func testEncKey() [32]byte {
	return sha256.Sum256([]byte("e2e-citest-key"))
}

// ── world: the shared stack every step below drives ─────────────────────────

// world bundles a real Postgres (dbtest), a real store.Store, a real `web`
// router (mirroring web/main.go's route table for the four routes this
// loop needs), and a real MCP server hosted over HTTP -- everything a step
// needs, so no step has to re-wire its own stack.
type world struct {
	t        *testing.T
	ctx      context.Context
	pg       *dbtest.Postgres
	st       *store.Store
	creds    mcpauth.CredentialStore
	sessions *auth.SessionManager
	web      http.Handler
	mcpURL   string
}

func newWorld(t *testing.T) *world {
	t.Helper()
	ctx := context.Background()

	pg := dbtest.NewPostgres(ctx, t, dbtest.Options{})

	sqlDB, err := sql.Open("pgx", pg.ConnString)
	require.NoError(t, err)
	t.Cleanup(func() { _ = sqlDB.Close() })

	runner := migrate.NewRunner(sqlDB, schema.Migrations, schema.Dir)
	require.NoError(t, runner.Up(), "apply every migration from the real embedded schema")

	st := store.New(pg.Pool)
	creds, err := mcpauth.NewCredentialStore(ctx, mcpauth.StoreConfig{
		Pool:           pg.Pool,
		TableName:      "mcp_credential",
		IdentityColumn: "person_id",
		IdentityCast:   "uuid",
	})
	require.NoError(t, err)
	sessions := auth.NewSessionManager(pg.Pool, testCookieName, "session-secret", testEncKey())
	a := auth.NewForTests(st.Persons(), sessions)
	inv := invite.New(st, sessions)
	sch := schedule.New(st)
	acc := access.New(st)

	// web router: mirrors web/main.go's setupRoutes for exactly the
	// routes this loop drives (invite generate/resume, schedule approve/
	// unapprove/edit, M2's access-management page) -- same pattern as
	// web/invite/invite_integration_test.go, web/schedule/
	// schedule_integration_test.go, and web/access/access_integration_test.go.
	// GET /channels (FR26) and GET /my-work (FR27) are deliberately NOT
	// mirrored here: both handlers live inline on web/main.go's own `app`
	// type (package main, not importable) rather than in a dedicated
	// package like access/invite/schedule -- see web/channels_integration_test.go
	// and web/my_work_integration_test.go for their own HTTP-level
	// coverage. This file instead drives their exact data sources
	// directly (store.AccessStore.ChannelsWithRoleForPerson,
	// store.MyWorkStore.SummariesForPerson) -- identical to what those
	// two handlers call -- and cross-checks the MCP equivalents
	// (list_channels, get_my_work) against them, which is what proves
	// "web and MCP agree" for those two capabilities absent a directly
	// drivable web handler.
	mux := http.NewServeMux()
	mux.HandleFunc("GET /invites/{code}", inv.HandleShow)
	mux.HandleFunc("POST /channels/{id}/invites", a.RequireSignedIn(inv.HandleGenerate))
	mux.HandleFunc("GET /invites/{code}/resume", a.RequireSignedIn(inv.HandleResume))
	mux.HandleFunc("POST /invites/{code}/accept", a.RequireSignedIn(inv.HandleAccept))
	mux.HandleFunc("POST /invites/{code}/decline", a.RequireSignedIn(inv.HandleDecline))
	mux.HandleFunc("GET /channels/{id}/schedule", a.RequireSignedIn(sch.HandleList))
	mux.HandleFunc("POST /schedule/{scriptID}/approve", a.RequireSignedIn(sch.HandleGreenlight))
	mux.HandleFunc("POST /schedule/{scriptID}/deny", a.RequireSignedIn(sch.HandleDeny))
	mux.HandleFunc("POST /schedule/{scriptID}/archive", a.RequireSignedIn(sch.HandleArchive))
	mux.HandleFunc("GET /channels/{id}/access", a.RequireSignedIn(acc.HandleShow))
	mux.HandleFunc("POST /channels/{id}/access/invites", a.RequireSignedIn(acc.HandleInviteCoCreator))
	mux.HandleFunc("POST /channels/{id}/access/promote", a.RequireSignedIn(acc.HandlePromote))
	mux.HandleFunc("POST /channels/{id}/access/remove", a.RequireSignedIn(acc.HandleRemove))

	// mcp server: mirrors mcp/main.go's tool registration exactly.
	srv := mcpserver.New(st)
	reg := mcpserver.NewRegistry(srv, st)
	mcptools.RegisterWhoami(reg)
	mcptools.RegisterListChannels(reg, st.Access())
	mcptools.RegisterResearch(reg, st)
	mcptools.RegisterVerdict(reg, st)
	mcptools.RegisterVideoScript(reg, st)
	mcptools.RegisterStrategy(reg, st)
	mcptools.RegisterMatches(reg, st)
	mcptools.RegisterBrowse(reg, st)
	mcptools.RegisterAccess(reg, st)
	mcptools.RegisterMyWork(reg, st.MyWork())
	mcptools.RegisterChannelAccess(reg, st.Access(), st.Roles())
	mcptools.RegisterOutcomeBar(reg, st.OutcomeBars(), st.Calibration())
	mcpHandler := mcpserver.NewHTTPHandler(srv, creds, mcpserver.ResourceMetadataConfig{
		Resource:            "https://mcp.example.com",
		AuthorizationServer: "https://web.example.com",
		ResourceName:        "Test MCP",
	})
	mcpTS := httptest.NewServer(mcpHandler)
	t.Cleanup(mcpTS.Close)

	return &world{t: t, ctx: ctx, pg: pg, st: st, creds: creds, sessions: sessions, web: mux, mcpURL: mcpTS.URL}
}

// establishSession stands in for a completed C1 Google sign-in (FR1/FR2's
// OAuth mechanics are covered by web/auth's own tests -- see this file's
// header comment) by writing a real web_session row directly, exactly like
// web/invite/invite_integration_test.go and
// web/schedule/schedule_integration_test.go do.
func (w *world) establishSession(personID uuid.UUID) *http.Cookie {
	w.t.Helper()
	rec := httptest.NewRecorder()
	require.NoError(w.t, w.sessions.Establish(w.ctx, rec, personID.String(), ""))
	return findCookie(w.t, rec.Result().Cookies(), testCookieName)
}

func findCookie(t *testing.T, cookies []*http.Cookie, name string) *http.Cookie {
	t.Helper()
	for _, c := range cookies {
		if c.Name == name {
			return c
		}
	}
	t.Fatalf("cookie %q not found", name)
	return nil
}

func (w *world) get(cookie *http.Cookie, path string) *httptest.ResponseRecorder {
	w.t.Helper()
	req := httptest.NewRequest(http.MethodGet, path, nil)
	if cookie != nil {
		req.AddCookie(cookie)
	}
	rec := httptest.NewRecorder()
	w.web.ServeHTTP(rec, req)
	return rec
}

func (w *world) postForm(cookie *http.Cookie, path string, form url.Values) *httptest.ResponseRecorder {
	w.t.Helper()
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	if cookie != nil {
		req.AddCookie(cookie)
	}
	rec := httptest.NewRecorder()
	w.web.ServeHTTP(rec, req)
	return rec
}

// bearerRoundTripper injects an "Authorization: Bearer <token>" header --
// mirrors mcp/server/server_integration_test.go's.
type bearerRoundTripper struct{ token string }

func (rt bearerRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	req = req.Clone(req.Context())
	req.Header.Set("Authorization", "Bearer "+rt.token)
	return http.DefaultTransport.RoundTrip(req)
}

// mcpConnect mints a real bearer credential for personID (mcpauth.
// CredentialStore.Mint, the same mechanism web's token-mint endpoint uses
// in production) and opens a real streamable-HTTP MCP client session.
func (w *world) mcpConnect(personID uuid.UUID) *mcp.ClientSession {
	w.t.Helper()
	token, _, err := w.creds.Mint(w.ctx, personID.String())
	require.NoError(w.t, err)

	transport := &mcp.StreamableClientTransport{
		Endpoint:   w.mcpURL,
		HTTPClient: &http.Client{Transport: bearerRoundTripper{token: token}},
	}
	client := mcp.NewClient(&mcp.Implementation{Name: "e2e-test-client", Version: "0.0.1"}, nil)
	cs, err := client.Connect(w.ctx, transport, nil)
	require.NoError(w.t, err)
	w.t.Cleanup(func() { _ = cs.Close() })
	return cs
}

func callTool(t *testing.T, cs *mcp.ClientSession, name string, args any) *mcp.CallToolResult {
	t.Helper()
	res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: name, Arguments: args})
	require.NoError(t, err)
	return res
}

func textOf(res *mcp.CallToolResult) string {
	var sb strings.Builder
	for _, c := range res.Content {
		if tc, ok := c.(*mcp.TextContent); ok {
			sb.WriteString(tc.Text)
		}
	}
	return sb.String()
}

// decode round-trips res.StructuredContent through encoding/json into a
// typed T -- mirrors mcp/tools/research_integration_test.go's decode/
// mapDecode helpers.
func decode[T any](t *testing.T, res *mcp.CallToolResult) T {
	t.Helper()
	require.False(t, res.IsError, "unexpected tool error: %s", textOf(res))
	var out T
	body, err := json.Marshal(res.StructuredContent)
	require.NoError(t, err)
	require.NoError(t, json.Unmarshal(body, &out))
	return out
}

// ── Temporal testsuite: real ChannelSyncWorkflow + real Activities ─────────

// activityCounts tracks how many times each activity actually ran during
// one runSyncCycle call -- used by step 9 to prove a needs_reauth cycle
// touches neither SyncSchedule nor SyncOutcomes (no YouTube quota burned)
// and that a resumed cycle runs both again.
type activityCounts struct {
	loadState, syncSchedule, syncOutcomes int32
}

// runSyncCycle executes the real worker/sync.ChannelSyncWorkflow for
// channelID inside Temporal's testsuite.TestWorkflowEnvironment, wired to
// REAL Activities (worker/sync.Activities) backed by w.st and a real
// tokens.Store -- NOT mocked activities, unlike worker/sync/workflow_test.go's
// own suite, which mocks every activity to test the workflow's own control
// flow in isolation. Here the whole point is running the genuine
// SyncSchedule/SyncOutcomes logic against real Postgres, with only the
// YouTube API boundary (Activities.NewYouTubeClient) swapped for fc so no
// call ever reaches the network -- mirrors worker/sync/video_sync_test.go's
// and outcomes_test.go's own real-Activities-fake-YouTube pattern, just
// driven through the real workflow via testsuite instead of calling
// SyncSchedule/SyncOutcomes directly.
//
// Every activity is registered as a small counting wrapper around the
// real Activities method (not the method itself), purely so step 9 can
// assert exactly how many times each one genuinely ran per cycle --
// counts, not mocked behavior; the wrapper always delegates to the real
// implementation.
func (w *world) runSyncCycle(channelID uuid.UUID, fc *fake.Client) (*activityCounts, error) {
	w.t.Helper()

	act := &sync.Activities{
		Channels: w.st.Channels(),
		Tokens:   tokens.NewStore(w.pg.Pool, w.st.Channels(), testEncKey(), tokens.Config{}),
		Sync:     w.st.Sync(),
		Matches:  w.st.Matches(),
		NewYouTubeClient: func(ts oauth2.TokenSource) youtube.Client {
			return fc
		},
	}

	counts := &activityCounts{}
	ts := testsuite.WorkflowTestSuite{}
	env := ts.NewTestWorkflowEnvironment()

	env.RegisterActivityWithOptions(func(ctx context.Context, id uuid.UUID) (sync.ChannelState, error) {
		atomic.AddInt32(&counts.loadState, 1)
		return act.LoadChannelState(ctx, id)
	}, activity.RegisterOptions{Name: sync.ActivityLoadChannelState})
	env.RegisterActivityWithOptions(func(ctx context.Context, id uuid.UUID) error {
		atomic.AddInt32(&counts.syncSchedule, 1)
		return act.SyncSchedule(ctx, id)
	}, activity.RegisterOptions{Name: sync.ActivitySyncSchedule})
	env.RegisterActivityWithOptions(func(ctx context.Context, id uuid.UUID) error {
		atomic.AddInt32(&counts.syncOutcomes, 1)
		return act.SyncOutcomes(ctx, id)
	}, activity.RegisterOptions{Name: sync.ActivitySyncOutcomes})

	env.ExecuteWorkflow(sync.ChannelSyncWorkflow, sync.ChannelSyncInput{ChannelID: channelID})
	require.True(w.t, env.IsWorkflowCompleted(), "the workflow run must complete synchronously inside the test environment")
	return counts, env.GetWorkflowError()
}

// ── the loop itself ─────────────────────────────────────────────────────────

// TestE2E_ThreeLoopsEndToEnd is M1's own acceptance test (issue #1583): one
// idea, driven by one Creator and one Analyst on one Channel, through
// research -> verdict -> schedule -> approve -> sync -> outcome, exactly
// as the issue's 9-step Testing section lays out. Every subtest shares
// this function's state (channel, idea, verdict, schedule entry) with the
// next, in order -- t.Run subtests run sequentially here (none call
// t.Parallel()), so later steps see exactly the state earlier steps left
// behind, the same way a real Creator/Analyst session would.
func TestE2E_ThreeLoopsEndToEnd(t *testing.T) {
	w := newWorld(t)
	ctx := w.ctx

	const ideaTitle = "Building A Cool Widget"

	// existingDraftTime is a scheduled/private draft ALREADY on YouTube
	// (e.g. the Creator scheduled it manually in Studio) -- distinct from
	// the Analyst's own video_script proposal below, whose target publish
	// date (proposedSlot) FR43's matcher scores against once the
	// corresponding video actually publishes (step 7).
	existingDraftTime := time.Date(2026, 3, 2, 15, 0, 0, 0, time.UTC)
	proposedSlot := existingDraftTime.Add(2 * time.Hour)

	var (
		creator, analyst             store.Person
		ch                           store.Channel
		creatorCookie, analystCookie *http.Cookie
		ideaID, note1ID, note2ID     uuid.UUID
		verdict1ID, verdict2ID       uuid.UUID
		scriptID                     uuid.UUID
	)

	// ── Step 1: signup & connect ────────────────────────────────────────
	t.Run("1_signup_and_connect", func(t *testing.T) {
		var created bool
		var err error
		creator, created, err = w.st.Persons().UpsertByGoogleSubject(ctx, "sub-creator-e2e", "creator@example.com", "Creator Person")
		require.NoError(t, err)
		assert.True(t, created, "FR1: a visitor with no existing Person record gets one created automatically")
		creatorCookie = w.establishSession(creator.ID)

		// FR3: Channel-connect writes the Channel plus its role=creator
		// join row (LB2) via the same store call
		// web/channel.Handler.HandleCallback's fresh-connect branch makes;
		// the OAuth consent mechanics feeding that call are covered by
		// web/channel/channel_test.go (see this file's header comment).
		ch, err = w.st.Channels().Create(ctx, "yt-e2e-channel", "E2E Test Channel", creator.ID)
		require.NoError(t, err)
		assert.Equal(t, store.ConnectionStateConnected, ch.ConnectionState)

		canApprove, err := store.CanApprove(ctx, w.st.Roles(), ch.ID, creator.ID)
		require.NoError(t, err)
		assert.True(t, canApprove, "FR3: the connecting Person's role=creator row must grant approve authority")

		// "the sync schedule is ensured (#1574)" -- worker/sync.
		// ScheduleManager.EnsureSchedule/Reconcile own this against a real
		// Temporal ScheduleClient, which this package deliberately does not
		// stand up (see worker/sync/schedule_test.go for that coverage);
		// this loop instead drives ChannelSyncWorkflow directly via
		// Temporal's testsuite (steps 5, 7, 9 below), which is what that
		// schedule would actually invoke on its own cadence.
	})

	// ── Step 2: invite ───────────────────────────────────────────────────
	t.Run("2_invite_analyst", func(t *testing.T) {
		// FR5: Creator generates a single-use invite code -- the same
		// store call web/invite.Handlers.HandleGenerate makes.
		// Regenerate-returns-existing-code idempotency (FR30) is covered by
		// store/store_integration_test.go.
		inv, err := w.st.Invites().Generate(ctx, ch.ID, creator.ID, store.RoleAnalyst)
		require.NoError(t, err)
		require.NotEmpty(t, inv.Code)

		var created bool
		analyst, created, err = w.st.Persons().UpsertByGoogleSubject(ctx, "sub-analyst-e2e", "analyst@example.com", "Analyst Person")
		require.NoError(t, err)
		require.True(t, created)
		analystCookie = w.establishSession(analyst.ID)

		// FR6: a brand-new Person redeeming a valid code lands on
		// GET /invites/{code}/resume (the new-visitor half of FR6 -- see
		// web/invite/invite_integration_test.go's identical rationale).
		// FR7 (existing-Person accept/decline prompt) and FR8 (consumed/
		// invalidated rejection) are covered there too.
		rec := w.get(analystCookie, "/invites/"+inv.Code+"/resume")
		require.Equal(t, http.StatusSeeOther, rec.Code, "body: %s", rec.Body.String())

		canWrite, err := store.CanWrite(ctx, w.st.Roles(), ch.ID, analyst.ID)
		require.NoError(t, err)
		assert.True(t, canWrite, "FR6: redeeming the invite must grant role=analyst")
		canApprove, err := store.CanApprove(ctx, w.st.Roles(), ch.ID, analyst.ID)
		require.NoError(t, err)
		assert.False(t, canApprove, "an Analyst must never hold approve authority (LB2)")
	})

	// ── Step 3: research loop ────────────────────────────────────────────
	t.Run("3_research_loop", func(t *testing.T) {
		csAnalyst := w.mcpConnect(analyst.ID)

		ideaRes := callTool(t, csAnalyst, "create_idea", mcptools.CreateIdeaInput{
			ChannelID: ch.ID.String(),
			Title:     ideaTitle,
		})
		idea := decode[mcptools.IdeaOutput](t, ideaRes)
		ideaID = uuid.MustParse(idea.IdeaID)

		// FR9/FR10: one cited, one uncited note.
		citedRes := callTool(t, csAnalyst, "save_research_note", mcptools.SaveResearchNoteInput{
			ChannelID:         ch.ID.String(),
			IdeaID:            ideaID.String(),
			Text:              "Competitor X's widget video got 2x the channel's usual views.",
			SourceURL:         "https://example.com/competitor-x-analytics",
			IdempotencyKeyArg: uuid.NewString(),
		})
		cited := decode[mcptools.ResearchNoteOutput](t, citedRes)
		assert.True(t, cited.Cited, "FR10: a note with a source_url renders cited")
		note1ID = uuid.MustParse(cited.ID)

		uncitedRes := callTool(t, csAnalyst, "save_research_note", mcptools.SaveResearchNoteInput{
			ChannelID:         ch.ID.String(),
			IdeaID:            ideaID.String(),
			Text:              "Gut sense: widgets are trending right now.",
			IdempotencyKeyArg: uuid.NewString(),
		})
		uncited := decode[mcptools.ResearchNoteOutput](t, uncitedRes)
		assert.False(t, uncited.Cited, "FR10: a note with no source_url renders uncited")
		note2ID = uuid.MustParse(uncited.ID)

		// Creator lists them and sees the same cited/uncited distinction
		// (FR9/FR10/FR24 -- mutual visibility).
		csCreator := w.mcpConnect(creator.ID)
		listRes := callTool(t, csCreator, "list_research_notes", mcptools.ListResearchNotesInput{ChannelID: ch.ID.String()})
		list := decode[mcptools.ListResearchNotesOutput](t, listRes)
		require.Len(t, list.Notes, 2)
		byID := map[string]mcptools.ResearchNoteOutput{}
		for _, n := range list.Notes {
			byID[n.ID] = n
		}
		assert.True(t, byID[note1ID.String()].Cited)
		assert.False(t, byID[note2ID.String()].Cited)
	})

	// ── Step 4: verdict loop ─────────────────────────────────────────────
	t.Run("4_verdict_loop", func(t *testing.T) {
		csAnalyst := w.mcpConnect(analyst.ID)

		// FR11/FR12: first a needs-more-research verdict, then a viable
		// one -- the second must never mutate the first.
		v1Res := callTool(t, csAnalyst, "save_viability_verdict", mcptools.SaveViabilityVerdictInput{
			ChannelID:            ch.ID.String(),
			IdeaID:               ideaID.String(),
			Verdict:              string(store.VerdictNeedsMoreResearch),
			Reasoning:            "Need to confirm this isn't a one-off spike for Competitor X.",
			CitedResearchNoteIDs: []string{note1ID.String()},
			IdempotencyKeyArg:    uuid.NewString(),
		})
		v1 := decode[mcptools.VerdictOutput](t, v1Res)
		verdict1ID = uuid.MustParse(v1.ID)
		assert.Equal(t, 1, v1.Version)

		v2Res := callTool(t, csAnalyst, "save_viability_verdict", mcptools.SaveViabilityVerdictInput{
			ChannelID:            ch.ID.String(),
			IdeaID:               ideaID.String(),
			Verdict:              string(store.VerdictViable),
			Reasoning:            "Confirmed: two more comps show the same lift. Green light.",
			CitedResearchNoteIDs: []string{note1ID.String(), note2ID.String()},
			IdempotencyKeyArg:    uuid.NewString(),
		})
		v2 := decode[mcptools.VerdictOutput](t, v2Res)
		verdict2ID = uuid.MustParse(v2.ID)
		assert.Equal(t, 2, v2.Version)

		// FR13: current + full history in one read call; v1 unchanged.
		getRes := callTool(t, csAnalyst, "get_viability_verdict", mcptools.GetViabilityVerdictInput{
			ChannelID: ch.ID.String(),
			IdeaID:    ideaID.String(),
		})
		got := decode[mcptools.GetViabilityVerdictOutput](t, getRes)
		require.NotNil(t, got.Current)
		assert.Equal(t, verdict2ID.String(), got.Current.ID)
		assert.Equal(t, string(store.VerdictViable), got.Current.Verdict)
		require.Len(t, got.History, 2)
		assert.Equal(t, verdict1ID.String(), got.History[0].ID)
		assert.Equal(t, string(store.VerdictNeedsMoreResearch), got.History[0].Verdict,
			"FR12: the first verdict version must be unchanged after a second was saved")
		assert.Equal(t, verdict2ID.String(), got.History[1].ID)
	})

	// ── Step 5: schedule loop ────────────────────────────────────────────
	var fc1 *fake.Client
	t.Run("5_schedule_loop", func(t *testing.T) {
		csAnalyst := w.mcpConnect(analyst.ID)

		// FR17 retired outright by FR41 (issue #1832): the Channel pacing
		// policy this step used to set no longer exists (C7 cut, not
		// replaced).

		// FR14: sync populates the synced schedule, including a
		// scheduled/private draft already on YouTube.
		fc1 = &fake.Client{
			Schedule: []youtube.Video{
				{
					YouTubeVideoID:   "yt-existing-draft",
					Title:            "Some Other Scheduled Upload",
					PrivacyStatus:    store.PrivacyStatusPrivate,
					PublishAt:        &existingDraftTime,
					IsScheduledDraft: true,
				},
			},
		}
		counts, err := w.runSyncCycle(ch.ID, fc1)
		require.NoError(t, err)
		assert.EqualValues(t, 1, counts.syncSchedule)
		assert.EqualValues(t, 1, counts.syncOutcomes)

		// FR15 (retired, FR46): get_channel_schedule no longer exists, so
		// this reads the synced schedule directly from the store instead of
		// through an MCP tool.
		syncedVids, _, err := w.st.Sync().ListSchedule(ctx, ch.ID, nil, nil, true, 0)
		require.NoError(t, err)
		require.Len(t, syncedVids, 1)
		assert.Equal(t, "yt-existing-draft", syncedVids[0].YouTubeVideoID)
		assert.True(t, syncedVids[0].IsScheduledDraft)

		// FR36's own Strategy grounding requirement: the Analyst first
		// builds a Strategy from the viable verdict version, then proposes
		// a video_script under it (FR16's closest surviving analog --
		// FR18's collision check itself was retired with pacing_policy,
		// #1832).
		strategyRes := callTool(t, csAnalyst, "save_strategy", mcptools.SaveStrategyInput{
			ChannelID:         ch.ID.String(),
			Title:             "Widget Strategy",
			VerdictIDs:        []string{verdict2ID.String()},
			IdempotencyKeyArg: uuid.NewString(),
		})
		strategy := decode[mcptools.StrategyOutput](t, strategyRes)

		scriptRes := callTool(t, csAnalyst, "save_video_script", mcptools.SaveVideoScriptInput{
			ChannelID:         ch.ID.String(),
			VerdictID:         verdict2ID.String(),
			StrategyID:        strategy.StrategyID,
			Title:             ideaTitle,
			ScriptText:        "Script text for " + ideaTitle,
			TargetPublishDate: proposedSlot.Format(time.RFC3339),
			IdempotencyKeyArg: uuid.NewString(),
		})
		script := decode[mcptools.VideoScriptOutput](t, scriptRes)
		assert.Equal(t, verdict2ID.String(), script.VerdictID, "FR36: bound to the specific viable verdict version")
		assert.Equal(t, "proposed", script.Status)
		scriptID = uuid.MustParse(script.VideoScriptID)

		// The proposed script is genuinely persisted, not just returned --
		// re-read directly from the store (LB3's FK chain: video_script ->
		// verdict_id).
		stored, err := w.st.VideoScripts().GetByID(ctx, scriptID)
		require.NoError(t, err)
		assert.Equal(t, verdict2ID, stored.VerdictID)
		assert.Equal(t, store.VideoScriptStatusProposed, stored.Status)
	})

	// ── Step 6: approval ─────────────────────────────────────────────────
	t.Run("6_approval", func(t *testing.T) {
		// FR19/FR37: Analyst's greenlight attempt is 403, no state change.
		// The route path keeps its pre-existing "approve" spelling (FR49's
		// route-and-package-naming note); the store transition it drives is
		// video_script's proposed->greenlit.
		rec := w.postForm(analystCookie, "/schedule/"+scriptID.String()+"/approve", nil)
		assert.Equal(t, http.StatusForbidden, rec.Code)
		script, err := w.st.VideoScripts().GetByID(ctx, scriptID)
		require.NoError(t, err)
		assert.Equal(t, store.VideoScriptStatusProposed, script.Status, "an Analyst's rejected greenlight must not change status")

		// FR19/FR37: Creator greenlights.
		rec = w.postForm(creatorCookie, "/schedule/"+scriptID.String()+"/approve", nil)
		require.Equal(t, http.StatusSeeOther, rec.Code, "body: %s", rec.Body.String())
		script, err = w.st.VideoScripts().GetByID(ctx, scriptID)
		require.NoError(t, err)
		assert.Equal(t, store.VideoScriptStatusGreenlit, script.Status)

		// FR20's un-approve/edit affordances have no video_script analog
		// (FR49's HandleUnapprove/HandleEdit retirement note): a
		// video_script's target date is set once at propose time (FR36),
		// and FR40 defines no greenlit->proposed transition.
	})

	// ── Step 7: outcome loop ─────────────────────────────────────────────
	var matchBID uuid.UUID
	t.Run("7_outcome_loop", func(t *testing.T) {
		publishedAtA := proposedSlot
		publishedAtB := proposedSlot.Add(20 * 24 * time.Hour)

		viewsA, viewsB := int64(12345), int64(50)
		fc2 := &fake.Client{
			Schedule: []youtube.Video{
				// The existing draft (retained from the prior cycle,
				// FR14 -- never deleted, only left un-refreshed if it
				// disappears; here it simply stays as-is).
				{
					YouTubeVideoID:   "yt-existing-draft",
					Title:            "Some Other Scheduled Upload",
					PrivacyStatus:    store.PrivacyStatusPrivate,
					PublishAt:        &existingDraftTime,
					IsScheduledDraft: true,
				},
				// The idea's own video, now published with a title/date
				// that exactly matches the greenlit video_script's target
				// date (FR43) -- scores confidence 1.0, auto-links (FR22).
				// Ordered before the ambiguous video below (ListSchedule/
				// SyncOutcomes process in COALESCE(publish_at,
				// published_at) order) so it claims the sole greenlit
				// candidate first.
				{
					YouTubeVideoID: "yt-idea-video",
					Title:          ideaTitle,
					PrivacyStatus:  store.PrivacyStatusPublic,
					PublishedAt:    &publishedAtA,
				},
				// An unrelated video published later, with no plausible
				// candidate left once yt-idea-video has claimed the only
				// committed entry -- queues pending (FR23).
				{
					YouTubeVideoID: "yt-ambiguous",
					Title:          "Totally Unrelated Vlog Update",
					PrivacyStatus:  store.PrivacyStatusPublic,
					PublishedAt:    &publishedAtB,
				},
			},
			MetricsByVideoID: map[string]youtube.VideoMetrics{
				"yt-idea-video": {YouTubeVideoID: "yt-idea-video", Views: &viewsA, MeasuredAt: publishedAtA.Add(24 * time.Hour)},
				"yt-ambiguous":  {YouTubeVideoID: "yt-ambiguous", Views: &viewsB, MeasuredAt: publishedAtB.Add(24 * time.Hour)},
			},
		}

		// FR22's auto-linker now scores against greenlit video_script
		// candidates, not schedule_entry rows (FR43/#1829's re-anchor,
		// matching.go) -- give it one, title/date-exact against
		// yt-idea-video, so the sync cycle below has something to
		// auto-link to.
		strategy, err := w.st.Strategies().Save(ctx, store.SaveStrategyInput{
			ChannelID: ch.ID, Title: "Widget Strategy", PreferredWeekday: "Tuesday",
			Active: true, VerdictIDs: []uuid.UUID{verdict2ID}, CreatedByPersonID: creator.ID,
		})
		require.NoError(t, err)
		script, err := w.st.VideoScripts().Propose(ctx, store.ProposeVideoScriptInput{
			ChannelID: ch.ID, VerdictID: verdict2ID, StrategyID: strategy.ID,
			Title: ideaTitle, ScriptText: "script text", TargetPublishDate: &publishedAtA,
			CreatedByPersonID: creator.ID,
		})
		require.NoError(t, err)
		require.NoError(t, w.st.VideoScripts().Greenlight(ctx, script.ID, creator.ID))

		counts, err := w.runSyncCycle(ch.ID, fc2)
		require.NoError(t, err)
		assert.EqualValues(t, 1, counts.syncSchedule)
		assert.EqualValues(t, 1, counts.syncOutcomes, "FR21: outcome sync runs on the same cycle as schedule sync")

		videoA, err := w.st.Sync().GetByID(ctx, mustSyncedVideoID(t, ctx, w.st, ch.ID, "yt-idea-video"))
		require.NoError(t, err)
		hasMatch, err := w.st.Matches().HasMatch(ctx, videoA.ID)
		require.NoError(t, err)
		require.True(t, hasMatch, "FR22: the exact title/date match must auto-link")

		videoB, err := w.st.Sync().GetByID(ctx, mustSyncedVideoID(t, ctx, w.st, ch.ID, "yt-ambiguous"))
		require.NoError(t, err)

		pendingRes := callTool(t, w.mcpConnect(creator.ID), "list_pending_matches", mcptools.ListPendingMatchesInput{ChannelID: ch.ID.String()})
		pending := decode[mcptools.ListPendingMatchesOutput](t, pendingRes)
		require.Len(t, pending.Matches, 1, "FR23: exactly the ambiguous video must be queued pending")
		matchBID = uuid.MustParse(pending.Matches[0].MatchID)
		assert.Equal(t, "yt-ambiguous", pending.Matches[0].Video.YouTubeVideoID)

		m, err := w.st.Matches().GetByID(ctx, matchBID)
		require.NoError(t, err)
		assert.Equal(t, videoB.ID, m.SyncedVideoID)
		assert.Equal(t, store.MatchStatePending, m.State)

		// FR24: the comparison shows the auto-linked video next to the
		// SPECIFIC verdict version that predicted it.
		cmpRes := callTool(t, w.mcpConnect(analyst.ID), "get_prediction_vs_outcome", mcptools.GetPredictionVsOutcomeInput{ChannelID: ch.ID.String()})
		cmp := decode[mcptools.GetPredictionVsOutcomeOutput](t, cmpRes)
		require.Len(t, cmp.Rows, 1, "the pending match must not appear in the comparison yet")
		assert.Equal(t, "yt-idea-video", cmp.Rows[0].Video.YouTubeVideoID)
		assert.Equal(t, verdict2ID.String(), cmp.Rows[0].Verdict.VerdictID, "FR24: predicted by the exact verdict version that judged it viable")
		assert.Equal(t, "auto", cmp.Rows[0].MatchProvenance)
		assert.Equal(t, 1, cmp.PendingMatchCount)

		// FR23: the Analyst confirms the pending match via MCP.
		resolveRes := callTool(t, w.mcpConnect(analyst.ID), "resolve_pending_match", mcptools.ResolvePendingMatchInput{
			ChannelID:         ch.ID.String(),
			MatchID:           matchBID.String(),
			Confirm:           true,
			VideoScriptID:     scriptID.String(),
			IdempotencyKeyArg: uuid.NewString(),
		})
		resolved := decode[mcptools.ResolvedMatchOutput](t, resolveRes)
		assert.Equal(t, "confirmed", resolved.State)

		// It now appears in the comparison too.
		cmpRes2 := callTool(t, w.mcpConnect(creator.ID), "get_prediction_vs_outcome", mcptools.GetPredictionVsOutcomeInput{ChannelID: ch.ID.String()})
		cmp2 := decode[mcptools.GetPredictionVsOutcomeOutput](t, cmpRes2)
		require.Len(t, cmp2.Rows, 2, "the just-confirmed match must now appear in the comparison")
		var sawConfirmed bool
		for _, r := range cmp2.Rows {
			if r.Video.YouTubeVideoID == "yt-ambiguous" {
				sawConfirmed = true
				assert.Equal(t, "confirmed", r.MatchProvenance)
			}
		}
		assert.True(t, sawConfirmed)
	})

	// ── Step 8: freeze ───────────────────────────────────────────────────
	t.Run("8_freeze", func(t *testing.T) {
		published, err := w.st.VideoScripts().IsPublished(ctx, scriptID)
		require.NoError(t, err)
		require.True(t, published, "the greenlit script's video is now auto-matched and published")

		// FR20/FR39: archive must be rejected once published (the
		// video_script analog of FR20's freeze, rebuilt by FR39/FR49).
		rec := w.postForm(creatorCookie, "/schedule/"+scriptID.String()+"/archive", nil)
		assert.Equal(t, http.StatusConflict, rec.Code, "FR39: archive must be rejected once published")

		script, err := w.st.VideoScripts().GetByID(ctx, scriptID)
		require.NoError(t, err)
		assert.Equal(t, store.VideoScriptStatusGreenlit, script.Status, "no state change from the rejected archive")
	})

	// ── Step 9: disconnect/reconnect ─────────────────────────────────────
	t.Run("9_disconnect_reconnect", func(t *testing.T) {
		// The fake reports the credential revoked (Google's invalid_grant
		// equivalent, see youtube.ErrRevoked's doc comment).
		fcRevoked := &fake.Client{Err: youtube.ErrRevoked}
		counts, err := w.runSyncCycle(ch.ID, fcRevoked)
		require.NoError(t, err, "a revoked credential must end the workflow run cleanly (FR4/FR14), never fail it")
		assert.EqualValues(t, 1, counts.syncSchedule)
		assert.EqualValues(t, 0, counts.syncOutcomes, "outcome sync must not run once schedule sync hits a revoked credential")

		ch2, err := w.st.Channels().GetByID(ctx, ch.ID)
		require.NoError(t, err)
		assert.Equal(t, store.ConnectionStateNeedsReauth, ch2.ConnectionState, "FR4: a revoked credential marks the Channel needs_reauth")

		// The next scheduled run is a clean, quota-free no-op: neither
		// sync activity runs at all once LoadChannelState reports
		// needs_reauth (FR14's skip gate).
		fcShouldNotBeCalled := &fake.Client{Err: youtube.ErrPermanent}
		counts2, err := w.runSyncCycle(ch.ID, fcShouldNotBeCalled)
		require.NoError(t, err)
		assert.EqualValues(t, 1, counts2.loadState)
		assert.EqualValues(t, 0, counts2.syncSchedule, "a needs_reauth cycle must never invoke SyncSchedule (no YouTube quota touched)")
		assert.EqualValues(t, 0, counts2.syncOutcomes, "a needs_reauth cycle must never invoke SyncOutcomes (no YouTube quota touched)")

		// All prior data is still readable.
		cmpRes := callTool(t, w.mcpConnect(creator.ID), "get_prediction_vs_outcome", mcptools.GetPredictionVsOutcomeInput{ChannelID: ch.ID.String()})
		cmp := decode[mcptools.GetPredictionVsOutcomeOutput](t, cmpRes)
		assert.Len(t, cmp.Rows, 2, "prior comparison data must remain readable while needs_reauth")

		// The Analyst's reconnect attempt is 403.
		canReconnectAnalyst, err := store.CanReconnect(ctx, w.st.Roles(), ch.ID, analyst.ID)
		require.NoError(t, err)
		assert.False(t, canReconnectAnalyst, "FR4/NFR5: only a Creator may reconnect")

		// The Creator reconnects (the same store call
		// web/channel.Handler.HandleCallback's reconnect branch makes
		// after tokens.Store.Save succeeds).
		canReconnectCreator, err := store.CanReconnect(ctx, w.st.Roles(), ch.ID, creator.ID)
		require.NoError(t, err)
		require.True(t, canReconnectCreator)
		require.NoError(t, w.st.Channels().SetConnectionState(ctx, ch.ID, store.ConnectionStateConnected))

		// The following run syncs again.
		fcHealthy := &fake.Client{Schedule: []youtube.Video{}}
		counts3, err := w.runSyncCycle(ch.ID, fcHealthy)
		require.NoError(t, err)
		assert.EqualValues(t, 1, counts3.syncSchedule, "FR14: syncing resumes automatically once reconnected, no manual step beyond reconnect itself")
		assert.EqualValues(t, 1, counts3.syncOutcomes)

		ch3, err := w.st.Channels().GetByID(ctx, ch.ID)
		require.NoError(t, err)
		assert.Equal(t, store.ConnectionStateConnected, ch3.ConnectionState)
	})
}

// mustSyncedVideoID resolves a synced_video's id by its YouTube video id --
// a small lookup helper this file needs since store.SyncStore has no
// by-natural-key getter of its own (only GetByID/ListSchedule).
func mustSyncedVideoID(t *testing.T, ctx context.Context, st *store.Store, channelID uuid.UUID, youtubeVideoID string) uuid.UUID {
	t.Helper()
	vids, _, err := st.Sync().ListSchedule(ctx, channelID, nil, nil, true, 0)
	require.NoError(t, err)
	for _, v := range vids {
		if v.YouTubeVideoID == youtubeVideoID {
			return v.ID
		}
	}
	t.Fatalf("no synced_video found for youtube_video_id %q", youtubeVideoID)
	return uuid.Nil
}
