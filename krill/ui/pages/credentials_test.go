package pages

import (
	"context"
	"strings"
	"testing"

	"github.com/a-h/templ"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func renderBody(t *testing.T, c templ.Component) string {
	t.Helper()
	var sb strings.Builder
	require.NoError(t, c.Render(context.Background(), &sb))
	return sb.String()
}

// TestCredentials_PreservesTheSelfServeContract is the regression guard
// on the deliberate non-conversion in credentialsScript. The widget talks
// to app.mcpProvider's self-serve JSON API, not to an htmx fragment, so
// every id the script binds to and the fetch() targets it uses must
// survive the templ port untouched -- if any of them drifts, the widget
// silently stops working in the browser with no server-side signal.
func TestCredentials_PreservesTheSelfServeContract(t *testing.T) {
	body := renderBody(t, Credentials())

	for _, id := range []string{
		"generate-btn",
		"new-token",
		"new-token-value",
		"credentials-table",
		"credentials-body",
		"refresh-btn",
	} {
		assert.Contains(t, body, `id="`+id+`"`, "the widget script binds to #%s", id)
	}
	assert.Contains(t, body, `fetch('/credentials'`)
	assert.Contains(t, body, `fetch('/credentials/'`)
}

// TestCredentials_ScriptMarkupIsNotEscaped guards the reason the script
// is a const injected with templ.Raw rather than inline templ: templ
// escapes Go expressions inside <script>, which would turn the widget's
// '<tr><td colspan="4">' row markup into visible entities and break every
// table it builds.
func TestCredentials_ScriptMarkupIsNotEscaped(t *testing.T) {
	body := renderBody(t, Credentials())

	assert.Contains(t, body, `'<tr><td colspan="4">failed to load credentials</td></tr>'`,
		"row markup inside the script must not be HTML-escaped")
	assert.NotContains(t, body, "&lt;tr&gt;",
		"an escaped <tr> means templ escaped the script body")
}

func TestCredentials_RendersTheOneTimeWarning(t *testing.T) {
	// An operator who leaves this page without the token cannot get it
	// back, so the warning is load-bearing copy, not decoration.
	body := renderBody(t, Credentials())
	assert.Contains(t, body, "it will not be shown again")
}

func TestAreaIndex_RendersOneLinkedRowPerEntry(t *testing.T) {
	body := renderBody(t, AreaIndex("Ops console", []AreaLink{
		{Path: "/ops/claimed", Label: "Claimed tasks", Blurb: "every task that currently holds a claim."},
	}))

	assert.Contains(t, body, `href="/ops/claimed"`)
	assert.Contains(t, body, "Claimed tasks")
	assert.Contains(t, body, "every task that currently holds a claim.")
}

func TestAreaIndex_EmptyListRendersTheHeadingAlone(t *testing.T) {
	// htmxui §4's "empty means render nothing" applied to a list: no
	// empty <ul> left dangling in the chrome.
	body := renderBody(t, AreaIndex("Ops console", nil))

	assert.Contains(t, body, "Ops console")
	assert.NotContains(t, body, "<ul")
}
