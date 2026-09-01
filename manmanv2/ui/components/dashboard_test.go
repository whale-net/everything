package components

import (
	"context"
	"strings"
	"testing"
)

// This file guards #1532's dashboard-card link rework (FR1/FR11): the
// dashboard's per-deployment card must route to the deployment page
// (/sgc/{server_game_config_id}) instead of a session-detail page
// (/sessions/{session_id}), for every persona (no role branching, NFR2 --
// DashboardSessions takes no persona/role parameter at all, so this is
// structurally guaranteed rather than merely tested).
//
// Red/green (verified by hand): reverting the href in DashboardSessions
// (dashboard.templ) from
// `templ.URL(fmt.Sprintf("/sgc/%d", s.SGCID))` back to
// `templ.URL(fmt.Sprintf("/sessions/%d", s.SessionID))` made
// TestDashboardSessions_CardLinksToDeploymentNotSession fail (no /sgc/7
// link found; a /sessions/3 link was present instead); restoring the
// implementation line made it pass again.

func renderDashboardSessions(t *testing.T, sessions []ActiveSessionInfo) string {
	t.Helper()
	var buf strings.Builder
	if err := DashboardSessions(sessions).Render(context.Background(), &buf); err != nil {
		t.Fatalf("DashboardSessions render failed: %v", err)
	}
	return buf.String()
}

// TestDashboardSessions_CardLinksToDeploymentNotSession covers test 1: a
// single entry whose SGCID and SessionID differ must link to
// /sgc/<SGCID>, never to /sessions/<SessionID>.
func TestDashboardSessions_CardLinksToDeploymentNotSession(t *testing.T) {
	sessions := []ActiveSessionInfo{
		{SessionID: 3, SGCID: 7, Status: "running", ServerName: "Alpha", GameName: "Minecraft", ConfigName: "Survival"},
	}
	body := renderDashboardSessions(t, sessions)

	if !strings.Contains(body, `href="/sgc/7"`) {
		t.Errorf("expected card href %q, got body: %s", `href="/sgc/7"`, body)
	}
	if strings.Contains(body, `href="/sessions/3"`) {
		t.Errorf("expected no /sessions/3 card link, got body: %s", body)
	}
	if strings.Contains(body, "/sessions/") {
		t.Errorf("expected no /sessions/ link anywhere on the dashboard card, got body: %s", body)
	}
}

// TestDashboardSessions_MultipleEntriesEachLinkOwnDeployment covers test 2:
// several entries, each linking to its own deployment (SGCID), not to a
// sibling's or to the session id.
func TestDashboardSessions_MultipleEntriesEachLinkOwnDeployment(t *testing.T) {
	sessions := []ActiveSessionInfo{
		{SessionID: 10, SGCID: 100, Status: "running", ServerName: "Alpha", GameName: "Minecraft", ConfigName: "Survival"},
		{SessionID: 11, SGCID: 200, Status: "starting", ServerName: "Beta", GameName: "Valheim", ConfigName: "Coop"},
		{SessionID: 12, SGCID: 300, Status: "stopped", ServerName: "Gamma", GameName: "Terraria", ConfigName: "Expert"},
	}
	body := renderDashboardSessions(t, sessions)

	for _, want := range []string{`href="/sgc/100"`, `href="/sgc/200"`, `href="/sgc/300"`} {
		if got := strings.Count(body, want); got != 1 {
			t.Errorf("expected card link %q exactly once, got %d occurrences in %q", want, got, body)
		}
	}
	for _, sid := range []string{"10", "11", "12"} {
		if strings.Contains(body, `href="/sessions/`+sid+`"`) {
			t.Errorf("expected no session-detail link for session %s, got body: %s", sid, body)
		}
	}
}

// TestDashboardSessions_EmptySliceRendersEmptyState is a regression guard
// (test 3): an empty sessions slice must still render the existing empty
// state, not an empty grid or a panic.
func TestDashboardSessions_EmptySliceRendersEmptyState(t *testing.T) {
	body := renderDashboardSessions(t, nil)

	if !strings.Contains(body, "No active sessions") {
		t.Errorf("expected empty state text %q, got body: %s", "No active sessions", body)
	}
	if strings.Contains(body, "card bg-base-100 border border-base-300 shadow-sm hover:shadow-lg") {
		t.Errorf("expected no session cards in the empty state, got body: %s", body)
	}
}
