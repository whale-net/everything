package components

import (
	"testing"

	manmanpb "github.com/whale-net/everything/manmanv2/protos"
)

// TestComputeDeploymentActions_StatusMapping covers one case per row of the
// availability table in the task issue (FR1/FR3/FR5), asserting all three
// booleans per case so that, e.g., Stop leaking onto a stopped deployment
// would be caught even though CanStart is correct for that row.
func TestComputeDeploymentActions_StatusMapping(t *testing.T) {
	cases := []struct {
		name   string
		status string
		want   DeploymentActions
	}{
		{"pending: all false", "pending", DeploymentActions{}},
		{"starting: all false", "starting", DeploymentActions{}},
		{"running: stop+restart", "running", DeploymentActions{CanStart: false, CanStop: true, CanRestart: true}},
		{"stopping: all false", "stopping", DeploymentActions{}},
		{"stopped: start only", "stopped", DeploymentActions{CanStart: true, CanStop: false, CanRestart: false}},
		{"crashed: start+restart", "crashed", DeploymentActions{CanStart: true, CanStop: false, CanRestart: true}},
		{"lost: start+restart", "lost", DeploymentActions{CanStart: true, CanStop: false, CanRestart: true}},
		{"empty status: all false", "", DeploymentActions{}},
		{"unknown status: all false (fail closed)", "weird", DeploymentActions{}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			session := &manmanpb.Session{SessionId: 1, Status: tc.status}
			got := ComputeDeploymentActions(session)
			if got != tc.want {
				t.Errorf("ComputeDeploymentActions(status=%q) = %+v, want %+v", tc.status, got, tc.want)
			}
		})
	}
}

// TestComputeDeploymentActions_NilLatestSession is FR1's "no session yet
// shows no Start action" case: a deployment that never had a session must
// not offer any action, including Start (deploy-and-start is out of scope
// for M2).
func TestComputeDeploymentActions_NilLatestSession(t *testing.T) {
	got := ComputeDeploymentActions(nil)
	want := DeploymentActions{}
	if got != want {
		t.Errorf("ComputeDeploymentActions(nil) = %+v, want %+v", got, want)
	}
}

// TestLatestSessionStatus_PicksNewest exercises LatestSessionStatus's use of
// LatestSession's started_at-then-session_id ordering rule with sessions
// supplied out of order, including a started_at==0 pending session, and
// confirms the input slice is not mutated.
func TestLatestSessionStatus_PicksNewest(t *testing.T) {
	older := &manmanpb.Session{SessionId: 1, StartedAt: 100, Status: "running"}
	newer := &manmanpb.Session{SessionId: 2, StartedAt: 300, Status: "stopped"}
	pending := &manmanpb.Session{SessionId: 3, StartedAt: 0, Status: "pending"}

	sessions := []*manmanpb.Session{older, pending, newer}
	got := LatestSessionStatus(sessions)
	if got != "stopped" {
		t.Errorf("LatestSessionStatus(mixed order) = %q, want %q", got, "stopped")
	}

	wantOrder := []int64{1, 3, 2}
	for i, s := range sessions {
		if s.GetSessionId() != wantOrder[i] {
			t.Fatalf("input slice order changed: index %d = session_id %d, want %d", i, s.GetSessionId(), wantOrder[i])
		}
	}
}

// TestLatestSessionStatus_NoSessions covers the "never had a session" case
// for both nil and empty slices.
func TestLatestSessionStatus_NoSessions(t *testing.T) {
	if got := LatestSessionStatus(nil); got != "" {
		t.Errorf("LatestSessionStatus(nil) = %q, want %q", got, "")
	}
	if got := LatestSessionStatus([]*manmanpb.Session{}); got != "" {
		t.Errorf("LatestSessionStatus([]) = %q, want %q", got, "")
	}
}

// TestIsTransientStatus covers every status IsTransientStatus is documented
// to classify, both transient (pending/starting/stopping) and terminal
// (running/stopped/crashed/lost) plus empty/unknown strings.
func TestIsTransientStatus(t *testing.T) {
	cases := []struct {
		status string
		want   bool
	}{
		{"pending", true},
		{"starting", true},
		{"stopping", true},
		{"running", false},
		{"stopped", false},
		{"crashed", false},
		{"lost", false},
		{"", false},
		{"weird", false},
	}

	for _, tc := range cases {
		t.Run(tc.status, func(t *testing.T) {
			if got := IsTransientStatus(tc.status); got != tc.want {
				t.Errorf("IsTransientStatus(%q) = %v, want %v", tc.status, got, tc.want)
			}
		})
	}
}
