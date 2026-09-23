// No-database unit test for list_entity_notes (entity_note.go): it
// registers as exactly {list_entity_notes}, resolves scope from the entity,
// and returns handlers.ListEntityNotesResponse. In-memory fakes over a real
// in-memory MCP client/server connection, mirroring
// discovery_registration_test.go.
package tools_test

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/google/uuid"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/whale-net/everything/krill/api/handlers"
	"github.com/whale-net/everything/krill/mcp/server"
	"github.com/whale-net/everything/krill/mcp/tools"
	"github.com/whale-net/everything/krill/store"
)

// entityNoteFeatureSets overrides only GetCurrentByID.
type entityNoteFeatureSets struct {
	store.FeatureSetStore
	scopes map[uuid.UUID]uuid.UUID
}

func (f entityNoteFeatureSets) GetCurrentByID(_ context.Context, id uuid.UUID) (store.FeatureSet, error) {
	scope, ok := f.scopes[id]
	if !ok {
		return store.FeatureSet{}, store.ErrNotFound
	}
	return store.FeatureSet{ID: id, ScopeID: scope}, nil
}

// entityNoteTaskStore overrides only ListNotesForEntity, keyed by
// (scope, kind, entity) so a wrong scope or kind yields no notes.
type entityNoteTaskStore struct {
	store.TaskStore
	scopeID  uuid.UUID
	kind     store.NoteEntityKind
	entityID uuid.UUID
	notes    []store.Note
}

func (f entityNoteTaskStore) ListNotesForEntity(_ context.Context, scopeID uuid.UUID, kind store.NoteEntityKind, entityID uuid.UUID) ([]store.Note, error) {
	if scopeID != f.scopeID || kind != f.kind || entityID != f.entityID {
		return nil, nil
	}
	return f.notes, nil
}

func connectEntityNoteTool(t *testing.T, scopes handlers.NoteEntityScopes, tasks store.TaskStore) *mcp.ClientSession {
	t.Helper()
	ctx := context.Background()

	srv := mcp.NewServer(server.Implementation, nil)
	srv.AddReceivingMiddleware(operatorPersona)
	reg := server.NewRegistry(srv)
	tools.RegisterListEntityNotes(reg, scopes, tasks)

	serverTransport, clientTransport := mcp.NewInMemoryTransports()
	_, err := srv.Connect(ctx, serverTransport, nil)
	require.NoError(t, err)

	client := mcp.NewClient(&mcp.Implementation{Name: "test-client", Version: "0.0.1"}, nil)
	cs, err := client.Connect(ctx, clientTransport, nil)
	require.NoError(t, err)
	t.Cleanup(func() { _ = cs.Close() })
	return cs
}

func callListEntityNotes(t *testing.T, cs *mcp.ClientSession, kind, id string) *mcp.CallToolResult {
	t.Helper()
	res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{
		Name:      "list_entity_notes",
		Arguments: map[string]any{"entity_kind": kind, "entity_id": id},
	})
	require.NoError(t, err)
	return res
}

func TestRegisterListEntityNotes_RegistersExactlyOneTool(t *testing.T) {
	cs := connectEntityNoteTool(t, handlers.NoteEntityScopes{}, entityNoteTaskStore{})

	registered := map[string]bool{}
	for tool, err := range cs.Tools(context.Background(), nil) {
		require.NoError(t, err)
		registered[tool.Name] = true
	}
	assert.Equal(t, map[string]bool{"list_entity_notes": true}, registered)
}

func TestListEntityNotes_ReturnsEveryNoteOnEntity(t *testing.T) {
	scopeID, fsID, noteID := uuid.New(), uuid.New(), uuid.New()
	kind := store.NoteEntityKindFeatureSet
	cs := connectEntityNoteTool(t,
		handlers.NoteEntityScopes{FeatureSets: entityNoteFeatureSets{scopes: map[uuid.UUID]uuid.UUID{fsID: scopeID}}},
		entityNoteTaskStore{scopeID: scopeID, kind: kind, entityID: fsID, notes: []store.Note{
			{ID: noteID, ScopeID: scopeID, EntityKind: &kind, EntityID: &fsID, Kind: store.NoteKindComment, Body: "round 1 minutes", CurrentStatus: store.NoteLifecycleStatusClosed},
		}})

	res := callListEntityNotes(t, cs, "feature_set", fsID.String())
	require.False(t, res.IsError, discoveryTextOf(res))

	raw, err := json.Marshal(res.StructuredContent)
	require.NoError(t, err)
	var got handlers.ListEntityNotesResponse
	require.NoError(t, json.Unmarshal(raw, &got))

	assert.Equal(t, "feature_set", got.EntityKind)
	assert.Equal(t, fsID.String(), got.EntityID)
	require.Len(t, got.Notes, 1)
	assert.Equal(t, noteID.String(), got.Notes[0].ID)
	assert.Equal(t, "round 1 minutes", got.Notes[0].Body)
	assert.Equal(t, "closed", got.Notes[0].Status, "non-noted statuses must still be returned")
}

func TestListEntityNotes_NoNotes_ReturnsEmptyList(t *testing.T) {
	scopeID, fsID := uuid.New(), uuid.New()
	cs := connectEntityNoteTool(t,
		handlers.NoteEntityScopes{FeatureSets: entityNoteFeatureSets{scopes: map[uuid.UUID]uuid.UUID{fsID: scopeID}}},
		entityNoteTaskStore{})

	res := callListEntityNotes(t, cs, "feature_set", fsID.String())
	require.False(t, res.IsError, discoveryTextOf(res))

	raw, err := json.Marshal(res.StructuredContent)
	require.NoError(t, err)
	assert.JSONEq(t, `{"entity_kind": "feature_set", "entity_id": "`+fsID.String()+`", "notes": []}`, string(raw))
}

func TestListEntityNotes_Rejections(t *testing.T) {
	fsID := uuid.New()
	cs := connectEntityNoteTool(t,
		handlers.NoteEntityScopes{FeatureSets: entityNoteFeatureSets{scopes: map[uuid.UUID]uuid.UUID{fsID: uuid.New()}}},
		entityNoteTaskStore{})

	for name, args := range map[string][2]string{
		"malformed id":   {"feature_set", "not-a-uuid"},
		"unknown entity": {"feature_set", uuid.NewString()},
		"milestone kind": {"milestone", fsID.String()},
		"empty kind":     {"", fsID.String()},
	} {
		t.Run(name, func(t *testing.T) {
			res := callListEntityNotes(t, cs, args[0], args[1])
			assert.True(t, res.IsError, "expected a tool error, got %s", discoveryTextOf(res))
		})
	}
}
