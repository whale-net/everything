package pages

import (
	"strings"
	"testing"

	"github.com/whale-net/everything/manmanv2/ui/components"
)

func TestGameOverview_Online(t *testing.T) {
	data := GameOverviewData{
		GameID: 1,
		Deployments: []GameDeploymentOverview{
			{
				SGCID:         10,
				ServerID:      2,
				ServerName:    "host-01",
				ConfigID:      5,
				ConfigName:    "vanilla",
				DisplayName:   "vanilla on host-01",
				Status:        "Online",
				StatusVariant: "success",
				Uptime:        "2h 15m",
				Connect: components.ConnectAddressView{
					Addresses: []components.ConnectAddress{
						{Address: "192.168.1.50:2456", Port: 2456, Protocol: "UDP"},
					},
					Unavailable: false,
				},
				CanStart:    false,
				CanStop:     true,
				CanRestart:  true,
				IsTransient: false,
				LogsURL:     "/sessions/42",
			},
		},
	}

	body := renderPage(t, GameOverview(data))

	// High-visibility Online badge
	if !strings.Contains(body, "ONLINE") {
		t.Errorf("expected ONLINE badge in overview, got: %s", body)
	}
	if !strings.Contains(body, "badge-success") {
		t.Errorf("expected badge-success class for online status, got: %s", body)
	}

	// Runtime / Uptime counter
	if !strings.Contains(body, "2h 15m") {
		t.Errorf("expected uptime 2h 15m in overview, got: %s", body)
	}

	// Connection info
	if !strings.Contains(body, "192.168.1.50:2456") {
		t.Errorf("expected connect address 192.168.1.50:2456, got: %s", body)
	}

	// Action bar: Stop and Restart active; Start disabled
	if !strings.Contains(body, "Stop") || !strings.Contains(body, "Restart") {
		t.Errorf("expected Stop and Restart buttons, got: %s", body)
	}
	if !strings.Contains(body, "confirmStop") || !strings.Contains(body, "confirmRestart") {
		t.Errorf("expected confirmation gates for Stop and Restart, got: %s", body)
	}

	// One-click diagnostics: View Live Output / Logs button with href
	if !strings.Contains(body, "/sessions/42") {
		t.Errorf("expected link to /sessions/42 for diagnostics, got: %s", body)
	}
	if !strings.Contains(body, "View Live Output / Logs") {
		t.Errorf("expected 'View Live Output / Logs' button text, got: %s", body)
	}

	// Not transient, so no self-terminating polling trigger
	if strings.Contains(body, "hx-trigger=\"every 3s\"") {
		t.Errorf("expected no 3s poll trigger for settled online server, got: %s", body)
	}
}

func TestGameOverview_Offline(t *testing.T) {
	data := GameOverviewData{
		GameID: 1,
		Deployments: []GameDeploymentOverview{
			{
				SGCID:         10,
				ServerID:      2,
				ServerName:    "host-01",
				ConfigID:      5,
				ConfigName:    "vanilla",
				DisplayName:   "vanilla on host-01",
				Status:        "Offline",
				StatusVariant: "neutral",
				Uptime:        "—",
				Connect: components.ConnectAddressView{
					Addresses: []components.ConnectAddress{
						{Address: "192.168.1.50:2456", Port: 2456, Protocol: "UDP"},
					},
					Unavailable: false,
				},
				CanStart:    true,
				CanStop:     false,
				CanRestart:  false,
				IsTransient: false,
				LogsURL:     "",
			},
		},
	}

	body := renderPage(t, GameOverview(data))

	// High-visibility Offline badge
	if !strings.Contains(body, "OFFLINE") {
		t.Errorf("expected OFFLINE badge, got: %s", body)
	}

	// Runtime counter shows —
	if !strings.Contains(body, "—") {
		t.Errorf("expected — for offline uptime, got: %s", body)
	}

	// Start button is active (contains form targeting overview/action)
	if !strings.Contains(body, "action=\"/games/1/overview/action\"") {
		t.Errorf("expected Start action form targeting /games/1/overview/action, got: %s", body)
	}
	if !strings.Contains(body, "value=\"start\"") {
		t.Errorf("expected start action value, got: %s", body)
	}

	// Stop and Restart are disabled
	if !strings.Contains(body, "btn-disabled") {
		t.Errorf("expected disabled buttons for Stop/Restart, got: %s", body)
	}

	// Diagnostics disabled
	if !strings.Contains(body, "No Logs Available") {
		t.Errorf("expected 'No Logs Available' when logsURL is empty, got: %s", body)
	}
}

func TestGameOverview_Restarting_SelfTerminatingPoll(t *testing.T) {
	data := GameOverviewData{
		GameID: 1,
		Deployments: []GameDeploymentOverview{
			{
				SGCID:         10,
				ServerID:      2,
				ServerName:    "host-01",
				ConfigID:      5,
				ConfigName:    "vanilla",
				DisplayName:   "vanilla on host-01",
				Status:        "Restarting",
				StatusVariant: "info",
				Uptime:        "Restarting...",
				Connect: components.ConnectAddressView{
					Addresses: []components.ConnectAddress{
						{Address: "192.168.1.50:2456", Port: 2456, Protocol: "UDP"},
					},
					Unavailable: false,
				},
				CanStart:    false,
				CanStop:     false,
				CanRestart:  false,
				IsTransient: true,
				LogsURL:     "/sessions/42",
			},
		},
	}

	body := renderPage(t, GameOverview(data))

	// High-visibility Restarting badge
	if !strings.Contains(body, "RESTARTING") {
		t.Errorf("expected RESTARTING badge, got: %s", body)
	}
	if !strings.Contains(body, "badge-info") {
		t.Errorf("expected badge-info for restarting, got: %s", body)
	}

	// Self-terminating poll attributes present when transient
	if !strings.Contains(body, "hx-trigger=\"every 3s\"") {
		t.Errorf("expected hx-trigger='every 3s' for restarting state, got: %s", body)
	}
	if !strings.Contains(body, "hx-get=\"/games/1/overview\"") {
		t.Errorf("expected hx-get='/games/1/overview', got: %s", body)
	}
	if !strings.Contains(body, "hx-target=\"#daily-ops-overview\"") {
		t.Errorf("expected hx-target='#daily-ops-overview', got: %s", body)
	}
	if !strings.Contains(body, "hx-swap=\"outerHTML\"") {
		t.Errorf("expected hx-swap='outerHTML', got: %s", body)
	}
}

func TestGameOverview_Error(t *testing.T) {
	data := GameOverviewData{
		GameID: 1,
		Deployments: []GameDeploymentOverview{
			{
				SGCID:         10,
				ServerID:      2,
				ServerName:    "host-01",
				ConfigID:      5,
				ConfigName:    "vanilla",
				DisplayName:   "vanilla on host-01",
				Status:        "Error",
				StatusVariant: "error",
				Uptime:        "—",
				Connect: components.ConnectAddressView{
					Unavailable: true,
				},
				CanStart:    true,
				CanStop:     false,
				CanRestart:  true,
				IsTransient: false,
				LogsURL:     "/sessions/42",
			},
		},
	}

	body := renderPage(t, GameOverview(data))

	// High-visibility Error badge
	if !strings.Contains(body, "ERROR") {
		t.Errorf("expected ERROR badge, got: %s", body)
	}
	if !strings.Contains(body, "badge-error") {
		t.Errorf("expected badge-error, got: %s", body)
	}

	// In error state, both Start and Restart can be attempted
	if !strings.Contains(body, "value=\"start\"") {
		t.Errorf("expected Start active in Error state, got: %s", body)
	}
	if !strings.Contains(body, "value=\"restart\"") {
		t.Errorf("expected Restart active in Error state, got: %s", body)
	}
}

func TestGameOverview_NoDeployments(t *testing.T) {
	data := GameOverviewData{
		GameID:      1,
		Deployments: nil,
	}

	body := renderPage(t, GameOverview(data))

	if !strings.Contains(body, "OFFLINE") {
		t.Errorf("expected OFFLINE badge for no deployments, got: %s", body)
	}
	if !strings.Contains(body, "Create Config to Deploy") {
		t.Errorf("expected Create Config button, got: %s", body)
	}
}

func TestGameOverview_ActionError(t *testing.T) {
	data := GameOverviewData{
		GameID:      1,
		ActionError: "Failed to restart the deployment.",
		Deployments: []GameDeploymentOverview{
			{
				SGCID:         10,
				Status:        "Online",
				StatusVariant: "success",
				Uptime:        "1h 10m",
				CanStop:       true,
				CanRestart:    true,
			},
		},
	}

	body := renderPage(t, GameOverview(data))

	if !strings.Contains(body, "Failed to restart the deployment.") {
		t.Errorf("expected action error alert in overview, got: %s", body)
	}
	if !strings.Contains(body, "alert-error") {
		t.Errorf("expected alert-error class, got: %s", body)
	}
}
