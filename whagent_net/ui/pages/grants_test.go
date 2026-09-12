package pages

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/a-h/templ"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// This file guards issue #2432's Implementation-phase Testing section:
// "Render test: the list renders correctly from the plain GrantRow type
// with no whagent_net-specific input." GrantRow/GrantsData are plain data
// (FR13's shared render shape) -- this test drives Grants with nothing
// but that data, no App/handler/store involved.

func renderGrants(t *testing.T, data GrantsData) string {
	t.Helper()
	var buf strings.Builder
	require.NoError(t, Grants(data).Render(context.Background(), &buf))
	return buf.String()
}

// TestGrants_RendersRowsFromPlainData proves the table renders every
// field of a plain GrantRow, and offers a revoke control for a
// not-yet-revoked row.
func TestGrants_RendersRowsFromPlainData(t *testing.T) {
	granted := time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC)
	body := renderGrants(t, GrantsData{
		Rows: []GrantRow{
			{OperatorLabel: "alice", Scope: "audience_score_system", Status: "active", GrantedAt: granted},
		},
	})

	assert.Contains(t, body, "alice")
	assert.Contains(t, body, "audience_score_system")
	assert.Contains(t, body, "Active")
	assert.Contains(t, body, `action="/grants/revoke"`)
	assert.Contains(t, body, `name="scope"`)
	assert.Contains(t, body, `value="audience_score_system"`)
}

// TestGrants_RevokedRowHasNoRevokeControl proves an already-revoked row
// offers no revoke form -- there is nothing left to revoke.
func TestGrants_RevokedRowHasNoRevokeControl(t *testing.T) {
	body := renderGrants(t, GrantsData{
		Rows: []GrantRow{
			{OperatorLabel: "alice", Scope: "manmanv2", Status: "revoked", GrantedAt: time.Now()},
		},
	})

	assert.Contains(t, body, "Revoked")
	assert.NotContains(t, body, `action="/grants/revoke"`)
}

// TestGrants_EmptyRowsRendersPlaceholder guards the zero-grants case.
func TestGrants_EmptyRowsRendersPlaceholder(t *testing.T) {
	body := renderGrants(t, GrantsData{})
	assert.Contains(t, body, "You have not granted access to any scope yet.")
}

// TestGrants_RendersPageError guards GrantsData.Error's rendering (the
// "delegated-grant management is not configured" degrade path).
func TestGrants_RendersPageError(t *testing.T) {
	body := renderGrants(t, GrantsData{Error: "Delegated-grant management is not configured on this deployment."})
	assert.Contains(t, body, "Delegated-grant management is not configured on this deployment.")
}

// TestGrants_RendersAvailableScopesAsClickableLinks proves an available
// scope renders as a link to the standalone consent route -- so starting
// a new consent never requires hand-typing /mcp/consent?scope=<s>.
func TestGrants_RendersAvailableScopesAsClickableLinks(t *testing.T) {
	body := renderGrants(t, GrantsData{AvailableScopes: []string{"audience_score_system"}})

	assert.Contains(t, body, "Available grants")
	assert.Contains(t, body, `href="/mcp/consent?scope=audience_score_system"`)
	assert.Contains(t, body, "Grant audience_score_system")
}

// TestGrants_NoAvailableScopesOmitsSection guards the common case (nothing
// left to grant, or delegated-grant unconfigured): no "Available grants"
// section renders at all.
func TestGrants_NoAvailableScopesOmitsSection(t *testing.T) {
	body := renderGrants(t, GrantsData{})
	assert.NotContains(t, body, "Available grants")
}

// TestScopeConsentURL_EncodesScope proves the consent link URL-encodes its
// scope rather than concatenating it raw.
func TestScopeConsentURL_EncodesScope(t *testing.T) {
	assert.Equal(t, templ.SafeURL("/mcp/consent?scope=a+b"), scopeConsentURL("a b"))
}

// TestStatusLabel_MapsKnownStatuses guards statusLabel's mapping without
// this package importing grpcauth.GrantStatus (GrantRow's own doc
// comment).
func TestStatusLabel_MapsKnownStatuses(t *testing.T) {
	assert.Equal(t, "Active", statusLabel("active"))
	assert.Equal(t, "Needs re-auth", statusLabel("needs_reauth"))
	assert.Equal(t, "Revoked", statusLabel("revoked"))
	assert.Equal(t, "unknown", statusLabel("unknown"))
}
