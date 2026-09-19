package pages

import (
	"strings"
	"testing"
	manmanpb "github.com/whale-net/everything/manmanv2/protos"
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

func fixtureGameDetailPageData(isAdmin bool) GameDetailPageData {
	game := &manmanpb.Game{
		GameId:     42,
		Name:       "Left 4 Dead 2",
		SteamAppId: "550",
		Metadata: &manmanpb.GameMetadata{
			Genre:     "Shooter",
			Publisher: "Valve",
			Tags:      []string{"zombies", "co-op"},
		},
	}

	configs := []*manmanpb.GameConfig{
		{
			ConfigId: 101,
			GameId:   42,
			Name:     "default",
			Image:    "l4d2-server:latest",
		},
	}

	deployments := []GameDeploymentRow{
		{
			Row: DeploymentRowData{
				ServerGameConfigID: 201,
				DisplayName:        "default on server 1",
				SGCStatus:          "active",
				LatestSession: &manmanpb.Session{
					SessionId: 301,
					Status:    "running",
					StartedAt: 1700000000,
				},
				LiveSession: &manmanpb.Session{
					SessionId: 301,
					Status:    "running",
					StartedAt: 1700000000,
				},
				Actions: components.DeploymentActions{
					CanStop:    true,
					CanRestart: true,
				},
			},
			Connect: components.BuildConnectAddressView("192.168.1.50", []*manmanpb.PortBinding{{HostPort: 27015, Protocol: "udp"}}),
			LogsURL:    "/sessions/301",
			ActionsURL: "/games/42/configs/101/actions",
		},
	}

	return GameDetailPageData{
		Layout: components.LayoutData{
			Title:  "Left 4 Dead 2",
			Active: "Games",
		},
		Game:          game,
		IsAdmin:       isAdmin,
		ActiveTab:     "overview",
		RunState:      components.DeploymentRunning,
		Connect:       components.BuildConnectAddressView("192.168.1.50", []*manmanpb.PortBinding{{HostPort: 27015, Protocol: "udp"}}),
		Deployments:   deployments,
		LatestSession: deployments[0].Row.LatestSession,
		LiveSession:   deployments[0].Row.LiveSession,
		Sessions: []*manmanpb.Session{
			deployments[0].Row.LatestSession,
		},
		Configs: configs,
		SgcCounts: map[int64]int{
			101: 1,
		},
		PathPresets: []*manmanpb.GameAddonPathPreset{
			{
				PresetId:         501,
				GameId:           42,
				Name:             "Maps",
				InstallationPath: "left4dead2/maps/",
			},
		},
		Overview: GameOverviewData{
			GameID: 42,
			Deployments: []GameDeploymentOverview{
				{
					SGCID:         201,
					DisplayName:   "default on server 1",
					Status:        "Online",
					StatusVariant: "success",
					Uptime:        "running",
					Connect:       components.BuildConnectAddressView("192.168.1.50", []*manmanpb.PortBinding{{HostPort: 27015, Protocol: "udp"}}),
					CanStop:       true,
					CanRestart:    true,
					LogsURL:       "/sessions/301",
				},
			},
		},
	}
}

func TestGameDetail_TabbedLayout_AdminRendersAllTabs(t *testing.T) {
	data := fixtureGameDetailPageData(true)
	body := renderPage(t, GameDetail(data))

	// All four tab buttons must be rendered for admin
	for _, wantTab := range []string{"Overview", "Console &amp; Logs", "Configuration", "Advanced"} {
		if !strings.Contains(body, wantTab) {
			t.Errorf("expected tab button %q in body, got: %s", wantTab, body)
		}
	}

	// Tab panel IDs must be present
	for _, wantPanel := range []string{"tab-panel-overview", "tab-panel-logs", "tab-panel-configuration", "tab-panel-advanced"} {
		if !strings.Contains(body, `id="`+wantPanel+`"`) {
			t.Errorf("expected tab panel %q in body, got: %s", wantPanel, body)
		}
	}
}

func TestGameDetail_TabbedLayout_NonAdminHidesConfigAndAdvanced(t *testing.T) {
	data := fixtureGameDetailPageData(false)
	body := renderPage(t, GameDetail(data))

	// Overview and Console & Logs must be present
	if !strings.Contains(body, "Overview") {
		t.Errorf("expected Overview tab for non-admin, got: %s", body)
	}
	if !strings.Contains(body, "Console &amp; Logs") {
		t.Errorf("expected Console & Logs tab for non-admin, got: %s", body)
	}

	// Configuration and Advanced tab buttons must NOT be rendered
	if strings.Contains(body, `id="tab-btn-configuration"`) {
		t.Errorf("Configuration tab button must NOT be rendered for non-admin, got: %s", body)
	}
	if strings.Contains(body, `id="tab-btn-advanced"`) {
		t.Errorf("Advanced tab button must NOT be rendered for non-admin, got: %s", body)
	}

	// Configuration and Advanced tab panels must NOT be present in DOM
	if strings.Contains(body, `id="tab-panel-configuration"`) {
		t.Errorf("Configuration tab panel must NOT exist for non-admin, got: %s", body)
	}
	if strings.Contains(body, `id="tab-panel-advanced"`) {
		t.Errorf("Advanced tab panel must NOT exist for non-admin, got: %s", body)
	}
}

func TestGameDetail_Overview_SeparatesLowFrequencyActions(t *testing.T) {
	data := fixtureGameDetailPageData(true)
	body := renderPage(t, GameDetail(data))

	// Find the Overview panel content
	startIdx := strings.Index(body, `id="tab-panel-overview"`)
	if startIdx == -1 {
		t.Fatalf("missing tab-panel-overview")
	}
	endIdx := strings.Index(body, `id="tab-panel-logs"`)
	if endIdx == -1 {
		t.Fatalf("missing tab-panel-logs")
	}
	overviewContent := body[startIdx:endIdx]

	// Overview MUST contain status, runtime, and start/stop/restart controls
	if !strings.Contains(overviewContent, "Server Status &amp; Controls") {
		t.Errorf("Overview must have Server Status & Controls section, got: %s", overviewContent)
	}
	if !strings.Contains(overviewContent, "192.168.1.50:27015") {
		t.Errorf("Overview must show connect address, got: %s", overviewContent)
	}
	if !strings.Contains(overviewContent, "Stop") {
		t.Errorf("Overview must show Stop control, got: %s", overviewContent)
	}
	if !strings.Contains(overviewContent, "Restart") {
		t.Errorf("Overview must show Restart control, got: %s", overviewContent)
	}

	// Overview MUST NOT contain "Edit Configuration" or "Deploy" (AC: separate low-frequency actions)
	if strings.Contains(overviewContent, "Edit Configuration") {
		t.Errorf("Overview must NOT contain 'Edit Configuration' (moved to Configuration tab), got: %s", overviewContent)
	}
	if strings.Contains(overviewContent, "Deploy to Server") || strings.Contains(overviewContent, "+ Deploy") {
		t.Errorf("Overview must NOT contain Deploy controls (moved to Advanced tab), got: %s", overviewContent)
	}
}

func TestGameDetail_ConfigurationTab_HasEditConfiguration(t *testing.T) {
	data := fixtureGameDetailPageData(true)
	body := renderPage(t, GameDetail(data))

	startIdx := strings.Index(body, `id="tab-panel-configuration"`)
	if startIdx == -1 {
		t.Fatalf("missing tab-panel-configuration")
	}
	endIdx := strings.Index(body, `id="tab-panel-advanced"`)
	if endIdx == -1 {
		t.Fatalf("missing tab-panel-advanced")
	}
	configContent := body[startIdx:endIdx]

	// Must contain Game Configurations, Edit Configuration, and Path Presets
	if !strings.Contains(configContent, "Game Configurations") {
		t.Errorf("Configuration tab must have Game Configurations section, got: %s", configContent)
	}
	if !strings.Contains(configContent, "Edit Configuration") {
		t.Errorf("Configuration tab must have 'Edit Configuration' button, got: %s", configContent)
	}
	if !strings.Contains(configContent, "Addon Path Presets") {
		t.Errorf("Configuration tab must have Addon Path Presets section, got: %s", configContent)
	}
}

func TestGameDetail_AdvancedTab_HasDeployAndDangerZone(t *testing.T) {
	data := fixtureGameDetailPageData(true)
	body := renderPage(t, GameDetail(data))

	startIdx := strings.Index(body, `id="tab-panel-advanced"`)
	if startIdx == -1 {
		t.Fatalf("missing tab-panel-advanced")
	}
	advancedContent := body[startIdx:]

	// Must contain Deploy button, Edit Game form, and Danger Zone
	if !strings.Contains(advancedContent, "Deploy to Server") {
		t.Errorf("Advanced tab must have Deploy button, got: %s", advancedContent)
	}
	if !strings.Contains(advancedContent, "Edit Game Details") {
		t.Errorf("Advanced tab must have Edit Game Details form, got: %s", advancedContent)
	}
	if !strings.Contains(advancedContent, "Danger Zone") {
		t.Errorf("Advanced tab must have Danger Zone section, got: %s", advancedContent)
	}
	if !strings.Contains(advancedContent, "Delete Game") {
		t.Errorf("Advanced tab must have Delete Game button, got: %s", advancedContent)
	}
}

// TestGameDetail_ConfigurationTab_HasConfigEditorTrigger guards the Config
// Editor blade's only entry point (root plan #2266, task #2276, FR13):
// once the Games list row's own Configurations section (data-config-
// editor-trigger, #2273) was trimmed away for a quick-glance list, this
// admin-gated Configuration tab became its sole home.
func TestGameDetail_ConfigurationTab_HasConfigEditorTrigger(t *testing.T) {
	data := fixtureGameDetailPageData(true)
	body := renderPage(t, GameDetail(data))

	if !strings.Contains(body, "data-config-editor-trigger") {
		t.Errorf("Configuration tab must carry an Edit control opening the Config Editor blade, got: %s", body)
	}
	if !strings.Contains(body, `hx-get="/games/42/configs/101/editor"`) {
		t.Errorf("expected the Edit control to hx-get the Config Editor route, got: %s", body)
	}
}

// TestGameDetail_ConfigurationTab_HasWorkshopLibraries guards that the
// Workshop Libraries panel (task #2367, FR8/FR9/FR10) is reachable here:
// it moved off the Games list row entirely, so this admin-gated
// Configuration tab is now its only home.
func TestGameDetail_ConfigurationTab_HasWorkshopLibraries(t *testing.T) {
	data := fixtureGameDetailPageData(true)
	body := renderPage(t, GameDetail(data))

	if !strings.Contains(body, "Workshop Libraries") {
		t.Errorf("Configuration tab must contain the Workshop Libraries panel, got: %s", body)
	}
	if !strings.Contains(body, `hx-get="/games/42/workshop-panel"`) {
		t.Errorf("expected the Workshop Libraries panel to lazily fetch its content, got: %s", body)
	}
}
