package components

import (
	"context"
	"strings"
	"testing"

	"github.com/whale-net/everything/libs/go/htmxauth"
)

// This file guards the FR2/FR1a wiring for Shell()'s adoption of
// htmxui.Shell (issue #1005) and the FR8 wiring for htmxui.UserMenu /
// htmxui.ThemeSwitcher (issue #1010): Shell is a thin wrapper that hands
// its own nav list, MisconfigBanner, and user-identity + logout control to
// htmxui.Shell's app-owned slots (ShellData.Nav/Banner/HeaderRight) rather
// than rendering its own chrome inline. Nothing here exercises
// htmxui.Shell/UserMenu/ThemeSwitcher's own rendering contract (slot
// ordering, nil-slot omission, dropdown markup, etc.) -- that is covered
// directly by libs/go/htmxui's own tests. This file only proves
// app-registry's call site still wires its real nav list, banner, identity
// label, and logout href through those slots correctly, i.e. that the
// refactor didn't drop chrome or the logout affordance FR8 requires.

// noRolesUser and absentRolesUser are local to this file (not shared with
// pages/role_gating_test.go's identically-named helpers, which live in a
// different package) -- same FR-59 present-but-empty vs. absent-claim
// distinction banner_test.go already exercises against MisconfigBanner
// directly.
func noRolesShellUser() *htmxauth.UserInfo     { return &htmxauth.UserInfo{Roles: []string{}} }
func absentRolesShellUser() *htmxauth.UserInfo { return &htmxauth.UserInfo{Roles: nil} }

func renderShell(t *testing.T, user *htmxauth.UserInfo) string {
	t.Helper()
	var buf strings.Builder
	if err := Shell("Environments", user).Render(context.Background(), &buf); err != nil {
		t.Fatalf("Shell render failed: %v", err)
	}
	return buf.String()
}

// TestShell_RendersEveryNavItemExactlyOnce is the direct regression guard
// requested for the htmxui.Shell adoption: proves the slot wiring didn't
// silently drop a nav item (or duplicate one) when Shell() switched from
// rendering its <ul> inline to passing shellNav() as htmxui.ShellData.Nav.
//
// Red/green (issue #1005): temporarily deleting the "Reconcile Runs" <li>
// from shellNav() in layout.templ makes this test fail with "expected nav
// item ... exactly once, got 0 occurrences"; restoring the <li> makes it
// pass again. Verified by hand during this change.
//
// The exact-<li>-count assertion is scoped to the nav <ul> itself (rather
// than the whole rendered body) since issue #1010 mounted
// htmxui.ThemeSwitcher's own dropdown <li> options (and, when a user is
// present, htmxui.UserMenu's logout <li>) alongside the nav -- both are
// legitimate top-right chrome, not extra hardcoded nav items, so counting
// <li> across the full body would false-positive on them.
//
// Per issue #770, "Environments" moved off the top-level nav into the
// "Admin" dropdown alongside "Reconcile Runs" and "Drift & Audit" -- the
// nav <ul>'s own close tag is found by skipping past the Admin dropdown's
// *nested* <ul>...</ul> (its own dropdown-content menu), not the first
// "</ul>" in the body, and the <li> count below (8) is the 4 top-level
// day-to-day items + 1 "Admin" wrapper <li> + 3 items nested inside the
// Admin dropdown.
func TestShell_RendersEveryNavItemExactlyOnce(t *testing.T) {
	body := renderShell(t, noRolesShellUser())

	wantNavItems := []string{
		`<li><a href="/deployments">Deployments</a></li>`,
		`<li><a href="/apps">Apps</a></li>`,
		`<li><a href="/builds">Builds</a></li>`,
		`<li><a href="/releases/trigger">Trigger Release</a></li>`,
		`<li><a href="/environments">Environments</a></li>`,
		`<li><a href="/reconcile-runs">Reconcile Runs</a></li>`,
		`<li><a href="/drift-audit">Drift &amp; Audit</a></li>`,
	}
	for _, want := range wantNavItems {
		if got := strings.Count(body, want); got != 1 {
			t.Errorf("expected nav item %q exactly once, got %d occurrences in %q", want, got, body)
		}
	}

	navStart := strings.Index(body, `<ul class="menu menu-horizontal px-1">`)
	if navStart == -1 {
		t.Fatalf("could not locate nav <ul> in %q", body)
	}
	// The Admin dropdown nests its own <ul>...</ul> inside the nav <ul>, so
	// the nav <ul>'s real close tag is the *second* "</ul>" from navStart,
	// not the first (which belongs to the nested dropdown-content menu).
	firstClose := strings.Index(body[navStart:], "</ul>")
	if firstClose == -1 {
		t.Fatalf("nav <ul> never closes in %q", body)
	}
	afterFirstClose := navStart + firstClose + len("</ul>")
	secondClose := strings.Index(body[afterFirstClose:], "</ul>")
	if secondClose == -1 {
		t.Fatalf("nav <ul>'s outer close tag not found in %q", body)
	}
	navEnd := afterFirstClose + secondClose + len("</ul>")
	navSection := body[navStart:navEnd]
	// Exactly eight <li> elements within the nav <ul> (7 nav items + 1
	// "Admin" dropdown wrapper) -- guards against an extra hardcoded item
	// sitting alongside the real nav list.
	wantLiCount := len(wantNavItems) + 1
	if got := strings.Count(navSection, "<li>"); got != wantLiCount {
		t.Errorf("expected exactly %d <li> nav items in the nav <ul>, got %d in %q", wantLiCount, got, navSection)
	}
}

// TestShell_AbsentRolesMisconfigBannerRendersThroughBannerSlot proves
// MisconfigBanner(user) is still wired into htmxui.Shell's Banner slot --
// not dropped when Shell stopped rendering <main> itself. The banner's own
// exact wording/styling per role state is covered by
// components/banner_test.go; this only checks the slot wiring end to end.
func TestShell_AbsentRolesMisconfigBannerRendersThroughBannerSlot(t *testing.T) {
	body := renderShell(t, absentRolesShellUser())
	if !strings.Contains(body, "Roles claim missing from this session.") {
		t.Errorf("expected FR-59 misconfig banner wired through htmxui.Shell's Banner slot, got %q", body)
	}
}

// TestShell_NoRolesOmitsMisconfigBanner is the negation of the above: a
// legitimate read-only viewer (present, empty Roles) must not trip the
// banner through the new slot wiring either.
func TestShell_NoRolesOmitsMisconfigBanner(t *testing.T) {
	body := renderShell(t, noRolesShellUser())
	if strings.Contains(body, "Roles claim missing from this session.") {
		t.Errorf("expected no misconfig banner for a present-but-empty roles claim, got %q", body)
	}
}

// TestShell_RendersUserIdentityLabel proves user.PreferredUsername still
// reaches htmxui.UserMenu's IdentityLabel field, wired through
// htmxui.Shell's HeaderRight slot (issue #1010, FR8) rather than the old
// bare `<span>` / UserLabel slot.
//
// Red/green (issue #1010): temporarily hardcoding
// htmxui.UserMenuData{IdentityLabel: ""} in Shell() (dropping user.
// PreferredUsername) makes this test fail because UserMenu renders nothing
// for an empty IdentityLabel (see htmxui/user_menu.templ); restoring the
// real value makes it pass again. Verified by hand during this change.
func TestShell_RendersUserIdentityLabel(t *testing.T) {
	user := &htmxauth.UserInfo{PreferredUsername: "alice", Roles: []string{}}
	body := renderShell(t, user)
	want := `<div class="dropdown dropdown-end" data-htmxui-user-menu><div tabindex="0" role="button" class="btn btn-ghost btn-sm" aria-label="Account menu">alice</div>`
	if got := strings.Count(body, want); got != 1 {
		t.Errorf("expected user identity label %q wired through htmxui.UserMenu's IdentityLabel field exactly once, got %d occurrences in %q", want, got, body)
	}
}

// TestShell_RendersLogoutAffordance is FR8's direct regression guard: proves
// the shared htmxui.UserMenu wires app-registry's real logout route
// (tools/app_registry/ui/main.go's "/auth/logout" -> app.auth.HandleLogout)
// into a visible logout link, closing the gap where app-registry previously
// had no logout control in its chrome at all.
//
// Red/green (issue #1010): temporarily deleting the
// `<li><a href={ templ.URL(data.LogoutHref) }>Logout</a></li>` line from
// htmxui/user_menu.templ makes this test fail with "expected logout link
// ... exactly once, got 0 occurrences"; restoring it makes it pass again.
// Verified by hand during this change.
func TestShell_RendersLogoutAffordance(t *testing.T) {
	user := &htmxauth.UserInfo{PreferredUsername: "alice", Roles: []string{}}
	body := renderShell(t, user)
	want := `<li><a href="/auth/logout">Logout</a></li>`
	if got := strings.Count(body, want); got != 1 {
		t.Errorf("expected logout affordance %q targeting /auth/logout exactly once, got %d occurrences in %q", want, got, body)
	}
}

// TestShell_NilUserOmitsIdentityLabel guards the nil-user path (no
// PreferredUsername to hand to UserMenu's IdentityLabel) rendering cleanly
// with no user-menu dropdown, no identity label, and -- since UserMenu is
// the sole source of the logout affordance now -- no logout link either.
// This matters for AUTH_MODE=none, per htmxui/user_menu.templ's doc
// comment: an empty-but-present menu would offer a logout control with no
// identity behind it.
func TestShell_NilUserOmitsIdentityLabel(t *testing.T) {
	body := renderShell(t, nil)
	if strings.Contains(body, "data-htmxui-user-menu") {
		t.Errorf("expected no user-menu dropdown for a nil user, got %q", body)
	}
	if strings.Contains(body, "Logout") {
		t.Errorf("expected no logout affordance for a nil user, got %q", body)
	}
}

// TestShell_RendersThemeSwitcherOptions guards app-registry's adoption of
// the shared htmxui.ThemeSwitcher (issue #1010's "mount a theme switcher if
// one isn't already mounted" scope, extended by issue #1012's FR10 POC
// theme): app-registry previously mounted no theme switcher at all, so this
// proves shellThemes (layout.templ) reaches htmxui.Shell's Themes slot and
// renders all four offered themes -- including "sunset" -- independent of
// whether a user is signed in.
//
// Red/green (issue #1010, re-verified for #1012's "sunset" addition):
// temporarily dropping the "sunset" entry from shellThemes in layout.templ
// makes this test fail with "expected theme option ... exactly once, got 0
// occurrences"; restoring it makes it pass again. Verified by hand during
// this change.
func TestShell_RendersThemeSwitcherOptions(t *testing.T) {
	body := renderShell(t, nil)
	wantThemeOptions := []string{
		`<button type="button" data-htmxui-theme-value="light">☀️ Light</button>`,
		`<button type="button" data-htmxui-theme-value="night">🌙 Night</button>`,
		`<button type="button" data-htmxui-theme-value="oled">⚫ OLED Night</button>`,
		`<button type="button" data-htmxui-theme-value="sunset">🌅 Sunset</button>`,
	}
	for _, want := range wantThemeOptions {
		if got := strings.Count(body, want); got != 1 {
			t.Errorf("expected theme option %q exactly once, got %d occurrences in %q", want, got, body)
		}
	}
}

// Page content still landing inside htmxui.Shell's <main> region (after the
// banner slot) is exercised end to end by pages/role_gating_test.go's
// TestShell_AbsentRoles_MisconfigBannerAndReadContentBothRender, which
// renders a full page through components.Shell and asserts read content is
// present underneath the banner -- not duplicated here.
