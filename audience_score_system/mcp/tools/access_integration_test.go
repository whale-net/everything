//go:build integration

// Real Postgres + real HTTP-transport MCP client integration test for
// access.go's tool group (issue #1718): invite_co_creator (FR30),
// promote_to_co_creator (FR31), and remove_channel_person (FR33) --
// mirroring schedule_draft_integration_test.go's pattern (dbtest +
// real embedded migrations + a real *mcp.Server behind httptest.Server).
// This file is self-contained (its own newAccessTestDB/
// newTestCredentialStore/bearerRoundTripper/decode helpers) since each
// *_integration_test.go in this package is its own go_test target,
// compiled alone -- see BUILD.bazel's comment on this rule and every
// sibling *_integration_test.go for the same pattern.
//
// access_test.go's pure-Go suite already covers flipRefBit's round-trip
// property, ref-decoding branches against fakes, and argument validation;
// this file proves what that cannot: real store.CanInvite/store.CanRemove
// authorization wired through server.RegisterWrite, the actual
// grant/revoke SCD2 writes (valid_to, granted_by_person_id,
// revoked_by_person_id) landing correctly in Postgres, and idempotency-key
// replay converging without a second mutation. See issue #1718's Testing
// section, which every test function below is named after.
//
// Run it explicitly (requires a working Docker daemon):
//
//	bazel test //audience_score_system/mcp/tools:access_integration_test --test_output=all
package tools_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
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

// newAccessTestDB provisions an isolated Postgres database via dbtest and
// applies every migration in the package's own embedded schema, mirroring
// schedule_draft_integration_test.go's newScheduleDraftTestDB.
func newAccessTestDB(t *testing.T) *dbtest.Postgres {
	t.Helper()
	ctx := context.Background()

	pg := dbtest.NewPostgres(ctx, t, dbtest.Options{})

	sqlDB, err := sql.Open("pgx", pg.ConnString)
	require.NoError(t, err)
	t.Cleanup(func() { _ = sqlDB.Close() })

	runner := migrate.NewRunner(sqlDB, schema.Migrations, schema.Dir)
	require.NoError(t, runner.Up(), "apply every migration from the real embedded schema")

	return pg
}

// newTestCredentialStore builds the mcpauth.CredentialStore against pool's
// mcp_credential table (migration 006) -- the same construction main.go
// does.
func newTestCredentialStore(t *testing.T, pool *pgxpool.Pool) mcpauth.CredentialStore {
	t.Helper()
	creds, err := mcpauth.NewCredentialStore(context.Background(), mcpauth.StoreConfig{
		Pool:           pool,
		TableName:      "mcp_credential",
		IdentityColumn: "person_id",
		IdentityCast:   "uuid",
	})
	require.NoError(t, err)
	return creds
}

// accessFixture is the common setup every test below needs: a Channel
// with a live Founder, two Co-Creators, an Analyst, and an unassociated
// Outsider, hosted behind a real MCP server with RegisterAccess wired.
type accessFixture struct {
	st         *store.Store
	pool       *pgxpool.Pool
	creds      mcpauth.CredentialStore
	ch         store.Channel
	founder    store.Person
	coCreator  store.Person
	coCreator2 store.Person
	analyst    store.Person
	outsider   store.Person
	url        string
}

func newAccessFixture(t *testing.T) *accessFixture {
	t.Helper()
	ctx := context.Background()

	pg := newAccessTestDB(t)
	st := store.New(pg.Pool)
	creds := newTestCredentialStore(t, pg.Pool)

	founder, _, err := st.Persons().UpsertByGoogleSubject(ctx, "sub-ac-founder-"+uuid.NewString(), "ac-founder@example.com", "Founder Person")
	require.NoError(t, err)
	coCreator, _, err := st.Persons().UpsertByGoogleSubject(ctx, "sub-ac-cocreator-"+uuid.NewString(), "ac-cocreator@example.com", "Co-Creator Person")
	require.NoError(t, err)
	coCreator2, _, err := st.Persons().UpsertByGoogleSubject(ctx, "sub-ac-cocreator2-"+uuid.NewString(), "ac-cocreator2@example.com", "Second Co-Creator Person")
	require.NoError(t, err)
	analyst, _, err := st.Persons().UpsertByGoogleSubject(ctx, "sub-ac-analyst-"+uuid.NewString(), "ac-analyst@example.com", "Analyst Person")
	require.NoError(t, err)
	outsider, _, err := st.Persons().UpsertByGoogleSubject(ctx, "sub-ac-outsider-"+uuid.NewString(), "ac-outsider@example.com", "Outsider Person")
	require.NoError(t, err)

	ch, err := st.Channels().Create(ctx, "yt-ac-"+uuid.NewString(), "Channel", founder.ID)
	require.NoError(t, err)
	require.NoError(t, st.Roles().AddRole(ctx, ch.ID, coCreator.ID, store.RoleCoCreator, founder.ID))
	require.NoError(t, st.Roles().AddRole(ctx, ch.ID, coCreator2.ID, store.RoleCoCreator, founder.ID))
	require.NoError(t, st.Roles().AddRole(ctx, ch.ID, analyst.ID, store.RoleAnalyst, founder.ID))

	srv := server.New(st)
	reg := server.NewRegistry(srv, st)
	tools.RegisterAccess(reg, st)

	handler := server.NewHTTPHandler(srv, creds, server.ResourceMetadataConfig{
		Resource:            "https://mcp.example.com",
		AuthorizationServer: "https://web.example.com",
		ResourceName:        "Test MCP",
	})
	ts := httptest.NewServer(handler)
	t.Cleanup(ts.Close)

	return &accessFixture{
		st: st, pool: pg.Pool, creds: creds, ch: ch,
		founder: founder, coCreator: coCreator, coCreator2: coCreator2, analyst: analyst, outsider: outsider,
		url: ts.URL,
	}
}

// bearerRoundTripper injects an "Authorization: Bearer <token>" header on
// every request -- mirrors every sibling *_integration_test.go's.
type bearerRoundTripper struct{ token string }

func (rt bearerRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	req = req.Clone(req.Context())
	req.Header.Set("Authorization", "Bearer "+rt.token)
	return http.DefaultTransport.RoundTrip(req)
}

// connect mirrors scheduleDraftFixture.connect.
func (f *accessFixture) connect(t *testing.T, personID uuid.UUID) *mcp.ClientSession {
	t.Helper()
	ctx := context.Background()

	token, _, err := f.creds.Mint(ctx, personID.String())
	require.NoError(t, err)

	transport := &mcp.StreamableClientTransport{
		Endpoint:   f.url,
		HTTPClient: &http.Client{Transport: bearerRoundTripper{token: token}},
	}
	client := mcp.NewClient(&mcp.Implementation{Name: "test-client", Version: "0.0.1"}, nil)
	cs, err := client.Connect(ctx, transport, nil)
	require.NoError(t, err)
	t.Cleanup(func() { _ = cs.Close() })
	return cs
}

// call mirrors scheduleDraftFixture.call.
func (f *accessFixture) call(t *testing.T, cs *mcp.ClientSession, name string, args any) *mcp.CallToolResult {
	t.Helper()
	res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: name, Arguments: args})
	require.NoError(t, err)
	return res
}

// acTextOf concatenates every TextContent block in res.Content -- the
// error message a rejected call's Content carries.
func acTextOf(res *mcp.CallToolResult) string {
	var sb string
	for _, c := range res.Content {
		if tc, ok := c.(*mcp.TextContent); ok {
			sb += tc.Text
		}
	}
	return sb
}

func acDecode[T any](t *testing.T, res *mcp.CallToolResult) T {
	t.Helper()
	var out T
	require.False(t, res.IsError, "unexpected tool error: %s", acTextOf(res))
	require.NoError(t, acMapDecode(res.StructuredContent, &out))
	return out
}

func acMapDecode(v any, out any) error {
	body, err := json.Marshal(v)
	if err != nil {
		return err
	}
	return json.Unmarshal(body, out)
}

// openRoleRow is the comparable snapshot TestRemoveChannelPerson_
// UnauthorizedAttempts_PermissionDenied_NoChange uses to prove a rejected
// removal makes byte-identical no change to the target's open row (FR33).
type openRoleRow struct {
	Role      string
	ValidFrom time.Time
	GrantedBy *uuid.UUID
}

func fetchOpenRoleRow(t *testing.T, ctx context.Context, pool *pgxpool.Pool, channelID, personID uuid.UUID) openRoleRow {
	t.Helper()
	var row openRoleRow
	err := pool.QueryRow(ctx, `
		SELECT role, valid_from, granted_by_person_id FROM channel_person
		WHERE channel_id = $1 AND person_id = $2 AND valid_to IS NULL
	`, channelID, personID).Scan(&row.Role, &row.ValidFrom, &row.GrantedBy)
	require.NoError(t, err)
	return row
}

// ── invite_co_creator ─────────────────────────────────────────────────────

func TestInviteCoCreator_Founder_SecondCallReturnsSameCode_AlreadyLive_AnalystInviteUntouched(t *testing.T) {
	f := newAccessFixture(t)
	ctx := context.Background()

	analystInv, err := f.st.Invites().Generate(ctx, f.ch.ID, f.founder.ID, store.RoleAnalyst)
	require.NoError(t, err)

	cs := f.connect(t, f.founder.ID)

	res1 := f.call(t, cs, "invite_co_creator", tools.InviteCoCreatorInput{ChannelID: f.ch.ID.String()})
	out1 := acDecode[tools.InviteCoCreatorOutput](t, res1)
	assert.False(t, out1.AlreadyLive)
	assert.NotEmpty(t, out1.Code)
	assert.Equal(t, "co_creator", out1.Role)

	res2 := f.call(t, cs, "invite_co_creator", tools.InviteCoCreatorInput{ChannelID: f.ch.ID.String()})
	out2 := acDecode[tools.InviteCoCreatorOutput](t, res2)
	assert.True(t, out2.AlreadyLive, "a second call while the Co-Creator invite is still live must report already_live")
	assert.Equal(t, out1.Code, out2.Code, "must return the same code, not mint a second one")

	liveAnalyst, err := f.st.Invites().Lookup(ctx, analystInv.Code)
	require.NoError(t, err)
	assert.Nil(t, liveAnalyst.ConsumedAt, "the live Analyst invite on the same Channel must be untouched")
	assert.Nil(t, liveAnalyst.InvalidatedAt)
}

func TestInviteCoCreator_CoCreatorInvites_Allowed(t *testing.T) {
	f := newAccessFixture(t)
	cs := f.connect(t, f.coCreator.ID)

	res := f.call(t, cs, "invite_co_creator", tools.InviteCoCreatorInput{ChannelID: f.ch.ID.String()})
	out := acDecode[tools.InviteCoCreatorOutput](t, res)
	assert.False(t, out.AlreadyLive)
	assert.NotEmpty(t, out.Code)
}

func TestInviteCoCreator_Analyst_PermissionDenied_NoRowCreated(t *testing.T) {
	f := newAccessFixture(t)
	cs := f.connect(t, f.analyst.ID)

	res := f.call(t, cs, "invite_co_creator", tools.InviteCoCreatorInput{ChannelID: f.ch.ID.String()})
	assert.True(t, res.IsError, "an Analyst must not be able to invite a Co-Creator")

	var count int
	err := f.pool.QueryRow(context.Background(), `SELECT count(*) FROM channel_invite WHERE channel_id = $1`, f.ch.ID).Scan(&count)
	require.NoError(t, err)
	assert.Equal(t, 0, count, "no channel_invite row may be created on a rejected call")
}

// ── promote_to_co_creator ───────────────────────────────────────────────

func TestPromoteToCoCreator_Founder_PromotesAnalyst_ThenIdempotentNoOp(t *testing.T) {
	f := newAccessFixture(t)
	ctx := context.Background()
	cs := f.connect(t, f.founder.ID)

	res1 := f.call(t, cs, "promote_to_co_creator", tools.PromoteToCoCreatorInput{ChannelID: f.ch.ID.String(), PersonID: f.analyst.ID.String()})
	out1 := acDecode[tools.PromoteToCoCreatorOutput](t, res1)
	assert.True(t, out1.Changed)
	assert.Equal(t, "co_creator", out1.Role)

	openRow := fetchOpenRoleRow(t, ctx, f.pool, f.ch.ID, f.analyst.ID)
	assert.Equal(t, "co_creator", openRow.Role)
	require.NotNil(t, openRow.GrantedBy)
	assert.Equal(t, f.founder.ID, *openRow.GrantedBy, "granted_by_person_id must record the promoting caller (FR34)")

	var closedAnalystValidTo *time.Time
	err := f.pool.QueryRow(ctx, `
		SELECT valid_to FROM channel_person WHERE channel_id = $1 AND person_id = $2 AND role = 'analyst'
	`, f.ch.ID, f.analyst.ID).Scan(&closedAnalystValidTo)
	require.NoError(t, err)
	require.NotNil(t, closedAnalystValidTo, "the original analyst row must be closed (valid_to set), not deleted")

	var rowsAfterFirst int
	err = f.pool.QueryRow(ctx, `SELECT count(*) FROM channel_person WHERE channel_id = $1 AND person_id = $2`, f.ch.ID, f.analyst.ID).Scan(&rowsAfterFirst)
	require.NoError(t, err)
	assert.Equal(t, 2, rowsAfterFirst, "closed analyst row + open co_creator row")

	// Promote again -- idempotent no-op (FR31): changed=false, no new row.
	res2 := f.call(t, cs, "promote_to_co_creator", tools.PromoteToCoCreatorInput{ChannelID: f.ch.ID.String(), PersonID: f.analyst.ID.String()})
	out2 := acDecode[tools.PromoteToCoCreatorOutput](t, res2)
	assert.False(t, out2.Changed)
	assert.Equal(t, "co_creator", out2.Role)

	var rowsAfterSecond int
	err = f.pool.QueryRow(ctx, `SELECT count(*) FROM channel_person WHERE channel_id = $1 AND person_id = $2`, f.ch.ID, f.analyst.ID).Scan(&rowsAfterSecond)
	require.NoError(t, err)
	assert.Equal(t, rowsAfterFirst, rowsAfterSecond, "re-promoting an already-co_creator target must not insert a new row")

	stillOpen := fetchOpenRoleRow(t, ctx, f.pool, f.ch.ID, f.analyst.ID)
	assert.Equal(t, openRow, stillOpen, "the open co_creator row itself must be unchanged by the no-op")
}

func TestPromoteToCoCreator_Analyst_PermissionDenied(t *testing.T) {
	f := newAccessFixture(t)
	cs := f.connect(t, f.analyst.ID)

	res := f.call(t, cs, "promote_to_co_creator", tools.PromoteToCoCreatorInput{ChannelID: f.ch.ID.String(), PersonID: f.outsider.ID.String()})
	assert.True(t, res.IsError, "an Analyst must not be able to promote anyone")
}

func TestPromoteToCoCreator_PromotingFounder_Rejected_Unchanged(t *testing.T) {
	f := newAccessFixture(t)
	ctx := context.Background()
	cs := f.connect(t, f.coCreator.ID)

	before := fetchOpenRoleRow(t, ctx, f.pool, f.ch.ID, f.founder.ID)

	res := f.call(t, cs, "promote_to_co_creator", tools.PromoteToCoCreatorInput{ChannelID: f.ch.ID.String(), PersonID: f.founder.ID.String()})
	assert.True(t, res.IsError, "no path may re-tier or demote a Founder")

	after := fetchOpenRoleRow(t, ctx, f.pool, f.ch.ID, f.founder.ID)
	assert.Equal(t, before, after)
	assert.Equal(t, "creator", after.Role)
}

// ── remove_channel_person ───────────────────────────────────────────────

func TestRemoveChannelPerson_AuthorizedRemovals_Succeed(t *testing.T) {
	cases := []struct {
		name         string
		actorOf      func(f *accessFixture) store.Person
		targetOf     func(f *accessFixture) store.Person
	}{
		{"founder removes co_creator", func(f *accessFixture) store.Person { return f.founder }, func(f *accessFixture) store.Person { return f.coCreator }},
		{"founder removes analyst", func(f *accessFixture) store.Person { return f.founder }, func(f *accessFixture) store.Person { return f.analyst }},
		{"co_creator removes analyst", func(f *accessFixture) store.Person { return f.coCreator }, func(f *accessFixture) store.Person { return f.analyst }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newAccessFixture(t)
			ctx := context.Background()
			actor, target := tc.actorOf(f), tc.targetOf(f)

			cs := f.connect(t, actor.ID)
			res := f.call(t, cs, "remove_channel_person", tools.RemoveChannelPersonInput{ChannelID: f.ch.ID.String(), PersonID: target.ID.String()})
			out := acDecode[tools.RemoveChannelPersonOutput](t, res)
			assert.True(t, out.Removed)
			assert.Equal(t, f.ch.ID.String(), out.ChannelID)
			assert.Equal(t, target.ID.String(), out.PersonID)

			var validTo *time.Time
			var revokedBy uuid.UUID
			err := f.pool.QueryRow(ctx, `
				SELECT valid_to, revoked_by_person_id FROM channel_person
				WHERE channel_id = $1 AND person_id = $2 ORDER BY valid_from DESC LIMIT 1
			`, f.ch.ID, target.ID).Scan(&validTo, &revokedBy)
			require.NoError(t, err)
			require.NotNil(t, validTo)
			assert.Equal(t, actor.ID, revokedBy, "revoked_by_person_id must record the removing caller (FR34)")
		})
	}
}

func TestRemoveChannelPerson_UnauthorizedAttempts_PermissionDenied_NoChange(t *testing.T) {
	cases := []struct {
		name     string
		actorOf  func(f *accessFixture) store.Person
		targetOf func(f *accessFixture) store.Person
	}{
		{"co_creator removes founder", func(f *accessFixture) store.Person { return f.coCreator }, func(f *accessFixture) store.Person { return f.founder }},
		{"co_creator removes co_creator", func(f *accessFixture) store.Person { return f.coCreator }, func(f *accessFixture) store.Person { return f.coCreator2 }},
		{"analyst removes co_creator", func(f *accessFixture) store.Person { return f.analyst }, func(f *accessFixture) store.Person { return f.coCreator }},
		{"analyst removes founder", func(f *accessFixture) store.Person { return f.analyst }, func(f *accessFixture) store.Person { return f.founder }},
		{"founder removes self", func(f *accessFixture) store.Person { return f.founder }, func(f *accessFixture) store.Person { return f.founder }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newAccessFixture(t)
			ctx := context.Background()
			actor, target := tc.actorOf(f), tc.targetOf(f)

			before := fetchOpenRoleRow(t, ctx, f.pool, f.ch.ID, target.ID)

			cs := f.connect(t, actor.ID)
			res := f.call(t, cs, "remove_channel_person", tools.RemoveChannelPersonInput{ChannelID: f.ch.ID.String(), PersonID: target.ID.String()})
			assert.True(t, res.IsError, "expected a permission error")

			after := fetchOpenRoleRow(t, ctx, f.pool, f.ch.ID, target.ID)
			assert.Equal(t, before, after, "target's open channel_person row must be byte-identical after a rejected removal (FR33)")
		})
	}
}

func TestRemoveChannelPerson_NoOpenRole_ReturnsRemovedFalse_Success(t *testing.T) {
	f := newAccessFixture(t)
	cs := f.connect(t, f.founder.ID)

	res := f.call(t, cs, "remove_channel_person", tools.RemoveChannelPersonInput{ChannelID: f.ch.ID.String(), PersonID: f.outsider.ID.String()})
	out := acDecode[tools.RemoveChannelPersonOutput](t, res)
	assert.False(t, out.Removed)
	assert.Equal(t, f.outsider.ID.String(), out.PersonID)
}

// ── idempotency-key replay (LB4) ─────────────────────────────────────────

func TestAccessTools_IdempotencyKeyReplay_NoSecondMutation(t *testing.T) {
	t.Run("invite_co_creator", func(t *testing.T) {
		f := newAccessFixture(t)
		cs := f.connect(t, f.founder.ID)
		key := uuid.NewString()
		in := tools.InviteCoCreatorInput{ChannelID: f.ch.ID.String(), IdempotencyKeyArg: key}

		out1 := acDecode[tools.InviteCoCreatorOutput](t, f.call(t, cs, "invite_co_creator", in))
		out2 := acDecode[tools.InviteCoCreatorOutput](t, f.call(t, cs, "invite_co_creator", in))
		assert.Equal(t, out1, out2)

		var count int
		err := f.pool.QueryRow(context.Background(), `
			SELECT count(*) FROM channel_invite WHERE channel_id = $1 AND role = 'co_creator'
		`, f.ch.ID).Scan(&count)
		require.NoError(t, err)
		assert.Equal(t, 1, count, "a replay must not mint a second invite")
	})

	t.Run("promote_to_co_creator", func(t *testing.T) {
		f := newAccessFixture(t)
		cs := f.connect(t, f.founder.ID)
		key := uuid.NewString()
		in := tools.PromoteToCoCreatorInput{ChannelID: f.ch.ID.String(), PersonID: f.analyst.ID.String(), IdempotencyKeyArg: key}

		out1 := acDecode[tools.PromoteToCoCreatorOutput](t, f.call(t, cs, "promote_to_co_creator", in))
		out2 := acDecode[tools.PromoteToCoCreatorOutput](t, f.call(t, cs, "promote_to_co_creator", in))
		assert.Equal(t, out1, out2)
		assert.True(t, out1.Changed)

		var count int
		err := f.pool.QueryRow(context.Background(), `
			SELECT count(*) FROM channel_person WHERE channel_id = $1 AND person_id = $2
		`, f.ch.ID, f.analyst.ID).Scan(&count)
		require.NoError(t, err)
		assert.Equal(t, 2, count, "a replay must not insert a second row -- one closed analyst + one open co_creator")
	})

	t.Run("remove_channel_person", func(t *testing.T) {
		f := newAccessFixture(t)
		cs := f.connect(t, f.founder.ID)
		key := uuid.NewString()
		in := tools.RemoveChannelPersonInput{ChannelID: f.ch.ID.String(), PersonID: f.analyst.ID.String(), IdempotencyKeyArg: key}

		out1 := acDecode[tools.RemoveChannelPersonOutput](t, f.call(t, cs, "remove_channel_person", in))
		out2 := acDecode[tools.RemoveChannelPersonOutput](t, f.call(t, cs, "remove_channel_person", in))
		assert.Equal(t, out1, out2)
		assert.True(t, out1.Removed)
	})
}
