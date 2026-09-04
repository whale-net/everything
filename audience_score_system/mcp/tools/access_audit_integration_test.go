//go:build integration

// This file only builds under the "integration" build tag so `bazel test
// //...` (which runs on Docker-less machines too) never compiles or runs
// it. Mirrors access_integration_test.go's pattern: spin up a throwaway
// Postgres via dbtest, apply the real embedded migrations, host
// RegisterChannelAccess's get_channel_access (access_audit.go, issue
// #1719, FR35) behind a real *mcp.Server over HTTP, and drive it with a
// real in-process MCP client.
//
// The load-bearing assertion in this file is
// TestGetChannelAccess_Analyst_DeniedWithNoRosterAndNoHistory: FR35's
// stricter store.CanViewAudit gate (Founder/Co-Creator only) refuses an
// Analyst the WHOLE tool -- neither roster nor history leak through.
//
// Run it explicitly (requires a working Docker daemon):
//
//	bazel test //audience_score_system/mcp/tools:access_audit_integration_test --test_output=all
package tools_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/google/uuid"
	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/whale-net/everything/audience_score_system/mcp/server"
	"github.com/whale-net/everything/audience_score_system/mcp/tools"
	"github.com/whale-net/everything/audience_score_system/migrate/schema"
	"github.com/whale-net/everything/audience_score_system/store"
	"github.com/whale-net/everything/libs/go/dbtest"
	"github.com/whale-net/everything/libs/go/mcpauth"
	"github.com/whale-net/everything/libs/go/migrate"
)

// accessAuditFixture wires an isolated Postgres (dbtest, real embedded
// migrations) and a real *mcp.Server hosting get_channel_access over HTTP,
// plus a Channel with a Founder, a Co-Creator, an Analyst, and an
// unassociated outsider.
type accessAuditFixture struct {
	pg       *dbtest.Postgres
	st       *store.Store
	creds    mcpauth.CredentialStore
	ch       store.Channel
	founder  store.Person
	coCr     store.Person
	analyst  store.Person
	outsider store.Person
	url      string
}

func newAccessAuditFixture(t *testing.T) *accessAuditFixture {
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

	founder, _, err := st.Persons().UpsertByGoogleSubject(ctx, "sub-aa-founder-"+uuid.NewString(), "aa-founder@example.com", "Founder")
	require.NoError(t, err)
	coCr, _, err := st.Persons().UpsertByGoogleSubject(ctx, "sub-aa-cocreator-"+uuid.NewString(), "aa-cocreator@example.com", "Co-Creator")
	require.NoError(t, err)
	analyst, _, err := st.Persons().UpsertByGoogleSubject(ctx, "sub-aa-analyst-"+uuid.NewString(), "aa-analyst@example.com", "Analyst")
	require.NoError(t, err)
	outsider, _, err := st.Persons().UpsertByGoogleSubject(ctx, "sub-aa-outsider-"+uuid.NewString(), "aa-outsider@example.com", "Outsider")
	require.NoError(t, err)

	ch, err := st.Channels().Create(ctx, "yt-aa-"+uuid.NewString(), "Access Audit Channel", founder.ID)
	require.NoError(t, err)
	require.NoError(t, st.Roles().AddRole(ctx, ch.ID, coCr.ID, store.RoleCoCreator, founder.ID))
	require.NoError(t, st.Roles().AddRole(ctx, ch.ID, analyst.ID, store.RoleAnalyst, founder.ID))

	srv := server.New(st)
	reg := server.NewRegistry(srv, st)
	tools.RegisterChannelAccess(reg, st.Access(), st.Roles())

	handler := server.NewHTTPHandler(srv, creds, server.ResourceMetadataConfig{
		Resource:            "https://mcp.example.com",
		AuthorizationServer: "https://web.example.com",
		ResourceName:        "Test MCP",
	})
	ts := httptest.NewServer(handler)
	t.Cleanup(ts.Close)

	return &accessAuditFixture{pg: pg, st: st, creds: creds, ch: ch, founder: founder, coCr: coCr, analyst: analyst, outsider: outsider, url: ts.URL}
}

type aaBearerRoundTripper struct{ token string }

func (rt aaBearerRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	req = req.Clone(req.Context())
	req.Header.Set("Authorization", "Bearer "+rt.token)
	return http.DefaultTransport.RoundTrip(req)
}

func (f *accessAuditFixture) connect(t *testing.T, personID uuid.UUID) *mcp.ClientSession {
	t.Helper()
	ctx := context.Background()

	token, _, err := f.creds.Mint(ctx, personID.String())
	require.NoError(t, err)

	transport := &mcp.StreamableClientTransport{
		Endpoint:   f.url,
		HTTPClient: &http.Client{Transport: aaBearerRoundTripper{token: token}},
	}
	client := mcp.NewClient(&mcp.Implementation{Name: "test-client", Version: "0.0.1"}, nil)
	cs, err := client.Connect(ctx, transport, nil)
	require.NoError(t, err)
	t.Cleanup(func() { _ = cs.Close() })
	return cs
}

func (f *accessAuditFixture) call(t *testing.T, cs *mcp.ClientSession, args any) *mcp.CallToolResult {
	t.Helper()
	res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: "get_channel_access", Arguments: args})
	require.NoError(t, err)
	return res
}

func aaTextOf(res *mcp.CallToolResult) string {
	var sb string
	for _, c := range res.Content {
		if tc, ok := c.(*mcp.TextContent); ok {
			sb += tc.Text
		}
	}
	return sb
}

func aaDecode(t *testing.T, res *mcp.CallToolResult) tools.GetChannelAccessOutput {
	t.Helper()
	require.False(t, res.IsError, "unexpected tool error: %s", aaTextOf(res))
	var out tools.GetChannelAccessOutput
	body, err := json.Marshal(res.StructuredContent)
	require.NoError(t, err)
	require.NoError(t, json.Unmarshal(body, &out))
	return out
}

// ── Founder and Co-Creator: roster + history ────────────────────────────

func TestGetChannelAccess_Founder_SeesRosterAndHistory(t *testing.T) {
	f := newAccessAuditFixture(t)
	cs := f.connect(t, f.founder.ID)

	res := f.call(t, cs, tools.GetChannelAccessInput{ChannelID: f.ch.ID.String()})
	out := aaDecode(t, res)

	require.Len(t, out.Roster, 3, "founder + co-creator + analyst")
	var roles []string
	for _, r := range out.Roster {
		roles = append(roles, r.Role)
	}
	assert.ElementsMatch(t, []string{string(store.RoleCreator), string(store.RoleCoCreator), string(store.RoleAnalyst)}, roles)

	// Two grants recorded beyond the Founder's own self-granted row: the
	// migration's grant/revoke union backs history (FR35).
	require.GreaterOrEqual(t, len(out.History), 2, "co-creator and analyst grants must both appear")
}

func TestGetChannelAccess_CoCreator_SeesRosterAndHistory(t *testing.T) {
	f := newAccessAuditFixture(t)
	cs := f.connect(t, f.coCr.ID)

	res := f.call(t, cs, tools.GetChannelAccessInput{ChannelID: f.ch.ID.String()})
	out := aaDecode(t, res)

	assert.Len(t, out.Roster, 3)
	assert.NotEmpty(t, out.History)
}

// ── Analyst: denied entirely, no partial response ───────────────────────

// TestGetChannelAccess_Analyst_DeniedWithNoRosterAndNoHistory is FR35's
// load-bearing assertion: RegisterRead's automatic ChannelScoped gate
// (store.CanRead) would let an Analyst through -- store.CanViewAudit must
// refuse them explicitly, and the refusal must be total: no roster, no
// history, not a partially-filled response.
func TestGetChannelAccess_Analyst_DeniedWithNoRosterAndNoHistory(t *testing.T) {
	f := newAccessAuditFixture(t)
	cs := f.connect(t, f.analyst.ID)

	res := f.call(t, cs, tools.GetChannelAccessInput{ChannelID: f.ch.ID.String()})
	require.True(t, res.IsError, "an Analyst must be refused this tool entirely (FR35)")

	out := tools.GetChannelAccessOutput{}
	if res.StructuredContent != nil {
		body, err := json.Marshal(res.StructuredContent)
		require.NoError(t, err)
		require.NoError(t, json.Unmarshal(body, &out))
	}
	assert.Empty(t, out.Roster, "an Analyst must never see the roster")
	assert.Empty(t, out.History, "an Analyst must never see the history")
}

// ── non-member: the existing ChannelScoped rejection ────────────────────

func TestGetChannelAccess_NonMember_Rejected(t *testing.T) {
	f := newAccessAuditFixture(t)
	cs := f.connect(t, f.outsider.ID)

	res := f.call(t, cs, tools.GetChannelAccessInput{ChannelID: f.ch.ID.String()})
	assert.True(t, res.IsError, "a non-member must be rejected by RegisterRead's ChannelScoped gate before the handler even runs")
}

// ── history ordering, grant+revoke coverage, and history_limit ─────────

// TestGetChannelAccess_History_NewestFirstIncludesGrantAndRevoke_RespectsLimit
// proves history is most-recent-first, includes both a grant and a revoke
// event, and respects an explicit history_limit.
func TestGetChannelAccess_History_NewestFirstIncludesGrantAndRevoke_RespectsLimit(t *testing.T) {
	f := newAccessAuditFixture(t)
	ctx := context.Background()

	// Promote a fresh Analyst, then revoke it -- one grant, one revoke,
	// beyond the fixture's own two grants (co-creator, analyst).
	third, _, err := f.st.Persons().UpsertByGoogleSubject(ctx, "sub-aa-third-"+uuid.NewString(), "aa-third@example.com", "Third")
	require.NoError(t, err)
	require.NoError(t, f.st.Roles().AddRole(ctx, f.ch.ID, third.ID, store.RoleAnalyst, f.founder.ID))
	removed, err := f.st.Roles().RemoveRole(ctx, f.ch.ID, third.ID, f.founder.ID)
	require.NoError(t, err)
	require.True(t, removed)

	cs := f.connect(t, f.founder.ID)

	res := f.call(t, cs, tools.GetChannelAccessInput{ChannelID: f.ch.ID.String()})
	out := aaDecode(t, res)

	require.GreaterOrEqual(t, len(out.History), 4, "co-creator grant, analyst grant, third grant, third revoke")

	var sawGranted, sawRevoked bool
	for i, ev := range out.History {
		if ev.Event == "granted" {
			sawGranted = true
		}
		if ev.Event == "revoked" {
			sawRevoked = true
		}
		if i > 0 {
			assert.GreaterOrEqual(t, out.History[i-1].OccurredAt, ev.OccurredAt, "history must be most-recent-first")
		}
	}
	assert.True(t, sawGranted, "history must include grant events")
	assert.True(t, sawRevoked, "history must include revoke events")

	limited := f.call(t, cs, tools.GetChannelAccessInput{ChannelID: f.ch.ID.String(), HistoryLimit: 1})
	limitedOut := aaDecode(t, limited)
	assert.Len(t, limitedOut.History, 1, "history_limit must be respected")
}

// ── unknown actor/granter rendering ──────────────────────────────────────

// TestGetChannelAccess_PreM2Row_UnknownGranterAndActor simulates a
// pre-migration-009 channel_person row -- granted_by_person_id NULL, as
// every row predating M2's grant attribution columns is -- by writing one
// directly (bypassing RoleStore.AddRole, which always attributes a
// granter) and proves both the roster's granted_by_display_name and the
// corresponding history entry's actor_display_name render "unknown"
// rather than a fabricated name.
func TestGetChannelAccess_PreM2Row_UnknownGranterAndActor(t *testing.T) {
	f := newAccessAuditFixture(t)
	ctx := context.Background()

	preM2, _, err := f.st.Persons().UpsertByGoogleSubject(ctx, "sub-aa-prem2-"+uuid.NewString(), "aa-prem2@example.com", "Pre M2")
	require.NoError(t, err)
	_, err = f.pg.Pool.Exec(ctx, `
		INSERT INTO channel_person (channel_id, person_id, role, granted_by_person_id)
		VALUES ($1, $2, 'analyst', NULL)
	`, f.ch.ID, preM2.ID)
	require.NoError(t, err)

	cs := f.connect(t, f.founder.ID)
	res := f.call(t, cs, tools.GetChannelAccessInput{ChannelID: f.ch.ID.String()})
	out := aaDecode(t, res)

	var preM2Entry *tools.RosterEntryOutput
	for i := range out.Roster {
		if out.Roster[i].PersonID == preM2.ID.String() {
			preM2Entry = &out.Roster[i]
		}
	}
	require.NotNil(t, preM2Entry)
	assert.Equal(t, "unknown", preM2Entry.GrantedByDisplayName)

	var preM2Event *tools.AuditEventOutput
	for i := range out.History {
		if out.History[i].SubjectPersonID == preM2.ID.String() {
			preM2Event = &out.History[i]
		}
	}
	require.NotNil(t, preM2Event, "the pre-M2 row must still surface as a grant event in history")
	assert.Equal(t, "unknown", preM2Event.ActorDisplayName)
}
