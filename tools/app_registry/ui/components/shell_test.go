package components

import (
	"context"
	"strings"
	"testing"

	"github.com/whale-net/everything/libs/go/htmxauth"
)

// This file guards the FR2/FR1a wiring for Shell()'s adoption of
// htmxui.Shell (issue #1005): Shell is now a thin wrapper that hands its
// own nav list, MisconfigBanner, and user-identity label to htmxui.Shell's
// app-owned slots (ShellData.Nav/Banner/UserLabel) rather than rendering
// its own chrome inline. Nothing here exercises htmxui.Shell's own
// rendering contract (slot ordering, nil-slot omission, etc.) -- that is
// covered directly by libs/go/htmxui's own tests. This file only proves
// app-registry's call site still wires its real nav list and banner
// through those slots correctly, i.e. that the refactor didn't drop
// chrome.

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
func TestShell_RendersEveryNavItemExactlyOnce(t *testing.T) {
	body := renderShell(t, noRolesShellUser())

	wantNavItems := []string{
		`<li><a href="/environments">Environments</a></li>`,
		`<li><a href="/deployments">Deployments</a></li>`,
		`<li><a href="/apps">Apps</a></li>`,
		`<li><a href="/builds">Builds</a></li>`,
		`<li><a href="/reconcile-runs">Reconcile Runs</a></li>`,
		`<li><a href="/drift-audit">Drift &amp; Audit</a></li>`,
		`<li><a href="/releases/trigger">Trigger Release</a></li>`,
	}
	for _, want := range wantNavItems {
		if got := strings.Count(body, want); got != 1 {
			t.Errorf("expected nav item %q exactly once, got %d occurrences in %q", want, got, body)
		}
	}
	// Exactly seven <li> elements -- guards against an extra hardcoded item
	// sitting alongside the real nav list.
	if got := strings.Count(body, "<li>"); got != len(wantNavItems) {
		t.Errorf("expected exactly %d <li> nav items, got %d in %q", len(wantNavItems), got, body)
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
// reaches htmxui.Shell's UserLabel slot.
func TestShell_RendersUserIdentityLabel(t *testing.T) {
	user := &htmxauth.UserInfo{PreferredUsername: "alice", Roles: []string{}}
	body := renderShell(t, user)
	want := `<span class="mr-2 text-sm opacity-90">alice</span>`
	if !strings.Contains(body, want) {
		t.Errorf("expected user identity label %q wired through htmxui.Shell's UserLabel slot, got %q", want, body)
	}
}

// TestShell_NilUserOmitsIdentityLabel guards the nil-user path (no
// PreferredUsername to hand to UserLabel) rendering cleanly with no
// identity span at all.
func TestShell_NilUserOmitsIdentityLabel(t *testing.T) {
	body := renderShell(t, nil)
	if strings.Contains(body, "opacity-90") {
		t.Errorf("expected no identity label span for a nil user, got %q", body)
	}
}

// Page content still landing inside htmxui.Shell's <main> region (after the
// banner slot) is exercised end to end by pages/role_gating_test.go's
// TestShell_AbsentRoles_MisconfigBannerAndReadContentBothRender, which
// renders a full page through components.Shell and asserts read content is
// present underneath the banner -- not duplicated here.
