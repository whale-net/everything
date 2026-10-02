package components

import (
	"context"
	"strings"
	"testing"

	"github.com/a-h/templ"
	"github.com/stretchr/testify/assert"

	"github.com/whale-net/everything/libs/go/htmxui"
)

// renderComponent renders c to a string. Tests assert on specific emitted
// markup rather than a golden snapshot (htmxui ARCHITECTURE §14).
func renderComponent(t *testing.T, c templ.Component) string {
	t.Helper()
	var sb strings.Builder
	if err := c.Render(context.Background(), &sb); err != nil {
		t.Fatalf("render failed: %v", err)
	}
	return sb.String()
}

func TestShell_MountsTheSharedChromeContributions(t *testing.T) {
	body := renderComponent(t, Shell(ShellData{LayoutData: LayoutData{Title: "Claimed tasks"}}))

	assert.Contains(t, body, "krill", "the brand label is krill's own")
	// htmxui's own contribution: the theme switcher and the shared theme
	// list, which krill must not redeclare.
	assert.Contains(t, body, "data-htmxui-theme-switcher")
	assert.Contains(t, body, `data-htmxui-theme-value="night"`,
		"the shared Themes list supplies the night theme; krill must not redeclare it")
}

func TestShell_IdentityIsRenderedAsAPlainLabel(t *testing.T) {
	// The AUTH_MODE=none dev user must still surface, because its
	// presence is the signal that the page rendered authenticated.
	body := renderComponent(t, Shell(ShellData{LayoutData: LayoutData{UserLabel: "developer"}}))
	assert.Contains(t, body, "developer")
}

func TestShell_EmptyIdentityRendersNoMenuContent(t *testing.T) {
	// htmxui §4: an empty identity means render nothing, so a deployment
	// with no signed-in user never shows a logout control with no identity
	// behind it.
	body := renderComponent(t, Shell(ShellData{LayoutData: LayoutData{UserLabel: ""}}))
	assert.NotContains(t, body, "Logout")
}

// TestShell_CarriesThePrimaryLandmark keeps the sidebar a real landmark
// rather than a bare list, and keeps the data-krill hook nav_test.go
// scopes its active-link scan to.
func TestShell_CarriesThePrimaryLandmark(t *testing.T) {
	body := renderComponent(t, Shell(ShellData{NavGroups: shellGroups}))
	assert.Contains(t, body, `data-krill="primary-nav"`)
}

func TestSubNav_ScopesToItsOwnRegion(t *testing.T) {
	// A product sub-nav's active link must not be findable as a primary
	// nav link -- that is exactly what the data-krill hook is for.
	body := renderComponent(t, SubNav([]NavLink{
		{Label: "Capability map", Href: "/spec/products/p1", Active: true},
	}))
	assert.Contains(t, body, `data-krill="product-nav"`)
	assert.NotContains(t, body, `data-krill="primary-nav"`)
}

// ── the workspace shell ─────────────────────────────────────────────────────

// shellGroups is a representative sidebar: an unheaded group (how Overview
// sits above the headings), six headed groups, and one active item. It is
// written out literally rather than derived from the production nav table,
// so a group dropped from the table cannot shrink this fixture and pass.
var shellGroups = []NavGroup{
	{Items: []NavLink{{Label: "Overview", Href: "/"}}},
	{Title: "Work", Items: []NavLink{
		{Label: "Needs attention", Href: "/ops", Active: true},
		{Label: "Tasks", Href: "/spec/products/p1/delivery"},
		{Label: "Board", Href: "/spec/products/p1/board"},
	}},
	{Title: "Delivery", Items: []NavLink{
		{Label: "Milestones", Href: "/spec/products/p1/delivery"},
	}},
	{Title: "Design", Items: []NavLink{
		{Label: "Design sessions", Href: "/design/products/p1/design-sessions"},
	}},
	{Title: "Spec", Items: []NavLink{
		{Label: "Capabilities", Href: "/spec/products/p1"},
		{Label: "Decisions", Href: "/spec/products/p1/decisions"},
		{Label: "Personas", Href: "/spec/products/p1/personas"},
		{Label: "Non-goals", Href: "/spec/products/p1/non-goals"},
	}},
	{Title: "Admin", Items: []NavLink{
		{Label: "Credentials", Href: "/account/credentials"},
	}},
}

func renderShell(t *testing.T, groups []NavGroup) string {
	t.Helper()
	return renderComponent(t, Shell(ShellData{
		LayoutData: LayoutData{Title: "Task board", UserLabel: "developer"},
		NavGroups:  groups,
	}))
}

// shellNavRegion slices the shell's primary nav out of a rendered page. It
// keys on the data-krill hook rather than on an <h2> or a class, so a scan
// cannot wander into the page body -- and so the same rule nav_test.go
// applies to the flat top-bar nav holds for the drawer.
func shellNavRegion(body string) string {
	start := strings.Index(body, `data-krill="primary-nav"`)
	if start < 0 {
		return ""
	}
	end := strings.Index(body[start:], "</nav>")
	if end < 0 {
		return ""
	}
	return body[start : start+end]
}

func TestShell_RendersEveryGroupsItems(t *testing.T) {
	region := shellNavRegion(renderShell(t, shellGroups))

	for _, g := range shellGroups {
		for _, item := range g.Items {
			assert.Contains(t, region, `href="`+item.Href+`"`,
				"nav item %q under group %q is missing", item.Label, g.Title)
			assert.Contains(t, region, ">"+item.Label+"<",
				"nav item %q under group %q has no label", item.Label, g.Title)
		}
	}
}

func TestShell_RendersGroupHeadingsDistinctFromItems(t *testing.T) {
	region := shellNavRegion(renderShell(t, shellGroups))

	// menu-title is daisyUI's own heading treatment, which is what keeps a
	// group heading from reading as another item in the list.
	for _, title := range []string{"Work", "Delivery", "Design", "Spec", "Admin"} {
		assert.Contains(t, region, `class="menu-title`, "group %q has no menu-title heading", title)
		assert.Regexp(t, `<h2[^>]*>`+title+`</h2>`, region, "group %q heading is not an h2", title)
	}
	// The unheaded group renders its items with no heading of its own, which
	// is what keeps Overview from appearing to belong to the Work group.
	assert.NotRegexp(t, `<h2[^>]*>Overview</h2>`, region)
}

func TestShell_MarksTheActiveItemAndOnlyIt(t *testing.T) {
	body := renderShell(t, shellGroups)
	region := shellNavRegion(body)

	assert.Contains(t, region, `href="/ops" class="menu-active" aria-current="page"`,
		"the active item carries both the styling and the ARIA current state")
	assert.Equal(t, 1, strings.Count(region, `aria-current="page"`),
		"exactly one sidebar item is ever active")
}

func TestShell_NoActiveItemRendersNoneMarked(t *testing.T) {
	groups := []NavGroup{{Items: []NavLink{
		{Label: "Overview", Href: "/"},
		{Label: "Needs attention", Href: "/ops"},
	}}}
	region := shellNavRegion(renderShell(t, groups))

	assert.NotContains(t, region, `aria-current="page"`,
		"a page matching no item must leave every item unmarked, not light the first")
	assert.NotContains(t, region, "menu-active")
}

func TestShell_DrawerOpensAndClosesWithoutJavaScript(t *testing.T) {
	body := renderShell(t, shellGroups)

	// The four daisyUI drawer parts plus the checkbox that drives them.
	for _, want := range []string{
		`class="drawer lg:drawer-open`, // persistent at lg, overlay below it
		`type="checkbox" class="drawer-toggle"`,
		`class="drawer-content`,
		`class="drawer-side`,
		`class="drawer-overlay"`,
	} {
		assert.Contains(t, body, want)
	}

	// The top-bar button and the overlay both drive the same checkbox
	// through `for`, so opening and closing need no script.
	ids := shellDrawerCheckboxIDs(body)
	assert.Len(t, ids, 1, "the toggle id must be unique or the labels bind to nothing")
	assert.Contains(t, body, `for="`+ids[0]+`" class="btn btn-ghost btn-square lg:hidden" aria-label="Open navigation"`,
		"the top-bar button is a label for the drawer checkbox, shown only below lg")
	assert.Contains(t, body, `for="`+ids[0]+`" class="drawer-overlay" aria-label="Close navigation"`,
		"the overlay is the click-outside close target")
}

// shellDrawerCheckboxIDs pulls the drawer's checkbox id out of the markup
// by keying on the input tag, so an id belonging to the theme switcher or
// the user menu can never be mistaken for it.
func shellDrawerCheckboxIDs(body string) []string {
	var ids []string
	const marker = `<input id="`
	rest := body
	for {
		i := strings.Index(rest, marker)
		if i < 0 {
			return ids
		}
		rest = rest[i+len(marker):]
		j := strings.Index(rest, `"`)
		ids = append(ids, rest[:j])
		rest = rest[j:]
	}
}

// TestShell_SidebarIsPersistentAtLg is the other half of the drawer
// contract: below lg the sidebar is an overlay, but at lg it sits beside
// the content. Only the toggle button is responsive (lg:hidden); the
// sidebar itself must not be, or a wide viewport would find it collapsed
// away behind a hamburger.
func TestShell_SidebarIsPersistentAtLg(t *testing.T) {
	region := shellDrawerSideRegion(renderShell(t, shellGroups))

	// A bare `hidden` hides the sidebar at every width, and a responsive
	// `lg:hidden` hides it above lg -- the opposite of the contract. Both
	// are matched here, the second as a word rather than a substring, since
	// `lg:hidden` legitimately appears on the toggle button elsewhere.
	assert.NotRegexp(t, `(^|[\s"])hidden([\s"]|$)`, region,
		"the sidebar must not carry a bare `hidden` class")
	assert.NotContains(t, region, "lg:hidden",
		"the sidebar must stay visible at lg; only the toggle is responsive")
	// It is the aside inside drawer-side that carries the width, so an
	// operator at lg sees the w-56 column beside the content.
	assert.Contains(t, region, "w-56", "the sidebar is a fixed-width column beside the content")
	// The drawer starts closed: daisyUI opens it at lg through
	// lg:drawer-open, not through a checked checkbox, so shipping one
	// would slam the overlay open over the content on a narrow viewport.
	assert.NotContains(t, renderShell(t, shellGroups), "drawer-toggle\" checked",
		"the drawer must ship closed; lg:drawer-open is what makes it persistent")
}

// shellDrawerSideRegion slices the drawer's side panel out of a rendered
// page, keyed on daisyUI's own class so it cannot wander into the content.
func shellDrawerSideRegion(body string) string {
	start := strings.Index(body, `class="drawer-side`)
	if start < 0 {
		return ""
	}
	rest := body[start:]
	if end := strings.Index(rest, "</aside>"); end >= 0 {
		return rest[:end]
	}
	return rest
}

// TestShell_NavItemsArePlainNavigation is why clicking a nav item closes
// the drawer: the items are ordinary anchors, so activating one navigates
// and the fresh page starts with the checkbox unticked. An htmx swap or
// any JS-driven handler would leave the drawer standing over the page it
// swapped in.
func TestShell_NavItemsArePlainNavigation(t *testing.T) {
	region := shellNavRegion(renderShell(t, shellGroups))

	anchors := strings.Count(region, "<a ")
	assert.Equal(t, len(shellGroups[0].Items)+
		countItems(shellGroups[1:]), anchors,
		"every sidebar item is one plain anchor")
	for _, forbidden := range []string{"hx-get", "hx-post", "hx-target", "onclick"} {
		assert.NotContains(t, region, forbidden,
			"%q would swap a nav item in place instead of navigating, leaving the drawer open", forbidden)
	}
}

func countItems(groups []NavGroup) int {
	n := 0
	for _, g := range groups {
		n += len(g.Items)
	}
	return n
}

func TestShell_ShipsNoScriptBeyondTheThemeSwitchers(t *testing.T) {
	body := renderShell(t, shellGroups)

	// The drawer is checkbox-driven, so the shell's own markup contributes
	// no script: htmxui's ThemeSwitcher ships one and it is the only one
	// allowed, or the sidebar would stop working with JS disabled.
	allowed := strings.Count(renderComponent(t, htmxui.ThemeSwitcher(htmxui.Themes)), "<script")
	assert.Equal(t, allowed, strings.Count(body, "<script"),
		"the shell must add no script of its own beyond htmxui's ThemeSwitcher")
	for _, forbidden := range []string{"onclick", "hx-trigger", "alpine:", "@click", "hx-on"} {
		assert.NotContains(t, body, forbidden,
			"%q would make the drawer depend on JavaScript", forbidden)
	}
}

func TestShell_HasNoJumpToControl(t *testing.T) {
	body := strings.ToLower(renderShell(t, shellGroups))

	assert.NotContains(t, body, "jump to")
	assert.NotContains(t, body, "jump-to")
}

func TestShell_ShowsTheSignedInUserAndPageTitle(t *testing.T) {
	body := renderShell(t, shellGroups)

	assert.Contains(t, body, "Task board", "the top bar names the page")
	assert.Contains(t, body, "developer", "the top bar shows the signed-in user")
	assert.Contains(t, body, `href="/"`, "the sidebar's brand block links home")
}
