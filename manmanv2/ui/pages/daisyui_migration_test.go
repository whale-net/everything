package pages

import (
	"context"
	"strings"
	"testing"

	"github.com/a-h/templ"
	manmanpb "github.com/whale-net/everything/manmanv2/protos"
	"github.com/whale-net/everything/manmanv2/ui/components"
)

// This file guards issue #1008's per-screen daisyUI/htmxui migration (FR4,
// FR7): nothing pre-existing rendered manmanv2/ui/pages at all -- there was
// no //manmanv2/ui/pages go_test target before this file -- so a regression
// in the migrated markup (a dropped hx-* attribute, a mismapped badge/button
// colour variant, a lost Alpine confirm-gate directive) would have compiled
// cleanly and shipped silently.
//
// Two migration shapes carry real regression risk here, both exercised
// below:
//
//  1. hx-* attributes that moved from literal HTML onto htmxui.Button's
//     `attrs templ.Attributes` escape hatch (session_detail.templ's action
//     execute/submit buttons). templ.Attributes is a map, so RenderAttributes emits keys
//     in sorted order (see a-h/templ's runtime.go Items()); a mutation
//     that dropped or renamed a key would silently stop reaching the
//     rendered attribute.
//  2. The state -> htmxui.BadgeVariant / ButtonVariant colour mappings
//     (statusBadgeVariant, actionButtonVariant) that replaced the old
//     badgeClasses/buttonStyleClass switches. A regression that remapped
//     e.g. "danger" onto a neutral/success colour is a silent operator-
//     facing defect (FR7's explicit "do not silently re-map a danger
//     state to a neutral badge" rule) with no compile-time signal.
//
// Also guarded: the Danger Zone sections (sgc_detail/game_detail/
// workshop_addon_detail/workshop_library_detail) deliberately did NOT
// adopt htmxui.Confirm -- collapsing their existing Alpine x-show
// click-to-reveal-then-confirm gate into Confirm's always-visible
// single-submit shape would itself be an observable behavior change,
// which FR7 forbids (see be60c947's commit message and each file's
// Danger Zone comment). TestWorkshopAddonDetail_DangerZoneKeepsAlpineGate
// proves that documented decision continues to hold: the x-show/@click
// gate must still wire to the restyled buttons, not have been silently
// dropped or migrated to Confirm's shape.
//
// mutation-tested (verified red, by hand, then reverted): swapping
// actionButtonVariant's "danger" case from htmxui.ButtonError to
// htmxui.ButtonWarning made TestSessionDetail_ActionButtonsPreserveHxWiring
// fail on the "btn btn-error" assertion, reverted to green afterward.

func renderPage(t *testing.T, c templ.Component) string {
	t.Helper()
	var buf strings.Builder
	if err := c.Render(context.Background(), &buf); err != nil {
		t.Fatalf("render failed: %v", err)
	}
	return buf.String()
}

// --- servers.templ / server_detail.templ ------------------------------
//
// Both pages were retired by #2372 (superseded by /infrastructure); their
// daisyUI-migration regression tests (TestServers_StatusBadgeVariantsAndViewButton,
// TestServerDetail_StartSessionButtonPreservesHxWiring) were removed along
// with the pages themselves. formatTime, previously defined alongside
// server_detail.templ, moved to helpers.go since session_detail.templ and
// workshop_cache.templ still use it.

// --- session_detail.templ: action button variant + hx-* wiring ------------

func TestSessionDetail_ActionButtonsPreserveHxWiring(t *testing.T) {
	session := &manmanpb.Session{SessionId: 7, Status: "running"}
	actions := []*manmanpb.ActionDefinition{
		{
			ActionId:             3,
			Label:                "Restart",
			ButtonStyle:          "danger",
			RequiresConfirmation: true,
			ConfirmationMessage:  "Really restart?",
		},
		{
			ActionId:    8,
			Label:       "Change Map",
			ButtonStyle: "primary",
			InputFields: []*manmanpb.ActionInputField{
				{Name: "map", Label: "Map", FieldType: "text", Required: true, Placeholder: "e.g. de_dust2"},
			},
		},
	}
	data := SessionDetailPageData{
		Layout:  components.LayoutData{Title: "Session"},
		Session: session,
		Actions: actions,
	}
	body := renderPage(t, SessionDetail(data))

	// Simple (no-input) action: actionButtonVariant("danger") -> ButtonError.
	if !strings.Contains(body, `class="btn btn-error"`) {
		t.Errorf("expected the Restart action button to render btn-error (danger), got %q", body)
	}
	if strings.Contains(body, `class="btn btn-warning"`) || strings.Contains(body, `class="btn btn-success"`) {
		t.Errorf("expected no warning/success button class from a danger-styled action, got %q", body)
	}
	wantExecuteAttrs := []string{
		`hx-post="/sessions/7/actions/execute"`,
		`hx-vals="{&#34;action_id&#34;: &#34;3&#34;}"`,
		`hx-target="#action-status"`,
		`hx-swap="innerHTML"`,
		`hx-confirm="Really restart?"`,
	}
	for _, want := range wantExecuteAttrs {
		if !strings.Contains(body, want) {
			t.Errorf("expected Restart action button to carry %s, got %q", want, body)
		}
	}

	// Parameterized action: actionButtonVariant("primary") -> ButtonPrimary,
	// no RequiresConfirmation -> no hx-confirm on the submit button.
	if !strings.Contains(body, `<button class="btn btn-primary" type="submit">`) {
		t.Errorf("expected the Change Map submit button to render btn-primary with only type=submit (no hx-confirm), got %q", body)
	}
	if !strings.Contains(body, `<input type="hidden" name="action_id" value="8">`) {
		t.Errorf("expected the parameterized form's action_id hidden input preserved, got %q", body)
	}
	if !strings.Contains(body, `name="map"`) {
		t.Errorf("expected the Map input field's name preserved, got %q", body)
	}
}

// --- workshop_addon_detail.templ: Danger Zone keeps its Alpine gate -------
//
// FR7: collapsing the existing click-to-reveal-then-confirm Alpine gate
// into htmxui.Confirm's always-visible single-submit shape would be an
// observable behavior change, so this migration deliberately left it in
// place (only chrome restyled). This guards that decision continues to
// hold -- a regression that dropped the x-show/@click directives (whether
// by accident or by "helpfully" migrating to Confirm) would ship a
// destructive action with no confirmation step.
func TestWorkshopAddonDetail_DangerZoneKeepsAlpineGate(t *testing.T) {
	addon := &manmanpb.WorkshopAddon{AddonId: 99, Name: "Test Addon"}
	data := WorkshopAddonDetailPageData{
		Layout: components.LayoutData{Title: "Addon"},
		Addon:  addon,
	}
	body := renderPage(t, WorkshopAddonDetail(data))

	if !strings.Contains(body, `<button x-show="!confirmDelete" @click="confirmDelete = true" class="btn btn-error">Delete Addon</button>`) {
		t.Errorf("expected the reveal button to keep its Alpine x-show/@click gate, got %q", body)
	}
	if !strings.Contains(body, `<div x-show="confirmDelete" x-cloak class="flex gap-2 items-center">`) {
		t.Errorf("expected the confirm panel to keep x-show/x-cloak gating, got %q", body)
	}
	if !strings.Contains(body, `<form method="POST" action="/workshop/delete-addon" class="inline">`) {
		t.Errorf("expected the delete form's method/action preserved, got %q", body)
	}
	if !strings.Contains(body, `<input type="hidden" name="addon_id" value="99">`) {
		t.Errorf("expected the addon_id hidden input preserved, got %q", body)
	}
	// No htmxui.Confirm markup (it renders a fixed "Type X to confirm" /
	// override-checkbox shape this page never had) should have crept in.
	if strings.Contains(body, "confirm-override") || strings.Contains(body, `type="checkbox" name="confirm_override"`) {
		t.Errorf("expected no htmxui.Confirm override-checkbox markup on this page, got %q", body)
	}
}
