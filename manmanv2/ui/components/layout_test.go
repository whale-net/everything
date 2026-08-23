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

// TestLayout_ThemeSwitcherOffersAllThemes is the direct FR6/FR10
// reachability guard: proves manmanv2's own chrome (not just the shared
// htmxui.ThemeSwitcher component in isolation) actually offers OLED and
// sunset as selectable themes, keyed to the [data-theme="..."] rules
// libs/go/htmxui/themes.css defines (oled distinct from night -- see
// themes.css base-100/base-200/neutral values, which differ between the two
// rules; sunset added by #1012 as FR10's POC theme, then wired into
// manmanv2's own manmanThemes call site by #1086 to match
// tools/app_registry/ui/components/layout.templ's shellThemes -- see #1085's
// finding that manmanv2's switcher had fallen behind app-registry's). Under
// the pre-migration bug (FR6), OLED and Night rendered identically because
// the old themeManagement() script only ever toggled a `dark` class; this
// asserts the exact per-theme control markup htmxui.ThemeSwitcher emits for
// manmanThemes, so a regression that dropped a theme from manmanThemes
// (layout.templ) -- reintroducing a defect at the call site even with the
// shared component fixed -- would fail here.
//
// Red/green (verified by hand): temporarily removing the oled entry from
// manmanThemes in layout.templ makes this test fail with "expected exactly
// 1 occurrence" for the oled control; restoring it makes the test pass
// again. Same for sunset (#1086): removing its entry from manmanThemes made
// this test fail with "expected exactly 1 occurrence" for the sunset
// control before the fix landed; restoring it (matching shellThemes) makes
// it pass.
func TestLayout_ThemeSwitcherOffersAllThemes(t *testing.T) {
	body := renderLayout(t, LayoutData{Title: "Dashboard"})

	wantControls := []string{
		`<li><button type="button" data-htmxui-theme-value="light">☀️ Light</button></li>`,
		`<li><button type="button" data-htmxui-theme-value="night">🌙 Night</button></li>`,
		`<li><button type="button" data-htmxui-theme-value="oled">⚫ OLED Night</button></li>`,
		`<li><button type="button" data-htmxui-theme-value="sunset">🌅 Sunset</button></li>`,
	}
	for _, want := range wantControls {
		if got := strings.Count(body, want); got != 1 {
			t.Errorf("expected theme control %q exactly once, got %d occurrences in %q", want, got, body)
		}
	}
	// Exactly four theme controls -- guards against an extra/duplicate
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
// dropdown), and the logout affordance must all still reach the rendered
// page. Nav items and the server selector each render twice (once for the
// wide-viewport menu, once folded into the mobile dropdown -- see
// navItems' doc comment in layout.templ), so this asserts an exact count
// of 2 for those, not mere presence, to guard against a regression that
// dropped one of the two responsive copies.
//
// The logout affordance is different since #1011 (FR8): manmanv2 no
// longer hand-rolls a desktop link plus a separate mobile-menu copy --
// both are replaced by a single shared htmxui.UserMenu instance, whose
// own dropdown already covers narrow viewports (see UserMenu's doc
// comment in libs/go/htmxui/user_menu.templ). So the identity label, the
// logout link, and its href must each appear exactly once, not twice --
// asserting a count of 2 here (the pre-#1011 shape) would silently mask a
// regression that reintroduced the retired mobile-duplicate link.
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

	// Logout affordance: a single shared htmxui.UserMenu instance covers
	// both desktop and mobile (FR8), so identity, the logout link, and its
	// href must each appear exactly once -- not twice, which would be the
	// pre-#1011 hand-rolled-duplicate shape.
	if got := strings.Count(body, "data-htmxui-user-menu"); got != 1 {
		t.Errorf("expected exactly one htmxui.UserMenu instance, got %d occurrences in %q", got, body)
	}
	if got := strings.Count(body, `aria-label="Account menu">Alice</div>`); got != 1 {
		t.Errorf("expected identity label %q exactly once, got %d occurrences in %q", "Alice", got, body)
	}
	if got := strings.Count(body, `<li><a href="/auth/logout">Logout</a></li>`); got != 1 {
		t.Errorf("expected logout affordance exactly once, got %d occurrences in %q", got, body)
	}
	if got := strings.Count(body, `href="/auth/logout"`); got != 1 {
		t.Errorf("expected logout href exactly once, got %d occurrences in %q", got, body)
	}
}

// TestLayout_ServerSelectorComposesAlongsideUserMenu is manmanv2's
// call-site guard for FR8's composition-not-folding rule: headerRight
// (layout.templ) must render ServerSelector and htmxui.UserMenu as
// siblings in the HeaderRight slot, never with ServerSelector's markup
// folded inside UserMenu's own dropdown container. This is the manmanv2
// counterpart to libs/go/htmxui/htmxui_test.go's
// TestUserMenu_ComposesAlongsideHeaderRightSlot (which guards the shared
// component's composition contract in isolation); this test instead
// proves manmanv2's actual Layout() call site upholds it, so a regression
// that nested ServerSelector inside the @htmxui.UserMenu(...) call in
// headerRight -- rather than keeping it a sibling -- would fail here even
// if the shared component's own contract test still passed.
//
// Red/green (verified by hand): temporarily moving the
// `<div class="hidden lg:flex items-center gap-2">...ServerSelector...</div>`
// block in headerRight (layout.templ) to after the
// @htmxui.UserMenu(...) call but keeping it inside UserMenu's own
// <ul>...</ul> dropdown-content -- i.e. actually folding it into
// UserMenu's rendered dropdown rather than passing it as a component
// parameter -- makes this test fail with "expected ServerSelector to
// render outside UserMenu's dropdown"; reverting restores green.
func TestLayout_ServerSelectorComposesAlongsideUserMenu(t *testing.T) {
	servers := []*manmanpb.Server{
		{ServerId: 1, Name: "Alpha", IsDefault: true},
	}
	body := renderLayout(t, LayoutData{
		Title:          "Dashboard",
		User:           &htmxauth.UserInfo{Name: "Alice"},
		Servers:        servers,
		SelectedServer: servers[0],
	})

	menuOpenIdx := strings.Index(body, "data-htmxui-user-menu")
	// UserMenu's own dropdown closes right after its logout link -- see
	// UserMenu's exact markup in libs/go/htmxui/user_menu.templ -- so
	// anchoring the close boundary there (rather than on the next
	// sibling's markup) ties this test to UserMenu's own contract, not to
	// ThemeSwitcher happening to render immediately afterward.
	logoutLinkIdx := strings.Index(body, `<li><a href="/auth/logout">Logout</a></li>`)
	selectIdx := strings.Index(body, `<select name="server_id"`)
	if menuOpenIdx == -1 || logoutLinkIdx == -1 || selectIdx == -1 {
		t.Fatalf("expected UserMenu dropdown, its logout link, and ServerSelector's <select> all present, got %q", body)
	}
	menuCloseIdx := logoutLinkIdx + len(`<li><a href="/auth/logout">Logout</a></li></ul>`)
	if selectIdx > menuOpenIdx && selectIdx < menuCloseIdx {
		t.Errorf("expected ServerSelector to render outside UserMenu's dropdown (composed as a sibling, not folded in per FR8), got %q", body)
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
