package repository

import "testing"

// TestDerivePromotionSyncOutcome covers FR8's classification, including the
// case that most distinguishes this from a naive "scan for any terminal
// entry ever" implementation: a non-terminal LAST entry must yield PENDING
// even when an EARLIER entry in the same history was terminal -- "current"
// always means the most recent poll session's own outcome, never a stale
// terminal result from before a later refresh/retry cycle started
// re-observing.
func TestDerivePromotionSyncOutcome(t *testing.T) {
	tests := []struct {
		name             string
		events           []PromotionSyncEvent
		wantOutcome      PromotionSyncOutcome
		wantSyncStatus   string
		wantHealthStatus string
	}{
		{
			name:        "no events yet",
			events:      nil,
			wantOutcome: PromotionSyncOutcomePending,
		},
		{
			name: "last entry Synced+Healthy",
			events: []PromotionSyncEvent{
				{Source: PromotionSyncEventSourceRefreshTriggered},
				{Source: PromotionSyncEventSourcePollObserved, SyncStatus: "Synced", HealthStatus: "Healthy"},
			},
			wantOutcome:      PromotionSyncOutcomeSyncedHealthy,
			wantSyncStatus:   "Synced",
			wantHealthStatus: "Healthy",
		},
		{
			name: "last entry Degraded",
			events: []PromotionSyncEvent{
				{Source: PromotionSyncEventSourceRefreshTriggered},
				{Source: PromotionSyncEventSourcePollObserved, SyncStatus: "OutOfSync", HealthStatus: "Degraded"},
			},
			wantOutcome:      PromotionSyncOutcomeSyncFailed,
			wantSyncStatus:   "OutOfSync",
			wantHealthStatus: "Degraded",
		},
		{
			name: "last entry never reached terminal (FR5 exhausted-attempts case)",
			events: []PromotionSyncEvent{
				{Source: PromotionSyncEventSourcePollObserved, SyncStatus: "OutOfSync", HealthStatus: "Progressing"},
				{Source: PromotionSyncEventSourcePollObserved, SyncStatus: "OutOfSync", HealthStatus: "Progressing"},
				{Source: PromotionSyncEventSourcePollObserved, SyncStatus: "OutOfSync", HealthStatus: "Progressing"},
			},
			wantOutcome:      PromotionSyncOutcomePending,
			wantSyncStatus:   "OutOfSync",
			wantHealthStatus: "Progressing",
		},
		{
			name: "earlier terminal observation must not leak through a later non-terminal one",
			events: []PromotionSyncEvent{
				{Source: PromotionSyncEventSourcePollObserved, SyncStatus: "Synced", HealthStatus: "Healthy"},
				// A later redeploy started a fresh refresh/poll cycle that
				// hasn't reached terminal again yet.
				{Source: PromotionSyncEventSourceRefreshTriggered},
				{Source: PromotionSyncEventSourcePollObserved, SyncStatus: "OutOfSync", HealthStatus: "Progressing"},
			},
			wantOutcome:      PromotionSyncOutcomePending,
			wantSyncStatus:   "OutOfSync",
			wantHealthStatus: "Progressing",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			outcome, syncStatus, healthStatus := DerivePromotionSyncOutcome(tt.events)
			if outcome != tt.wantOutcome {
				t.Errorf("outcome = %v, want %v", outcome, tt.wantOutcome)
			}
			if syncStatus != tt.wantSyncStatus {
				t.Errorf("currentSyncStatus = %q, want %q", syncStatus, tt.wantSyncStatus)
			}
			if healthStatus != tt.wantHealthStatus {
				t.Errorf("currentHealthStatus = %q, want %q", healthStatus, tt.wantHealthStatus)
			}
		})
	}
}
