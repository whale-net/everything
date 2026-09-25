//go:build integration

// Real-Postgres integration coverage for ListProductDelivery (issue
// #2689, krill M3, FR11, C28). Follows query_integration_test.go's
// harness (newTestStore/createScope/seedWorld/world) exactly -- see that
// file's own doc comment for the dbtest/migration/seeding pattern this
// reuses rather than duplicates.
//
// Run it explicitly (requires a working Docker daemon):
//
//	bazel test //krill/slice:milestone_listing_integration_test --test_output=all
package slice_test

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/whale-net/everything/krill/slice"
	"github.com/whale-net/everything/krill/store"
)

func milestoneListingTestSubject() store.Subject {
	return store.Subject{Iss: "https://keycloak.example.test/realms/humans", Sub: "human-1", Kind: store.SubjectKindHuman}
}

// milestoneEntry finds e's own entry by id, failing the test if it is not
// present -- most of this file's assertions are "is this milestone in the
// listing, and with what shape", not "what is at index N".
func milestoneEntry(t *testing.T, listing slice.DeliveryListing, id uuid.UUID) slice.MilestoneListingEntry {
	t.Helper()
	for _, m := range listing.Milestones {
		if m.ID == id {
			return m
		}
	}
	t.Fatalf("milestone %s not found in listing (got %d milestones)", id, len(listing.Milestones))
	return slice.MilestoneListingEntry{}
}

// TestListProductDelivery_FiltersToSingleStatus is issue #2689's Testing
// item 1: five milestones across four statuses, filtered to `planned`,
// returns exactly the planned ones, each with its own milepebbles (a
// milepebble that itself matches the filter, plus one that does not and
// must be excluded).
func TestListProductDelivery_FiltersToSingleStatus(t *testing.T) {
	ctx := context.Background()
	entities, pool := newTestStore(t)
	scopeID := createScope(t, ctx, pool, "whale-net/slice-fr11-single-status-test")
	w := seedWorld(t, ctx, entities, scopeID)
	self := milestoneListingTestSubject()

	statusByName := map[string]store.MilestoneStatus{
		"A-planned":  store.MilestoneStatusPlanned,
		"B-planned":  store.MilestoneStatusPlanned,
		"C-progress": store.MilestoneStatusInProgress,
		"D-shipped":  store.MilestoneStatusShipped,
		"E-abandon":  store.MilestoneStatusAbandoned,
	}
	ids := map[string]uuid.UUID{}
	for _, name := range []string{"A-planned", "B-planned", "C-progress", "D-shipped", "E-abandon"} {
		m, err := entities.MilestoneAuthoring().CreateMilestone(ctx, scopeID, w.Product.ID, name, "", nil, self, self)
		require.NoError(t, err)
		_, err = entities.MilestoneStatus().RecordTransition(ctx, scopeID, m.ID, statusByName[name], nil, self, self)
		require.NoError(t, err)
		ids[name] = m.ID
	}

	// A-planned gets a milepebble that itself also matches the filter.
	mpMatch, err := entities.MilestoneAuthoring().CreateMilepebble(ctx, scopeID, ids["A-planned"], "mp-match", "", nil, self, self)
	require.NoError(t, err)
	_, err = entities.MilestoneStatus().RecordTransition(ctx, scopeID, mpMatch.ID, store.MilestoneStatusPlanned, nil, self, self)
	require.NoError(t, err)

	// B-planned gets a milepebble that does NOT match the filter -- it
	// must not appear even though its parent does.
	mpNoMatch, err := entities.MilestoneAuthoring().CreateMilepebble(ctx, scopeID, ids["B-planned"], "mp-no-match", "", nil, self, self)
	require.NoError(t, err)
	_, err = entities.MilestoneStatus().RecordTransition(ctx, scopeID, mpNoMatch.ID, store.MilestoneStatusInProgress, nil, self, self)
	require.NoError(t, err)

	q := slice.NewQuerier(entities)
	listing, err := q.ListProductDelivery(ctx, scopeID, w.Product.ID, []store.MilestoneStatus{store.MilestoneStatusPlanned})
	require.NoError(t, err)

	require.Len(t, listing.Milestones, 2, "exactly the two planned milestones, never the in-progress/shipped/abandoned ones")
	a := milestoneEntry(t, listing, ids["A-planned"])
	require.Len(t, a.Milepebbles, 1)
	assert.Equal(t, mpMatch.ID, a.Milepebbles[0].ID)

	b := milestoneEntry(t, listing, ids["B-planned"])
	assert.Empty(t, b.Milepebbles, "B's own milepebble does not match `planned` and must not appear")

	for _, excluded := range []string{"C-progress", "D-shipped", "E-abandon"} {
		for _, m := range listing.Milestones {
			assert.NotEqual(t, ids[excluded], m.ID, "%s must not appear in a `planned`-only listing", excluded)
		}
	}
}

// TestListProductDelivery_FiltersToMultipleStatuses is issue #2689's
// Testing section (a repeated `status` filter): filtering to
// {planned, shipped} returns exactly those two milestones, excluding the
// in-progress and not-started ones.
func TestListProductDelivery_FiltersToMultipleStatuses(t *testing.T) {
	ctx := context.Background()
	entities, pool := newTestStore(t)
	scopeID := createScope(t, ctx, pool, "whale-net/slice-fr11-multi-status-test")
	w := seedWorld(t, ctx, entities, scopeID)
	self := milestoneListingTestSubject()

	planned, err := entities.MilestoneAuthoring().CreateMilestone(ctx, scopeID, w.Product.ID, "planned", "", nil, self, self)
	require.NoError(t, err)
	_, err = entities.MilestoneStatus().RecordTransition(ctx, scopeID, planned.ID, store.MilestoneStatusPlanned, nil, self, self)
	require.NoError(t, err)

	shipped, err := entities.MilestoneAuthoring().CreateMilestone(ctx, scopeID, w.Product.ID, "shipped", "", nil, self, self)
	require.NoError(t, err)
	_, err = entities.MilestoneStatus().RecordTransition(ctx, scopeID, shipped.ID, store.MilestoneStatusShipped, nil, self, self)
	require.NoError(t, err)

	inProgress, err := entities.MilestoneAuthoring().CreateMilestone(ctx, scopeID, w.Product.ID, "in-progress", "", nil, self, self)
	require.NoError(t, err)
	_, err = entities.MilestoneStatus().RecordTransition(ctx, scopeID, inProgress.ID, store.MilestoneStatusInProgress, nil, self, self)
	require.NoError(t, err)

	q := slice.NewQuerier(entities)
	listing, err := q.ListProductDelivery(ctx, scopeID, w.Product.ID, []store.MilestoneStatus{store.MilestoneStatusPlanned, store.MilestoneStatusShipped})
	require.NoError(t, err)

	require.Len(t, listing.Milestones, 2)
	gotIDs := []uuid.UUID{listing.Milestones[0].ID, listing.Milestones[1].ID}
	assert.Contains(t, gotIDs, planned.ID)
	assert.Contains(t, gotIDs, shipped.ID)
	assert.NotContains(t, gotIDs, inProgress.ID)
}

// TestListProductDelivery_NotStartedFilter_IncludesZeroStatusRowContainers
// is issue #2689's Testing item 2 and its own design constraint's sharpest
// edge case: a milestone with zero `milestone_status_event` rows at all
// still surfaces under a `not started` filter -- the naive
// `WHERE status = 'not started'` a real event table would need is exactly
// what CurrentStatuses' derivation (absence of a row) sidesteps.
func TestListProductDelivery_NotStartedFilter_IncludesZeroStatusRowContainers(t *testing.T) {
	ctx := context.Background()
	entities, pool := newTestStore(t)
	scopeID := createScope(t, ctx, pool, "whale-net/slice-fr11-not-started-test")
	w := seedWorld(t, ctx, entities, scopeID)
	self := milestoneListingTestSubject()

	untouched, err := entities.MilestoneAuthoring().CreateMilestone(ctx, scopeID, w.Product.ID, "never transitioned", "", nil, self, self)
	require.NoError(t, err)

	planned, err := entities.MilestoneAuthoring().CreateMilestone(ctx, scopeID, w.Product.ID, "planned", "", nil, self, self)
	require.NoError(t, err)
	_, err = entities.MilestoneStatus().RecordTransition(ctx, scopeID, planned.ID, store.MilestoneStatusPlanned, nil, self, self)
	require.NoError(t, err)

	q := slice.NewQuerier(entities)
	listing, err := q.ListProductDelivery(ctx, scopeID, w.Product.ID, []store.MilestoneStatus{store.MilestoneStatusNotStarted})
	require.NoError(t, err)

	require.Len(t, listing.Milestones, 1, "a naive `WHERE status = ...` join would drop the zero-row milestone entirely")
	assert.Equal(t, untouched.ID, listing.Milestones[0].ID)
	assert.Equal(t, store.MilestoneStatusNotStarted, listing.Milestones[0].Status)
}

// TestListProductDelivery_EmptyFilterReturnsEverything_PositionOrder is
// issue #2689's Testing item 3: no filter (nil/empty statuses) returns
// every milestone, each with its milepebbles nested underneath (not a
// flat peer list), both levels in Position order (issue #2682).
func TestListProductDelivery_EmptyFilterReturnsEverything_PositionOrder(t *testing.T) {
	ctx := context.Background()
	entities, pool := newTestStore(t)
	scopeID := createScope(t, ctx, pool, "whale-net/slice-fr11-empty-filter-test")
	w := seedWorld(t, ctx, entities, scopeID)
	self := milestoneListingTestSubject()

	var milestoneIDs []uuid.UUID
	for _, name := range []string{"first", "second", "third"} {
		m, err := entities.MilestoneAuthoring().CreateMilestone(ctx, scopeID, w.Product.ID, name, "", nil, self, self)
		require.NoError(t, err)
		milestoneIDs = append(milestoneIDs, m.ID)
	}

	// Two milepebbles under "second", created in a known order -- the
	// listing must preserve that order, not e.g. reverse or name-sort it.
	var milepebbleIDs []uuid.UUID
	for _, name := range []string{"mp-first", "mp-second"} {
		mp, err := entities.MilestoneAuthoring().CreateMilepebble(ctx, scopeID, milestoneIDs[1], name, "", nil, self, self)
		require.NoError(t, err)
		milepebbleIDs = append(milepebbleIDs, mp.ID)
	}

	q := slice.NewQuerier(entities)
	listing, err := q.ListProductDelivery(ctx, scopeID, w.Product.ID, nil)
	require.NoError(t, err)

	require.Len(t, listing.Milestones, 3, "every milestone under the product, no filter dropping any of them")
	var gotOrder []uuid.UUID
	for _, m := range listing.Milestones {
		gotOrder = append(gotOrder, m.ID)
	}
	assert.Equal(t, milestoneIDs, gotOrder, "milestones must come back in Position (creation) order")

	second := milestoneEntry(t, listing, milestoneIDs[1])
	require.Len(t, second.Milepebbles, 2, "milepebbles are nested under their parent, never a flat peer list")
	assert.Equal(t, milepebbleIDs[0], second.Milepebbles[0].ID)
	assert.Equal(t, milepebbleIDs[1], second.Milepebbles[1].ID)

	first := milestoneEntry(t, listing, milestoneIDs[0])
	assert.Empty(t, first.Milepebbles)
}

// TestListProductDelivery_MilestoneNoSelfMatch_MilepebbleMatch_
// ReturnsOnlyMatchingMilepebble is issue #2689's Testing item 4 and its
// own design constraint's headline rule: a milestone whose own status does
// not match the filter, but which owns a milepebble that does, is still
// returned -- carrying only that matching milepebble, with its own Status
// field reporting its real (non-matching) status unchanged.
func TestListProductDelivery_MilestoneNoSelfMatch_MilepebbleMatch_ReturnsOnlyMatchingMilepebble(t *testing.T) {
	ctx := context.Background()
	entities, pool := newTestStore(t)
	scopeID := createScope(t, ctx, pool, "whale-net/slice-fr11-parent-carries-child-test")
	w := seedWorld(t, ctx, entities, scopeID)
	self := milestoneListingTestSubject()

	milestone, err := entities.MilestoneAuthoring().CreateMilestone(ctx, scopeID, w.Product.ID, "M1", "", nil, self, self)
	require.NoError(t, err)
	_, err = entities.MilestoneStatus().RecordTransition(ctx, scopeID, milestone.ID, store.MilestoneStatusShipped, nil, self, self)
	require.NoError(t, err)

	matching, err := entities.MilestoneAuthoring().CreateMilepebble(ctx, scopeID, milestone.ID, "matching", "", nil, self, self)
	require.NoError(t, err)
	_, err = entities.MilestoneStatus().RecordTransition(ctx, scopeID, matching.ID, store.MilestoneStatusPlanned, nil, self, self)
	require.NoError(t, err)

	nonMatching, err := entities.MilestoneAuthoring().CreateMilepebble(ctx, scopeID, milestone.ID, "non-matching", "", nil, self, self)
	require.NoError(t, err)
	_, err = entities.MilestoneStatus().RecordTransition(ctx, scopeID, nonMatching.ID, store.MilestoneStatusShipped, nil, self, self)
	require.NoError(t, err)

	q := slice.NewQuerier(entities)
	listing, err := q.ListProductDelivery(ctx, scopeID, w.Product.ID, []store.MilestoneStatus{store.MilestoneStatusPlanned})
	require.NoError(t, err)

	require.Len(t, listing.Milestones, 1, "the parent must be returned even though its own status does not match")
	entry := listing.Milestones[0]
	assert.Equal(t, milestone.ID, entry.ID)
	assert.Equal(t, store.MilestoneStatusShipped, entry.Status, "the parent's own Status is unaffected by which of its children matched")
	require.Len(t, entry.Milepebbles, 1, "only the matching milepebble, never the non-matching sibling")
	assert.Equal(t, matching.ID, entry.Milepebbles[0].ID)
}

// TestListProductDelivery_PartiallyCompleteCarriesInlineCounts is issue
// #2689's Testing item 6: a `partially complete` container's entry carries
// its shipped/unshipped counts inline, agreeing with a direct
// GetDeliveryBreakdown call (FR10, issue #2686) -- and a container at any
// other status carries neither field (nil, not zero).
func TestListProductDelivery_PartiallyCompleteCarriesInlineCounts(t *testing.T) {
	ctx := context.Background()
	entities, pool := newTestStore(t)
	scopeID := createScope(t, ctx, pool, "whale-net/slice-fr11-partial-counts-test")
	w := seedWorld(t, ctx, entities, scopeID)
	self := milestoneListingTestSubject()

	partial, err := entities.MilestoneAuthoring().CreateMilestone(ctx, scopeID, w.Product.ID, "partial", "", nil, self, self)
	require.NoError(t, err)
	require.NoError(t, entities.MilestoneAuthoring().AddDelivers(ctx, scopeID, partial.ID, w.FeatureA1.ID, self, self))
	require.NoError(t, entities.MilestoneAuthoring().AddDelivers(ctx, scopeID, partial.ID, w.RequirementA1FR.ID, self, self))
	require.NoError(t, entities.DeliveryShipments().MarkShipped(ctx, scopeID, partial.ID, w.FeatureA1.ID, nil, self, self))
	_, err = entities.MilestoneStatus().RecordTransition(ctx, scopeID, partial.ID, store.MilestoneStatusPartiallyComplete, nil, self, self)
	require.NoError(t, err)

	planned, err := entities.MilestoneAuthoring().CreateMilestone(ctx, scopeID, w.Product.ID, "planned", "", nil, self, self)
	require.NoError(t, err)
	_, err = entities.MilestoneStatus().RecordTransition(ctx, scopeID, planned.ID, store.MilestoneStatusPlanned, nil, self, self)
	require.NoError(t, err)

	q := slice.NewQuerier(entities)
	listing, err := q.ListProductDelivery(ctx, scopeID, w.Product.ID, nil)
	require.NoError(t, err)

	partialEntry := milestoneEntry(t, listing, partial.ID)
	require.NotNil(t, partialEntry.ShippedCount)
	require.NotNil(t, partialEntry.UnshippedCount)
	assert.Equal(t, 1, *partialEntry.ShippedCount)
	assert.Equal(t, 1, *partialEntry.UnshippedCount)

	shippedDoc, unshippedDoc, err := q.GetDeliveryBreakdown(ctx, partial.ID)
	require.NoError(t, err)
	assert.Equal(t, len(shippedDoc.Features)+len(shippedDoc.Requirements), *partialEntry.ShippedCount, "the inlined count must agree with GetDeliveryBreakdown's own shipped Document (FR10)")
	assert.Equal(t, len(unshippedDoc.Features)+len(unshippedDoc.Requirements), *partialEntry.UnshippedCount)

	plannedEntry := milestoneEntry(t, listing, planned.ID)
	assert.Nil(t, plannedEntry.ShippedCount, "a non-partially-complete container must carry a nil count, never a zero-value pair")
	assert.Nil(t, plannedEntry.UnshippedCount)
}

// TestListProductDelivery_MilepebblesNeverAppearAsTopLevelEntries is issue
// #2689's Testing item 5's structural half: a kind='milepebble' row never
// surfaces as a DeliveryListing.Milestones[] entry in its own right, only
// nested under its parent -- the same ListRefsByProduct kind='milestone'
// filter that will exclude a future kind='backlog' row (issue #2687) once
// that kind can be constructed. As of this schema version, migration
// 011's own CHECK constraint restricts milestone_ref.kind to exactly
// "milestone"/"milepebble" (see krill/store/milestone_status_integration_
// test.go's identical note), so a real kind='backlog' row cannot be
// constructed to assert against directly.
func TestListProductDelivery_MilepebblesNeverAppearAsTopLevelEntries(t *testing.T) {
	ctx := context.Background()
	entities, pool := newTestStore(t)
	scopeID := createScope(t, ctx, pool, "whale-net/slice-fr11-no-milepebble-toplevel-test")
	w := seedWorld(t, ctx, entities, scopeID)
	self := milestoneListingTestSubject()

	milestone, err := entities.MilestoneAuthoring().CreateMilestone(ctx, scopeID, w.Product.ID, "M1", "", nil, self, self)
	require.NoError(t, err)
	milepebble, err := entities.MilestoneAuthoring().CreateMilepebble(ctx, scopeID, milestone.ID, "mp1", "", nil, self, self)
	require.NoError(t, err)

	q := slice.NewQuerier(entities)
	listing, err := q.ListProductDelivery(ctx, scopeID, w.Product.ID, nil)
	require.NoError(t, err)

	require.Len(t, listing.Milestones, 1, "only the real milestone, never the milepebble, is a top-level entry")
	assert.Equal(t, milestone.ID, listing.Milestones[0].ID)
	for _, m := range listing.Milestones {
		assert.NotEqual(t, milepebble.ID, m.ID)
	}
}

// ── N+1 guard (issue #2689's Testing item 7 and design constraint) ─────────

// milestoneListingQueryLog is a pgx.QueryTracer recording every SQL
// statement issued through the pool it is attached to -- lets a test
// assert exactly how many queries a call issued, and which ones, without
// needing pg_stat_statements. Mirrors audience_score_system/store's own
// queryCounter precedent, extended to keep the statement text too so this
// file can single out the one status query
// (CurrentStatuses/milestone_status_event) from everything else.
type milestoneListingQueryLog struct {
	statements []string
}

func (l *milestoneListingQueryLog) TraceQueryStart(ctx context.Context, _ *pgx.Conn, data pgx.TraceQueryStartData) context.Context {
	l.statements = append(l.statements, data.SQL)
	return ctx
}

func (l *milestoneListingQueryLog) TraceQueryEnd(context.Context, *pgx.Conn, pgx.TraceQueryEndData) {}

func (l *milestoneListingQueryLog) countContaining(substr string) int {
	n := 0
	for _, s := range l.statements {
		if strings.Contains(strings.ToLower(s), strings.ToLower(substr)) {
			n++
		}
	}
	return n
}

// tracedMilestoneListingStore builds a second *store.Store against the
// same database as pool's connection string, but through a pool whose
// every query is recorded by log.
func tracedMilestoneListingStore(t *testing.T, ctx context.Context, connString string, log *milestoneListingQueryLog) *store.Store {
	t.Helper()

	cfg, err := pgxpool.ParseConfig(connString)
	require.NoError(t, err)
	cfg.ConnConfig.Tracer = log

	tracedPool, err := pgxpool.NewWithConfig(ctx, cfg)
	require.NoError(t, err)
	t.Cleanup(tracedPool.Close)

	return store.New(tracedPool)
}

// TestListProductDelivery_NPlusOneGuard is issue #2689's Testing item 7
// and its own design constraint ("Listing N milestones must not be N+1
// status queries; assert this in a test"): listing 20 milestones with 60
// milepebbles issues exactly one query against milestone_status_event
// (CurrentStatuses' own batched shape), never one per container, and the
// call's total query count stays far below what an O(milestones *
// milepebbles) -- or an N+1-per-container -- implementation would issue.
func TestListProductDelivery_NPlusOneGuard(t *testing.T) {
	ctx := context.Background()
	entities, pool := newTestStore(t)
	dbConnString := pool.Config().ConnString()
	scopeID := createScope(t, ctx, pool, "whale-net/slice-fr11-n-plus-one-test")
	w := seedWorld(t, ctx, entities, scopeID)
	self := milestoneListingTestSubject()

	const numMilestones = 20
	const milepebblesPerMilestone = 3 // 20 * 3 = 60, matching the issue's own example.

	for i := 0; i < numMilestones; i++ {
		m, err := entities.MilestoneAuthoring().CreateMilestone(ctx, scopeID, w.Product.ID, fmt.Sprintf("M%d", i), "", nil, self, self)
		require.NoError(t, err)
		_, err = entities.MilestoneStatus().RecordTransition(ctx, scopeID, m.ID, store.MilestoneStatusPlanned, nil, self, self)
		require.NoError(t, err)

		for j := 0; j < milepebblesPerMilestone; j++ {
			mp, err := entities.MilestoneAuthoring().CreateMilepebble(ctx, scopeID, m.ID, fmt.Sprintf("M%d-mp%d", i, j), "", nil, self, self)
			require.NoError(t, err)
			_, err = entities.MilestoneStatus().RecordTransition(ctx, scopeID, mp.ID, store.MilestoneStatusPlanned, nil, self, self)
			require.NoError(t, err)
		}
	}

	log := &milestoneListingQueryLog{}
	tracedEntities := tracedMilestoneListingStore(t, ctx, dbConnString, log)
	q := slice.NewQuerier(tracedEntities)

	listing, err := q.ListProductDelivery(ctx, scopeID, w.Product.ID, nil)
	require.NoError(t, err)
	require.Len(t, listing.Milestones, numMilestones)

	statusQueries := log.countContaining("milestone_status_event")
	assert.Equal(t, 1, statusQueries, "CurrentStatuses must resolve every milestone's and milepebble's status in one query, never one per container (%d containers here)", numMilestones+numMilestones*milepebblesPerMilestone)

	// A cost linear in (milestones + milepebbles) is expected -- each
	// container costs a small, fixed number of queries (its own
	// associations/deferrals/entity-set-slice calls), never a number that
	// grows with how many *other* containers exist. generousLinearBound is
	// well above the actual linear cost (observed ~1.5 queries/container)
	// but far below what a per-container full-rescan bug would produce.
	totalContainers := numMilestones + numMilestones*milepebblesPerMilestone
	generousLinearBound := 10 * totalContainers
	assert.Less(t, len(log.statements), generousLinearBound,
		"total query count (%d) must stay within a generous linear-in-containers bound (%d, for %d containers)",
		len(log.statements), generousLinearBound, totalContainers)
}
