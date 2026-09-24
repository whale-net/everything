//go:build integration

// Real Postgres coverage for SliceStore (slice.go): the cross-table reads
// behind krill/slice's product, feature-set, and entity-set slices. Shares
// newStore/newScope with store_integration_test.go.
//
// Run it explicitly (requires a working Docker daemon):
//
//	bazel test //krill/store:slice_integration_test --test_output=all
package store_test

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/whale-net/everything/krill/store"
	"github.com/whale-net/everything/libs/go/dbtest"
)

// sliceFixture is a two-FeatureSet product tree plus an unrelated product
// whose rows must never leak into the first product's slices.
type sliceFixture struct {
	product, otherProduct   store.Product
	fsA, fsB, otherFS       store.FeatureSet
	featA1, featA2, featB1  store.Feature
	otherFeat               store.Feature
	frA1, nfrA1, frA2, frB1 store.Requirement
	otherReq                store.Requirement
	decA, decB, otherDec    store.LoadBearingDecision
}

func newSliceFixture(t *testing.T, ctx context.Context, s *store.Store, scopeID uuid.UUID) sliceFixture {
	t.Helper()
	var f sliceFixture
	var err error

	f.product, err = s.Products().Create(ctx, scopeID, "Krill", "")
	require.NoError(t, err)
	f.fsA, err = s.FeatureSets().Create(ctx, scopeID, f.product.ID, "Alpha", nil)
	require.NoError(t, err)
	f.fsB, err = s.FeatureSets().Create(ctx, scopeID, f.product.ID, "Beta", nil)
	require.NoError(t, err)

	f.featA1, err = s.Features().Create(ctx, scopeID, f.fsA.ID, "A1", nil)
	require.NoError(t, err)
	f.featA2, err = s.Features().Create(ctx, scopeID, f.fsA.ID, "A2", nil)
	require.NoError(t, err)
	f.featB1, err = s.Features().Create(ctx, scopeID, f.fsB.ID, "B1", nil)
	require.NoError(t, err)

	// NFR is created before FR under A1 so the kind ordering is proven
	// independent of insertion order.
	f.nfrA1, err = s.Requirements().Create(ctx, scopeID, f.featA1.ID, store.RequirementKindNFR, "A1 nfr", nil)
	require.NoError(t, err)
	f.frA1, err = s.Requirements().Create(ctx, scopeID, f.featA1.ID, store.RequirementKindFR, "A1 fr", nil)
	require.NoError(t, err)
	f.frA2, err = s.Requirements().Create(ctx, scopeID, f.featA2.ID, store.RequirementKindFR, "A2 fr", nil)
	require.NoError(t, err)
	f.frB1, err = s.Requirements().Create(ctx, scopeID, f.featB1.ID, store.RequirementKindFR, "B1 fr", nil)
	require.NoError(t, err)

	f.decA, err = s.Decisions().Create(ctx, scopeID, f.fsA.ID, "A decision", nil)
	require.NoError(t, err)
	f.decB, err = s.Decisions().Create(ctx, scopeID, f.fsB.ID, "B decision", nil)
	require.NoError(t, err)

	f.otherProduct, err = s.Products().Create(ctx, scopeID, "Other", "")
	require.NoError(t, err)
	f.otherFS, err = s.FeatureSets().Create(ctx, scopeID, f.otherProduct.ID, "Other FS", nil)
	require.NoError(t, err)
	f.otherFeat, err = s.Features().Create(ctx, scopeID, f.otherFS.ID, "Other feature", nil)
	require.NoError(t, err)
	f.otherReq, err = s.Requirements().Create(ctx, scopeID, f.otherFeat.ID, store.RequirementKindFR, "Other fr", nil)
	require.NoError(t, err)
	f.otherDec, err = s.Decisions().Create(ctx, scopeID, f.otherFS.ID, "Other decision", nil)
	require.NoError(t, err)

	return f
}

// closeCurrentRow marks an entity's current row superseded without
// opening a successor, the state a retired Feature/FeatureSet is left in.
func closeCurrentRow(t *testing.T, ctx context.Context, db *dbtest.Postgres, table string, id uuid.UUID) {
	t.Helper()
	tag, err := db.Pool.Exec(ctx, `UPDATE `+table+` SET valid_to = NOW() WHERE id = $1 AND valid_to IS NULL`, id)
	require.NoError(t, err)
	require.EqualValues(t, 1, tag.RowsAffected(), "expected exactly one current %s row for %s", table, id)
}

func setPosition(t *testing.T, ctx context.Context, db *dbtest.Postgres, table string, id uuid.UUID, position int) {
	t.Helper()
	_, err := db.Pool.Exec(ctx, `UPDATE `+table+` SET position = $2 WHERE id = $1 AND valid_to IS NULL`, id, position)
	require.NoError(t, err)
}

func requirementIDs(rs []store.Requirement) []uuid.UUID {
	ids := make([]uuid.UUID, len(rs))
	for i, r := range rs {
		ids[i] = r.ID
	}
	return ids
}

func featureIDs(fs []store.Feature) []uuid.UUID {
	ids := make([]uuid.UUID, len(fs))
	for i, f := range fs {
		ids[i] = f.ID
	}
	return ids
}

func featureSetIDs(fss []store.FeatureSet) []uuid.UUID {
	ids := make([]uuid.UUID, len(fss))
	for i, fs := range fss {
		ids[i] = fs.ID
	}
	return ids
}

func decisionIDs(ds []store.LoadBearingDecision) []uuid.UUID {
	ids := make([]uuid.UUID, len(ds))
	for i, d := range ds {
		ids[i] = d.ID
	}
	return ids
}

func TestSliceStore_ListRequirementsByFeatureSet(t *testing.T) {
	ctx := context.Background()
	s, db := newStore(t)
	f := newSliceFixture(t, ctx, s, newScope(t, ctx, db))
	slices := s.Slices()

	t.Run("orders by feature then kind", func(t *testing.T) {
		got, err := slices.ListRequirementsByFeatureSet(ctx, f.fsA.ID)
		require.NoError(t, err)
		assert.Equal(t, []uuid.UUID{f.frA1.ID, f.nfrA1.ID, f.frA2.ID}, requirementIDs(got))
		assert.Equal(t, store.RequirementKindFR, got[0].Kind)
		assert.Equal(t, f.featA1.ID, got[0].FeatureID)
	})

	t.Run("feature position overrides creation order", func(t *testing.T) {
		setPosition(t, ctx, db, "feature", f.featA1.ID, 10)
		t.Cleanup(func() { setPosition(t, ctx, db, "feature", f.featA1.ID, f.featA1.Position) })

		got, err := slices.ListRequirementsByFeatureSet(ctx, f.fsA.ID)
		require.NoError(t, err)
		assert.Equal(t, []uuid.UUID{f.frA2.ID, f.frA1.ID, f.nfrA1.ID}, requirementIDs(got))
	})

	t.Run("unknown feature set is empty", func(t *testing.T) {
		got, err := slices.ListRequirementsByFeatureSet(ctx, uuid.New())
		require.NoError(t, err)
		assert.Empty(t, got)
	})

	t.Run("amended requirement returns only its current revision", func(t *testing.T) {
		amended, err := s.Amend().AmendRequirement(ctx, f.frB1.ID, "B1 fr v2", nil)
		require.NoError(t, err)

		got, err := slices.ListRequirementsByFeatureSet(ctx, f.fsB.ID)
		require.NoError(t, err)
		require.Len(t, got, 1)
		assert.Equal(t, f.frB1.ID, got[0].ID)
		assert.Equal(t, amended.RevisionID, got[0].RevisionID)
		assert.Equal(t, "B1 fr v2", got[0].Name)
	})

	t.Run("superseded feature drops its requirements", func(t *testing.T) {
		closeCurrentRow(t, ctx, db, "feature", f.featA2.ID)

		got, err := slices.ListRequirementsByFeatureSet(ctx, f.fsA.ID)
		require.NoError(t, err)
		assert.Equal(t, []uuid.UUID{f.frA1.ID, f.nfrA1.ID}, requirementIDs(got))
	})
}

func TestSliceStore_ListFeaturesByProduct(t *testing.T) {
	ctx := context.Background()
	s, db := newStore(t)
	f := newSliceFixture(t, ctx, s, newScope(t, ctx, db))
	slices := s.Slices()

	got, err := slices.ListFeaturesByProduct(ctx, f.product.ID)
	require.NoError(t, err)
	assert.Equal(t, []uuid.UUID{f.featA1.ID, f.featA2.ID, f.featB1.ID}, featureIDs(got))

	// Feature-set position dominates feature position.
	setPosition(t, ctx, db, "feature_set", f.fsA.ID, 10)
	got, err = slices.ListFeaturesByProduct(ctx, f.product.ID)
	require.NoError(t, err)
	assert.Equal(t, []uuid.UUID{f.featB1.ID, f.featA1.ID, f.featA2.ID}, featureIDs(got))

	closeCurrentRow(t, ctx, db, "feature_set", f.fsB.ID)
	got, err = slices.ListFeaturesByProduct(ctx, f.product.ID)
	require.NoError(t, err)
	assert.Equal(t, []uuid.UUID{f.featA1.ID, f.featA2.ID}, featureIDs(got),
		"features under a superseded feature set must not appear")

	got, err = slices.ListFeaturesByProduct(ctx, uuid.New())
	require.NoError(t, err)
	assert.Empty(t, got)
}

func TestSliceStore_ListRequirementsByProduct(t *testing.T) {
	ctx := context.Background()
	s, db := newStore(t)
	f := newSliceFixture(t, ctx, s, newScope(t, ctx, db))
	slices := s.Slices()

	got, err := slices.ListRequirementsByProduct(ctx, f.product.ID)
	require.NoError(t, err)
	assert.Equal(t, []uuid.UUID{f.frA1.ID, f.nfrA1.ID, f.frA2.ID, f.frB1.ID}, requirementIDs(got))
	assert.NotContains(t, requirementIDs(got), f.otherReq.ID)

	// A superseded feature hides its requirements even while its
	// feature set stays current.
	closeCurrentRow(t, ctx, db, "feature", f.featA1.ID)
	got, err = slices.ListRequirementsByProduct(ctx, f.product.ID)
	require.NoError(t, err)
	assert.Equal(t, []uuid.UUID{f.frA2.ID, f.frB1.ID}, requirementIDs(got))

	// A superseded feature set hides everything beneath it, even
	// features that are themselves still current.
	closeCurrentRow(t, ctx, db, "feature_set", f.fsB.ID)
	got, err = slices.ListRequirementsByProduct(ctx, f.product.ID)
	require.NoError(t, err)
	assert.Equal(t, []uuid.UUID{f.frA2.ID}, requirementIDs(got))
}

func TestSliceStore_ListDecisionsByProduct(t *testing.T) {
	ctx := context.Background()
	s, db := newStore(t)
	f := newSliceFixture(t, ctx, s, newScope(t, ctx, db))
	slices := s.Slices()

	got, err := slices.ListDecisionsByProduct(ctx, f.product.ID)
	require.NoError(t, err)
	assert.Equal(t, []uuid.UUID{f.decA.ID, f.decB.ID}, decisionIDs(got))

	amended, err := s.Amend().AmendLoadBearingDecision(ctx, f.decA.ID, "A decision v2", nil)
	require.NoError(t, err)
	got, err = slices.ListDecisionsByProduct(ctx, f.product.ID)
	require.NoError(t, err)
	require.Len(t, got, 2)
	assert.Equal(t, amended.RevisionID, got[0].RevisionID)
	assert.Equal(t, "A decision v2", got[0].Name)

	closeCurrentRow(t, ctx, db, "feature_set", f.fsA.ID)
	got, err = slices.ListDecisionsByProduct(ctx, f.product.ID)
	require.NoError(t, err)
	assert.Equal(t, []uuid.UUID{f.decB.ID}, decisionIDs(got))
}

func TestSliceStore_ListCurrentByIDs(t *testing.T) {
	ctx := context.Background()
	s, db := newStore(t)
	f := newSliceFixture(t, ctx, s, newScope(t, ctx, db))
	slices := s.Slices()
	unknown := uuid.New()

	t.Run("empty input returns nil", func(t *testing.T) {
		fss, err := slices.ListFeatureSetsCurrentByIDs(ctx, nil)
		require.NoError(t, err)
		assert.Nil(t, fss)
		feats, err := slices.ListFeaturesCurrentByIDs(ctx, []uuid.UUID{})
		require.NoError(t, err)
		assert.Nil(t, feats)
		reqs, err := slices.ListRequirementsCurrentByIDs(ctx, nil)
		require.NoError(t, err)
		assert.Nil(t, reqs)
		decs, err := slices.ListDecisionsCurrentByIDs(ctx, nil)
		require.NoError(t, err)
		assert.Nil(t, decs)
	})

	t.Run("resolves by kind, dedupes, and skips unknown or other-kind ids", func(t *testing.T) {
		// Every id in the mixed set is passed to every kind's lookup, the
		// way GetEntitySetSlice fans a heterogeneous set out by kind.
		mixed := []uuid.UUID{
			f.fsA.ID, f.fsA.ID, f.otherFS.ID,
			f.featB1.ID, f.featB1.ID,
			f.frA1.ID, f.otherReq.ID,
			f.decB.ID,
			unknown,
		}

		fss, err := slices.ListFeatureSetsCurrentByIDs(ctx, mixed)
		require.NoError(t, err)
		assert.ElementsMatch(t, []uuid.UUID{f.fsA.ID, f.otherFS.ID}, featureSetIDs(fss))

		feats, err := slices.ListFeaturesCurrentByIDs(ctx, mixed)
		require.NoError(t, err)
		assert.ElementsMatch(t, []uuid.UUID{f.featB1.ID}, featureIDs(feats))

		reqs, err := slices.ListRequirementsCurrentByIDs(ctx, mixed)
		require.NoError(t, err)
		assert.ElementsMatch(t, []uuid.UUID{f.frA1.ID, f.otherReq.ID}, requirementIDs(reqs))

		decs, err := slices.ListDecisionsCurrentByIDs(ctx, mixed)
		require.NoError(t, err)
		assert.ElementsMatch(t, []uuid.UUID{f.decB.ID}, decisionIDs(decs))
	})

	t.Run("amended entities return only their current revision", func(t *testing.T) {
		amendedReq, err := s.Amend().AmendRequirement(ctx, f.frA2.ID, "A2 fr v2", nil)
		require.NoError(t, err)
		amendedDec, err := s.Amend().AmendLoadBearingDecision(ctx, f.decA.ID, "A decision v2", nil)
		require.NoError(t, err)

		reqs, err := slices.ListRequirementsCurrentByIDs(ctx, []uuid.UUID{f.frA2.ID})
		require.NoError(t, err)
		require.Len(t, reqs, 1)
		assert.Equal(t, amendedReq.RevisionID, reqs[0].RevisionID)

		decs, err := slices.ListDecisionsCurrentByIDs(ctx, []uuid.UUID{f.decA.ID})
		require.NoError(t, err)
		require.Len(t, decs, 1)
		assert.Equal(t, amendedDec.RevisionID, decs[0].RevisionID)
	})

	t.Run("superseded rows are absent", func(t *testing.T) {
		closeCurrentRow(t, ctx, db, "feature_set", f.fsB.ID)
		closeCurrentRow(t, ctx, db, "feature", f.featA1.ID)
		closeCurrentRow(t, ctx, db, "requirement", f.nfrA1.ID)
		closeCurrentRow(t, ctx, db, "load_bearing_decision", f.otherDec.ID)

		fss, err := slices.ListFeatureSetsCurrentByIDs(ctx, []uuid.UUID{f.fsA.ID, f.fsB.ID})
		require.NoError(t, err)
		assert.ElementsMatch(t, []uuid.UUID{f.fsA.ID}, featureSetIDs(fss))

		feats, err := slices.ListFeaturesCurrentByIDs(ctx, []uuid.UUID{f.featA1.ID, f.featA2.ID})
		require.NoError(t, err)
		assert.ElementsMatch(t, []uuid.UUID{f.featA2.ID}, featureIDs(feats))

		reqs, err := slices.ListRequirementsCurrentByIDs(ctx, []uuid.UUID{f.nfrA1.ID, f.frA1.ID})
		require.NoError(t, err)
		assert.ElementsMatch(t, []uuid.UUID{f.frA1.ID}, requirementIDs(reqs))

		decs, err := slices.ListDecisionsCurrentByIDs(ctx, []uuid.UUID{f.otherDec.ID, f.decB.ID})
		require.NoError(t, err)
		assert.ElementsMatch(t, []uuid.UUID{f.decB.ID}, decisionIDs(decs))
	})
}
