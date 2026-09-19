// fakeMilestoneStatusStore is an in-memory store.MilestoneStatusEventStore
// backing milestone_status_test.go: SetMilestoneStatusHandler/
// GetMilestoneStatusHandler/GetMilestoneStatusHistoryHandler (milestone_status.go,
// issue #2685) depend only on this interface, so handler-level tests never
// need a real Postgres (that is
// krill/store/milestone_status_integration_test.go's job).
package handlers_test

import (
	"context"

	"github.com/google/uuid"

	"github.com/whale-net/everything/krill/store"
)

// fakeMilestoneStatusStore records every RecordTransition call it sees
// (so a test can assert the write gate reached, or never reached, the
// store) and can be told to fail with a fixed error, and pre-seeds
// CurrentStatus/ListTransitions answers for the read-side handlers.
type fakeMilestoneStatusStore struct {
	recordErr error

	// recordCalled reports whether RecordTransition was ever called --
	// the NFR4 "a handler call without a session writes nothing" case
	// asserts this stays false.
	recordCalled bool

	gotScopeID     uuid.UUID
	gotMilestoneID uuid.UUID
	gotStatus      store.MilestoneStatus
	gotNote        *string
	gotActing      store.Subject
	gotOnBehalfOf  store.Subject

	currentStatus store.MilestoneStatus
	transitions   []store.MilestoneStatusEvent
}

func (f *fakeMilestoneStatusStore) RecordTransition(ctx context.Context, scopeID, milestoneID uuid.UUID, status store.MilestoneStatus, note *string, acting, onBehalfOf store.Subject) (store.MilestoneStatusEvent, error) {
	f.recordCalled = true
	f.gotScopeID, f.gotMilestoneID, f.gotStatus, f.gotNote = scopeID, milestoneID, status, note
	f.gotActing, f.gotOnBehalfOf = acting, onBehalfOf
	if f.recordErr != nil {
		return store.MilestoneStatusEvent{}, f.recordErr
	}
	return store.MilestoneStatusEvent{
		ID: uuid.New(), ScopeID: scopeID, MilestoneID: milestoneID, Status: status, Note: note,
		CreatedByActing: acting, CreatedByOnBehalfOf: onBehalfOf,
	}, nil
}

func (f *fakeMilestoneStatusStore) CurrentStatus(ctx context.Context, milestoneID uuid.UUID) (store.MilestoneStatus, error) {
	if f.currentStatus == "" {
		return store.MilestoneStatusNotStarted, nil
	}
	return f.currentStatus, nil
}

func (f *fakeMilestoneStatusStore) CurrentStatuses(ctx context.Context, milestoneIDs []uuid.UUID) (map[uuid.UUID]store.MilestoneStatus, error) {
	result := make(map[uuid.UUID]store.MilestoneStatus, len(milestoneIDs))
	for _, id := range milestoneIDs {
		result[id] = store.MilestoneStatusNotStarted
	}
	return result, nil
}

func (f *fakeMilestoneStatusStore) ListTransitions(ctx context.Context, milestoneID uuid.UUID) ([]store.MilestoneStatusEvent, error) {
	return f.transitions, nil
}

var _ store.MilestoneStatusEventStore = (*fakeMilestoneStatusStore)(nil)
