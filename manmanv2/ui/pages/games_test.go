package pages

import (
	"strings"
	"testing"

	"github.com/whale-net/everything/manmanv2/ui/components"
)

// This file guards the Games list row (root plan #2266): the expanded row
// renders the Daily Ops Overview panel (GameOverview, game_detail.templ --
// status, runtime, connect, and per-deployment Start/Stop/Restart/
// Customize/Console Commands) plus a "View More" link out to GameDetail.
// The Configurations (formerly FR7, task #2273) and settings/danger footer
// (formerly FR9, task #2273) sections that used to render here moved to
// GameDetail (game_detail.templ) -- a quick-glance list shouldn't require
// parsing four sections to find the actionable controls. gameWorkshopPlaceholder
// (task #2367) moved there too; its coverage now lives in game_detail_test.go
// alongside it.

// --- Expanded row: Daily Ops Overview + View More only -----------------------

func TestGameRow_Expanded_ViewMoreLinksToGameDetail(t *testing.T) {
	row := GameRow{GameID: 42, Name: "Valheim"}
	body := renderPage(t, gameRow(row, true))

	if !strings.Contains(body, `href="/games/42"`) {
		t.Errorf("expected a View More link out to /games/42, got: %s", body)
	}
	if !strings.Contains(body, "View More") {
		t.Errorf("expected a View More affordance, got: %s", body)
	}
}

// TestGameRow_Expanded_NoConfigsWorkshopOrFooter guards the declutter: the
// expanded row must not carry the Configurations table, the Workshop
// Libraries panel, or the settings/danger footer -- those now require
// following View More to GameDetail instead.
func TestGameRow_Expanded_NoConfigsWorkshopOrFooter(t *testing.T) {
	row := GameRow{GameID: 1, Name: "Valheim"}
	body := renderPage(t, gameRow(row, true))

	for _, unwanted := range []string{">Configurations<", ">Workshop Libraries<", "settings &amp; presets", "Delete Game"} {
		if strings.Contains(body, unwanted) {
			t.Errorf("expanded row must not contain %q -- it belongs on GameDetail now, got: %s", unwanted, body)
		}
	}
}

// TestGameRow_Expanded_DailyOpsOverviewStillRenders guards that the
// actionable Daily Ops Overview panel (start/stop/restart) is still
// rendered directly in the list, not gated behind an additional
// navigation.
func TestGameRow_Expanded_DailyOpsOverviewStillRenders(t *testing.T) {
	row := GameRow{
		GameID: 1,
		Name:   "Valheim",
		Overview: GameOverviewData{
			GameID: 1,
			Deployments: []GameDeploymentOverview{
				{
					SGCID:       100,
					DisplayName: "Survival on host-01",
					Status:      "Offline",
					CanStart:    true,
				},
			},
		},
	}
	body := renderPage(t, gameRow(row, true))

	if !strings.Contains(body, "Survival on host-01") {
		t.Errorf("expected the deployment's display name to render in the expanded row, got: %s", body)
	}
}

// TestGameRow_HeaderMarkupAndNoNestedButtons guards the game row header markup:
// the row header must be a div[role="button"], not a <button>, to prevent
// invalid HTML button-inside-button nesting when ConnectAddressDisplay renders
// its copy button. It also ensures @click.stop isolates the connect address
// from triggering row expansion.
func TestGameRow_HeaderMarkupAndNoNestedButtons(t *testing.T) {
	row := GameRow{
		GameID:   1,
		Name:     "Valheim",
		RunState: components.DeploymentRunning,
		Connect: components.ConnectAddressView{
			Addresses: []components.ConnectAddress{
				{Address: "203.0.113.7:2456", Protocol: "UDP"},
			},
		},
	}
	body := renderPage(t, gameRow(row, false))

	if strings.Contains(body, `<button type="button" @click="expanded = !expanded"`) ||
		strings.Contains(body, `<button @click="expanded = !expanded"`) {
		t.Errorf("gameRow header must not be a <button> element (prevents button-inside-button HTML nesting), got: %s", body)
	}
	if !strings.Contains(body, `role="button"`) {
		t.Errorf("gameRow header must have role=\"button\" for accessibility, got: %s", body)
	}
	if !strings.Contains(body, `@click="expanded = !expanded"`) {
		t.Errorf("gameRow header must bind @click to toggle expanded, got: %s", body)
	}
	if !strings.Contains(body, `@click.stop`) {
		t.Errorf("gameRow header must isolate connect address with @click.stop, got: %s", body)
	}
}

// TestOverviewDeploymentContent_ConsoleCommandsAndCustomize verifies that
// the ops panel's per-deployment content (shared by the Games list row and
// GameDetail's Overview tab) carries the Console Commands link and
// Customize control folded in from the retired games.templ gameDeploymentRow,
// using explicit terminology with no ambiguous standalone "Actions" link.
func TestOverviewDeploymentContent_ConsoleCommandsAndCustomize(t *testing.T) {
	dep := GameDeploymentOverview{
		SGCID:       10,
		DisplayName: "Default on host-01",
		Status:      "Online",
		CanStop:     true,
		CanRestart:  true,
		Connect:     components.ConnectAddressView{Addresses: []components.ConnectAddress{{Address: "1.2.3.4:2456", Protocol: "UDP"}}},
		LogsURL:     "/sessions/42",
		ActionsURL:  "/games/1/configs/2/actions",
	}

	body := renderPage(t, overviewDeploymentContent(1, dep))

	// 1. Console Commands link exists and points to ActionsURL.
	if !strings.Contains(body, ">Console Commands<") {
		t.Errorf("expected 'Console Commands' link text, got: %s", body)
	}
	if !strings.Contains(body, `href="/games/1/configs/2/actions"`) {
		t.Errorf("expected ActionsURL link href, got: %s", body)
	}

	// 2. Ambiguous standalone "Actions" link is not present.
	if strings.Contains(body, ">Actions<") {
		t.Errorf("expected no ambiguous '>Actions<' link text, got: %s", body)
	}

	// 3. Customize control and its deployment settings blade are present.
	if !strings.Contains(body, ">Customize<") {
		t.Errorf("expected 'Customize' button, got: %s", body)
	}
	if !strings.Contains(body, `data-open-blade-template="deployment-settings-blade-template-10"`) {
		t.Errorf("expected Customize to open the deployment settings blade template, got: %s", body)
	}

	// 4. Diagnostics link to live output/logs is present.
	if !strings.Contains(body, "View Live Output / Logs") {
		t.Errorf("expected 'View Live Output / Logs' diagnostics link, got: %s", body)
	}
}

