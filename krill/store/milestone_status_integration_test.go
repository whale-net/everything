//go:build integration

// Real-Postgres coverage for MilestoneStatusEventStore
// (milestone_status.go, migration 012, issue #2685's Testing section,
// FR8/FR9/FR12): a milestone or milepebble with no transitions reports
// "not started" with zero rows (item 1), every one of the seven statuses
// is DB-CHECK-accepted while an eighth is rejected by the database itself
// (item 2), the same reads/writes work against a kind='milepebble' row and
// an unknown id is rejected loudly (item 3, FR9), a
// planned -> in progress -> shipped sequence returns three chronological
// history entries with CurrentStatus deriving "shipped" (item 4, FR12), a
// later RecordTransition never mutates an earlier row (item 5, NFR2's
// behavioral half -- see milestone_status_nfr2_test.go for its structural
// half), re-affirming the same status still appends a new row (item 6),
// every event's subject pair is populated (item 7, NFR4's store-level
// half -- see milestone_status_test.go in krill/api/handlers for the
// 401-and-writes-nothing half), and CurrentStatuses' one-query batch form
// matches 50 individual CurrentStatus calls (item 8). See
// milestone_authoring_integration_test.go's package doc for why this file
// only builds under the "integration" build tag, and for the shared
// newMilestoneAuthoringTestStore/newMilestoneAuthoringTestScope/
// milestoneAuthoringTestSubject helpers this file reuses (same package,
// same go_test target).
//
// Run it explicitly (requires a working Docker daemon):
//
//	bazel test //krill/store:milestone_status_integration_test --test_output=all
package store_test

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/whale-net/everything/krill/store"
)

// milestoneStatusStepDelay separates sequential RecordTransition calls in
// TestMilestoneStatusStore_RecordTransition_ChronologicalHistory_
// CurrentStatusDerivesLatest so each event lands with its own distinct
// CreatedAt -- mirrors history_integration_test.go's own
// historyStepDelay.
const milestoneStatusStepDelay = 20 * time.Millisecond

// TestMilestoneStatusStore_NoTransitions_ReportsNotStartedWithZeroRows is
// issue #2685's Testing item 1 (FR8): a freshly created milestone has zero
// milestone_status_event rows, and CurrentStatus derives "not started"
// from that absence -- never a seeded row.
func TestMilestoneStatusStore_NoTransitions_ReportsNotStartedWithZeroRows(t *testing.T) {
	ctx := context.Background()
	s, db := newMilestoneAuthoringTestStore(t)
	scopeID := newMilestoneAuthoringTestScope(t, ctx, db)
	product, err := s.Products().Create(ctx, scopeID, "Krill", "spec-of-record")
	require.NoError(t, err)

	self := milestoneAuthoringTestSubject("agent-1")
	milestone, err := s.MilestoneAuthoring().CreateMilestone(ctx, scopeID, product.ID, "M1", "", nil, self, self)
	require.NoError(t, err)

	status, err := s.MilestoneStatus().CurrentStatus(ctx, milestone.ID)
	require.NoError(t, err)
	assert.Equal(t, store.MilestoneStatusNotStarted, status)

	var count int
	require.NoError(t, db.Pool.QueryRow(ctx, `SELECT count(*) FROM milestone_status_event WHERE milestone_id = $1`, milestone.ID).Scan(&count))
	assert.Equal(t, 0, count, "\"not started\" must never be a seeded row -- it is derived from the absence of any milestone_status_event row")

	transitions, err := s.MilestoneStatus().ListTransitions(ctx, milestone.ID)
	require.NoError(t, err)
	assert.Empty(t, transitions)
}

// TestMilestoneStatusStore_AllSevenStatuses_EighthRejectedByDBCheck is
// issue #2685's Testing item 2 (FR8): every one of the seven fixed status
// values is accepted by RecordTransition, and an eighth, made-up value is
// rejected by the database's own CHECK constraint (migration 012) --
// RecordTransition itself performs no Go-side status validation (that is
// krill/api/handlers.ValidMilestoneStatuses' job), so a rejection here can
// only be the DB CHECK firing.
func TestMilestoneStatusStore_AllSevenStatuses_EighthRejectedByDBCheck(t *testing.T) {
	ctx := context.Background()
	s, db := newMilestoneAuthoringTestStore(t)
	scopeID := newMilestoneAuthoringTestScope(t, ctx, db)
	product, err := s.Products().Create(ctx, scopeID, "Krill", "spec-of-record")
	require.NoError(t, err)

	self := milestoneAuthoringTestSubject("agent-1")
	milestone, err := s.MilestoneAuthoring().CreateMilestone(ctx, scopeID, product.ID, "M1", "", nil, self, self)
	require.NoError(t, err)

	sevenStatuses := []store.MilestoneStatus{
		store.MilestoneStatusNotStarted,
		store.MilestoneStatusInDesign,
		store.MilestoneStatusPlanned,
		store.MilestoneStatusInProgress,
		store.MilestoneStatusShipped,
		store.MilestoneStatusPartiallyComplete,
		store.MilestoneStatusAbandoned,
	}
	for _, status := range sevenStatuses {
		_, err := s.MilestoneStatus().RecordTransition(ctx, scopeID, milestone.ID, status, nil, self, self)
		assert.NoError(t, err, "status %q must be accepted -- it is one of FR8's fixed seven values", status)
	}

	var count int
	require.NoError(t, db.Pool.QueryRow(ctx, `SELECT count(*) FROM milestone_status_event WHERE milestone_id = $1`, milestone.ID).Scan(&count))
	assert.Equal(t, len(sevenStatuses), count)

	_, err = s.MilestoneStatus().RecordTransition(ctx, scopeID, milestone.ID, store.MilestoneStatus("bogus-status"), nil, self, self)
	assert.Error(t, err, "an eighth, made-up status value must be rejected by the DB CHECK constraint, not silently accepted")

	require.NoError(t, db.Pool.QueryRow(ctx, `SELECT count(*) FROM milestone_status_event WHERE milestone_id = $1`, milestone.ID).Scan(&count))
	assert.Equal(t, len(sevenStatuses), count, "the rejected eighth status must not have inserted a row")
}

// TestMilestoneStatusStore_Milepebble_SupportsSameOperations_
// UnknownIDRejected is issue #2685's Testing item 3 (FR9): the exact same
// RecordTransition/CurrentStatus/ListTransitions operations work
// unmodified against a kind='milepebble' milestone_ref row, and an id that
// names no real milestone_ref row at all -- of either kind -- is rejected
// loudly rather than silently accepted. (A kind='backlog' row cannot be
// constructed at all: migration 011's own CHECK constraint restricts
// milestone_ref.kind to exactly "milestone"/"milepebble" -- see
// RecordTransition's own doc comment for why a plain existence check
// under scopeID is therefore sufficient.)
func TestMilestoneStatusStore_Milepebble_SupportsSameOperations_UnknownIDRejected(t *testing.T) {
	ctx := context.Background()
	s, db := newMilestoneAuthoringTestStore(t)
	scopeID := newMilestoneAuthoringTestScope(t, ctx, db)
	product, err := s.Products().Create(ctx, scopeID, "Krill", "spec-of-record")
	require.NoError(t, err)

	self := milestoneAuthoringTestSubject("agent-1")
	milestone, err := s.MilestoneAuthoring().CreateMilestone(ctx, scopeID, product.ID, "M1", "", nil, self, self)
	require.NoError(t, err)
	milepebble, err := s.MilestoneAuthoring().CreateMilepebble(ctx, scopeID, milestone.ID, "cut 1", "", nil, self, self)
	require.NoError(t, err)

	status, err := s.MilestoneStatus().CurrentStatus(ctx, milepebble.ID)
	require.NoError(t, err)
	assert.Equal(t, store.MilestoneStatusNotStarted, status)

	_, err = s.MilestoneStatus().RecordTransition(ctx, scopeID, milepebble.ID, store.MilestoneStatusInProgress, nil, self, self)
	require.NoError(t, err, "RecordTransition must accept a kind='milepebble' target, same as a kind='milestone' one (FR9)")

	status, err = s.MilestoneStatus().CurrentStatus(ctx, milepebble.ID)
	require.NoError(t, err)
	assert.Equal(t, store.MilestoneStatusInProgress, status)

	transitions, err := s.MilestoneStatus().ListTransitions(ctx, milepebble.ID)
	require.NoError(t, err)
	require.Len(t, transitions, 1)
	assert.Equal(t, store.MilestoneStatusInProgress, transitions[0].Status)

	// The parent milestone's own status is unaffected by its milepebble's.
	milestoneStatus, err := s.MilestoneStatus().CurrentStatus(ctx, milestone.ID)
	require.NoError(t, err)
	assert.Equal(t, store.MilestoneStatusNotStarted, milestoneStatus)

	// An id naming no real milestone_ref row at all is rejected loudly.
	_, err = s.MilestoneStatus().RecordTransition(ctx, scopeID, uuid.New(), store.MilestoneStatusPlanned, nil, self, self)
	assert.ErrorIs(t, err, store.ErrNotFound)

	var count int
	require.NoError(t, db.Pool.QueryRow(ctx, `SELECT count(*) FROM milestone_status_event`).Scan(&count))
	assert.Equal(t, 1, count, "the rejected RecordTransition against an unknown id must insert no row")
}

// TestMilestoneStatusStore_RecordTransition_ChronologicalHistory_
// CurrentStatusDerivesLatest is issue #2685's Testing item 4 (FR12): a
// planned -> in progress -> shipped sequence returns three history
// entries in chronological order, each with its own distinct timestamp
// and its own recorded subject pair, and CurrentStatus derives "shipped"
// -- the latest of the three.
func TestMilestoneStatusStore_RecordTransition_ChronologicalHistory_CurrentStatusDerivesLatest(t *testing.T) {
	ctx := context.Background()
	s, db := newMilestoneAuthoringTestStore(t)
	scopeID := newMilestoneAuthoringTestScope(t, ctx, db)
	product, err := s.Products().Create(ctx, scopeID, "Krill", "spec-of-record")
	require.NoError(t, err)

	self := milestoneAuthoringTestSubject("agent-1")
	milestone, err := s.MilestoneAuthoring().CreateMilestone(ctx, scopeID, product.ID, "M1", "", nil, self, self)
	require.NoError(t, err)

	sequence := []store.MilestoneStatus{store.MilestoneStatusPlanned, store.MilestoneStatusInProgress, store.MilestoneStatusShipped}
	var events []store.MilestoneStatusEvent
	for i, status := range sequence {
		if i > 0 {
			time.Sleep(milestoneStatusStepDelay)
		}
		event, err := s.MilestoneStatus().RecordTransition(ctx, scopeID, milestone.ID, status, nil, self, self)
		require.NoError(t, err)
		events = append(events, event)
	}

	status, err := s.MilestoneStatus().CurrentStatus(ctx, milestone.ID)
	require.NoError(t, err)
	assert.Equal(t, store.MilestoneStatusShipped, status)

	transitions, err := s.MilestoneStatus().ListTransitions(ctx, milestone.ID)
	require.NoError(t, err)
	require.Len(t, transitions, 3)
	for i, want := range sequence {
		assert.Equal(t, want, transitions[i].Status, "ListTransitions must return chronological (oldest-first) order (FR12)")
		assert.Equal(t, events[i].ID, transitions[i].ID)
		assert.Equal(t, self, transitions[i].CreatedByActing)
		assert.Equal(t, self, transitions[i].CreatedByOnBehalfOf)
	}
	assert.True(t, transitions[0].CreatedAt.Before(transitions[1].CreatedAt), "each transition must carry its own distinct, increasing timestamp")
	assert.True(t, transitions[1].CreatedAt.Before(transitions[2].CreatedAt), "each transition must carry its own distinct, increasing timestamp")
}

// TestMilestoneStatusStore_RecordTransition_NeverMutatesPriorRow is issue
// #2685's Testing item 5's behavioral half (NFR2): recording a second
// transition never updates the first row -- its id and created_at are
// byte-identical before and after. See milestone_status_nfr2_test.go for
// this item's structural half (no update/delete method exists at all).
func TestMilestoneStatusStore_RecordTransition_NeverMutatesPriorRow(t *testing.T) {
	ctx := context.Background()
	s, db := newMilestoneAuthoringTestStore(t)
	scopeID := newMilestoneAuthoringTestScope(t, ctx, db)
	product, err := s.Products().Create(ctx, scopeID, "Krill", "spec-of-record")
	require.NoError(t, err)

	self := milestoneAuthoringTestSubject("agent-1")
	milestone, err := s.MilestoneAuthoring().CreateMilestone(ctx, scopeID, product.ID, "M1", "", nil, self, self)
	require.NoError(t, err)

	first, err := s.MilestoneStatus().RecordTransition(ctx, scopeID, milestone.ID, store.MilestoneStatusPlanned, nil, self, self)
	require.NoError(t, err)

	time.Sleep(milestoneStatusStepDelay)
	_, err = s.MilestoneStatus().RecordTransition(ctx, scopeID, milestone.ID, store.MilestoneStatusInProgress, nil, self, self)
	require.NoError(t, err)

	transitions, err := s.MilestoneStatus().ListTransitions(ctx, milestone.ID)
	require.NoError(t, err)
	require.Len(t, transitions, 2)

	assert.Equal(t, first.ID, transitions[0].ID, "the earlier row's id must be byte-identical after a later write")
	assert.True(t, first.CreatedAt.Equal(transitions[0].CreatedAt), "the earlier row's created_at must be byte-identical after a later write -- got %v, want %v", transitions[0].CreatedAt, first.CreatedAt)
	assert.Equal(t, store.MilestoneStatusPlanned, transitions[0].Status, "the earlier row's own status must be unchanged")
}

// TestMilestoneStatusStore_ReaffirmingSameStatus_AppendsRow_
// CurrentStatusUnchanged is issue #2685's Testing item 6: recording the
// same status twice in a row still appends a second row -- a
// re-affirmation is history, not a no-op -- while CurrentStatus's answer
// is unchanged.
func TestMilestoneStatusStore_ReaffirmingSameStatus_AppendsRow_CurrentStatusUnchanged(t *testing.T) {
	ctx := context.Background()
	s, db := newMilestoneAuthoringTestStore(t)
	scopeID := newMilestoneAuthoringTestScope(t, ctx, db)
	product, err := s.Products().Create(ctx, scopeID, "Krill", "spec-of-record")
	require.NoError(t, err)

	self := milestoneAuthoringTestSubject("agent-1")
	milestone, err := s.MilestoneAuthoring().CreateMilestone(ctx, scopeID, product.ID, "M1", "", nil, self, self)
	require.NoError(t, err)

	first, err := s.MilestoneStatus().RecordTransition(ctx, scopeID, milestone.ID, store.MilestoneStatusPlanned, nil, self, self)
	require.NoError(t, err)
	time.Sleep(milestoneStatusStepDelay)
	second, err := s.MilestoneStatus().RecordTransition(ctx, scopeID, milestone.ID, store.MilestoneStatusPlanned, nil, self, self)
	require.NoError(t, err)

	assert.NotEqual(t, first.ID, second.ID, "a re-affirmation of the same status must still append a new row with its own id")

	transitions, err := s.MilestoneStatus().ListTransitions(ctx, milestone.ID)
	require.NoError(t, err)
	require.Len(t, transitions, 2, "a re-affirmation is history, not a no-op -- both rows must exist")
	assert.Equal(t, store.MilestoneStatusPlanned, transitions[0].Status)
	assert.Equal(t, store.MilestoneStatusPlanned, transitions[1].Status)

	status, err := s.MilestoneStatus().CurrentStatus(ctx, milestone.ID)
	require.NoError(t, err)
	assert.Equal(t, store.MilestoneStatusPlanned, status, "CurrentStatus is unchanged by the re-affirmation -- it was already \"planned\"")
}

// TestMilestoneStatusStore_RecordTransition_SubjectPairAlwaysPopulated is
// issue #2685's Testing item 7's store-level half (NFR4): every recorded
// event carries both subject slots, and acting/on-behalf-of differing is
// stored distinctly rather than collapsed to one value. See
// milestone_status_test.go in krill/api/handlers for this item's other
// half (a handler call without a session is 401 and writes nothing).
func TestMilestoneStatusStore_RecordTransition_SubjectPairAlwaysPopulated(t *testing.T) {
	ctx := context.Background()
	s, db := newMilestoneAuthoringTestStore(t)
	scopeID := newMilestoneAuthoringTestScope(t, ctx, db)
	product, err := s.Products().Create(ctx, scopeID, "Krill", "spec-of-record")
	require.NoError(t, err)

	acting := milestoneAuthoringTestSubject("agent-1")
	onBehalfOf := milestoneAuthoringTestSubject("human-1")
	milestone, err := s.MilestoneAuthoring().CreateMilestone(ctx, scopeID, product.ID, "M1", "", nil, acting, onBehalfOf)
	require.NoError(t, err)

	event, err := s.MilestoneStatus().RecordTransition(ctx, scopeID, milestone.ID, store.MilestoneStatusPlanned, nil, acting, onBehalfOf)
	require.NoError(t, err)

	assert.Equal(t, acting, event.CreatedByActing)
	assert.Equal(t, onBehalfOf, event.CreatedByOnBehalfOf)
	assert.NotEqual(t, event.CreatedByActing, event.CreatedByOnBehalfOf, "acting and on-behalf-of must be stored distinctly, not collapsed")

	transitions, err := s.MilestoneStatus().ListTransitions(ctx, milestone.ID)
	require.NoError(t, err)
	require.Len(t, transitions, 1)
	assert.Equal(t, acting, transitions[0].CreatedByActing)
	assert.Equal(t, onBehalfOf, transitions[0].CreatedByOnBehalfOf)
}

// TestMilestoneStatusStore_CurrentStatuses_BatchOverFifty_
// MatchesIndividualCalls is issue #2685's Testing item 8: CurrentStatuses'
// one-query batch form, over 50 milestones (a mix of some with a
// transition and some left untouched), matches 50 individual CurrentStatus
// calls exactly.
func TestMilestoneStatusStore_CurrentStatuses_BatchOverFifty_MatchesIndividualCalls(t *testing.T) {
	ctx := context.Background()
	s, db := newMilestoneAuthoringTestStore(t)
	scopeID := newMilestoneAuthoringTestScope(t, ctx, db)
	product, err := s.Products().Create(ctx, scopeID, "Krill", "spec-of-record")
	require.NoError(t, err)

	self := milestoneAuthoringTestSubject("agent-1")

	const total = 50
	ids := make([]uuid.UUID, 0, total)
	for i := 0; i < total; i++ {
		milestone, err := s.MilestoneAuthoring().CreateMilestone(ctx, scopeID, product.ID, uuid.NewString(), "", nil, self, self)
		require.NoError(t, err)
		ids = append(ids, milestone.ID)

		// Every third milestone gets a real transition; the rest are left
		// untouched to prove the NotStarted-by-default half of the batch
		// derivation too.
		if i%3 == 0 {
			_, err := s.MilestoneStatus().RecordTransition(ctx, scopeID, milestone.ID, store.MilestoneStatusInProgress, nil, self, self)
			require.NoError(t, err)
		}
	}

	batch, err := s.MilestoneStatus().CurrentStatuses(ctx, ids)
	require.NoError(t, err)
	require.Len(t, batch, total)

	for i, id := range ids {
		individual, err := s.MilestoneStatus().CurrentStatus(ctx, id)
		require.NoError(t, err)
		assert.Equal(t, individual, batch[id], "CurrentStatuses' batch answer for milestone %d must match CurrentStatus's individual answer", i)
		if i%3 == 0 {
			assert.Equal(t, store.MilestoneStatusInProgress, individual)
		} else {
			assert.Equal(t, store.MilestoneStatusNotStarted, individual)
		}
	}
}
