package main

import (
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"

	"github.com/whale-net/everything/krill/store"
	"github.com/whale-net/everything/krill/ui/pages"
)

// TestCancelledTaskBadgesDropEscalated: cancel leaves the escalation on
// record, but a dead-lettered task is not waiting on anyone, so every task
// view shows it as Cancelled alone.
func TestCancelledTaskBadgesDropEscalated(t *testing.T) {
	now := time.Now()
	esc := uuid.New()
	row := store.ProductTaskRow{State: store.TaskStateEscalated, CancelledAt: &now, AttemptCap: store.DefaultAttemptCap}
	summary := store.TaskSummary{CurrentEscalationID: &esc, CancelledAt: &now}

	for name, badges := range map[string][]pages.TaskBadge{
		"tasks list": productTaskBadgesOf(row, now),
		"board":      boardCardBadges(row, now),
		"detail":     taskStateBadges(summary, now),
	} {
		var keys []string
		for _, b := range badges {
			keys = append(keys, b.Key)
		}
		assert.Equal(t, []string{"cancelled"}, keys, name)
	}
}
