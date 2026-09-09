package pages

import (
	"strings"
	"testing"
)

// This file guards task #2273 (root plan #2266): the expanded game row's
// Configurations (FR7), read-only Workshop Libraries (FR8), and
// settings/danger footer (FR9) sections. It renders gameConfigsSection,
// gameWorkshopSection, and gameFooter directly against hand-built GameRow
// fixtures -- these three are unexported templ funcs in this package, so
// no HTTP round trip or handler wiring is needed to guard their markup;
// handlers_games_test.go (package main) separately guards buildGameRows'
// join (FR7's per-config deployment count, no cross-game leakage) and
// NFR7's constant-call-count bound.
//
// mutation-tested (verified red, by hand, then reverted): adding a
// <th>Port</th> column and a { fmt.Sprintf("%d", 0) } port cell to
// gameConfigsSection in games.templ made
// TestGameConfigsSection_WD10_NoPortColumnOrValue fail on its "port"
// substring assertion; reverting restored green.

func fixtureConfigRow(gameID, configID int64, name, image string, deploymentCount int) ConfigRowView {
	return ConfigRowView{
		GameID:          gameID,
		ConfigID:        configID,
		Name:            name,
		Image:           image,
		DeploymentCount: deploymentCount,
	}
}

// --- Configurations section (FR7, WD10, WD7) -------------------------------

func TestGameConfigsSection_FR7_NameImageDeploymentCount(t *testing.T) {
	row := GameRow{
		GameID: 1,
		Configs: []ConfigRowView{
			fixtureConfigRow(1, 10, "Survival", "itzg/minecraft-server:latest", 3),
			// Zero-deployment case (FR7 explicitly calls this out).
			fixtureConfigRow(1, 11, "Creative", "itzg/minecraft-server:creative", 0),
		},
	}
	body := renderPage(t, gameConfigsSection(row))

	for _, want := range []string{"Survival", "itzg/minecraft-server:latest", "Creative", "itzg/minecraft-server:creative"} {
		if !strings.Contains(body, want) {
			t.Errorf("Configurations section missing %q, got: %s", want, body)
		}
	}
	// Deployment counts: 3 for Survival, 0 for Creative (the zero case
	// must render as "0", not be omitted or blank).
	if !strings.Contains(body, "<td>3</td>") {
		t.Errorf("Configurations section missing deployment count cell '3' for Survival, got: %s", body)
	}
	if !strings.Contains(body, "<td>0</td>") {
		t.Errorf("Configurations section missing deployment count cell '0' for the zero-deployment Creative config, got: %s", body)
	}
}

func TestGameConfigsSection_EmptyState(t *testing.T) {
	body := renderPage(t, gameConfigsSection(GameRow{GameID: 1}))
	if !strings.Contains(body, "No configurations yet.") {
		t.Errorf("expected empty-state text for a game with no configs, got: %s", body)
	}
}

// TestGameConfigsSection_WD10_NoPortColumnOrValue guards WD10: the
// Configurations section must never render a port column or a port value
// -- port_bindings live on ServerGameConfig (a deployment), never on
// GameConfig.
func TestGameConfigsSection_WD10_NoPortColumnOrValue(t *testing.T) {
	row := GameRow{
		GameID: 1,
		Configs: []ConfigRowView{
			fixtureConfigRow(1, 10, "Survival", "itzg/minecraft-server:latest", 2),
		},
	}
	body := renderPage(t, gameConfigsSection(row))

	if strings.Contains(strings.ToLower(body), "port") {
		t.Errorf("Configurations section must not mention 'port' anywhere (WD10), got: %s", body)
	}
}

// TestGameConfigsSection_WD7_DeployIsLinkNotForm guards WD7: the deploy
// control is a link out to today's existing create-deployment flow, never
// an inline form that starts anything.
func TestGameConfigsSection_WD7_DeployIsLinkNotForm(t *testing.T) {
	row := GameRow{
		GameID: 1,
		Configs: []ConfigRowView{
			fixtureConfigRow(1, 10, "Survival", "itzg/minecraft-server:latest", 2),
		},
	}
	body := renderPage(t, gameConfigsSection(row))

	if !strings.Contains(body, `href="/games/1/configs/10"`) {
		t.Errorf("expected an anchor linking to the existing create-deployment flow (/games/1/configs/10), got: %s", body)
	}
	if strings.Contains(body, "<form") {
		t.Errorf("Configurations section must not contain a <form> -- deploy is a link out (WD7), got: %s", body)
	}
}

// --- Workshop Libraries section (FR8, LB6 guard, WD5) -----------------------

func TestGameWorkshopSection_FR8_ListsLibrariesAndM6Note(t *testing.T) {
	row := GameRow{
		GameID: 1,
		WorkshopLibraries: []WorkshopLibraryRow{
			{LibraryID: 1, LibraryName: "Better Maps", ConfigName: "Survival", Target: "data · valheim/maps/"},
		},
	}
	body := renderPage(t, gameWorkshopSection(row))

	for _, want := range []string{"Better Maps", "Survival", "data · valheim/maps/"} {
		if !strings.Contains(body, want) {
			t.Errorf("Workshop Libraries section missing %q, got: %s", want, body)
		}
	}
	if !strings.Contains(body, "shared across configs and editable here in M6") {
		t.Errorf("Workshop Libraries section missing the visible M6 note, got: %s", body)
	}
}

func TestGameWorkshopSection_EmptyState(t *testing.T) {
	body := renderPage(t, gameWorkshopSection(GameRow{GameID: 1}))
	if !strings.Contains(body, "No workshop libraries attached.") {
		t.Errorf("expected empty-state text for a game with no workshop libraries, got: %s", body)
	}
	// The M6 note is visible even with an empty list.
	if !strings.Contains(body, "shared across configs and editable here in M6") {
		t.Errorf("Workshop Libraries empty state must still show the M6 note, got: %s", body)
	}
}

// TestGameWorkshopSection_LB6_NoAttachDetachEditControl is the load-bearing
// negative assertion: the panel only reads, so M5 must add no attach,
// detach, or edit control, and must issue no request to the retained
// /sgc/add-library or /sgc/remove-library routes (A2). Checked against a
// populated row -- an empty section trivially has no controls, which
// would not catch a regression that added a control only when there is
// data to act on.
func TestGameWorkshopSection_LB6_NoAttachDetachEditControl(t *testing.T) {
	row := GameRow{
		GameID: 1,
		WorkshopLibraries: []WorkshopLibraryRow{
			{LibraryID: 1, LibraryName: "Better Maps", ConfigName: "Survival", Target: "data · valheim/maps/"},
		},
	}
	body := renderPage(t, gameWorkshopSection(row))

	if strings.Contains(body, "<form") {
		t.Errorf("Workshop Libraries section must not contain a <form> (LB6: read-only), got: %s", body)
	}
	if strings.Contains(body, "<button") {
		t.Errorf("Workshop Libraries section must not contain a <button> -- no attach/detach/edit control (LB6), got: %s", body)
	}
	for _, forbidden := range []string{"/sgc/add-library", "/sgc/remove-library"} {
		if strings.Contains(body, forbidden) {
			t.Errorf("Workshop Libraries section must not reference retained route %q (LB6/A2), got: %s", forbidden, body)
		}
	}
}

// --- Settings/danger footer (FR9) -------------------------------------------

// TestGameFooter_FR9_LinksOutNoInlineDelete guards FR9's hard requirement:
// the footer links out to the existing settings and delete surfaces with
// their existing confirmation flows, never reproducing the destructive
// action inline or adding a one-click shortcut.
func TestGameFooter_FR9_LinksOutNoInlineDelete(t *testing.T) {
	body := renderPage(t, gameFooter(GameRow{GameID: 42}))

	if !strings.Contains(body, `href="/games/42#settings-presets"`) {
		t.Errorf("expected a link out to the existing settings/presets surface, got: %s", body)
	}
	if !strings.Contains(body, `href="/games/42#danger-zone"`) {
		t.Errorf("expected a link out to the existing delete-game (Danger Zone) surface, got: %s", body)
	}
	if strings.Contains(body, "<form") {
		t.Errorf("footer must not contain a <form> -- delete is a link out, never reproduced inline (FR9), got: %s", body)
	}
	if strings.Contains(body, "hx-post") || strings.Contains(body, "hx-delete") {
		t.Errorf("footer must not issue any request of its own -- no inline delete affordance (FR9), got: %s", body)
	}
}

// --- FR2 terminology, across all three sections -----------------------------

// TestGamesSections_FR2_NoSGCTerminology guards FR2 across all three
// sections this task owns, including the Workshop Libraries panel's M6
// note, which must say "Deployment"/"Game Config", never "SGC".
func TestGamesSections_FR2_NoSGCTerminology(t *testing.T) {
	row := GameRow{
		GameID: 1,
		Configs: []ConfigRowView{
			fixtureConfigRow(1, 10, "Survival", "itzg/minecraft-server:latest", 2),
		},
		WorkshopLibraries: []WorkshopLibraryRow{
			{LibraryID: 1, LibraryName: "Better Maps", ConfigName: "Survival", Target: "data · valheim/maps/"},
		},
	}

	body := renderPage(t, gameConfigsSection(row)) +
		renderPage(t, gameWorkshopSection(row)) +
		renderPage(t, gameFooter(row))

	lower := strings.ToLower(body)
	if strings.Contains(lower, "sgc") {
		t.Errorf("rendered sections contain %q (FR2 forbids SGC terminology in display text), got: %s", "sgc", body)
	}
	if strings.Contains(lower, "server game config") {
		t.Errorf("rendered sections contain %q (FR2 forbids raw entity terminology in display text), got: %s", "server game config", body)
	}
}
