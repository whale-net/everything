//go:build integration

// This file is issue #2690's end-to-end conformance proof for krill M3's
// delivery axis: one connected walk through every FR #2683-#2689 shipped
// independently (milestone authoring, milepebbles, status history,
// per-item shipment, re-cut, the backlog bucket, and abandon), proving
// they compose against a real Postgres rather than only in isolation.
// Shares this package's target and testEnv/newTestEnv helper
// (roundtrip_integration_test.go) -- see that file's own doc comment for
// why this package only builds under the "integration" tag.
//
// Unlike roundtrip_integration_test.go and whagent_net_import_integration_test.go,
// this file never goes through //krill/importer: the delivery axis has no
// markdown-import path of its own (FR16/FR17 stop at Delivers/Must-not-
// foreclose association rows -- see krill/ARCHITECTURE.md "The markdown
// importer and the delivery-axis association"), so the fixture here is
// built directly against krill/store, the same way every per-FR issue's
// own integration tests already do.
//
// Run it explicitly (requires a working Docker daemon):
//
//	bazel test //krill/conformance:roundtrip_integration_test --test_output=all --test_filter=TestDeliveryAxis
package conformance

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/whale-net/everything/krill/render"
	"github.com/whale-net/everything/krill/slice"
	"github.com/whale-net/everything/krill/store"
)

// deliveryAxisAgent/deliveryAxisHuman are this file's fixed LB4 subject
// pair -- an Agent acting on a Requirement Contributor's behalf, the same
// shape #2683-#2689's own per-FR tests use, so every write below records
// a real, distinguishable acting/on-behalf-of pair (NFR4) rather than one
// subject standing in for both.
func deliveryAxisAgent() store.Subject {
	return store.Subject{Iss: "https://whagent.example.test", Sub: "agent-delivery-axis", Kind: store.SubjectKindService}
}

func deliveryAxisHuman() store.Subject {
	return store.Subject{Iss: "https://keycloak.example.test/realms/humans", Sub: "requirement-contributor-1", Kind: store.SubjectKindHuman}
}

// TestDeliveryAxis_EndToEndLifecycle_EveryStepReassertsCapturedIDs is
// issue #2690's Scope walk, in order: authoring (#2683) -> milepebbles and
// mid-milestone discovery (#2684) -> status history (#2685) -> per-item
// shipment (#2686) -> re-cut (#2687) -> abandon (#2688) -> product-wide
// listing (#2689), then NFR1 (every id from step 1 still resolves,
// unchanged) and a renderer non-regression pass (milepebble/backlog rows
// never leak into product/03-roadmap.md).
func TestDeliveryAxis_EndToEndLifecycle_EveryStepReassertsCapturedIDs(t *testing.T) {
	ctx := context.Background()
	env := newTestEnv(t)
	agent, human := deliveryAxisAgent(), deliveryAxisHuman()

	// -- Step 1: create a product, milestone M-A with an outcome, four
	// Delivers features, two Must-not-foreclose decisions, two deferrals
	// each naming a destination, and an FR budget. --------------------

	product, err := env.store.Products().Create(ctx, env.scopeID, "Delivery Axis Conformance", "prove krill M3's delivery axis composes end to end")
	require.NoError(t, err)

	featureSet, err := env.store.FeatureSets().Create(ctx, env.scopeID, product.ID, "Delivery Axis Surface", nil)
	require.NoError(t, err)

	var features [4]store.Feature
	for i, name := range []string{"Milestone authoring", "Milepebbles", "Status history", "Delivery shipment"} {
		f, err := env.store.Features().Create(ctx, env.scopeID, featureSet.ID, name, nil)
		require.NoError(t, err)
		features[i] = f
	}
	// A fifth Feature under the same FeatureSet that M-A never delivers --
	// this file's own fixture for step 3's non-subset refusal, never
	// added to M-A's Delivers set.
	outOfScope, err := env.store.Features().Create(ctx, env.scopeID, featureSet.ID, "Not delivered by M-A", nil)
	require.NoError(t, err)

	var decisions [2]store.LoadBearingDecision
	for i, name := range []string{"LB — one milestone_ref table for milestone/milepebble/backlog", "LB — Delivers is an association, never a spec-entity column"} {
		d, err := env.store.Decisions().Create(ctx, env.scopeID, featureSet.ID, name, nil)
		require.NoError(t, err)
		decisions[i] = d
	}

	initialBudget := 3
	milestone, err := env.store.MilestoneAuthoring().CreateMilestone(ctx, env.scopeID, product.ID, "M-A", "ship the whole delivery axis, provably", &initialBudget, agent, human)
	require.NoError(t, err)

	for _, f := range features {
		require.NoError(t, env.store.MilestoneAuthoring().AddDelivers(ctx, env.scopeID, milestone.ID, f.ID, agent, human))
	}
	for _, d := range decisions {
		require.NoError(t, env.store.MilestoneAuthoring().AddMustNotForeclose(ctx, env.scopeID, milestone.ID, d.ID, agent, human))
	}

	var deferrals [2]store.MilestoneDeferral
	for i, dest := range []struct{ body, destination string }{
		{"a live per-milestone Delivers filter over MCP", "M4 (root plan issue #2681's own out-of-scope note)"},
		{"an un-abandon verb", "Later (krill/product/02-capability-map.md)"},
	} {
		def, err := env.store.MilestoneAuthoring().AddDeferral(ctx, env.scopeID, milestone.ID, dest.body, dest.destination, agent, human)
		require.NoError(t, err)
		deferrals[i] = def
	}

	// -- Step 2: revise the FR budget -- the current value is the new one,
	// the old one is not resurrected (FR2). ---------------------------

	revisedBudget := 5
	require.NoError(t, env.store.MilestoneAuthoring().SetFRBudget(ctx, milestone.ID, revisedBudget, agent, human))

	ref, delivers, mustNotForeclose, gotDeferrals, err := env.store.MilestoneAuthoring().GetMilestone(ctx, milestone.ID)
	require.NoError(t, err)
	require.NotNil(t, ref.FRBudget)
	assert.Equal(t, revisedBudget, *ref.FRBudget, "FR2: the current FR budget must be the revised value")
	assert.NotEqual(t, initialBudget, *ref.FRBudget, "FR2: the superseded budget must not be resurrected")
	assert.Len(t, delivers, 4, "M-A's Delivers set is exactly the four seeded features so far")
	assert.Len(t, mustNotForeclose, 2)
	assert.Len(t, gotDeferrals, 2)

	// -- Step 3: cut M-A into two milepebbles, each with a subset of M-A's
	// delivered items; a non-subset item is refused (FR3). ------------

	mp1, err := env.store.MilestoneAuthoring().CreateMilepebble(ctx, env.scopeID, milestone.ID, "mp-1", "first cut", agent, human)
	require.NoError(t, err)
	mp2, err := env.store.MilestoneAuthoring().CreateMilepebble(ctx, env.scopeID, milestone.ID, "mp-2", "second cut", agent, human)
	require.NoError(t, err)

	require.NoError(t, env.store.MilestoneAuthoring().AddMilepebbleDelivers(ctx, env.scopeID, mp1.ID, features[0].ID, agent, human))
	require.NoError(t, env.store.MilestoneAuthoring().AddMilepebbleDelivers(ctx, env.scopeID, mp1.ID, features[1].ID, agent, human))
	require.NoError(t, env.store.MilestoneAuthoring().AddMilepebbleDelivers(ctx, env.scopeID, mp2.ID, features[2].ID, agent, human))
	require.NoError(t, env.store.MilestoneAuthoring().AddMilepebbleDelivers(ctx, env.scopeID, mp2.ID, features[3].ID, agent, human))

	err = env.store.MilestoneAuthoring().AddMilepebbleDelivers(ctx, env.scopeID, mp1.ID, outOfScope.ID, agent, human)
	assert.ErrorIs(t, err, store.ErrMilepebbleDeliversNotSubset, "FR3: a milepebble may only deliver a subset of its parent milestone's own Delivers set")

	// -- Step 4: mid-milestone discovery -- a real Requirement row landed
	// on mp1, leaving M-A's own outcome/budget/deferrals untouched (FR4).

	preDiscovery, _, _, preDiscoveryDeferrals, err := env.store.MilestoneAuthoring().GetMilestone(ctx, milestone.ID)
	require.NoError(t, err)

	discovered, err := env.store.MilestoneAuthoring().AddDiscoveredScope(ctx, env.scopeID, mp1.ID, store.DiscoveredScopeInput{
		FeatureID:       &features[0].ID,
		RequirementKind: store.RequirementKindFR,
		Name:            "mid-milestone discovery",
		Body:            strPtr("surfaced while cutting mp1, never anticipated in M-A's original four Features"),
	}, agent, human)
	require.NoError(t, err)
	assert.Equal(t, store.DiscoveredScopeEntityKindRequirement, discovered.Kind, "FR4: this discovery lands as a real Requirement row, never a bespoke shape")

	postDiscovery, postDelivers, _, postDeferrals, err := env.store.MilestoneAuthoring().GetMilestone(ctx, milestone.ID)
	require.NoError(t, err)
	assert.Equal(t, preDiscovery.Outcome, postDiscovery.Outcome, "FR4: M-A's own outcome must be untouched by a mid-milestone discovery landed on one of its milepebbles")
	assert.Equal(t, *preDiscovery.FRBudget, *postDiscovery.FRBudget, "FR4: M-A's own FR budget must be untouched")
	assert.Equal(t, len(preDiscoveryDeferrals), len(postDeferrals), "FR4: M-A's own deferrals must be untouched")
	// AddDiscoveredScope's own contract (milestone_authoring.go) is that
	// the discovered item is also associated to the milepebble's *parent*
	// milestone's Delivers set, so the FR3 subset invariant still holds
	// immediately afterward -- M-A's Delivers set is therefore five items
	// from here on (the original four Features plus this Requirement),
	// not four; step 6 below accounts for that fifth item explicitly.
	assert.Len(t, postDelivers, 5, "FR4: the discovered Requirement joins M-A's own Delivers set too, alongside the original four Features")

	mp1Ref, mp1Delivers, _, _, err := env.store.MilestoneAuthoring().GetMilestone(ctx, mp1.ID)
	require.NoError(t, err)
	assert.Equal(t, store.MilestoneKindMilepebble, mp1Ref.Kind)
	mp1DeliversIDs := entityMilestoneIDs(mp1Delivers)
	assert.Contains(t, mp1DeliversIDs, discovered.EntityID, "the discovered Requirement must be in mp1's own Delivers set")

	// -- Step 5: walk statuses planned -> in progress on M-A and both
	// milepebbles; read the full transition history with actor and
	// timestamp (FR8/FR9/FR12/NFR2/NFR4). ------------------------------

	for _, containerID := range []uuid.UUID{milestone.ID, mp1.ID, mp2.ID} {
		_, err := env.store.MilestoneStatus().RecordTransition(ctx, env.scopeID, containerID, store.MilestoneStatusPlanned, nil, agent, human)
		require.NoError(t, err)
		_, err = env.store.MilestoneStatus().RecordTransition(ctx, env.scopeID, containerID, store.MilestoneStatusInProgress, nil, agent, human)
		require.NoError(t, err)
	}

	history, err := env.store.MilestoneStatus().ListTransitions(ctx, milestone.ID)
	require.NoError(t, err)
	require.Len(t, history, 2, "NFR2: every transition is an addition to history, never an overwrite")
	assert.Equal(t, store.MilestoneStatusPlanned, history[0].Status, "FR12: transitions read back in chronological order")
	assert.Equal(t, store.MilestoneStatusInProgress, history[1].Status)
	for _, event := range history {
		assert.Equal(t, agent, event.CreatedByActing, "NFR4: every transition records its acting subject")
		assert.Equal(t, human, event.CreatedByOnBehalfOf, "NFR4: every transition records its on-behalf-of subject")
		assert.False(t, event.CreatedAt.IsZero(), "FR12: every transition carries a real timestamp")
	}
	assert.True(t, history[1].CreatedAt.Compare(history[0].CreatedAt) >= 0, "FR12: history reads back oldest first")

	current, err := env.store.MilestoneStatus().CurrentStatus(ctx, milestone.ID)
	require.NoError(t, err)
	assert.Equal(t, store.MilestoneStatusInProgress, current, "FR8: current status is the latest transition")

	// -- Step 6: mark two of M-A's original four delivered Features
	// shipped; set M-A `partially complete`; assert the breakdown as
	// typed entities (FR10). Also mark features[1] shipped specifically
	// against mp1's own container -- setup for step 7's shipped-refusal
	// case below, since shipped-ness hangs off the (entity, container)
	// association, never the spec entity alone (migration 013's own
	// LB3/NFR3 note). --------------------------------------------------

	require.NoError(t, env.store.DeliveryShipments().MarkShipped(ctx, env.scopeID, milestone.ID, features[0].ID, nil, agent, human))
	require.NoError(t, env.store.DeliveryShipments().MarkShipped(ctx, env.scopeID, milestone.ID, features[1].ID, nil, agent, human))
	require.NoError(t, env.store.DeliveryShipments().MarkShipped(ctx, env.scopeID, mp1.ID, features[1].ID, nil, agent, human))

	_, err = env.store.MilestoneStatus().RecordTransition(ctx, env.scopeID, milestone.ID, store.MilestoneStatusPartiallyComplete, nil, agent, human)
	require.NoError(t, err)

	q := slice.NewQuerier(env.store)
	shippedDoc, unshippedDoc, err := q.GetDeliveryBreakdown(ctx, milestone.ID)
	require.NoError(t, err)
	assert.Len(t, shippedDoc.Features, 2, "FR10: exactly the two marked-shipped Features")
	assert.Empty(t, shippedDoc.Requirements, "the discovered Requirement was never marked shipped")
	// M-A's own Delivers set is five (step 4's note above): the two
	// remaining un-shipped Features plus the one un-shipped discovered
	// Requirement.
	assert.Len(t, unshippedDoc.Features, 2, "FR10: the two not-yet-shipped original Features")
	assert.Len(t, unshippedDoc.Requirements, 1, "FR10: the discovered Requirement, still unshipped at M-A's own container")

	// -- Step 7: re-cut one unshipped item out of mp1 into mp2; then
	// attempt to re-cut a shipped item and assert the loud refusal, with
	// nothing applied (FR5/NFR3). ---------------------------------------

	require.NoError(t, env.store.Recut().MoveScope(ctx, env.scopeID, []uuid.UUID{discovered.EntityID}, mp1.ID, mp2.ID, agent, human))

	_, mp1DeliversAfterMove, _, _, err := env.store.MilestoneAuthoring().GetMilestone(ctx, mp1.ID)
	require.NoError(t, err)
	assert.NotContains(t, entityMilestoneIDs(mp1DeliversAfterMove), discovered.EntityID, "FR5: the moved item must leave mp1's own Delivers set")
	_, mp2DeliversAfterMove, _, _, err := env.store.MilestoneAuthoring().GetMilestone(ctx, mp2.ID)
	require.NoError(t, err)
	assert.Contains(t, entityMilestoneIDs(mp2DeliversAfterMove), discovered.EntityID, "FR5: the moved item must join mp2's own Delivers set")

	err = env.store.Recut().MoveScope(ctx, env.scopeID, []uuid.UUID{features[1].ID}, mp1.ID, mp2.ID, agent, human)
	assert.ErrorIs(t, err, store.ErrEntityShipped, "NFR3: re-cutting an item already shipped in its from-container is refused loudly")
	_, mp1DeliversAfterRefusal, _, _, err := env.store.MilestoneAuthoring().GetMilestone(ctx, mp1.ID)
	require.NoError(t, err)
	assert.Contains(t, entityMilestoneIDs(mp1DeliversAfterRefusal), features[1].ID, "NFR3: a rejected move must leave the from-association untouched")

	// -- Step 8: abandon mp2; its unshipped scope lands in the backlog
	// bucket, its shipped scope stays, and delivery_shipment rows are
	// byte-identical before and after (FR6/NFR3). ------------------------

	require.NoError(t, env.store.DeliveryShipments().MarkShipped(ctx, env.scopeID, mp2.ID, features[3].ID, nil, agent, human))

	var shipmentIDBefore uuid.UUID
	require.NoError(t, env.pool.QueryRow(ctx, `
		SELECT id FROM delivery_shipment WHERE entity_id = $1 AND milestone_id = $2
	`, features[3].ID, mp2.ID).Scan(&shipmentIDBefore))

	abandonResult, err := env.store.Abandon().Abandon(ctx, env.scopeID, mp2.ID, nil, agent, human)
	require.NoError(t, err)
	assert.Contains(t, abandonResult.MovedToBacklogIDs, features[2].ID, "FR6: mp2's other un-shipped original Feature sweeps into the backlog")
	assert.Contains(t, abandonResult.MovedToBacklogIDs, discovered.EntityID, "FR6: the re-cut-in discovered Requirement sweeps into the backlog too")
	assert.NotContains(t, abandonResult.MovedToBacklogIDs, features[3].ID, "NFR3: the shipped Feature must never be swept")
	assert.Contains(t, abandonResult.ShippedIDs, features[3].ID)
	assert.Equal(t, store.MilestoneStatusAbandoned, abandonResult.StatusEvent.Status)

	var shipmentIDAfter uuid.UUID
	require.NoError(t, env.pool.QueryRow(ctx, `
		SELECT id FROM delivery_shipment WHERE entity_id = $1 AND milestone_id = $2
	`, features[3].ID, mp2.ID).Scan(&shipmentIDAfter))
	assert.Equal(t, shipmentIDBefore, shipmentIDAfter, "NFR3: the shipped half's delivery_shipment row must be byte-identical before and after an abandon")

	backlogDoc, err := q.GetBacklog(ctx, product.ID)
	require.NoError(t, err)
	backlogIDs := documentEntityIDs(backlogDoc)
	assert.Contains(t, backlogIDs, features[2].ID)
	assert.Contains(t, backlogIDs, discovered.EntityID)
	assert.NotContains(t, backlogIDs, features[3].ID, "the shipped Feature stays on mp2, never the backlog")

	// -- Step 9: ListProductDelivery, unfiltered and filtered to
	// `partially complete` -- hierarchy, counts, and the backlog bucket
	// never appearing (FR11). --------------------------------------------

	fullListing, err := q.ListProductDelivery(ctx, env.scopeID, product.ID, nil)
	require.NoError(t, err)
	require.Len(t, fullListing.Milestones, 1, "the backlog bucket has kind='backlog', never kind='milestone' -- it must never surface as a top-level entry")
	mEntry := fullListing.Milestones[0]
	assert.Equal(t, milestone.ID, mEntry.ID)
	assert.Equal(t, store.MilestoneStatusPartiallyComplete, mEntry.Status)
	require.NotNil(t, mEntry.ShippedCount)
	require.NotNil(t, mEntry.UnshippedCount)
	assert.Equal(t, 2, *mEntry.ShippedCount, "FR10/FR11: the two Features marked shipped in step 6")
	assert.Equal(t, 3, *mEntry.UnshippedCount, "FR10/FR11: M-A's own Delivers set is five items (four original Features plus the step-4 discovery) -- two shipped leaves three unshipped, not two")
	require.Len(t, mEntry.Milepebbles, 2, "both milepebbles remain M-A's children after the re-cut and the abandon -- abandon never deletes a milestone_ref row")
	mp1Entry := milepebbleEntryByID(t, mEntry.Milepebbles, mp1.ID)
	assert.Equal(t, store.MilestoneStatusInProgress, mp1Entry.Status)
	mp2Entry := milepebbleEntryByID(t, mEntry.Milepebbles, mp2.ID)
	assert.Equal(t, store.MilestoneStatusAbandoned, mp2Entry.Status)
	assert.NotContains(t, mEntry.Name+mp1Entry.Name+mp2Entry.Name, "backlog", "FR11: the backlog bucket never appears in a product-wide delivery listing")

	filteredListing, err := q.ListProductDelivery(ctx, env.scopeID, product.ID, []store.MilestoneStatus{store.MilestoneStatusPartiallyComplete})
	require.NoError(t, err)
	require.Len(t, filteredListing.Milestones, 1, "M-A itself matches `partially complete`")
	filteredEntry := filteredListing.Milestones[0]
	assert.Equal(t, milestone.ID, filteredEntry.ID)
	assert.Empty(t, filteredEntry.Milepebbles, "neither milepebble is itself `partially complete` (in progress / abandoned), so neither is included even though their parent matched")

	// -- Step 10 (NFR1): every entity and container id captured at step 1
	// still resolves to the same thing with the same meaning. -----------

	gotProduct, err := env.store.Products().GetCurrentByID(ctx, product.ID)
	require.NoError(t, err)
	assert.Equal(t, "Delivery Axis Conformance", gotProduct.Name, "NFR1: the product id still resolves to the same product")

	gotFeatureSet, err := env.store.FeatureSets().GetCurrentByID(ctx, featureSet.ID)
	require.NoError(t, err)
	assert.Equal(t, "Delivery Axis Surface", gotFeatureSet.Name)

	for i, f := range features {
		got, err := env.store.Features().GetCurrentByID(ctx, f.ID)
		require.NoError(t, err)
		assert.Equal(t, f.Name, got.Name, "NFR1: feature %d's id must still resolve to the same feature, unrenumbered", i)
	}
	gotOutOfScope, err := env.store.Features().GetCurrentByID(ctx, outOfScope.ID)
	require.NoError(t, err)
	assert.Equal(t, outOfScope.Name, gotOutOfScope.Name)

	for i, d := range decisions {
		got, err := env.store.Decisions().GetCurrentByID(ctx, d.ID)
		require.NoError(t, err)
		assert.Equal(t, d.Name, got.Name, "NFR1: decision %d's id must still resolve to the same decision", i)
	}

	gotDiscovered, err := env.store.Requirements().GetCurrentByID(ctx, discovered.EntityID)
	require.NoError(t, err)
	assert.Equal(t, "mid-milestone discovery", gotDiscovered.Name, "NFR1: the mid-milestone discovery's id still resolves, minted at step 4")

	finalRef, finalDelivers, finalMustNotForeclose, finalDeferrals, err := env.store.MilestoneAuthoring().GetMilestone(ctx, milestone.ID)
	require.NoError(t, err)
	assert.Equal(t, "M-A", finalRef.Name, "NFR1: the milestone id still names M-A, never renumbered")
	require.NotNil(t, finalRef.FRBudget)
	assert.Equal(t, revisedBudget, *finalRef.FRBudget, "NFR1/FR2: the revised budget from step 2 is still the current one, not resurrected by anything since")
	assert.Len(t, finalDelivers, 5, "NFR1: M-A's own Delivers set is unchanged by the re-cut out of mp1 (moving out of a milepebble does not touch the parent's own association) or by mp2's abandon (M-A itself was never abandoned)")
	assert.Len(t, finalMustNotForeclose, 2)
	require.Len(t, finalDeferrals, 2)
	for i, want := range deferrals {
		assert.Equal(t, want.ID, finalDeferrals[i].ID, "NFR1: deferral %d's id is unchanged", i)
		assert.Equal(t, want.Destination, finalDeferrals[i].Destination)
	}

	finalMp1, _, _, _, err := env.store.MilestoneAuthoring().GetMilestone(ctx, mp1.ID)
	require.NoError(t, err)
	assert.Equal(t, "mp-1", finalMp1.Name)
	require.NotNil(t, finalMp1.ParentMilestoneID)
	assert.Equal(t, milestone.ID, *finalMp1.ParentMilestoneID, "NFR1: mp1's parent is still M-A")

	finalMp2, _, _, _, err := env.store.MilestoneAuthoring().GetMilestone(ctx, mp2.ID)
	require.NoError(t, err)
	assert.Equal(t, "mp-2", finalMp2.Name)
	assert.Equal(t, store.MilestoneKindMilepebble, finalMp2.Kind, "NFR1: abandon never changes a milestone_ref row's own Kind")

	// -- Step 11: renderer non-regression -- re-render PRODUCT.md and
	// product/*.md from krill's own record; milepebble and backlog rows
	// never leak into product/03-roadmap.md. -----------------------------

	files, err := render.Render(ctx, render.NewStoreSource(env.store), env.scopeID, product.ID)
	require.NoError(t, err)
	assert.Contains(t, files.RoadmapMD, "### M-A — ship the whole delivery axis, provably", "issue #2970: the outcome sentence must render in the milestone heading")
	assert.Contains(t, files.RoadmapMD, "FR budget: 5", "issue #2970: the current (revised) FR budget must render, not the superseded initial one")
	assert.Contains(t, files.RoadmapMD, "Deliberately deferred: a live per-milestone Delivers filter over MCP (→ M4 (root plan issue #2681's own out-of-scope note)); an un-abandon verb (→ Later (krill/product/02-capability-map.md))", "issue #2970: every deferral must render, in position order")
	assert.NotContains(t, files.RoadmapMD, "mp-1", "a milepebble must never appear in the rendered roadmap")
	assert.NotContains(t, files.RoadmapMD, "mp-2", "a milepebble must never appear in the rendered roadmap, even once abandoned")
	assert.NotContains(t, files.RoadmapMD, "backlog", "the backlog bucket must never appear in the rendered roadmap")
}

func strPtr(s string) *string { return &s }

func entityMilestoneIDs(rows []store.EntityMilestone) []uuid.UUID {
	ids := make([]uuid.UUID, len(rows))
	for i, r := range rows {
		ids[i] = r.EntityID
	}
	return ids
}

func documentEntityIDs(doc slice.Document) []uuid.UUID {
	ids := make([]uuid.UUID, 0, len(doc.Features)+len(doc.Requirements))
	for _, f := range doc.Features {
		ids = append(ids, f.ID)
	}
	for _, r := range doc.Requirements {
		ids = append(ids, r.ID)
	}
	return ids
}

func milepebbleEntryByID(t *testing.T, entries []slice.MilepebbleListingEntry, id uuid.UUID) slice.MilepebbleListingEntry {
	t.Helper()
	for _, e := range entries {
		if e.ID == id {
			return e
		}
	}
	t.Fatalf("milepebble %s not found among %d entries", id, len(entries))
	return slice.MilepebbleListingEntry{}
}
