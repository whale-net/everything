package components

import (
	"context"
	"strings"
	"testing"

	"github.com/a-h/templ"
	"github.com/stretchr/testify/assert"
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

func TestLayout_MountsSharedShellChrome(t *testing.T) {
	body := renderComponent(t, Layout(LayoutData{Title: "Claimed tasks"}))

	assert.Contains(t, body, "krill", "the brand label is krill's own, passed via ShellData")
	// htmxui's own contribution: the theme switcher and the shared theme
	// list, which krill must not redeclare.
	assert.Contains(t, body, "data-htmxui-theme-switcher")
	assert.Contains(t, body, `data-htmxui-theme-value="night"`,
		"the shared Themes list supplies the night theme; krill must not redeclare it")
}

func TestLayout_IdentityIsRenderedAsAPlainLabel(t *testing.T) {
	// The AUTH_MODE=none dev user must still surface, because its
	// presence is the signal that the page rendered authenticated.
	body := renderComponent(t, Layout(LayoutData{UserLabel: "developer"}))
	assert.Contains(t, body, "developer")
}

func TestLayout_EmptyIdentityRendersNoMenuContent(t *testing.T) {
	// htmxui §4: an empty identity means render nothing, so a deployment
	// with no signed-in user never shows a logout control with no identity
	// behind it.
	body := renderComponent(t, Layout(LayoutData{UserLabel: ""}))
	assert.NotContains(t, body, "Logout")
}

func TestNav_MarksExactlyTheActiveLink(t *testing.T) {
	body := renderComponent(t, Layout(LayoutData{Nav: []NavLink{
		{Label: "Ops console", Href: "/ops", Active: true},
		{Label: "Spec & delivery", Href: "/spec", Active: false},
	}}))

	// aria-current is the contract; menu-active is the styling.
	assert.Contains(t, body, `href="/ops" class="menu-active" aria-current="page"`)
	assert.Contains(t, body, `href="/spec">`)
	assert.Equal(t, 1, strings.Count(body, `aria-current="page"`),
		"exactly one primary nav link is ever active")
}

func TestNav_CarriesThePrimaryLandmark(t *testing.T) {
	// nav_test.go asserts on <nav>; keep it a real landmark rather than a
	// bare list, and keep the data-krill hook it scopes its scan to.
	body := renderComponent(t, Layout(LayoutData{Nav: []NavLink{{Label: "Ops", Href: "/ops"}}}))
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
