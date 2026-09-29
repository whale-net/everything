//go:build integration

// Real-Postgres coverage for RevisionEventStore (migration 008, issue
// #2542, FR2-FR4/NFR1) -- the append-only round log a DesignSession
// accumulates. Shares design_session_integration_test.go's fixture
// helpers (same go_test target -- see that file's package doc for why).
//
// Run it explicitly (requires a working Docker daemon):
//
//	bazel test //krill/store:design_session_integration_test --test_output=all
package store_test

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/whale-net/everything/krill/store"
)

// reTestFixture bundles what every RevisionEventStore test needs: a store,
// its underlying scope, and a real design_session id opened against a real
// product and a real krill_session -- mirroring design_session_integration_
// test.go's own Open test rather than fabricating a session_id.
type reTestFixture struct {
	store           *store.Store
	scopeID         uuid.UUID
	designSessionID uuid.UUID
}

func newRevisionEventFixture(t *testing.T, repoFullName string) reTestFixture {
	t.Helper()
	ctx := context.Background()

	s, db := newDesignSessionTestStore(t)
	scopeID := newDesignSessionTestScope(t, ctx, db, repoFullName)
	product, err := s.Products().Create(ctx, scopeID, "Krill", "spec-of-record substrate")
	require.NoError(t, err)
	krillSessionID := mintKrillSession(t, ctx, db, scopeID, dsTestSubject("agent-1"))
	ds, err := s.DesignSessions().Open(ctx, scopeID, product.ID, "an opening idea", krillSessionID)
	require.NoError(t, err)

	return reTestFixture{store: s, scopeID: scopeID, designSessionID: ds.ID}
}

// baseRevisionEvent returns a NewRevisionEvent with every FR3/FR4
// conditional field left at its "not applicable" zero value (event_type
// ruling requires neither VerifiedAgainst nor SignoffStatus) -- tests
// override only the fields their case cares about.
func baseRevisionEvent(f reTestFixture, acting, onBehalfOf store.Subject) store.NewRevisionEvent {
	return store.NewRevisionEvent{
		ScopeID:            f.scopeID,
		SessionID:          f.designSessionID,
		Acting:             acting,
		OnBehalfOf:         onBehalfOf,
		EventType:          store.EventTypeRuling,
		OpenQuestionsDelta: store.OpenQuestionsDelta{Opened: []store.OpenQuestionOpened{}, Resolved: []string{}},
	}
}

func strPtr(s string) *string { return &s }

// TestRevisionEventStore_Append_SeqNoMonotonicPerSession is this issue's
// Testing case 3: Append on a fresh session yields seq_no = 1; three
// sequential appends yield 1, 2, 3.
func TestRevisionEventStore_Append_SeqNoMonotonicPerSession(t *testing.T) {
	ctx := context.Background()
	f := newRevisionEventFixture(t, "whale-net/revision-event-seqno-test")
	self := dsTestSubject("agent-1")

	var seqNos []int
	for i := 0; i < 3; i++ {
		ev, err := f.store.RevisionEvents().Append(ctx, baseRevisionEvent(f, self, self))
		require.NoError(t, err)
		seqNos = append(seqNos, ev.SeqNo)
	}
	assert.Equal(t, []int{1, 2, 3}, seqNos)
}

// TestRevisionEventStore_Append_ConcurrentAppends_YieldDistinctSeqNo is
// this issue's Testing case 4: concurrent appends to the same session (two
// goroutines) both succeed with distinct seq_no -- no unique-violation
// escape, no duplicate.
func TestRevisionEventStore_Append_ConcurrentAppends_YieldDistinctSeqNo(t *testing.T) {
	ctx := context.Background()
	f := newRevisionEventFixture(t, "whale-net/revision-event-concurrency-test")
	self := dsTestSubject("agent-1")

	const n = 2
	seqNos := make([]int, n)
	errs := make([]error, n)
	var wg sync.WaitGroup
	wg.Add(n)
	for i := 0; i < n; i++ {
		i := i
		go func() {
			defer wg.Done()
			ev, err := f.store.RevisionEvents().Append(ctx, baseRevisionEvent(f, self, self))
			seqNos[i] = ev.SeqNo
			errs[i] = err
		}()
	}
	wg.Wait()

	for _, err := range errs {
		require.NoError(t, err, "a concurrent Append must never fail with a unique-violation escape")
	}
	assert.ElementsMatch(t, []int{1, 2}, seqNos, "the two concurrent appends must land on distinct, gapless seq_no values")
}

// TestRevisionEventStore_Append_DraftRequiresVerifiedAgainst is this
// issue's Testing case 5: Append with event_type='draft' and empty
// verified_against fails (FR3); with a SHA, succeeds.
func TestRevisionEventStore_Append_DraftRequiresVerifiedAgainst(t *testing.T) {
	ctx := context.Background()
	f := newRevisionEventFixture(t, "whale-net/revision-event-draft-verified-test")
	self := dsTestSubject("agent-1")

	e := baseRevisionEvent(f, self, self)
	e.EventType = store.EventTypeDraft
	_, err := f.store.RevisionEvents().Append(ctx, e)
	assert.ErrorIs(t, err, store.ErrInvalidRevisionEvent, "a draft round with no verified_against must be rejected")

	e.VerifiedAgainst = strPtr("abc123def")
	ev, err := f.store.RevisionEvents().Append(ctx, e)
	require.NoError(t, err, "a draft round with verified_against set must succeed")
	assert.Equal(t, "abc123def", *ev.VerifiedAgainst)
}

// TestRevisionEventStore_Append_AnswerRejectsVerifiedAgainst is this
// issue's Testing case 6: Append with event_type='answer' and a non-empty
// verified_against fails (FR3's "otherwise NULL" half).
func TestRevisionEventStore_Append_AnswerRejectsVerifiedAgainst(t *testing.T) {
	ctx := context.Background()
	f := newRevisionEventFixture(t, "whale-net/revision-event-answer-verified-test")
	self := dsTestSubject("agent-1")

	e := baseRevisionEvent(f, self, self)
	e.EventType = store.EventTypeAnswer
	e.VerifiedAgainst = strPtr("should-not-be-allowed")
	_, err := f.store.RevisionEvents().Append(ctx, e)
	assert.ErrorIs(t, err, store.ErrInvalidRevisionEvent, "an answer round with verified_against set must be rejected")

	e.VerifiedAgainst = nil
	_, err = f.store.RevisionEvents().Append(ctx, e)
	require.NoError(t, err, "an answer round with verified_against left nil must succeed")
}

// TestRevisionEventStore_Append_SignoffRequiresSignoffStatus is this
// issue's Testing case 7: Append with event_type='signoff' and no
// signoff_status fails; with 'approved' and with 'changes_requested',
// succeeds; with any other string, fails (FR4).
func TestRevisionEventStore_Append_SignoffRequiresSignoffStatus(t *testing.T) {
	ctx := context.Background()
	f := newRevisionEventFixture(t, "whale-net/revision-event-signoff-test")
	self := dsTestSubject("agent-1")

	e := baseRevisionEvent(f, self, self)
	e.EventType = store.EventTypeSignoff
	_, err := f.store.RevisionEvents().Append(ctx, e)
	assert.ErrorIs(t, err, store.ErrInvalidRevisionEvent, "a signoff round with no signoff_status must be rejected")

	approved := store.SignoffStatusApproved
	e.SignoffStatus = &approved
	ev, err := f.store.RevisionEvents().Append(ctx, e)
	require.NoError(t, err, "a signoff round with 'approved' must succeed")
	require.NotNil(t, ev.SignoffStatus)
	assert.Equal(t, store.SignoffStatusApproved, *ev.SignoffStatus)

	changesRequested := store.SignoffStatusChangesRequested
	e.SignoffStatus = &changesRequested
	ev, err = f.store.RevisionEvents().Append(ctx, e)
	require.NoError(t, err, "a signoff round with 'changes_requested' must succeed")
	require.NotNil(t, ev.SignoffStatus)
	assert.Equal(t, store.SignoffStatusChangesRequested, *ev.SignoffStatus)

	bogus := store.SignoffStatus("bogus")
	e.SignoffStatus = &bogus
	_, err = f.store.RevisionEvents().Append(ctx, e)
	assert.ErrorIs(t, err, store.ErrInvalidRevisionEvent, "a signoff round with an unrecognized status string must be rejected")
}

// TestRevisionEventStore_Append_EntityDeltaChangeDeleted_Rejected is this
// issue's Testing case 8: Append with entity_deltas containing
// change:"deleted" fails -- the enum is created|updated only.
func TestRevisionEventStore_Append_EntityDeltaChangeDeleted_Rejected(t *testing.T) {
	ctx := context.Background()
	f := newRevisionEventFixture(t, "whale-net/revision-event-delta-deleted-test")
	self := dsTestSubject("agent-1")

	e := baseRevisionEvent(f, self, self)
	e.EntityDeltas = []store.EntityDelta{{
		EntityID:    uuid.New(),
		Change:      store.EntityDeltaChange("deleted"),
		SummaryLine: "retired an entity",
	}}
	_, err := f.store.RevisionEvents().Append(ctx, e)
	assert.ErrorIs(t, err, store.ErrInvalidRevisionEvent)

	list, err := f.store.RevisionEvents().ListBySession(ctx, f.designSessionID)
	require.NoError(t, err)
	assert.Empty(t, list, "a rejected Append must insert no row")
}

// TestRevisionEventStore_Append_RoundTripsJSONAndListsInSeqOrder is this
// issue's Testing case 9: Append round-trips entity_deltas and
// open_questions_delta JSON without loss; ListBySession returns them in
// seq_no order.
func TestRevisionEventStore_Append_RoundTripsJSONAndListsInSeqOrder(t *testing.T) {
	ctx := context.Background()
	f := newRevisionEventFixture(t, "whale-net/revision-event-jsonroundtrip-test")
	self := dsTestSubject("agent-1")

	first := baseRevisionEvent(f, self, self)
	first.EntityDeltas = []store.EntityDelta{
		{EntityID: uuid.New(), Change: store.EntityDeltaChangeCreated, SummaryLine: "created FR7"},
	}
	first.OpenQuestionsDelta = store.OpenQuestionsDelta{
		Opened: []store.OpenQuestionOpened{
			{QuestionID: "q1", Blocking: true, Text: "what storage backend?"},
			{QuestionID: "q2", Blocking: false, Text: "naming bikeshed"},
		},
		Resolved: []string{},
	}
	ev1, err := f.store.RevisionEvents().Append(ctx, first)
	require.NoError(t, err)

	second := baseRevisionEvent(f, self, self)
	second.EntityDeltas = []store.EntityDelta{
		{EntityID: uuid.New(), Change: store.EntityDeltaChangeUpdated, SummaryLine: "reworded LB3"},
	}
	second.OpenQuestionsDelta = store.OpenQuestionsDelta{Opened: []store.OpenQuestionOpened{}, Resolved: []string{"q1"}}
	ev2, err := f.store.RevisionEvents().Append(ctx, second)
	require.NoError(t, err)

	assert.Equal(t, first.EntityDeltas, ev1.EntityDeltas)
	assert.Equal(t, first.OpenQuestionsDelta, ev1.OpenQuestionsDelta)
	assert.Equal(t, second.EntityDeltas, ev2.EntityDeltas)
	assert.Equal(t, second.OpenQuestionsDelta, ev2.OpenQuestionsDelta)

	list, err := f.store.RevisionEvents().ListBySession(ctx, f.designSessionID)
	require.NoError(t, err)
	require.Len(t, list, 2)
	assert.Equal(t, ev1.ID, list[0].ID, "ListBySession must return seq_no 1 first")
	assert.Equal(t, ev2.ID, list[1].ID, "ListBySession must return seq_no 2 second")
	assert.Equal(t, first.EntityDeltas, list[0].EntityDeltas)
	assert.Equal(t, second.OpenQuestionsDelta, list[1].OpenQuestionsDelta)
}

// TestRevisionEventStore_Append_IdentityTriples_RoundTripDistinctAndIdentical
// is this issue's Testing case 10: both identity triples round-trip
// distinctly when acting != on-behalf-of, and identically when they are
// the same (FR2).
func TestRevisionEventStore_Append_IdentityTriples_RoundTripDistinctAndIdentical(t *testing.T) {
	ctx := context.Background()
	f := newRevisionEventFixture(t, "whale-net/revision-event-triples-test")

	self := dsTestSubject("agent-1")
	selfEvent, err := f.store.RevisionEvents().Append(ctx, baseRevisionEvent(f, self, self))
	require.NoError(t, err)
	assert.Equal(t, self, selfEvent.Acting)
	assert.Equal(t, self, selfEvent.OnBehalfOf, "a self-acting event must round-trip identical triples")

	acting := dsTestSubject("agent-caller")
	onBehalfOf := dsTestSubject("agent-owner")
	distinctEvent, err := f.store.RevisionEvents().Append(ctx, baseRevisionEvent(f, acting, onBehalfOf))
	require.NoError(t, err)
	assert.Equal(t, acting, distinctEvent.Acting)
	assert.Equal(t, onBehalfOf, distinctEvent.OnBehalfOf)
	assert.NotEqual(t, distinctEvent.Acting, distinctEvent.OnBehalfOf, "acting and on-behalf-of must round-trip distinctly when they differ")
}

// TestRevisionEventStore_MethodSet_HasNoUpdateOrDeleteVerb is this issue's
// Testing case 11 (NFR1 structural test): asserts by interface shape that
// no Update/Delete/Amend verb is reachable through RevisionEventStore --
// cheap on purpose, so a future contributor adding a mutation path trips
// this test rather than a slow behavioral one.
func TestRevisionEventStore_MethodSet_HasNoUpdateOrDeleteVerb(t *testing.T) {
	typ := reflect.TypeOf((*store.RevisionEventStore)(nil)).Elem()

	disallowed := []string{"update", "delete", "amend", "remove", "mutate", "patch"}
	for i := 0; i < typ.NumMethod(); i++ {
		name := strings.ToLower(typ.Method(i).Name)
		for _, verb := range disallowed {
			assert.NotContains(t, name, verb, "RevisionEventStore method %q must not expose a mutation verb (NFR1)", typ.Method(i).Name)
		}
	}
	assert.GreaterOrEqual(t, typ.NumMethod(), 1, "sanity: the interface must still expose at least Append/ListBySession")
}

// TestRevisionEventStore_ListLatestSignoffBySessionIDs_LastEventWins proves
// the batched signoff derivation the design-session list view relies on
// (krill/ui/design_page.go's listDesignSessions) matches what a per-session
// fold over ListBySession would compute, across two sessions of the same
// product in one call: a session with no signoff event is absent from the
// returned map, and a session with more than one signoff event reports its
// most recent one, not its first.
func TestRevisionEventStore_ListLatestSignoffBySessionIDs_LastEventWins(t *testing.T) {
	ctx := context.Background()
	s, db := newDesignSessionTestStore(t)
	scopeID := newDesignSessionTestScope(t, ctx, db, "whale-net/revision-event-latest-signoff-test")
	product, err := s.Products().Create(ctx, scopeID, "Krill", "spec-of-record substrate")
	require.NoError(t, err)
	self := dsTestSubject("agent-1")
	krillSessionID := mintKrillSession(t, ctx, db, scopeID, self)

	signedOff, err := s.DesignSessions().Open(ctx, scopeID, product.ID, "an opening idea", krillSessionID)
	require.NoError(t, err)
	neverSigned, err := s.DesignSessions().Open(ctx, scopeID, product.ID, "another opening idea", krillSessionID)
	require.NoError(t, err)

	// Approved-then-reopened session: two signoff events, the later one wins.
	approved := store.SignoffStatusApproved
	changesRequested := store.SignoffStatusChangesRequested
	e := store.NewRevisionEvent{
		ScopeID: scopeID, SessionID: signedOff.ID, Acting: self, OnBehalfOf: self,
		EventType:          store.EventTypeSignoff,
		OpenQuestionsDelta: store.OpenQuestionsDelta{Opened: []store.OpenQuestionOpened{}, Resolved: []string{}},
		SignoffStatus:      &approved,
	}
	_, err = s.RevisionEvents().Append(ctx, e)
	require.NoError(t, err)
	e.SignoffStatus = &changesRequested
	_, err = s.RevisionEvents().Append(ctx, e)
	require.NoError(t, err)

	got, err := s.RevisionEvents().ListLatestSignoffBySessionIDs(ctx, []uuid.UUID{signedOff.ID, neverSigned.ID})
	require.NoError(t, err)

	status, ok := got[signedOff.ID]
	require.True(t, ok, "a session with signoff events must be present in the map")
	assert.Equal(t, store.SignoffStatusChangesRequested, status, "the most recent signoff must win, not the first")

	_, ok = got[neverSigned.ID]
	assert.False(t, ok, "a session with no signoff event must be absent from the map, not present with a zero value")
}

// TestRevisionEventStore_Append_UnknownSession_ReturnsErrNotFound is a
// bonus guard alongside case 2's design_session equivalent: Append against
// a session_id with no design_session row must fail cleanly (via the
// SELECT ... FOR UPDATE lock) rather than surfacing a raw FK violation.
func TestRevisionEventStore_Append_UnknownSession_ReturnsErrNotFound(t *testing.T) {
	ctx := context.Background()
	f := newRevisionEventFixture(t, "whale-net/revision-event-unknown-session-test")
	self := dsTestSubject("agent-1")

	e := baseRevisionEvent(f, self, self)
	e.SessionID = uuid.New()
	_, err := f.store.RevisionEvents().Append(ctx, e)
	assert.True(t, errors.Is(err, store.ErrNotFound), "append against an unknown session must fail as ErrNotFound, not a raw FK violation")
}

// Migration 025 widens revision_event's kind CHECKs to admit 'agent'.
func TestRevisionEventStore_Append_AgentKind_ActingAndOnBehalfOf(t *testing.T) {
	ctx := context.Background()
	f := newRevisionEventFixture(t, "whale-net/revision-event-agent-kind-test")
	agent := store.Subject{Iss: "https://issuer.example.com", Sub: "agent-2", Kind: store.SubjectKindAgent}
	human := store.Subject{Iss: "https://issuer.example.com", Sub: "human-1", Kind: store.SubjectKindHuman}

	ev, err := f.store.RevisionEvents().Append(ctx, baseRevisionEvent(f, agent, human))
	require.NoError(t, err, "acting kind=agent must be accepted")
	assert.Equal(t, store.SubjectKindAgent, ev.Acting.Kind)

	ev, err = f.store.RevisionEvents().Append(ctx, baseRevisionEvent(f, human, agent))
	require.NoError(t, err, "on_behalf_of kind=agent must be accepted")
	assert.Equal(t, store.SubjectKindAgent, ev.OnBehalfOf.Kind)
}
