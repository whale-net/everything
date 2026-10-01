// No-database tests for bounded list reads: list_entity_notes filters,
// list_product_delivery view=summary, and a size budget on the default
// responses against a live-krill-shaped fixture.
package tools_test

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"

	"github.com/google/uuid"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/whale-net/everything/krill/api/handlers"
	"github.com/whale-net/everything/krill/mcp/server"
	"github.com/whale-net/everything/krill/mcp/tools"
	"github.com/whale-net/everything/krill/slice"
	"github.com/whale-net/everything/krill/store"
)

// Budgets in bytes for a default (unfiltered) list response over the
// fixtures below; raise only deliberately.
const (
	deliverySummaryBudget = 5 * 1024
	entityNotesBudget     = 40 * 1024
)

type fakeDeliveryQuerier struct{ listing slice.DeliveryListing }

func (f fakeDeliveryQuerier) ListProductDelivery(context.Context, uuid.UUID, uuid.UUID, []store.MilestoneStatus) (slice.DeliveryListing, error) {
	return f.listing, nil
}

type fakeProducts struct {
	store.ProductStore
	id uuid.UUID
}

func (f fakeProducts) GetCurrentByID(_ context.Context, id uuid.UUID) (store.Product, error) {
	if id != f.id {
		return store.Product{}, store.ErrNotFound
	}
	return store.Product{ID: id, ScopeID: uuid.New()}, nil
}

// krillShapedListing mimics the live krill product: 14 milestones, each
// with long outcomes, many Delivers entities and 3 milepebbles.
func krillShapedListing() slice.DeliveryListing {
	var l slice.DeliveryListing
	for i := 0; i < 14; i++ {
		outcome := fmt.Sprintf("%0400d", i)
		m := slice.MilestoneListingEntry{
			ID: uuid.New(), Name: fmt.Sprintf("M%d: a milestone name of typical length", i),
			Outcome: &outcome, Status: store.MilestoneStatusShipped,
			Deferrals: slice.NewDeferralDTOs([]store.MilestoneDeferral{{ID: uuid.New(), Body: outcome, Destination: "later"}}),
		}
		for j := 0; j < 3; j++ {
			m.Milepebbles = append(m.Milepebbles, slice.MilepebbleListingEntry{ID: uuid.New(), Name: outcome[:100], Outcome: &outcome, Status: store.MilestoneStatusPlanned})
		}
		l.Milestones = append(l.Milestones, m)
	}
	return l
}

func connectDelivery(t *testing.T, pid uuid.UUID, l slice.DeliveryListing) *mcp.ClientSession {
	t.Helper()
	ctx := context.Background()
	srv := mcp.NewServer(server.Implementation, nil)
	srv.AddReceivingMiddleware(operatorPersona)
	tools.RegisterListProductDelivery(server.NewRegistry(srv), fakeProducts{id: pid}, fakeDeliveryQuerier{l})
	st, ct := mcp.NewInMemoryTransports()
	_, err := srv.Connect(ctx, st, nil)
	require.NoError(t, err)
	cs, err := mcp.NewClient(&mcp.Implementation{Name: "c", Version: "0"}, nil).Connect(ctx, ct, nil)
	require.NoError(t, err)
	t.Cleanup(func() { _ = cs.Close() })
	return cs
}

func callDelivery(t *testing.T, cs *mcp.ClientSession, args map[string]any) *mcp.CallToolResult {
	t.Helper()
	res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: "list_product_delivery", Arguments: args})
	require.NoError(t, err)
	return res
}

func TestListProductDelivery_SummaryView(t *testing.T) {
	pid := uuid.New()
	l := krillShapedListing()
	cs := connectDelivery(t, pid, l)

	res := callDelivery(t, cs, map[string]any{"product_id": pid.String(), "view": "summary"})
	require.False(t, res.IsError, discoveryTextOf(res))
	raw, err := json.Marshal(res.StructuredContent)
	require.NoError(t, err)

	var got slice.DeliverySummary
	require.NoError(t, json.Unmarshal(raw, &got))
	require.Len(t, got.Milestones, 14)
	assert.Equal(t, l.Milestones[0].ID, got.Milestones[0].ID)
	assert.Equal(t, l.Milestones[0].Name, got.Milestones[0].Name)
	require.Len(t, got.Milestones[0].Milepebbles, 3)
	assert.Equal(t, l.Milestones[0].Milepebbles[1].ID, got.Milestones[0].Milepebbles[1].ID)
	assert.NotContains(t, string(raw), "outcome")
	assert.NotContains(t, string(raw), "deferrals")
	assert.Less(t, len(raw), deliverySummaryBudget)
}

func TestListProductDelivery_FullViewIsDefaultAndBadViewRejected(t *testing.T) {
	pid := uuid.New()
	cs := connectDelivery(t, pid, krillShapedListing())

	res := callDelivery(t, cs, map[string]any{"product_id": pid.String()})
	require.False(t, res.IsError, discoveryTextOf(res))
	raw, _ := json.Marshal(res.StructuredContent)
	assert.Contains(t, string(raw), `"outcome"`)

	res = callDelivery(t, cs, map[string]any{"product_id": pid.String(), "view": "bogus"})
	assert.True(t, res.IsError)
}

func entityNotesFixture(n int) (handlers.NoteEntityScopes, entityNoteTaskStore, uuid.UUID) {
	scopeID, fsID := uuid.New(), uuid.New()
	kind := store.NoteEntityKindFeatureSet
	var notes []store.Note
	for i := 0; i < n; i++ {
		k, st := store.NoteKindComment, store.NoteLifecycleStatusClosed
		if i%2 == 0 {
			k, st = store.NoteKindScopeNote, store.NoteLifecycleStatusNoted
		}
		prefix := "round"
		if i%3 == 0 {
			prefix = "retro"
		}
		notes = append(notes, store.Note{ID: uuid.New(), ScopeID: scopeID, EntityKind: &kind, EntityID: &fsID,
			Kind: k, CurrentStatus: st, Body: fmt.Sprintf("%s %d %0300d", prefix, i, i)})
	}
	return handlers.NoteEntityScopes{FeatureSets: entityNoteFeatureSets{scopes: map[uuid.UUID]uuid.UUID{fsID: scopeID}}},
		entityNoteTaskStore{scopeID: scopeID, kind: kind, entityID: fsID, notes: notes}, fsID
}

func listNotes(t *testing.T, cs *mcp.ClientSession, fsID uuid.UUID, extra map[string]any) (handlers.ListEntityNotesResponse, int) {
	t.Helper()
	args := map[string]any{"entity_kind": "feature_set", "entity_id": fsID.String()}
	for k, v := range extra {
		args[k] = v
	}
	res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: "list_entity_notes", Arguments: args})
	require.NoError(t, err)
	require.False(t, res.IsError, discoveryTextOf(res))
	raw, err := json.Marshal(res.StructuredContent)
	require.NoError(t, err)
	var got handlers.ListEntityNotesResponse
	require.NoError(t, json.Unmarshal(raw, &got))
	return got, len(raw)
}

func TestListEntityNotes_Filters(t *testing.T) {
	scopes, tasks, fsID := entityNotesFixture(12)
	cs := connectEntityNoteTool(t, scopes, tasks)

	all, _ := listNotes(t, cs, fsID, nil)
	require.Len(t, all.Notes, 12)
	assert.Equal(t, tasks.notes[0].ID.String(), all.Notes[0].ID, "unfiltered stays oldest first")

	lim, _ := listNotes(t, cs, fsID, map[string]any{"limit": 3})
	require.Len(t, lim.Notes, 3)
	assert.Equal(t, tasks.notes[11].ID.String(), lim.Notes[0].ID, "limit returns newest first")
	assert.Equal(t, tasks.notes[9].ID.String(), lim.Notes[2].ID)

	kind, _ := listNotes(t, cs, fsID, map[string]any{"kind": "comment"})
	require.Len(t, kind.Notes, 6)
	for _, n := range kind.Notes {
		assert.Equal(t, "comment", n.Kind)
	}

	st, _ := listNotes(t, cs, fsID, map[string]any{"status": "noted"})
	require.Len(t, st.Notes, 6)
	for _, n := range st.Notes {
		assert.Equal(t, "noted", n.Status)
	}

	pre, _ := listNotes(t, cs, fsID, map[string]any{"body_prefix": "retro"})
	require.Len(t, pre.Notes, 4)

	combo, _ := listNotes(t, cs, fsID, map[string]any{"body_prefix": "retro", "status": "noted", "limit": 1})
	require.Len(t, combo.Notes, 1)
	assert.Equal(t, tasks.notes[6].ID.String(), combo.Notes[0].ID)
}

func TestListEntityNotes_DefaultResponseWithinBudget(t *testing.T) {
	scopes, tasks, fsID := entityNotesFixture(60)
	cs := connectEntityNoteTool(t, scopes, tasks)
	_, size := listNotes(t, cs, fsID, nil)
	assert.Less(t, size, entityNotesBudget)
}

func TestListProductDelivery_DefaultResponseWithinBudget(t *testing.T) {
	pid := uuid.New()
	cs := connectDelivery(t, pid, krillShapedListing())
	res := callDelivery(t, cs, map[string]any{"product_id": pid.String(), "view": "summary"})
	raw, _ := json.Marshal(res.StructuredContent)
	assert.Less(t, len(raw), deliverySummaryBudget)
}
