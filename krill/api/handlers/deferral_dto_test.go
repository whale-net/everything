// Response-shape tests: deferrals carry id and snake_case keys on both
// get_milestone (MilestoneResponse) and list_product_delivery
// (slice.MilestoneListingEntry), and the two shapes are identical.
package handlers_test

import (
	"encoding/json"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/whale-net/everything/krill/api/handlers"
	"github.com/whale-net/everything/krill/slice"
	"github.com/whale-net/everything/krill/store"
)

func deferralKeys(t *testing.T, v any) []map[string]any {
	t.Helper()
	raw, err := json.Marshal(v)
	require.NoError(t, err)
	var m map[string]any
	require.NoError(t, json.Unmarshal(raw, &m))
	arr, ok := m["deferrals"].([]any)
	require.True(t, ok, "deferrals must be a JSON array: %s", raw)
	out := make([]map[string]any, len(arr))
	for i, a := range arr {
		out[i] = a.(map[string]any)
	}
	return out
}

func TestDeferralDTO_GetMilestoneAndListingShapesMatch(t *testing.T) {
	id := uuid.New()
	rows := []store.MilestoneDeferral{{ID: id, Body: "later", Destination: "M13"}}

	resp := handlers.NewMilestoneResponse(store.MilestoneRef{ID: uuid.New()}, nil, nil, rows)
	entry := slice.MilestoneListingEntry{Deferrals: slice.NewDeferralDTOs(rows)}

	got := deferralKeys(t, resp)
	want := map[string]any{"id": id.String(), "body": "later", "destination": "M13"}
	require.Len(t, got, 1)
	assert.Equal(t, want, got[0])
	assert.Equal(t, got, deferralKeys(t, entry))
}

func TestDeferralDTO_EmptyIsArray(t *testing.T) {
	resp := handlers.NewMilestoneResponse(store.MilestoneRef{ID: uuid.New()}, nil, nil, nil)
	assert.Empty(t, deferralKeys(t, resp))
	assert.Empty(t, deferralKeys(t, slice.MilestoneListingEntry{Deferrals: slice.NewDeferralDTOs(nil)}))
}
