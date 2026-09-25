// Field-parity tests for the ops console's row mappers: each read view
// must present the same subject pairs and target identity the store row
// and its GET /console/* wire carry, so an operator reading the browser
// sees what an MCP or api caller sees.
package main

import (
	"testing"

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
		TaskID:               id,
		Title:                "t",
		ClaimantActing:       store.Subject{Iss: "ai", Sub: "a", Kind: store.SubjectKindHuman},
		ClaimantOnBehalfOf:   store.Subject{Iss: "bi", Sub: "b", Kind: store.SubjectKindService},
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
