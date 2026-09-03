package pages

import (
	"fmt"
	"strings"
	"testing"

	manmanpb "github.com/whale-net/everything/manmanv2/protos"
	"github.com/whale-net/everything/manmanv2/ui/components"
)

// This file guards #1626's deployment-row partial (FR1/FR3/FR5/FR7/FR8 on
// the /sessions deployment-status table): DeploymentRow/DeploymentRowInner
// must render the one-click Start/Stop/Restart controls with availability
// coming entirely from components.ComputeDeploymentActions (#1625) -- never
// a status-string comparison inside the templ itself -- and Stop/Restart
// must stay behind the same click-to-reveal Alpine confirm gate the Danger
// Zone sections use (see sgc_detail.templ), never htmxui.Confirm's
// always-visible shape (daisyui_migration_test.go guards that distinction
// for the other Danger Zones; this file extends the same guarantee to this
// row).
//
// Every case below builds its DeploymentRowData.Actions from the real
// components.ComputeDeploymentActions helper (already unit-tested
// independently in components/deployment_actions_test.go), rather than
// hand-picking booleans, so a regression in that helper would also surface
// here as a rendering-level failure.
//
// mutation-tested (verified red, by hand, then reverted): commenting out
// the `if data.Actions.CanStop { ... }` block's outer `if` in
// sessions.templ (so the Stop control's Alpine markup always rendered)
// made TestDeploymentRow_Stopped_StartPresentNoConfirmGate fail on its
// "no x-show at all" assertion; reverting restored green.

// buildDeploymentRowData computes Actions via the real
// components.ComputeDeploymentActions helper, mirroring how handleSessions
// builds DeploymentRowData in production.
func buildDeploymentRowData(sgcID int64, displayName, sgcStatus string, latest, live *manmanpb.Session, actionError string) DeploymentRowData {
	return DeploymentRowData{
		ServerGameConfigID: sgcID,
		DisplayName:        displayName,
		SGCStatus:          sgcStatus,
		LatestSession:      latest,
		LiveSession:        live,
		Actions:            components.ComputeDeploymentActions(latest),
		ActionError:        actionError,
	}
}

// deploymentRowMarkup renders a single DeploymentRow in isolation.
func deploymentRowMarkup(t *testing.T, data DeploymentRowData) string {
	t.Helper()
	return renderPage(t, DeploymentRow(data))
}

func rowID(sgcID int64) string {
	return fmt.Sprintf("deployment-row-%d", sgcID)
}

// --- 1. running deployment: Stop + Restart, confirm-gated; no Start --------

func TestDeploymentRow_Running_StopAndRestartConfirmGated(t *testing.T) {
	latest := &manmanpb.Session{SessionId: 1, ServerGameConfigId: 10, Status: "running"}
	data := buildDeploymentRowData(10, "Alpha (Minecraft)", "active", latest, latest, "")
	body := deploymentRowMarkup(t, data)

	if !strings.Contains(body, `id="`+rowID(10)+`"`) {
		t.Fatalf("expected row id %q, got body %q", rowID(10), body)
	}
	if !strings.Contains(body, ">Stop<") {
		t.Errorf("expected a Stop trigger button, got body %q", body)
	}
	if !strings.Contains(body, ">Restart<") {
		t.Errorf("expected a Restart trigger button, got body %q", body)
	}
	if strings.Contains(body, ">Start<") {
		t.Errorf("expected no Start button for a running deployment, got body %q", body)
	}
	if !strings.Contains(body, `x-show="!confirmStop"`) || !strings.Contains(body, `x-show="confirmStop" x-cloak`) {
		t.Errorf("expected the Stop control to be gated by the confirmStop x-show/x-cloak pair, got body %q", body)
	}
	if !strings.Contains(body, `x-show="!confirmRestart"`) || !strings.Contains(body, `x-show="confirmRestart" x-cloak`) {
		t.Errorf("expected the Restart control to be gated by the confirmRestart x-show/x-cloak pair, got body %q", body)
	}
	if !strings.Contains(body, ">Yes, Stop<") || !strings.Contains(body, ">Yes, Restart<") {
		t.Errorf("expected confirm-click Yes buttons for both Stop and Restart, got body %q", body)
	}
	// No-JS fallback: forms keep method="POST" action=... alongside hx-post.
	stopAction := fmt.Sprintf(`action="/sessions/deployments/%d/stop"`, 10)
	restartAction := fmt.Sprintf(`action="/sessions/deployments/%d/restart"`, 10)
	if !strings.Contains(body, stopAction) {
		t.Errorf("expected no-JS form fallback action %q, got body %q", stopAction, body)
	}
	if !strings.Contains(body, restartAction) {
		t.Errorf("expected no-JS form fallback action %q, got body %q", restartAction, body)
	}
}

// --- 2. stopped deployment: Start present, no confirm gate -----------------

func TestDeploymentRow_Stopped_StartPresentNoConfirmGate(t *testing.T) {
	latest := &manmanpb.Session{SessionId: 2, ServerGameConfigId: 11, Status: "stopped"}
	data := buildDeploymentRowData(11, "Beta (Valheim)", "active", latest, nil, "")
	body := deploymentRowMarkup(t, data)

	if !strings.Contains(body, ">Start<") {
		t.Fatalf("expected a Start button for a stopped deployment, got body %q", body)
	}
	if strings.Contains(body, ">Stop<") || strings.Contains(body, ">Restart<") {
		t.Errorf("expected no Stop/Restart controls for a stopped deployment, got body %q", body)
	}
	// Start requires no confirmation (NFR): the actions cell must not carry
	// any x-show gate at all when Stop/Restart aren't rendered.
	if strings.Contains(body, "x-show") {
		t.Errorf("expected no x-show confirm gate anywhere in the row (Start needs none), got body %q", body)
	}
	startAction := fmt.Sprintf(`action="/sessions/deployments/%d/start"`, 11)
	if !strings.Contains(body, startAction) {
		t.Errorf("expected no-JS form fallback action %q, got body %q", startAction, body)
	}
}

// --- 3. crashed / lost: Start + Restart, no Stop ----------------------------

func TestDeploymentRow_CrashedOrLost_StartAndRestartNoStop(t *testing.T) {
	for _, status := range []string{"crashed", "lost"} {
		t.Run(status, func(t *testing.T) {
			latest := &manmanpb.Session{SessionId: 3, ServerGameConfigId: 12, Status: status}
			data := buildDeploymentRowData(12, "Gamma", "active", latest, nil, "")
			body := deploymentRowMarkup(t, data)

			if !strings.Contains(body, ">Start<") {
				t.Errorf("expected a Start button for a %s deployment, got body %q", status, body)
			}
			if !strings.Contains(body, ">Restart<") {
				t.Errorf("expected a Restart button for a %s deployment, got body %q", status, body)
			}
			if strings.Contains(body, ">Stop<") {
				t.Errorf("expected no Stop button for a %s deployment, got body %q", status, body)
			}
		})
	}
}

// --- 4. transient statuses: no action buttons at all ------------------------

func TestDeploymentRow_TransientStatuses_NoActions(t *testing.T) {
	for _, status := range []string{"pending", "starting", "stopping"} {
		t.Run(status, func(t *testing.T) {
			latest := &manmanpb.Session{SessionId: 4, ServerGameConfigId: 13, Status: status}
			data := buildDeploymentRowData(13, "Delta", "active", latest, nil, "")
			body := deploymentRowMarkup(t, data)

			if strings.Contains(body, ">Start<") || strings.Contains(body, ">Stop<") || strings.Contains(body, ">Restart<") {
				t.Errorf("expected no action buttons for a %s deployment, got body %q", status, body)
			}
			if !strings.Contains(body, "&mdash;") {
				t.Errorf("expected the muted em-dash placeholder when no action is available, got body %q", status)
			}
		})
	}
}

// --- 5. never-started: no Start, "Never started" indication -----------------

func TestDeploymentRow_NeverStarted_NoStartAndNeverStartedBadge(t *testing.T) {
	data := buildDeploymentRowData(14, "Epsilon", "active", nil, nil, "")
	body := deploymentRowMarkup(t, data)

	if strings.Contains(body, ">Start<") || strings.Contains(body, ">Stop<") || strings.Contains(body, ">Restart<") {
		t.Errorf("expected no action buttons for a never-started deployment, got body %q", body)
	}
	if !strings.Contains(body, "Never started") {
		t.Errorf("expected a 'Never started' indication rather than an empty status cell, got body %q", body)
	}
}

// --- 6. row id + swap target format -----------------------------------------

func TestDeploymentRow_IDAndSwapTargetsMatch(t *testing.T) {
	latest := &manmanpb.Session{SessionId: 5, ServerGameConfigId: 99, Status: "running"}
	data := buildDeploymentRowData(99, "Zeta", "active", latest, latest, "")
	body := deploymentRowMarkup(t, data)

	wantID := `id="deployment-row-99"`
	if !strings.Contains(body, wantID) {
		t.Fatalf("expected row id %q, got body %q", wantID, body)
	}
	wantTarget := `hx-target="#deployment-row-99"`
	targetCount := strings.Count(body, wantTarget)
	if targetCount == 0 {
		t.Fatalf("expected at least one hx-target=%q (one per action form), got body %q", wantTarget, body)
	}
	swapCount := strings.Count(body, `hx-swap="outerHTML"`)
	if swapCount != targetCount {
		t.Errorf("expected every hx-target to be paired with hx-swap=\"outerHTML\" (%d targets vs %d swaps), got body %q", targetCount, swapCount, body)
	}
	postCount := strings.Count(body, `hx-post="/sessions/deployments/99/`)
	if postCount != targetCount {
		t.Errorf("expected every action form to submit via hx-post (%d targets vs %d hx-post forms), got body %q", targetCount, postCount, body)
	}
}

// --- 7. ActionError rendering ------------------------------------------------

func TestDeploymentRow_ActionError(t *testing.T) {
	latest := &manmanpb.Session{SessionId: 6, ServerGameConfigId: 15, Status: "running"}

	withError := buildDeploymentRowData(15, "Eta", "active", latest, latest, "Failed to stop: timeout")
	body := deploymentRowMarkup(t, withError)
	if !strings.Contains(body, "alert-error") {
		t.Errorf("expected an alert-error element when ActionError is set, got body %q", body)
	}
	if !strings.Contains(body, "Failed to stop: timeout") {
		t.Errorf("expected the ActionError text to render, got body %q", body)
	}

	withoutError := buildDeploymentRowData(15, "Eta", "active", latest, latest, "")
	body2 := deploymentRowMarkup(t, withoutError)
	if strings.Contains(body2, "alert-error") {
		t.Errorf("expected no alert-error element when ActionError is unset, got body %q", body2)
	}
}

// --- 8. GSCStatusTable loops rows and includes an Actions header -----------

func TestGSCStatusTable_RendersAllRowsWithActionsHeader(t *testing.T) {
	latestRunning := &manmanpb.Session{SessionId: 7, ServerGameConfigId: 20, Status: "running"}
	latestStopped := &manmanpb.Session{SessionId: 8, ServerGameConfigId: 21, Status: "stopped"}
	rows := []DeploymentRowData{
		buildDeploymentRowData(20, "Theta", "active", latestRunning, latestRunning, ""),
		buildDeploymentRowData(21, "Iota", "active", latestStopped, nil, ""),
	}
	body := renderPage(t, GSCStatusTable(rows))

	if !strings.Contains(body, "<th>Actions</th>") {
		t.Errorf("expected an Actions column header, got body %q", body)
	}
	if !strings.Contains(body, `id="`+rowID(20)+`"`) || !strings.Contains(body, `id="`+rowID(21)+`"`) {
		t.Errorf("expected both rows to render with their own stable ids, got body %q", body)
	}
}
