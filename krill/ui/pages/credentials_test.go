package pages

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestCredentialsResults_IsAnHtmxSwapTarget(t *testing.T) {
	body := renderBody(t, CredentialsResults(CredentialsData{
		MintAction: "/account/credentials",
		Rows: []CredentialRow{
			{ID: "a", CreatedAt: "t", RevokeAction: "/account/credentials/a/revoke"},
			{ID: "b", CreatedAt: "t", Revoked: true, RevokeAction: "/account/credentials/b/revoke"},
		},
	}))

	assert.Contains(t, body, `id="credentials-results"`)
	assert.Contains(t, body, `hx-post="/account/credentials"`)
	assert.Contains(t, body, `hx-post="/account/credentials/a/revoke"`)
	assert.NotContains(t, body, `/account/credentials/b/revoke`, "a revoked row has no revoke control")
	assert.NotContains(t, body, "<script")
}

func TestCredentialsResults_RendersTheOneTimeToken(t *testing.T) {
	// An operator who leaves this page without the token cannot get it
	// back, so the warning is load-bearing copy, not decoration.
	body := renderBody(t, CredentialsResults(CredentialsData{NewToken: "tok-123"}))
	assert.Contains(t, body, `value="tok-123"`)
	assert.Contains(t, body, "it will not be shown again")

	body = renderBody(t, CredentialsResults(CredentialsData{}))
	assert.NotContains(t, body, "it will not be shown again")
	assert.Contains(t, body, "No credentials yet")
}

func TestCredentialsResults_RendersErrorInline(t *testing.T) {
	body := renderBody(t, CredentialsResults(CredentialsData{Error: "Could not generate a token."}))
	assert.Contains(t, body, "Could not generate a token.")
	assert.Contains(t, body, `id="credentials-results"`)
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
