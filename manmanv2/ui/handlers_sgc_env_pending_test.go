package main

import (
	"testing"

	manmanpb "github.com/whale-net/everything/manmanv2/protos"
)

// Guards task #2096's FR3 exact clearing rule for the pending-override
// hint: the hint is visible only while the deployment has an active
// session and the latest saved deployment-level env override edit (patch
// updated_at) is newer than that session's start. A start whose command
// build happened after the save ran the new values and clears the hint; a
// start that began before the save -- no matter how long it keeps
// running -- leaves the hint visible; no active session or no patch means
// nothing is pending.
//
// mutation-tested (verified red, by hand, then reverted): flipping the
// final comparison from >= to > (treating an equal-second save/start as
// already run) made TestPendingEnvOverrideHint_SameSecondSaveAndStart_HintStays
// fail; reverting restored green.

func running(startedAt int64) *manmanpb.Session {
	return &manmanpb.Session{SessionId: 1, Status: "running", StartedAt: startedAt}
}

func patchWithUpdatedAt(ts int64) *manmanpb.ConfigurationPatch {
	return &manmanpb.ConfigurationPatch{PatchId: 2, UpdatedAt: ts}
}

func TestPendingEnvOverrideHint_SaveWhileRunning_HintVisible(t *testing.T) {
	// Save at t=1000 while the session that started at t=500 is running:
	// the running start predates the edit, so the edit is pending.
	got := pendingEnvOverrideHint(patchWithUpdatedAt(1000), []*manmanpb.Session{running(500)})
	if !got {
		t.Error("hint not visible after an override save newer than the running session's start")
	}
}

func TestPendingEnvOverrideHint_StartAfterSave_Clears(t *testing.T) {
	// A start whose command build happened after the save (start t=2000 >
	// save t=1000) ran the new values: the hint clears.
	got := pendingEnvOverrideHint(patchWithUpdatedAt(1000), []*manmanpb.Session{running(2000)})
	if got {
		t.Error("hint still visible after a session start newer than the save")
	}
}

func TestPendingEnvOverrideHint_SameSecondSaveAndStart_HintStays(t *testing.T) {
	// Timestamps are Unix seconds: when the save and the start land in the
	// same second, the deployment's evidence cannot prove the start's
	// command build happened after the save. FR3's clearing rule is
	// "actually ran the new values", so equality must NOT clear the hint
	// (fail closed): a premature clear would be possible otherwise.
	got := pendingEnvOverrideHint(patchWithUpdatedAt(1000), []*manmanpb.Session{running(1000)})
	if !got {
		t.Error("hint cleared on an equal-second save/start, allowing a premature clear")
	}
}

func TestPendingEnvOverrideHint_StartPredatingSave_KeepsHintVisible(t *testing.T) {
	// The exact-rule edge the task calls out: a start that began before
	// the edit was saved keeps the hint visible for as long as it runs,
	// even though the deployment is technically "running" again after a
	// later force-start marks older sessions stopped.
	got := pendingEnvOverrideHint(patchWithUpdatedAt(3000), []*manmanpb.Session{
		{SessionId: 9, Status: "stopped", StartedAt: 100, EndedAt: 200},
		{SessionId: 5, Status: "stopped", StartedAt: 2500},
		running(2000),
	})
	if !got {
		t.Error("hint cleared despite the running session's start predating the save")
	}
}

func TestPendingEnvOverrideHint_NewestActiveStartWins(t *testing.T) {
	// If several starts are somehow active at once, the newest start is
	// the deployment's current one: a save newer than it is pending.
	got := pendingEnvOverrideHint(patchWithUpdatedAt(3000), []*manmanpb.Session{
		running(500),
		running(2500),
	})
	if !got {
		t.Error("hint not visible when the newest active start predates the save")
	}

	// And a save older than the newest active start is not pending.
	got = pendingEnvOverrideHint(patchWithUpdatedAt(1000), []*manmanpb.Session{
		running(500),
		running(2500),
	})
	if got {
		t.Error("hint visible although the newest active start ran after the save")
	}
}

func TestPendingEnvOverrideHint_PendingAndStartingSessionsCount(t *testing.T) {
	// pending/starting sessions have already had their start command
	// built (start time set at build time), so they satisfy the
	// ran-with-new-values rule the same way a running session does.
	got := pendingEnvOverrideHint(patchWithUpdatedAt(1000), []*manmanpb.Session{
		{SessionId: 3, Status: "pending", StartedAt: 2000},
	})
	if got {
		t.Error("hint visible although the pending session's start postdates the save")
	}

	got = pendingEnvOverrideHint(patchWithUpdatedAt(3000), []*manmanpb.Session{
		{SessionId: 3, Status: "starting", StartedAt: 2000},
	})
	if !got {
		t.Error("hint not visible when the starting session's start predates the save")
	}
}

func TestPendingEnvOverrideHint_NoActiveSession_NoHint(t *testing.T) {
	// No running session: edits apply at the next start trivially, so
	// nothing is pending.
	sessions := []*manmanpb.Session{
		{SessionId: 4, Status: "stopped", StartedAt: 100, EndedAt: 200},
		{SessionId: 5, Status: "crashed", StartedAt: 300},
	}
	if got := pendingEnvOverrideHint(patchWithUpdatedAt(1000), sessions); got {
		t.Error("hint visible with no active session")
	}
}

func TestPendingEnvOverrideHint_NoPatchOrUnknownTimestamp_NoHint(t *testing.T) {
	if got := pendingEnvOverrideHint(nil, []*manmanpb.Session{running(500)}); got {
		t.Error("hint visible with no override patch at all")
	}
	// updated_at = 0 (timestamp not exposed by the API) is treated as
	// unknown, never as "saved in the future" -- fail closed to no hint.
	if got := pendingEnvOverrideHint(patchWithUpdatedAt(0), []*manmanpb.Session{running(500)}); got {
		t.Error("hint visible for a patch with no known saved-at timestamp")
	}
}
