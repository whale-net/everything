package components

import (
	"context"
	"strings"
	"testing"

	"github.com/whale-net/everything/libs/go/htmxauth"
	manmanpb "github.com/whale-net/everything/manmanv2/protos"
)

// This file guards issue #1007's chrome migration onto htmxui.Shell/
// htmxui.ThemeSwitcher: that Layout() actually wires manmanv2's nav, server
// selector, and logout affordance through Shell's app-owned slots (FR1a/
// FR4), that OLED is reachable as a real switchable option through
// manmanv2's own chrome (not merely supported by the shared component,
// FR6), and that no script Layout emits calls location.reload() (FR5).
// htmxui.ThemeSwitcher's own rendering contract (one control per theme,
// data-theme attribute wiring, no-reload script body) is covered directly
// by libs/go/htmxui/htmxui_test.go; this file only proves manmanv2's call
// site actually reaches it with the right theme list and that manmanv2's
// own scripts (templ_render.go's bootstrap script, layout.templ's
// toastScript) don't reintroduce the reload manmanv2 used to have.

func renderLayout(t *testing.T, data LayoutData) string {
	t.Helper()
	var buf strings.Builder
	if err := Layout(data).Render(context.Background(), &buf); err != nil {
		t.Fatalf("Layout render failed: %v", err)
	}
	return buf.String()
}

// TestLayout_ThemeSwitcherOffersLightNightOled is the direct FR6
// reachability guard: proves manmanv2's own chrome (not just the shared
// htmxui.ThemeSwitcher component in isolation) actually offers OLED as a
// selectable theme, keyed to the same [data-theme="oled"] palette
// libs/go/htmxui/themes.css defines (distinct from night -- see themes.css
// base-100/base-200/neutral values, which differ between the two rules).
// Under the pre-migration bug (FR6), OLED and Night rendered identically
// because the old themeManagement() script only ever toggled a `dark`
// class; this asserts the exact per-theme control markup htmxui.ThemeSwitcher
// emits for manmanThemes, so a regression that dropped "oled" from
// manmanThemes (layout.templ) -- reintroducing the defect at the call site
// even with the shared component fixed -- would fail here.
//
// Red/green (verified by hand): temporarily removing the oled entry from
// manmanThemes in layout.templ makes this test fail with "expected exactly
// 1 occurrence" for the oled control; restoring it makes the test pass
// again.
func TestLayout_ThemeSwitcherOffersLightNightOled(t *testing.T) {
	body := renderLayout(t, LayoutData{Title: "Dashboard"})

	wantControls := []string{
		`<li><button type="button" data-htmxui-theme-value="light">☀️ Light</button></li>`,
		`<li><button type="button" data-htmxui-theme-value="night">🌙 Night</button></li>`,
		`<li><button type="button" data-htmxui-theme-value="oled">⚫ OLED Night</button></li>`,
	}
	for _, want := range wantControls {
		if got := strings.Count(body, want); got != 1 {
			t.Errorf("expected theme control %q exactly once, got %d occurrences in %q", want, got, body)
		}
	}
	// Exactly three theme controls -- guards against an extra/duplicate
	// theme entry sneaking into manmanThemes.
	if got := strings.Count(body, "data-htmxui-theme-value=\""); got != len(wantControls) {
		t.Errorf("expected exactly %d theme controls, got %d in %q", len(wantControls), got, body)
	}
	// The switcher's click handler must actually set data-theme on <html> --
	// proves the OLED control above is wired to a live theme-application
	// path, not just rendered as inert markup.
	if !strings.Contains(body, `root.setAttribute("data-theme", theme);`) {
		t.Errorf("expected theme switcher click handler to set data-theme, got %q", body)
	}
}

// TestLayout_NoLocationReloadInEmittedScripts is the direct FR5 guard on
// manmanv2's actual rendered chrome: the pre-migration themeManagement()
// script called location.reload() on every theme change (worked around the
// old Tailwind CDN build needing a fresh stylesheet recompute); that
// behavior must not survive anywhere in Layout's emitted output --
// htmxui.ThemeSwitcher's script (already guarded independently by
// htmxui_test.go's TestThemeSwitcher_NoLocationReload) and manmanv2's own
// toastScript both included. Checks for the exact call substring
// "location.reload(", not the bare word "reload", which would also flag a
// hypothetical differently-named identifier and produce a false failure.
//
// Red/green (verified by hand): temporarily adding
// `document.location.reload();` inside toastScript's event listener in
// layout.templ makes this test fail with "expected no location.reload()
// call"; removing it again restores green.
func TestLayout_NoLocationReloadInEmittedScripts(t *testing.T) {
	body := renderLayout(t, LayoutData{
		Title: "Dashboard",
		User:  &htmxauth.UserInfo{Name: "Alice"},
	})
	if strings.Contains(body, "location.reload(") {
		t.Errorf("expected no location.reload() call anywhere in Layout's emitted scripts (FR5), got %q", body)
	}
}

// TestLayout_RendersNavItemsServerSelectorAndLogoutAffordance proves the
// FR1a slot wiring didn't drop manmanv2's own chrome content when Layout
// switched from a hand-rolled navbar to composing htmxui.Shell: the nav
// item list, the server selector (desktop HeaderRight slot + mobile nav
// dropdown), and the existing logout link (kept working via the slot per
// #1007's scope, Phase 3 unifies it onto FR8's UserMenu) must all still
// reach the rendered page. Each renders twice (once for the wide-viewport
// menu, once folded into the mobile dropdown -- see navItems' doc comment
// in layout.templ), so this asserts an exact count of 2, not mere
// presence, to guard against a regression that dropped one of the two
// responsive copies.
func TestLayout_RendersNavItemsServerSelectorAndLogoutAffordance(t *testing.T) {
	servers := []*manmanpb.Server{
		{ServerId: 1, Name: "Alpha", IsDefault: true},
		{ServerId: 2, Name: "Beta"},
	}
	data := LayoutData{
		Title:          "Dashboard",
		User:           &htmxauth.UserInfo{Name: "Alice"},
		Servers:        servers,
		SelectedServer: servers[0],
	}
	body := renderLayout(t, data)

	// No Active label set above, so every nav item renders in its plain
	// (non-active) form.
	wantNavItems := []string{
		`<li><a href="/">Dashboard</a></li>`,
		`<li><a href="/servers">Servers</a></li>`,
		`<li><a href="/games">Games</a></li>`,
		`<li><a href="/sessions">Sessions</a></li>`,
		`<li><a href="/workshop/library">Workshop</a></li>`,
	}
	for _, want := range wantNavItems {
		if got := strings.Count(body, want); got != 2 {
			t.Errorf("expected nav item %q exactly twice (desktop + mobile), got %d occurrences in %q", want, got, body)
		}
	}

	// Active nav item (matched by data.Active) renders with the
	// menu-active class instead of the plain <a> markup asserted above --
	// proves navLink's active-comparison semantics still reach the
	// rendered page through the FR1a Nav slot.
	activeBody := renderLayout(t, LayoutData{Title: "Dashboard", Active: "Dashboard"})
	wantActive := `<li><a href="/" class="menu-active">Dashboard</a></li>`
	if got := strings.Count(activeBody, wantActive); got != 2 {
		t.Errorf("expected active nav item %q exactly twice, got %d occurrences in %q", wantActive, got, activeBody)
	}
	if got := strings.Count(activeBody, `<li><a href="/">Dashboard</a></li>`); got != 0 {
		t.Errorf("expected no plain (non-active) Dashboard link when it is the active item, got %d occurrences in %q", got, activeBody)
	}

	// Server selector: one <select> for desktop (HeaderRight slot), one for
	// mobile (folded into the nav dropdown).
	if got := strings.Count(body, `<select name="server_id"`); got != 2 {
		t.Errorf("expected server selector <select> exactly twice (desktop + mobile), got %d in %q", got, body)
	}
	if got := strings.Count(body, "Alpha"); got != 2 {
		t.Errorf("expected selected server option %q exactly twice, got %d occurrences in %q", "Alpha", got, body)
	}
	if got := strings.Count(body, "Beta"); got != 2 {
		t.Errorf("expected server option %q exactly twice, got %d occurrences in %q", "Beta", got, body)
	}

	// Logout affordance: desktop HeaderRight slot + mobile nav dropdown.
	if got := strings.Count(body, "Logout (Alice)"); got != 2 {
		t.Errorf("expected logout affordance exactly twice, got %d occurrences in %q", got, body)
	}
	if got := strings.Count(body, `href="/auth/logout"`); got != 2 {
		t.Errorf("expected logout href exactly twice, got %d occurrences in %q", got, body)
	}
}

// TestLayout_NilUserOmitsLogoutAffordance guards the signed-out path (nil
// User): Layout must not render a dangling logout link with no user behind
// it.
func TestLayout_NilUserOmitsLogoutAffordance(t *testing.T) {
	body := renderLayout(t, LayoutData{Title: "Dashboard"})
	if strings.Contains(body, "/auth/logout") {
		t.Errorf("expected no logout affordance when User is nil, got %q", body)
	}
}
