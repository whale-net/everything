// Pure unit tests for the placement guard AmendPlacementChange carries
// (amend.go, FR b62ed47a): the rule Refuse applies field by field against
// the entity's own current placement, and the Sent predicate that lets a
// body carrying no placement field skip the read altogether. No database
// needed -- CurrentPlacement's own read is covered against real Postgres in
// amend_integration_test.go, which is where the placement these cases
// compare against comes from.
package store_test

import (
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/whale-net/everything/krill/store"
)

func ptr(s string) *string { return &s }

// TestAmendPlacementChange_RefuseComparesValuesNotPresence is the whole
// narrowing in one table: for every one of the five fields, whether it was
// sent decides nothing on its own -- only whether what was sent differs
// from what the entity already has.
func TestAmendPlacementChange_RefuseComparesValuesNotPresence(t *testing.T) {
	held, other := ptr(uuid.NewString()), ptr(uuid.NewString())
	empty := ""

	// A Feature's current row: it hangs off a FeatureSet and has no kind
	// column of its own, so kind is absent.
	current := store.AmendPlacementChange{FeatureSetID: held}

	for _, tc := range []struct {
		name      string
		submitted store.AmendPlacementChange
		wantField string // "" means the amend must be accepted
	}{
		{"nothing sent", store.AmendPlacementChange{}, ""},
		{"echoed parent, every other field omitted", store.AmendPlacementChange{FeatureSetID: held}, ""},
		{
			"a different parent", store.AmendPlacementChange{FeatureSetID: other}, "feature_set_id",
		},
		{
			// A Product id IS a uuid, so an empty string can never be the
			// value already there; it is a submitted value and differs.
			"empty string against a non-empty parent", store.AmendPlacementChange{FeatureSetID: &empty}, "feature_set_id",
		},
		{
			// A Feature has no product_id column, so its current value is
			// absent and anything sent for it differs.
			"a field the kind has no column for", store.AmendPlacementChange{ProductID: other}, "product_id",
		},
		{
			"an empty string for a field with no column", store.AmendPlacementChange{Kind: &empty}, "kind",
		},
		{
			// product_id is compared before feature_set_id, so this names
			// the first offender and never the echoed one after it.
			"two changes name the first", store.AmendPlacementChange{ProductID: other, FeatureSetID: other}, "product_id",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.submitted.Refuse("feature", current)
			if tc.wantField == "" {
				assert.NoError(t, err)
				return
			}
			require.ErrorIs(t, err, store.ErrPlacementChange)
			assert.Contains(t, err.Error(), "feature cannot change "+tc.wantField+" on amend")
		})
	}
}

// TestAmendPlacementChange_RefuseAcceptsAMilestonesOwnKindAndParent proves
// the same rule for the kind that carries three placement fields at once:
// a milepebble echoing all of them back amends.
func TestAmendPlacementChange_RefuseAcceptsAMilestonesOwnKindAndParent(t *testing.T) {
	product, parent, kind := ptr(uuid.NewString()), ptr(uuid.NewString()), ptr("milepebble")
	current := store.AmendPlacementChange{ProductID: product, ParentMilestoneID: parent, Kind: kind}

	echoed := store.AmendPlacementChange{ProductID: product, ParentMilestoneID: parent, Kind: kind}
	assert.NoError(t, echoed.Refuse("milestone", current))

	rekinded := store.AmendPlacementChange{ProductID: product, ParentMilestoneID: parent, Kind: ptr("milestone")}
	assert.ErrorIs(t, rekinded.Refuse("milestone", current), store.ErrPlacementChange)

	// A top-level milestone's own parent_milestone_id is NULL, which reads
	// as an absent current value: an empty string sent for it differs, and
	// so does any other id -- the same narrowing a Requirement sees for the
	// parent_milestone_id it has no column for.
	topLevel := store.AmendPlacementChange{ProductID: product, Kind: ptr("milestone")}
	assert.NoError(t, store.AmendPlacementChange{ProductID: product, Kind: ptr("milestone")}.Refuse("milestone", topLevel))
	assert.ErrorIs(t, store.AmendPlacementChange{ParentMilestoneID: ptr("")}.Refuse("milestone", topLevel),
		store.ErrPlacementChange)
	assert.ErrorIs(t, store.AmendPlacementChange{ParentMilestoneID: parent}.Refuse("milestone", topLevel),
		store.ErrPlacementChange)
}

// TestAmendPlacementChange_Sent proves the predicate that decides whether a
// surface reads the current row at all: one field sent is enough, and none
// sent means there is nothing to compare.
func TestAmendPlacementChange_Sent(t *testing.T) {
	assert.False(t, store.AmendPlacementChange{}.Sent(), "a body with no placement field needs no read")
	for _, tc := range []store.AmendPlacementChange{
		{ProductID: ptr(uuid.NewString())},
		{FeatureSetID: ptr(uuid.NewString())},
		{FeatureID: ptr(uuid.NewString())},
		{ParentMilestoneID: ptr(uuid.NewString())},
		// An empty string is still a submitted value, so it is still Sent.
		{Kind: ptr("")},
	} {
		assert.True(t, tc.Sent())
	}
}
