package mcpscope

// Pure unit tests against a hand-rolled session.AgentDefinitionStore fake
// (no Postgres, runs as part of `bazel test //...`): proves Resolver's own
// logic -- which store method it calls for which input, how it maps a
// "no rows" nil into ErrNotFound, and that a transport/database error is
// never mistaken for ErrNotFound -- independently of whether the real
// Postgres-backed store actually behaves that way (that's
// mcpscope_integration_test.go's job). Each fake method panics if called
// unset, mirroring whagent_net/mcp/tools/fake_client_test.go's
// fakeSessionServiceClient convention, so a wrongly-invoked store method
// fails the test loudly instead of returning a misleading zero value.

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/whale-net/everything/whagent_net/session"
)

// fakeAgentDefinitionStore is a session.AgentDefinitionStore double: every
// method's behavior is supplied by a caller-set func field, nil meaning
// "must not be called".
type fakeAgentDefinitionStore struct {
	getLatestFunc         func(ctx context.Context, agentID string) (*session.AgentDefinition, error)
	getVersionFunc        func(ctx context.Context, agentID string, version int) (*session.AgentDefinition, error)
	upsertFunc            func(ctx context.Context, def *session.AgentDefinition) error
	assignToSessionFunc   func(ctx context.Context, sessionID uuid.UUID, agentID string, version int) error
	currentAssignmentFunc func(ctx context.Context, sessionID uuid.UUID) (*session.SessionAgent, error)
}

var _ session.AgentDefinitionStore = (*fakeAgentDefinitionStore)(nil)

func (f *fakeAgentDefinitionStore) GetLatest(ctx context.Context, agentID string) (*session.AgentDefinition, error) {
	if f.getLatestFunc == nil {
		panic("fakeAgentDefinitionStore: GetLatest called but no getLatestFunc set")
	}
	return f.getLatestFunc(ctx, agentID)
}

func (f *fakeAgentDefinitionStore) GetVersion(ctx context.Context, agentID string, version int) (*session.AgentDefinition, error) {
	if f.getVersionFunc == nil {
		panic("fakeAgentDefinitionStore: GetVersion called but no getVersionFunc set")
	}
	return f.getVersionFunc(ctx, agentID, version)
}

func (f *fakeAgentDefinitionStore) Upsert(ctx context.Context, def *session.AgentDefinition) error {
	if f.upsertFunc == nil {
		panic("fakeAgentDefinitionStore: Upsert called but no upsertFunc set")
	}
	return f.upsertFunc(ctx, def)
}

func (f *fakeAgentDefinitionStore) AssignToSession(ctx context.Context, sessionID uuid.UUID, agentID string, version int) error {
	if f.assignToSessionFunc == nil {
		panic("fakeAgentDefinitionStore: AssignToSession called but no assignToSessionFunc set")
	}
	return f.assignToSessionFunc(ctx, sessionID, agentID, version)
}

func (f *fakeAgentDefinitionStore) CurrentAssignment(ctx context.Context, sessionID uuid.UUID) (*session.SessionAgent, error) {
	if f.currentAssignmentFunc == nil {
		panic("fakeAgentDefinitionStore: CurrentAssignment called but no currentAssignmentFunc set")
	}
	return f.currentAssignmentFunc(ctx, sessionID)
}

var errTransport = errors.New("boom: connection reset")

func strPtr(s string) *string { return &s }

// TestScopeForAgent_ReturnsScopeFromLatestDefinition proves the happy
// path: ScopeForAgent forwards to GetLatest and returns its Scope
// unchanged.
func TestScopeForAgent_ReturnsScopeFromLatestDefinition(t *testing.T) {
	store := &fakeAgentDefinitionStore{
		getLatestFunc: func(_ context.Context, agentID string) (*session.AgentDefinition, error) {
			assert.Equal(t, "research-agent", agentID)
			return &session.AgentDefinition{AgentID: agentID, Scope: strPtr("audience_score_system"), Version: 3}, nil
		},
	}
	r := New(store)

	scope, err := r.ScopeForAgent(context.Background(), "research-agent")
	require.NoError(t, err)
	require.NotNil(t, scope)
	assert.Equal(t, "audience_score_system", *scope)
}

// TestScopeForAgent_NilScope_ReturnsNilWithoutError proves an agent
// definition with no scope resolves successfully to a nil *string, not an
// error -- a nil scope means "no delegated-grant scoping", not "not
// found".
func TestScopeForAgent_NilScope_ReturnsNilWithoutError(t *testing.T) {
	store := &fakeAgentDefinitionStore{
		getLatestFunc: func(_ context.Context, agentID string) (*session.AgentDefinition, error) {
			return &session.AgentDefinition{AgentID: agentID, Scope: nil, Version: 1}, nil
		},
	}
	r := New(store)

	scope, err := r.ScopeForAgent(context.Background(), "research-agent")
	require.NoError(t, err)
	assert.Nil(t, scope)
}

// TestScopeForAgent_UnknownAgent_ReturnsErrNotFound proves GetLatest's
// documented "no rows -> nil, nil" contract maps to a distinguishable
// ErrNotFound, not a nil scope with a nil error.
func TestScopeForAgent_UnknownAgent_ReturnsErrNotFound(t *testing.T) {
	store := &fakeAgentDefinitionStore{
		getLatestFunc: func(context.Context, string) (*session.AgentDefinition, error) { return nil, nil },
	}
	r := New(store)

	scope, err := r.ScopeForAgent(context.Background(), "does-not-exist")
	assert.Nil(t, scope)
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrNotFound), "unknown agent id must be a distinguishable ErrNotFound, got: %v", err)
}

// TestScopeForAgent_TransportError_NotMistakenForErrNotFound proves a
// genuine store/transport failure surfaces as an error but is never
// errors.Is(err, ErrNotFound) -- a caller must be able to tell "doesn't
// exist" (4xx-shaped) apart from "couldn't ask" (5xx-shaped).
func TestScopeForAgent_TransportError_NotMistakenForErrNotFound(t *testing.T) {
	store := &fakeAgentDefinitionStore{
		getLatestFunc: func(context.Context, string) (*session.AgentDefinition, error) { return nil, errTransport },
	}
	r := New(store)

	scope, err := r.ScopeForAgent(context.Background(), "research-agent")
	assert.Nil(t, scope)
	require.Error(t, err)
	assert.False(t, errors.Is(err, ErrNotFound), "a transport error must not be mistaken for ErrNotFound")
	assert.True(t, errors.Is(err, errTransport), "the underlying transport error must still be unwrappable")
}

// TestScopeForSession_MalformedSessionID_ReturnsErrNotFoundWithoutCallingStore
// proves an invalid uuid short-circuits to ErrNotFound before ever
// reaching the store -- the fake's currentAssignmentFunc is left nil, so
// a call would panic and fail this test.
func TestScopeForSession_MalformedSessionID_ReturnsErrNotFoundWithoutCallingStore(t *testing.T) {
	store := &fakeAgentDefinitionStore{}
	r := New(store)

	scope, err := r.ScopeForSession(context.Background(), "not-a-uuid")
	assert.Nil(t, scope)
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrNotFound))
}

// TestScopeForSession_UnknownSession_ReturnsErrNotFound proves
// CurrentAssignment's "no rows -> nil, nil" contract maps to ErrNotFound.
func TestScopeForSession_UnknownSession_ReturnsErrNotFound(t *testing.T) {
	store := &fakeAgentDefinitionStore{
		currentAssignmentFunc: func(context.Context, uuid.UUID) (*session.SessionAgent, error) { return nil, nil },
	}
	r := New(store)

	scope, err := r.ScopeForSession(context.Background(), uuid.NewString())
	assert.Nil(t, scope)
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrNotFound))
}

// TestScopeForSession_CurrentAssignmentTransportError_NotMistakenForErrNotFound
// mirrors the ScopeForAgent transport-error case for the
// CurrentAssignment call.
func TestScopeForSession_CurrentAssignmentTransportError_NotMistakenForErrNotFound(t *testing.T) {
	store := &fakeAgentDefinitionStore{
		currentAssignmentFunc: func(context.Context, uuid.UUID) (*session.SessionAgent, error) { return nil, errTransport },
	}
	r := New(store)

	scope, err := r.ScopeForSession(context.Background(), uuid.NewString())
	assert.Nil(t, scope)
	require.Error(t, err)
	assert.False(t, errors.Is(err, ErrNotFound))
}

// TestScopeForSession_AssignedVersionMissing_ReturnsErrNotFound proves
// the "assigned to an agent_definition row that no longer exists" edge
// case the doc comment calls out: CurrentAssignment succeeds but the
// exact (AgentID, Version) GetVersion is then asked for comes back nil.
func TestScopeForSession_AssignedVersionMissing_ReturnsErrNotFound(t *testing.T) {
	sid := uuid.New()
	store := &fakeAgentDefinitionStore{
		currentAssignmentFunc: func(_ context.Context, gotSID uuid.UUID) (*session.SessionAgent, error) {
			assert.Equal(t, sid, gotSID)
			return &session.SessionAgent{SessionID: sid, AgentID: "research-agent", AgentVersion: 1}, nil
		},
		getVersionFunc: func(_ context.Context, agentID string, version int) (*session.AgentDefinition, error) {
			assert.Equal(t, "research-agent", agentID)
			assert.Equal(t, 1, version)
			return nil, nil
		},
	}
	r := New(store)

	scope, err := r.ScopeForSession(context.Background(), sid.String())
	assert.Nil(t, scope)
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrNotFound))
}

// TestScopeForSession_GetVersionTransportError_NotMistakenForErrNotFound
// mirrors the transport-error case for the GetVersion call.
func TestScopeForSession_GetVersionTransportError_NotMistakenForErrNotFound(t *testing.T) {
	sid := uuid.New()
	store := &fakeAgentDefinitionStore{
		currentAssignmentFunc: func(context.Context, uuid.UUID) (*session.SessionAgent, error) {
			return &session.SessionAgent{SessionID: sid, AgentID: "research-agent", AgentVersion: 1}, nil
		},
		getVersionFunc: func(context.Context, string, int) (*session.AgentDefinition, error) { return nil, errTransport },
	}
	r := New(store)

	scope, err := r.ScopeForSession(context.Background(), sid.String())
	assert.Nil(t, scope)
	require.Error(t, err)
	assert.False(t, errors.Is(err, ErrNotFound))
}

// TestScopeForSession_UsesAssignedVersionNotLatest proves the doc
// comment's core promise -- "never the current/latest version" -- at the
// unit level: GetVersion is called with the exact version
// CurrentAssignment returned, and its Scope (not some other version's)
// is what comes back. GetLatest is left unset entirely, so any call to it
// would panic and fail the test -- the strongest possible proof this path
// never falls back to "current".
func TestScopeForSession_UsesAssignedVersionNotLatest(t *testing.T) {
	sid := uuid.New()
	store := &fakeAgentDefinitionStore{
		currentAssignmentFunc: func(context.Context, uuid.UUID) (*session.SessionAgent, error) {
			return &session.SessionAgent{SessionID: sid, AgentID: "research-agent", AgentVersion: 1}, nil
		},
		getVersionFunc: func(_ context.Context, agentID string, version int) (*session.AgentDefinition, error) {
			require.Equal(t, "research-agent", agentID)
			require.Equal(t, 1, version)
			return &session.AgentDefinition{AgentID: agentID, Version: version, Scope: strPtr("old-scope")}, nil
		},
	}
	r := New(store)

	scope, err := r.ScopeForSession(context.Background(), sid.String())
	require.NoError(t, err)
	require.NotNil(t, scope)
	assert.Equal(t, "old-scope", *scope)
}
