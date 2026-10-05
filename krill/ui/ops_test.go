// Field-parity tests for the ops console's row mappers: each read view
// must present the same subject pairs and target identity the store row
// and its GET /console/* wire carry, so an operator reading the browser
// sees what an MCP or api caller sees.
package main

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"

	"github.com/whale-net/everything/krill/store"
)

func TestOpsSubjectRendersUnsetAsDash(t *testing.T) {
	// A claim taken with no on-behalf-of leaves the subject zero-valued;
	// it must read as visibly empty, not as a bare space.
	assert.Equal(t, "-", opsSubject(store.Subject{}))
	assert.Equal(t, "iss sub", opsSubject(store.Subject{Iss: "iss", Sub: "sub"}))
}

func TestOpsActorShowsKind(t *testing.T) {
	// The acting identity's kind is what distinguishes a human operator
	// from a service; opsActor must carry it, opsSubject must not.
	assert.Equal(t, "iss sub (human)", opsActor(store.Subject{Iss: "iss", Sub: "sub", Kind: store.SubjectKindHuman}))
	assert.Equal(t, "iss sub", opsActor(store.Subject{Iss: "iss", Sub: "sub"}))
}

func TestNewClaimedRowCarriesBothClaimSubjects(t *testing.T) {
	id := uuid.New()
	row := newClaimedRow(store.ClaimedTaskRow{
		TaskID:             id,
		Title:              "t",
		ClaimantActing:     store.Subject{Iss: "ai", Sub: "a", Kind: store.SubjectKindHuman},
		ClaimantOnBehalfOf: store.Subject{Iss: "bi", Sub: "b", Kind: store.SubjectKindService},
	})
	assert.Equal(t, id.String(), row.TaskID)
	assert.Equal(t, "ai a (human)", row.Claimant, "claimant's acting subject shows its kind")
	assert.Equal(t, "bi b", row.OnBehalfOf, "the claim's on-behalf-of subject is presented too")
}

// TestNewClaimedRowCarriesClaimedSinceThroughOpsTime pins the claimed
// view's claimed-since: it is the claim's own instant, formatted
// absolute-RFC3339 by opsTime like every other time on these views. A
// relative "5m ago" would shift on every poll and rewrite the
// operator's page for a state that never changed.
func TestNewClaimedRowCarriesClaimedSinceThroughOpsTime(t *testing.T) {
	claimedAt := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	row := newClaimedRow(store.ClaimedTaskRow{
		TaskID:           uuid.New(),
		ClaimedAt:        claimedAt,
		LeaseExpiresAt:   claimedAt.Add(time.Hour),
		ClaimantActing:     store.Subject{Iss: "ai", Sub: "a", Kind: store.SubjectKindHuman},
		ClaimantOnBehalfOf: store.Subject{Iss: "bi", Sub: "b"},
	})
	assert.Equal(t, "2026-01-02T03:04:05Z", row.ClaimedSince)
	assert.NotEqual(t, row.ClaimedSince, row.Lease, "claimed-since and lease expiry are different instants and must not collapse")
}

func TestNewEscalatedRowCarriesBothEscalatorSubjects(t *testing.T) {
	row := newEscalatedRow(store.EscalatedTaskRow{
		EscalatedByActing:     store.Subject{Iss: "ai", Sub: "a", Kind: store.SubjectKindHuman},
		EscalatedByOnBehalfOf: store.Subject{Iss: "bi", Sub: "b"},
	})
	assert.Equal(t, "ai a (human)", row.Actor)
	assert.Equal(t, "bi b", row.OnBehalfOf)
}

func TestNewCancelledRowCarriesBothCancellerSubjects(t *testing.T) {
	reason := "superseded by the new plan"
	row := newCancelledRow(store.CancelledTaskRow{
		Lane:                  store.LaneImplementation,
		Reason:                &reason,
		CancelledByActing:     store.Subject{Iss: "ai", Sub: "a", Kind: store.SubjectKindHuman},
		CancelledByOnBehalfOf: store.Subject{Iss: "bi", Sub: "b"},
	}, "")
	assert.Equal(t, "ai a (human)", row.By)
	assert.Equal(t, "bi b", row.OnBehalfOf)
	assert.Equal(t, "Implementation", row.Lane, "the row carries the lane it was cancelled out of")
	assert.Equal(t, reason, row.Reason, "a given reason reaches the row")
	assert.Empty(t, row.TaskHref, "a read that named no product leaves the title unlinked")
}

func TestNewCancelledRowOmitsAnAbsentReason(t *testing.T) {
	row := newCancelledRow(store.CancelledTaskRow{TaskID: uuid.New()}, "/products/p/tasks/t")
	assert.Empty(t, row.Reason, "a cancellation with no rationale shows no reason")
	assert.Equal(t, "/products/p/tasks/t", row.TaskHref, "a product-scoped read links the task")
}

func TestNewNoteRowNamesTargetByID(t *testing.T) {
	// The target column carries the target's id, not just its human
	// label, so a note row names the same entity the note points at.
	taskID := uuid.New()
	task := newNoteRow(store.OpenNoteRow{TaskContext: &store.OpenNoteTaskContext{TaskID: taskID, Title: "a task"}}, "/products/p/tasks/"+taskID.String())
	assert.Contains(t, task.Target, taskID.String())
	assert.Contains(t, task.Target, "a task")
	assert.Equal(t, "/products/p/tasks/"+taskID.String(), task.TaskHref, "a task-targeted note links to the task")
	assert.Equal(t, string(store.NoteLifecycleStatusNoted), task.Status, "the status comes from the store's enumeration")

	entityID := uuid.New()
	entity := newNoteRow(store.OpenNoteRow{EntityContext: &store.OpenNoteEntityContext{EntityID: entityID, Title: "a req"}}, "")
	assert.Contains(t, entity.Target, entityID.String())
	assert.Contains(t, entity.Target, "a req")
	assert.Empty(t, entity.TaskHref, "a spec-entity note has no task page to link to")
}

// TestClaimedPollingDueOnlyFiresNearExpiry covers the rule that decides
// whether the claimed view keeps polling: it fires while any claim on the
// page is inside its lease's near-expiry window, and stops once none is.
// An empty page never polls, and a lapsed lease still counts as near
// expiry -- the operator watching for a row to disappear is exactly the
// case the poll exists for.
func TestClaimedPollingDueOnlyFiresNearExpiry(t *testing.T) {
	now := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	row := func(lease time.Time) store.ClaimedTaskRow {
		return store.ClaimedTaskRow{TaskID: uuid.New(), LeaseExpiresAt: lease}
	}

	assert.False(t, claimedPollingDue(nil, now), "an empty page has nothing transient to watch")
	assert.False(t, claimedPollingDue([]store.ClaimedTaskRow{row(now.Add(24 * time.Hour))}, now),
		"a comfortably held lease is settled, not transient")
	assert.True(t, claimedPollingDue([]store.ClaimedTaskRow{row(now.Add(time.Minute))}, now),
		"a lease about to lapse is the one transient state the console watches")
	assert.True(t, claimedPollingDue([]store.ClaimedTaskRow{
		row(now.Add(24 * time.Hour)), row(now.Add(-time.Minute)),
	}, now), "one transient claim on the page is enough to keep the whole view live")
}

// TestClaimedPollingHorizonIsAFractionOfTheLease is the guard against the
// horizon being a whole lease, which makes the predicate vacuous.
//
// ClaimTask sets lease_expires_at to now+DefaultLeaseDuration and
// HeartbeatTask resets it to the same, so EVERY row the store can return
// satisfies LeaseExpiresAt-now <= DefaultLeaseDuration. With a
// whole-lease horizon the poll would therefore be `len(rows) > 0`: any
// deployment with a claimed task -- including a swarm that heartbeats
// forever and never actually nears expiry -- would re-query Postgres
// every 3 seconds, indefinitely.
//
// The original version of this test passed only because it fed the
// function a lease 24 hours out, which the store cannot produce.
func TestClaimedPollingHorizonIsAFractionOfTheLease(t *testing.T) {
	assert.Less(t, claimedPollingHorizon, store.DefaultLeaseDuration,
		"a whole-lease horizon makes the predicate len(rows) > 0")
	assert.Greater(t, claimedPollingHorizon, time.Duration(0),
		"the horizon must still be a real window, not zero")

	now := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	// The state the store actually produces: a claim made right now, or
	// one heartbeated a moment ago. Neither is near expiry, and neither
	// may arm the poll.
	fresh := store.ClaimedTaskRow{TaskID: uuid.New(), LeaseExpiresAt: now.Add(store.DefaultLeaseDuration)}
	heartbeated := store.ClaimedTaskRow{TaskID: uuid.New(), LeaseExpiresAt: now.Add(store.DefaultLeaseDuration - time.Minute)}

	assert.False(t, claimedPollingDue([]store.ClaimedTaskRow{fresh}, now),
		"a claim that has barely been made is settled; polling it forever is the bug this guards")
	assert.False(t, claimedPollingDue([]store.ClaimedTaskRow{heartbeated}, now),
		"a claim heartbeated a minute ago is settled too")
}

// TestOpsSelfPathCarriesPagingParams guards the poll and the manual
// Refresh against resetting a paged operator back to page one. A refresh
// must re-request the view the operator is actually looking at, query
// string included.
func TestOpsSelfPathCarriesPagingParams(t *testing.T) {
	r := httptest.NewRequest(http.MethodGet, "/ops/claimed?page_size=50&page_token=abc123", nil)
	assert.Equal(t, "/ops/claimed?page_size=50&page_token=abc123", opsSelfPath(r),
		"a paged operator's refresh and poll must carry page_size and page_token")

	plain := httptest.NewRequest(http.MethodGet, "/ops/claimed", nil)
	assert.Equal(t, "/ops/claimed", opsSelfPath(plain))

	assert.Equal(t, "/", opsSelfPath(nil), "a nil request must not panic the renderer")
}
