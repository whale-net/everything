//go:build integration

// This file only builds under the "integration" build tag so `bazel test
// //...` (which runs on Docker-less machines too) never compiles or runs
// it. Run it explicitly (requires a working Docker daemon):
//
//	bazel test //whagent_net/worker/tools:dispatch_cross_domain_integration_test --test_output=all
//
// Issue #2121's Testing section, "Cross-domain integration test": worker's
// tool dispatch (whagent_net/worker/tools.Dispatcher, issue #2118) against
// a REAL audience_score_system/mcp instance (built from its own real,
// exported constructors -- server.New/NewRegistry/RegisterRead/
// RegisterWrite/WhagentPersonMiddleware/NewDualAuthHTTPHandler, issue
// #2116 -- never a hand-rolled substitute), both compiled and run from
// Bazel in one process, over a real *mcp.ClientSession and a real
// Postgres-backed store on the ASS side. This is the first test that
// exercises #2116 (ASS's whagent-net caller-auth path) and #2118 (worker's
// dispatch path) together -- the real FR8/FR10/FR12 proof:
//
//   - a tool call carries the persona credential Dispatcher mints via
//     whagent_net/api/persona.Issuer (FR10);
//   - ASS's WhagentPersonMiddleware verifies it against a real
//     libs/go/whagent.Verifier and resolves (iss, sub) to an
//     auto-provisioned Person (FR12);
//   - the tool result comes back through Dispatch verbatim (FR2) --
//     "transcribed" itself (the transcript_event commit) is
//     activities.go's DispatchTool, an activity-boundary wrapper over
//     this same Dispatcher.Dispatch call, covered by that file's own
//     doc comment and workflow_test.go's workflow-level coverage; this
//     file's job is proving the dispatch call itself is genuine
//     end-to-end, not re-proving the transcript-commit wiring;
//   - a retried mutating call applies its mutation once, "across both
//     idempotency ledgers" (FR11) -- whagent-net's own
//     session.IdempotencyLedger AND audience_score_system's independent
//     (tool, person_id, key) guard (libs/go/whagent's IdempotencyGuardScope
//     doc comment) -- see TestDispatch_CrossDomain_RetriedMutatingCall_
//     AppliesOnce_AcrossBothLedgers below for exactly how each layer is
//     isolated and proven.
//
// Red/green (this task's Testing section): point WhagentAuthConfig at the
// WRONG audience (simulating ASS's verifier being misconfigured against
// whagent-net's real JWKS/issuer/audience) and observe
// TestDispatch_CrossDomain_PersonaCredentialVerified_PersonAutoProvisioned
// go red with an authentication failure, then revert -- exercised by hand
// during this task's Testing phase (see the task's PR/issue comment), not
// automated as a third test case, since deliberately shipping a
// permanently-broken audience value would defeat the fixture's own
// purpose.
package tools_test

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"database/sql"
	"net"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/google/uuid"
	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/whale-net/everything/audience_score_system/mcp/server"
	assschema "github.com/whale-net/everything/audience_score_system/migrate/schema"
	"github.com/whale-net/everything/audience_score_system/store"
	"github.com/whale-net/everything/libs/go/dbtest"
	"github.com/whale-net/everything/libs/go/mcpauth"
	"github.com/whale-net/everything/libs/go/migrate"
	"github.com/whale-net/everything/libs/go/whagent"
	"github.com/whale-net/everything/whagent_net/api/persona"
	"github.com/whale-net/everything/whagent_net/llm"
	whagentschema "github.com/whale-net/everything/whagent_net/migrate/schema"
	"github.com/whale-net/everything/whagent_net/session"
	"github.com/whale-net/everything/whagent_net/worker/tools"
)

// mcpTestWriteResultSchema is a scratch table for this file's write-tool
// fixture (mirrors audience_score_system/mcp/server/
// server_integration_test.go's own mcpTestWriteResultSchema exactly) --
// its `calls` column is the single source of truth this test reads back
// to prove how many times the mutation genuinely ran against Postgres,
// independent of any in-process counter.
const mcpTestWriteResultSchema = `
CREATE TABLE mcp_test_write_result (
    id    UUID PRIMARY KEY,
    calls INT  NOT NULL
);
`

// newASSTestDB provisions an isolated Postgres database for
// audience_score_system's own schema (person, person_oidc_identity,
// mcp_credential, mcp_idempotency, ...) plus this file's scratch write
// table.
func newASSTestDB(t *testing.T) *dbtest.Postgres {
	t.Helper()
	ctx := context.Background()

	pg := dbtest.NewPostgres(ctx, t, dbtest.Options{Schema: mcpTestWriteResultSchema})

	sqlDB, err := sql.Open("pgx", pg.ConnString)
	require.NoError(t, err)
	t.Cleanup(func() { _ = sqlDB.Close() })

	runner := migrate.NewRunner(sqlDB, assschema.Migrations, assschema.Dir)
	require.NoError(t, runner.Up(), "apply every migration from audience_score_system's own real embedded schema")

	return pg
}

// newWhagentLedger provisions a SEPARATE, independent Postgres database
// for whagent-net's own schema and returns a ready session.IdempotencyLedger
// -- deliberately a distinct database from ASS's (newASSTestDB), the same
// way the two domains' real deployments never share a database, so this
// file's "across both idempotency ledgers" test genuinely exercises two
// independent stores rather than one shared one.
func newWhagentLedger(t *testing.T) session.IdempotencyLedger {
	t.Helper()
	ctx := context.Background()

	pg := dbtest.NewPostgres(ctx, t, dbtest.Options{})
	sqlDB, err := sql.Open("pgx", pg.ConnString)
	require.NoError(t, err)
	t.Cleanup(func() { _ = sqlDB.Close() })

	runner := migrate.NewRunner(sqlDB, whagentschema.Migrations, whagentschema.Dir)
	require.NoError(t, runner.Up(), "apply every migration from whagent-net's own real embedded schema")

	st := session.New(pg.Pool, nil)
	return st.Idempotency()
}

// whagentKeyPair mints a real Ed25519 whagent.Signer/Verifier pair for one
// issuer -- mirrors audience_score_system/mcp/server/
// whagent_auth_integration_test.go's newTestWhagentSignerVerifier.
func whagentKeyPair(t *testing.T, issuer string) (*whagent.Signer, *whagent.Verifier) {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	signer, err := whagent.New(priv, issuer, "test-key-1")
	require.NoError(t, err)
	verifier, err := whagent.NewVerifierFromKey(pub, issuer)
	require.NoError(t, err)
	return signer, verifier
}

// whoamiProbeHandler counts invocations of an unscoped, no-input read
// tool -- proof the call reached ASS's tool handler at all, independent
// of the response body it returns.
func whoamiProbeHandler(counter *int32) mcp.ToolHandlerFor[any, whoamiProbeOutput] {
	return func(ctx context.Context, _ *mcp.CallToolRequest, _ any) (*mcp.CallToolResult, whoamiProbeOutput, error) {
		atomic.AddInt32(counter, 1)
		person := server.PersonFromContext(ctx)
		if person == nil {
			return nil, whoamiProbeOutput{}, assertNoPersonErr
		}
		return nil, whoamiProbeOutput{PersonID: person.ID.String()}, nil
	}
}

type whoamiProbeOutput struct {
	PersonID string `json:"person_id"`
}

var assertNoPersonErr = errNoPerson{}

type errNoPerson struct{}

func (errNoPerson) Error() string { return "no Person resolved on context" }

// mutateProbeInput is a write tool's input carrying FR11's
// idempotency_key argument -- the exact shape dispatch.go's Dispatch
// injects via whagent.IdempotencyKeyArgument, and the same shape
// audience_score_system/mcp/server's own IdempotencyKeyed interface
// expects (libs/go/whagent's IdempotencyKeyArgument doc comment: "matches
// what audience_score_system already ships").
type mutateProbeInput struct {
	Key string `json:"idempotency_key,omitempty"`
}

func (i mutateProbeInput) IdempotencyKey() string { return i.Key }

type mutateProbeOutput struct {
	Calls int `json:"calls"`
}

// mutateProbeHandler persists its "how many times has this genuinely run"
// state in Postgres (mcp_test_write_result), mirroring
// server_integration_test.go's pgWriteHandler -- Invoked is a local,
// in-process counter this test also asserts on, kept separate from the
// Postgres-durable Calls value the same way that file's own fixture does.
type mutateProbeHandler struct {
	pg      *dbtest.Postgres
	invoked int32
}

func (h *mutateProbeHandler) mutate(ctx context.Context, _ mutateProbeInput) (uuid.UUID, error) {
	atomic.AddInt32(&h.invoked, 1)
	id := uuid.New()
	_, err := h.pg.Pool.Exec(ctx, `
		INSERT INTO mcp_test_write_result (id, calls)
		SELECT $1, count(*) + 1 FROM mcp_test_write_result
	`, id)
	return id, err
}

func (h *mutateProbeHandler) render(ctx context.Context, ref uuid.UUID) (*mcp.CallToolResult, mutateProbeOutput, error) {
	var calls int
	if err := h.pg.Pool.QueryRow(ctx, `SELECT calls FROM mcp_test_write_result WHERE id = $1`, ref).Scan(&calls); err != nil {
		return nil, mutateProbeOutput{}, err
	}
	return nil, mutateProbeOutput{Calls: calls}, nil
}

func (h *mutateProbeHandler) invokedCount() int32 { return atomic.LoadInt32(&h.invoked) }

// mutationRowCount reads mcp_test_write_result's row count directly --
// the ground truth for "how many times did the mutation genuinely run
// against Postgres", independent of what any handler counter or Dispatch
// result claims.
func mutationRowCount(t *testing.T, ctx context.Context, pg *dbtest.Postgres) int {
	t.Helper()
	var n int
	require.NoError(t, pg.Pool.QueryRow(ctx, `SELECT count(*) FROM mcp_test_write_result`).Scan(&n))
	return n
}

// crossDomainFixture bundles one real ASS mcp instance (real store, real
// dual-auth handler, real httptest.Server) plus the whagent-net-side
// pieces (a real persona.Issuer over a real Signer/Verifier pair, and a
// real whagent-net Postgres-backed IdempotencyLedger) that together let
// this file's tests drive tools.Dispatcher.Dispatch exactly the way
// worker/activities.go's DispatchTool activity does in production.
type crossDomainFixture struct {
	assPG      *dbtest.Postgres
	serverURL  string
	whoamiHits *int32
	mutate     *mutateProbeHandler
	issuer     *persona.Issuer
	ledger     session.IdempotencyLedger
	sess       *session.Session
}

const crossDomainIssuer = "https://whagent.example.test"
const crossDomainOnBehalfOfIss = "https://keycloak.example.test/realms/humans"

func newCrossDomainFixture(t *testing.T) *crossDomainFixture {
	t.Helper()
	assPG := newASSTestDB(t)
	st := store.New(assPG.Pool)

	signer, verifier := whagentKeyPair(t, crossDomainIssuer)

	srv := server.New(st)
	reg := server.NewRegistry(srv, st)
	var whoamiHits int32
	server.RegisterRead(reg, &mcp.Tool{Name: "whoami_probe", Description: "cross-domain test probe"}, whoamiProbeHandler(&whoamiHits))
	mutateHandler := &mutateProbeHandler{pg: assPG}
	server.RegisterWrite(reg, &mcp.Tool{Name: "mutate_probe", Description: "cross-domain test mutation probe"}, mutateHandler.mutate, mutateHandler.render)

	srv.AddReceivingMiddleware(server.WhagentPersonMiddleware(st.PersonIdentities()))

	creds, err := mcpauth.NewCredentialStore(context.Background(), mcpauth.StoreConfig{
		Pool:           assPG.Pool,
		TableName:      "mcp_credential",
		IdentityColumn: "person_id",
		IdentityCast:   "uuid",
	})
	require.NoError(t, err)

	// The persona credential's audience (mintCredential/keys.go) is always
	// exactly the ONE target server's own server_url -- the same
	// production convention whagent_net/config/agents.yaml's doc comment
	// documents ("issue #2116's server_url target"). httptest.Server only
	// assigns its address once started, so the listener is created first
	// (a fixed local address) and its URL used as BOTH the audience this
	// fixture's WhagentAuthConfig verifies against and the tool_set
	// server_url tests dispatch calls to -- otherwise the two would never
	// agree and every call would 401.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	// No trailing slash -- must match httptest.Server.URL's own format
	// exactly (http://<addr>), since ts.URL below is what's actually
	// stored as this fixture's tool_set server_url / persona-credential
	// audience once the transport round-trips real HTTP requests to it.
	serverURL := "http://" + ln.Addr().String()

	handler := server.NewDualAuthHTTPHandler(srv, creds, server.WhagentAuthConfig{
		Verifier: verifier,
		Audience: serverURL,
	}, server.ResourceMetadataConfig{
		Resource:            serverURL,
		AuthorizationServer: "https://web.example.test",
		ResourceName:        "Cross-domain test MCP",
	})
	ts := httptest.NewUnstartedServer(handler)
	_ = ts.Listener.Close()
	ts.Listener = ln
	ts.Start()
	t.Cleanup(ts.Close)

	sess := &session.Session{
		SessionID: uuid.New(),
		Subject: session.Subject{
			Iss:  crossDomainOnBehalfOfIss,
			Sub:  "human-worker-actor-1",
			Kind: session.SubjectKindHuman,
		},
		OnBehalfOf: session.Subject{
			Iss:  crossDomainOnBehalfOfIss,
			Sub:  "human-cross-domain-1",
			Kind: session.SubjectKindHuman,
		},
		AgentID: "cross-domain-test-agent",
		Model:   "test-model",
		Status:  session.StatusRunning,
	}

	return &crossDomainFixture{
		assPG:      assPG,
		serverURL:  ts.URL,
		whoamiHits: &whoamiHits,
		mutate:     mutateHandler,
		issuer:     persona.NewIssuer(signer),
		ledger:     newWhagentLedger(t),
		sess:       sess,
	}
}

func (f *crossDomainFixture) toolSet() []session.ToolServerRef {
	return []session.ToolServerRef{{ServerURL: f.serverURL, AllowedTools: nil}}
}

func (f *crossDomainFixture) personCount(t *testing.T, ctx context.Context, iss, sub string) int {
	t.Helper()
	var n int
	require.NoError(t, f.assPG.Pool.QueryRow(ctx, `
		SELECT COUNT(*) FROM person p JOIN person_oidc_identity i ON i.person_id = p.id
		WHERE i.iss = $1 AND i.sub = $2
	`, iss, sub).Scan(&n))
	return n
}

// TestDispatch_CrossDomain_PersonaCredentialVerified_PersonAutoProvisioned_ResultReturned
// is this task's FR8/FR10/FR12 proof: Dispatcher.Dispatch, driven exactly
// as activities.go's DispatchTool drives it, mints a persona credential
// (FR10), ASS's real whagent-net auth path verifies it and auto-
// provisions the on-behalf-of subject as a Person (FR12), the tool call
// actually reaches ASS's handler (FR8 -- only a tool the server exposes
// is ever called), and the result comes back through Dispatch verbatim
// (FR2).
func TestDispatch_CrossDomain_PersonaCredentialVerified_PersonAutoProvisioned_ResultReturned(t *testing.T) {
	ctx := context.Background()
	f := newCrossDomainFixture(t)

	dispatcher := &tools.Dispatcher{Issuer: f.issuer, Ledger: f.ledger}
	result, err := dispatcher.Dispatch(ctx, tools.DispatchInput{
		Session:   f.sess,
		AgentID:   f.sess.AgentID,
		ToolSet:   f.toolSet(),
		Turn:      1,
		CallIndex: 0,
		Call:      llm.ToolCall{ID: "call-1", Name: "whoami_probe", Arguments: "{}"},
	})
	require.NoError(t, err)
	assert.False(t, result.IsError, "unexpected tool error: %s", result.Content)
	assert.Equal(t, "call-1", result.ToolCallID)
	assert.Equal(t, "whoami_probe", result.Name)
	assert.Equal(t, int32(1), atomic.LoadInt32(f.whoamiHits), "the dispatched call must actually reach ASS's real tool handler")

	assert.Equal(t, 1, f.personCount(t, ctx, f.sess.OnBehalfOf.Iss, f.sess.OnBehalfOf.Sub),
		"the whagent-verified (iss, sub) must auto-provision exactly one Person on ASS's side (FR12)")
}

// TestDispatch_CrossDomain_UnexposedTool_NeverCalled proves FR8's "a tool
// no configured server exposes is not callable": Dispatch must fail
// BEFORE ever issuing a CallTool against any server, and the ASS-side
// handler for the (unrelated) tool that does exist must never see a hit
// from this call.
func TestDispatch_CrossDomain_UnexposedTool_NeverCalled(t *testing.T) {
	ctx := context.Background()
	f := newCrossDomainFixture(t)

	dispatcher := &tools.Dispatcher{Issuer: f.issuer, Ledger: f.ledger}
	_, err := dispatcher.Dispatch(ctx, tools.DispatchInput{
		Session:   f.sess,
		AgentID:   f.sess.AgentID,
		ToolSet:   f.toolSet(),
		Turn:      1,
		CallIndex: 0,
		Call:      llm.ToolCall{ID: "call-1", Name: "not_a_real_tool", Arguments: "{}"},
	})
	require.Error(t, err)
	assert.Equal(t, int32(0), atomic.LoadInt32(f.whoamiHits))
}

// TestDispatch_CrossDomain_RetriedMutatingCall_AppliesOnce_AcrossBothLedgers
// is FR11's cross-domain proof, isolating each of the two independent
// idempotency layers dispatch.go's package doc comment describes:
//
//  1. An ordinary Temporal-style retry (the SAME DispatchInput dispatched
//     twice) is caught by whagent-net's OWN ledger: the second Dispatch
//     call finds its (session, turn, call_index) reservation already
//     carries a recorded outcome and returns it verbatim WITHOUT ever
//     calling ASS again -- proven by the mutation row count staying at 1
//     after the second Dispatch call.
//  2. The narrower "activity ran the mutation but crashed before
//     RecordOutcome" window -- where whagent-net's own ledger has a
//     RESERVATION but no OUTCOME yet, so Dispatch does NOT short-circuit
//     and calls ASS again -- is where ASS's OWN, entirely independent
//     (tool, person_id, key) guard (libs/go/whagent's
//     IdempotencyGuardScope) is the only thing standing between a retry
//     and a duplicate mutation. This is simulated directly: reserve the
//     key in the whagent ledger without ever recording an outcome (so a
//     subsequent Dispatch call for the same key is forced past the
//     short-circuit and back out to ASS), call the tool once via the raw
//     transport (tools.Connect/CallTool, bypassing Dispatcher entirely,
//     standing in for the crashed first attempt actually reaching ASS),
//     then call Dispatch for real -- it must return ASS's cached outcome,
//     not a second mutation.
func TestDispatch_CrossDomain_RetriedMutatingCall_AppliesOnce_AcrossBothLedgers(t *testing.T) {
	ctx := context.Background()
	f := newCrossDomainFixture(t)
	dispatcher := &tools.Dispatcher{Issuer: f.issuer, Ledger: f.ledger}

	in := tools.DispatchInput{
		Session:   f.sess,
		AgentID:   f.sess.AgentID,
		ToolSet:   f.toolSet(),
		Turn:      2,
		CallIndex: 0,
		Call:      llm.ToolCall{ID: "call-1", Name: "mutate_probe", Arguments: "{}"},
	}

	// -- Layer 1: whagent-net's own ledger short-circuits an ordinary retry --
	first, err := dispatcher.Dispatch(ctx, in)
	require.NoError(t, err)
	assert.False(t, first.IsError, "unexpected tool error: %s", first.Content)
	assert.Equal(t, 1, mutationRowCount(t, ctx, f.assPG), "the first dispatch must apply the mutation exactly once")
	assert.Equal(t, int32(1), f.mutate.invokedCount())

	second, err := dispatcher.Dispatch(ctx, in)
	require.NoError(t, err)
	assert.Equal(t, first, second, "a retried Dispatch for the identical (session, turn, call_index) must return the exact cached outcome")
	assert.Equal(t, 1, mutationRowCount(t, ctx, f.assPG), "whagent-net's own ledger must short-circuit the retry before ever calling ASS again")
	assert.Equal(t, int32(1), f.mutate.invokedCount(), "ASS's handler must not be entered a second time once whagent-net's own ledger already recorded an outcome")

	// -- Layer 2: ASS's own guard catches the "reserved but never recorded" gap --
	gapIn := tools.DispatchInput{
		Session:   f.sess,
		AgentID:   f.sess.AgentID,
		ToolSet:   f.toolSet(),
		Turn:      3,
		CallIndex: 0,
		Call:      llm.ToolCall{ID: "call-2", Name: "mutate_probe", Arguments: "{}"},
	}
	gapKey := whagent.DeriveIdempotencyKey(f.sess.SessionID.String(), gapIn.Turn, gapIn.CallIndex)

	_, err = f.ledger.Reserve(ctx, gapKey, session.ToolCallReservation{
		SessionID: f.sess.SessionID, Turn: gapIn.Turn, CallIndex: gapIn.CallIndex, Tool: "mutate_probe", ServerURL: f.serverURL,
	})
	require.NoError(t, err, "simulate the reservation an in-flight Dispatch call would have made")

	token, err := f.issuer.Issue(ctx, f.sess, f.sess.AgentID, f.serverURL)
	require.NoError(t, err)
	cs, err := tools.Connect(ctx, f.serverURL, token)
	require.NoError(t, err)
	firstAttempt, err := tools.CallTool(ctx, cs, "mutate_probe", map[string]any{whagent.IdempotencyKeyArgument: gapKey})
	require.NoError(t, err)
	require.False(t, firstAttempt.IsError)
	cs.Close()
	// The reservation is deliberately left with no recorded outcome here --
	// standing in for an activity that crashed after this call returned but
	// before RecordOutcome ran.

	require.Equal(t, 2, mutationRowCount(t, ctx, f.assPG), "the simulated 'crashed first attempt' must have genuinely mutated once (on top of the first mutate_probe call above)")

	replayed, err := dispatcher.Dispatch(ctx, gapIn)
	require.NoError(t, err, "Dispatch must proceed past whagent-net's own ledger (no recorded outcome yet) and call ASS again")
	assert.False(t, replayed.IsError, "unexpected tool error: %s", replayed.Content)
	assert.Equal(t, 2, mutationRowCount(t, ctx, f.assPG),
		"ASS's OWN (tool, person_id, key) idempotency guard must return the cached outcome rather than mutating a second time for this key, even though whagent-net's own ledger did not short-circuit")
}
