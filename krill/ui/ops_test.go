// Field-parity tests for the ops console's row mappers: each read view
// must present the same subject pairs and target identity the store row
// and its GET /console/* wire carry, so an operator reading the browser
// sees what an MCP or api caller sees.
package main

import (
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

func TestNewEscalatedRowCarriesBothEscalatorSubjects(t *testing.T) {
	row := newEscalatedRow(store.EscalatedTaskRow{
		EscalatedByActing:     store.Subject{Iss: "ai", Sub: "a", Kind: store.SubjectKindHuman},
		EscalatedByOnBehalfOf: store.Subject{Iss: "bi", Sub: "b"},
	})
	assert.Equal(t, "ai a (human)", row.Actor)
	assert.Equal(t, "bi b", row.OnBehalfOf)
}

func TestNewCancelledRowCarriesBothCancellerSubjects(t *testing.T) {
	row := newCancelledRow(store.CancelledTaskRow{
		CancelledByActing:     store.Subject{Iss: "ai", Sub: "a", Kind: store.SubjectKindHuman},
		CancelledByOnBehalfOf: store.Subject{Iss: "bi", Sub: "b"},
	})
	assert.Equal(t, "ai a (human)", row.By)
	assert.Equal(t, "bi b", row.OnBehalfOf)
}

func TestNewNoteRowNamesTargetByID(t *testing.T) {
	// The target column carries the target's id, not just its human
	// label, so a note row names the same entity the note points at.
	taskID := uuid.New()
	task := newNoteRow(store.OpenNoteRow{TaskContext: &store.OpenNoteTaskContext{TaskID: taskID, Title: "a task"}})
	assert.Contains(t, task.Target, taskID.String())
	assert.Contains(t, task.Target, "a task")

	entityID := uuid.New()
	entity := newNoteRow(store.OpenNoteRow{EntityContext: &store.OpenNoteEntityContext{EntityID: entityID, Title: "a req"}})
	assert.Contains(t, entity.Target, entityID.String())
	assert.Contains(t, entity.Target, "a req")
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
