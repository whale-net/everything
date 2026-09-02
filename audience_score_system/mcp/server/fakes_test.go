package server

// Shared fakes and helpers for this package's pure-Go unit tests
// (registry_test.go, auth_test.go, channelscope_test.go). These run as
// part of `bazel test //...` -- no Docker required -- and complement
// server_integration_test.go's real-Postgres coverage of the same
// wiring (concurrent contention and cross-instance statelessness, which
// genuinely require Postgres and cannot be faked).
//
// Being in package server (not server_test) lets these tests reach
// withPerson and Registry's unexported fields directly, constructing a
// Registry from fakes without going through NewRegistry's *store.Store
// (which only ever wraps a real pgxpool.Pool).

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/google/uuid"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/require"

	"github.com/whale-net/everything/audience_score_system/store"
)

// ── fake RoleStore ──────────────────────────────────────────────────────────

type roleKey struct{ channelID, personID uuid.UUID }

// fakeRoleStore implements store.RoleStore in memory, keyed by
// (channelID, personID) -- enough to drive channelscope.go/registry.go's
// Channel-scope authorization without a real database.
type fakeRoleStore struct {
	mu    sync.Mutex
	roles map[roleKey][]store.Role
}

func newFakeRoleStore() *fakeRoleStore {
	return &fakeRoleStore{roles: map[roleKey][]store.Role{}}
}

func (f *fakeRoleStore) grant(channelID, personID uuid.UUID, roles ...store.Role) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.roles[roleKey{channelID, personID}] = roles
}

func (f *fakeRoleStore) RolesFor(_ context.Context, channelID, personID uuid.UUID) ([]store.Role, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.roles[roleKey{channelID, personID}], nil
}

func (f *fakeRoleStore) AddRole(context.Context, uuid.UUID, uuid.UUID, store.Role) error {
	return errors.New("fakeRoleStore.AddRole is not used by these tests")
}

func (f *fakeRoleStore) ChannelsForPerson(context.Context, uuid.UUID) ([]store.Channel, error) {
	return nil, errors.New("fakeRoleStore.ChannelsForPerson is not used by these tests")
}

var _ store.RoleStore = (*fakeRoleStore)(nil)

// ── fake Idempotency ─────────────────────────────────────────────────────────

type idemKey struct {
	tool, key string
	personID  uuid.UUID
}

type idemRecord struct {
	fingerprint string
	ref         uuid.UUID
}

// fakeIdempotency replicates store.idempotencyStore's Do() semantics
// (first call runs fn and records; same key+fingerprint replays; same
// key+different fingerprint conflicts) entirely in memory. This is
// sufficient to prove registry.go's wiring is correct (RegisterWrite
// calls Do at all, with the right arguments, and doesn't add its own
// idempotency logic on top) -- it deliberately does NOT prove Postgres's
// real-concurrency/row-contention guarantee, which server_integration_test.go
// covers separately against a real database.
type fakeIdempotency struct {
	mu      sync.Mutex
	records map[idemKey]idemRecord
}

func newFakeIdempotency() *fakeIdempotency {
	return &fakeIdempotency{records: map[idemKey]idemRecord{}}
}

func (f *fakeIdempotency) Do(ctx context.Context, tool string, personID uuid.UUID, key, fingerprint string, fn func(context.Context) (uuid.UUID, error)) (uuid.UUID, bool, error) {
	k := idemKey{tool: tool, key: key, personID: personID}

	f.mu.Lock()
	if rec, ok := f.records[k]; ok {
		f.mu.Unlock()
		if rec.fingerprint != fingerprint {
			return uuid.Nil, false, store.ErrIdempotencyConflict
		}
		return rec.ref, true, nil
	}
	f.mu.Unlock()

	ref, err := fn(ctx)
	if err != nil {
		return uuid.Nil, false, err
	}

	f.mu.Lock()
	f.records[k] = idemRecord{fingerprint: fingerprint, ref: ref}
	f.mu.Unlock()

	return ref, false, nil
}

var _ store.Idempotency = (*fakeIdempotency)(nil)

// ── fake PersonStore ─────────────────────────────────────────────────────────

// fakePersonStore implements store.PersonStore over a fixed in-memory set
// of Persons -- enough to drive auth.go's PersonMiddleware without a real
// database.
type fakePersonStore struct {
	byID map[uuid.UUID]store.Person
}

func (f fakePersonStore) UpsertByGoogleSubject(context.Context, string, string, string) (store.Person, bool, error) {
	return store.Person{}, false, errors.New("fakePersonStore.UpsertByGoogleSubject is not used by these tests")
}

func (f fakePersonStore) GetByID(_ context.Context, id uuid.UUID) (store.Person, error) {
	p, ok := f.byID[id]
	if !ok {
		return store.Person{}, errors.New("person not found")
	}
	return p, nil
}

var _ store.PersonStore = fakePersonStore{}

// ── test tool input/output types ─────────────────────────────────────────────

// scopedInput is a minimal ChannelScoped input, standing in for a real
// product read tool's argument struct. ChannelID is JSON-wire a string
// (parsed to uuid.UUID in ChannelScopeID), not a uuid.UUID field directly:
// jsonschema-go's reflection-based schema inference (google/jsonschema-go,
// which mcp.AddTool uses automatically when Tool.InputSchema is left nil)
// only special-cases a handful of stdlib json.Marshaler types (e.g.
// time.Time) as "string" -- an arbitrary [16]byte array type like
// uuid.UUID infers as schema type "array", which then rejects the string
// every real MCP client actually sends over the wire.
type scopedInput struct {
	ChannelID string `json:"channel_id" jsonschema:"channel to scope to, as a UUID string"`
}

func (i scopedInput) ChannelScopeID() uuid.UUID {
	id, _ := uuid.Parse(i.ChannelID)
	return id
}

// writeInput is a minimal ChannelScoped + IdempotencyKeyed input, standing
// in for a real product write tool's argument struct -- the fake
// write-tool handler this task's Implementation-phase note calls for,
// since whoami (a read tool) exercises neither ChannelScoped nor
// IdempotencyKeyed. See scopedInput's doc for why ChannelID is a string.
type writeInput struct {
	ChannelID string `json:"channel_id" jsonschema:"channel to scope to, as a UUID string"`
	Key       string `json:"idempotency_key,omitempty" jsonschema:"optional idempotency key"`
	Value     string `json:"value,omitempty" jsonschema:"arbitrary payload, varied across calls to change the request fingerprint"`
}

func (i writeInput) ChannelScopeID() uuid.UUID {
	id, _ := uuid.Parse(i.ChannelID)
	return id
}
func (i writeInput) IdempotencyKey() string { return i.Key }

// countOutput reports how many times the underlying fake handler actually
// ran -- the signal every test in this file reads to distinguish "handler
// invoked" from "handler replayed/skipped".
type countOutput struct {
	Calls int `json:"calls" jsonschema:"how many times the handler actually ran"`
}

// ── test server/registry/client plumbing ─────────────────────────────────────

// newTestRegistry builds a bare *mcp.Server plus a Registry constructed
// directly from fakes (bypassing NewRegistry, which only ever wraps a real
// *store.Store). If person is non-nil, every request is authenticated as
// that Person via a fixed receiving middleware standing in for
// PersonMiddleware (auth.go's own unit tests in auth_test.go cover
// PersonMiddleware itself); if nil, no middleware runs and
// PersonFromContext sees nothing, exactly like an unauthenticated caller.
func newTestRegistry(person *store.Person, roles store.RoleStore, idempotency store.Idempotency) (*mcp.Server, *Registry) {
	srv := mcp.NewServer(Implementation, nil)
	if person != nil {
		p := *person
		srv.AddReceivingMiddleware(func(next mcp.MethodHandler) mcp.MethodHandler {
			return func(ctx context.Context, method string, req mcp.Request) (mcp.Result, error) {
				return next(withPerson(ctx, p), method, req)
			}
		})
	}
	return srv, &Registry{server: srv, roles: roles, idempotency: idempotency}
}

// connectClient connects an in-memory client to srv (see mcp.NewInMemoryTransports)
// and returns the client session, ready to CallTool against whatever was
// registered on srv before this was called.
func connectClient(t *testing.T, srv *mcp.Server) *mcp.ClientSession {
	t.Helper()
	ctx := context.Background()

	serverTransport, clientTransport := mcp.NewInMemoryTransports()
	_, err := srv.Connect(ctx, serverTransport, nil)
	require.NoError(t, err)

	client := mcp.NewClient(&mcp.Implementation{Name: "test-client", Version: "0.0.1"}, nil)
	cs, err := client.Connect(ctx, clientTransport, nil)
	require.NoError(t, err)
	t.Cleanup(func() { _ = cs.Close() })

	return cs
}

// textOf concatenates every TextContent block in res.Content -- the error
// message a rejected call's Content carries (see ToolHandlerFor's doc:
// "an error result is ... packed into CallToolResult.Content, with
// IsError set").
func textOf(res *mcp.CallToolResult) string {
	var sb strings.Builder
	for _, c := range res.Content {
		if tc, ok := c.(*mcp.TextContent); ok {
			sb.WriteString(tc.Text)
		}
	}
	return sb.String()
}

// decodeCount decodes res.StructuredContent into countOutput -- the client
// side sees StructuredContent as an untyped any, so this round-trips it
// through JSON into the concrete type the test wants to assert on.
func decodeCount(t *testing.T, res *mcp.CallToolResult) countOutput {
	t.Helper()
	body, err := json.Marshal(res.StructuredContent)
	require.NoError(t, err)
	var out countOutput
	require.NoError(t, json.Unmarshal(body, &out))
	return out
}
