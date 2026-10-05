package pages

import (
	"strings"
	"testing"

	"github.com/a-h/templ"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/whale-net/everything/libs/go/htmxui"
)

// liveRow is one live credential as the mapper hands it over.
func liveRow(id, name, createdRelative, createdExact, lastUsedRelative, lastUsedExact string) CredentialRow {
	return CredentialRow{
		ID:                id,
		Name:              name,
		CreatedRelative:   createdRelative,
		CreatedExact:      createdExact,
		LastUsedRelative:  lastUsedRelative,
		LastUsedExact:     lastUsedExact,
		RevokeAction:      "/account/credentials/" + id + "/revoke",
		RevokeConfirmHref: "/account/credentials/" + id + "/revoke",
		RevokeDismissHref: "/account/credentials/" + id,
	}
}

// revokeRow is liveRow with the confirmation asked, which is the only
// difference between a row at rest and a row being revoked.
func revokeRow(id, name string) CredentialRow {
	row := liveRow(id, name, "3 weeks ago", "2026-09-13T10:00:00Z", "2 min ago", "2026-10-04T20:58:00Z")
	row.Confirming = true
	return row
}

// The four columns the FR names, in its order. A table that grows a status
// column would be how a revoked credential lingers looking live, so the
// header row is pinned rather than left to "whatever renders".
func TestCredentialsResults_TableCarriesTheFourColumns(t *testing.T) {
	body := renderBody(t, CredentialsResults(CredentialsData{
		MintAction: "/account/credentials",
		NewHref:    "/account/credentials/new",
		ListHref:   "/account/credentials",
		Rows:       []CredentialRow{liveRow("a", "claude-code laptop", "3 weeks ago", "2026-09-13T10:00:00Z", "2 min ago", "2026-10-04T20:58:00Z")},
	}))

	assert.Contains(t, body, `data-krill="credentials-table"`)
	for _, header := range []string{"Name", "Created", "Last used", "Actions"} {
		assert.Contains(t, body, header+"</th>", "missing the %q column", header)
	}
	assert.NotContains(t, body, "<th>Status</th>", "a revoked row must not linger looking live")
	assert.NotContains(t, body, "<th>ID</th>", "the id is the revoke path's handle, not a column an operator reads")
}

// A row's instants are the relative age as text with the exact RFC3339
// instant in the title and the datetime, the same shape the status
// register renders -- so a hover answers "exactly when" without a second
// request.
func TestCredentialsResults_RendersRelativeTextAndTheExactInstant(t *testing.T) {
	body := renderBody(t, CredentialsResults(CredentialsData{
		Rows: []CredentialRow{
			liveRow("a", "claude-code laptop", "3 weeks ago", "2026-09-13T10:00:00Z", "2 min ago", "2026-10-04T20:58:00Z"),
			liveRow("b", "ci runner", "2 months ago", "2026-08-04T09:00:00Z", "", ""),
		},
	}))

	assert.Contains(t, body, `title="2026-09-13T10:00:00Z"`)
	assert.Contains(t, body, `datetime="2026-09-13T10:00:00Z"`)
	assert.Contains(t, body, ">3 weeks ago<")
	assert.Contains(t, body, `data-krill-updated-at="2026-10-04T20:58:00Z"`)
	assert.Contains(t, body, ">2 min ago<")

	// A credential nobody has presented says so in words; a blank cell
	// would read as a read that dropped the value.
	assert.Contains(t, body, `data-krill="credential-last-used-never"`)
	assert.Contains(t, body, ">never<")
	assert.Equal(t, 2, strings.Count(body, `data-krill="credential-row"`))
}

// The empty state is htmxui's, and it offers the same one action the header
// does -- a control that cannot work must not look live.
func TestCredentialsResults_EmptyStateWhenNoCredentials(t *testing.T) {
	body := renderBody(t, CredentialsResults(CredentialsData{
		NewHref: "/account/credentials/new",
		Rows:    nil,
	}))

	assert.Contains(t, body, `data-krill="credentials-empty"`)
	assert.Contains(t, body, "No credentials yet.")
	assert.Contains(t, body, `href="/account/credentials/new"`)
	assert.NotContains(t, body, `data-krill="credentials-table"`)
	assert.NotContains(t, body, "Generate a token", "the unbladed mint button is gone")
}

// The token rides exactly one response. It is shown in the field and the
// copy control points AT the field rather than repeating the secret into
// its own attributes, so a later page load has nothing left to show.
func TestCredentialsResults_RendersTheOneTimeTokenExactlyOnce(t *testing.T) {
	body := renderBody(t, CredentialsResults(CredentialsData{
		NewToken:     "krill-secret-token",
		NewTokenName: "claude-code laptop",
	}))

	assert.Equal(t, 1, strings.Count(body, "krill-secret-token"),
		"the raw token must appear exactly once in the response, never twice in one page")
	assert.Contains(t, body, `data-krill="credentials-new-token-value"`)
	assert.Contains(t, body, "it will not be shown again")
	// The copy control is the shell's existing one, pointed at the field,
	// and it ships disabled: the clipboard is a client API.
	assert.Contains(t, body, `data-copy-source="credentials-new-token-value"`)
	assert.Contains(t, body, `data-krill="copy-task-id-status"`)

	// Nothing about the token on any other render.
	empty := renderBody(t, CredentialsResults(CredentialsData{}))
	assert.NotContains(t, empty, "it will not be shown again")
}

// A list failure is an inline alert inside the swap target, never a failed
// page and never a fragment that deletes its own region.
func TestCredentialsResults_RendersErrorInline(t *testing.T) {
	body := renderBody(t, CredentialsResults(CredentialsData{Error: "Could not load credentials."}))

	assert.Contains(t, body, "Could not load credentials.")
	assert.Contains(t, body, `data-krill="credentials-alert"`)
	assert.Contains(t, body, `id="credentials-results"`)
}

// The results block is the swap target every mint and revoke replaces, and
// it owns the blade region so one swap can close the blade and re-render
// the list underneath it.
func TestCredentialsResults_CarriesItsOwnSwapTarget(t *testing.T) {
	d := CredentialsData{MintAction: "/account/credentials", NewHref: "/account/credentials/new"}
	body := renderBody(t, CredentialsResults(d))

	assert.Contains(t, body, `id="credentials-results"`)
	assert.Contains(t, body, `id="`+CredentialsBladeAnchor+`"`,
		"the primary action's hx-target must resolve whether or not a blade is open")
	assert.NotContains(t, body, "<script")
}

// The closed region is the placeholder an open and a close swap between, so
// the anchor is rendered either way.
func TestCredentialsBladeSlot_ClosedIsTheAnchorAlone(t *testing.T) {
	body := renderBody(t, CredentialsBladeSlot(CredentialsData{}))

	assert.Equal(t, []string{"div"}, topLevelElements(body))
	assert.Contains(t, body, `id="`+CredentialsBladeAnchor+`"`)
	assert.NotContains(t, body, `data-krill="credentials-blade-body"`)
}

// The served blade fragment's ROOT must be the swap target: htmx's
// outerHTML inserts the response and then removes the element it replaced,
// so a fragment rooted anywhere else deletes the region for good.
func TestCredentialsBladeSlot_OpenIsRootedAtTheSwapTarget(t *testing.T) {
	body := renderBody(t, CredentialsBladeSlot(CredentialsData{BladeOpen: true}))

	top := topLevelElements(body)
	require.Len(t, top, 1, "one top-level element, or a swap splices the extras in on every open")
	assert.Equal(t, "div", top[0])
	assert.Contains(t, body, `id="`+CredentialsBladeAnchor+`"`)
}

// The blade is a Name field and the two controls that end it. It posts to
// the mint path by both halves of the doubling rule, so the no-JS path and
// the htmx path are the same form.
func TestCredentialsBlade_OffersANameFieldAndBothHalvesOfTheForm(t *testing.T) {
	body := renderBody(t, CredentialsBladeSlot(CredentialsData{
		BladeOpen:  true,
		BladeName:  "claude-code laptop",
		MintAction: "/account/credentials",
		ListHref:   "/account/credentials",
	}))

	assert.Contains(t, body, "Create credential")
	assert.Contains(t, body, `name="name"`)
	assert.Contains(t, body, `value="claude-code laptop"`)
	assert.Contains(t, body, `placeholder="claude-code laptop"`)
	assert.Contains(t, body, `method="post"`)
	assert.Contains(t, body, `action="/account/credentials"`)
	assert.Contains(t, body, `hx-post="/account/credentials"`)
	assert.Contains(t, body, `hx-target="#credentials-results"`)
	assert.Contains(t, body, `href="/account/credentials"`, "Cancel returns to the list")

	// An empty name is a refusal this page has to state in its own words;
	// the browser's own validation would take that answer away.
	assert.NotContains(t, body, " required")
	assert.NotContains(t, body, " required=")
}

// A refused mint re-renders the blade with the alert and the operator's
// typed name still in the field: htmx does not swap on an error status, so
// a refusal that dropped the typing would be a data-loss event.
func TestCredentialsResults_RefusalKeepsTheBladeAndTheTypedName(t *testing.T) {
	body := renderBody(t, CredentialsResults(CredentialsData{
		Error:      "A live credential is already named \"ci runner\". Revoke it first, or pick another name.",
		BladeOpen:  true,
		BladeName:  "ci runner",
		MintAction: "/account/credentials",
		ListHref:   "/account/credentials",
	}))

	assert.Contains(t, body, `data-krill="credentials-alert"`)
	assert.Contains(t, body, "Revoke it first, or pick another name.")
	assert.Contains(t, body, `data-krill="credentials-blade-body"`)
	assert.Contains(t, body, `value="ci runner"`)
	assert.NotContains(t, body, "krill-secret-token")
}

// The list's primary action carries a real href AND the hx-get of that
// same URL, so a no-JS click, a reload and a shared link all land on the
// same blade.
func TestCredentialsPage_CarriesBothHalvesOfTheCreateAction(t *testing.T) {
	body := renderBody(t, CredentialsPage(CredentialsData{NewHref: "/account/credentials/new"}))

	assert.Contains(t, body, `data-krill="credentials-create"`)
	assert.Contains(t, body, `href="/account/credentials/new"`)
	assert.Contains(t, body, `hx-get="/account/credentials/new"`)
	assert.Contains(t, body, `hx-target="#`+CredentialsBladeAnchor+`"`)
	assert.Contains(t, body, `data-krill="page-title"`)
	assert.NotContains(t, body, "Generate a token")
}

// The row's default state asks and posts nothing: there is no <form> in
// the region at all, and the Revoke control carries both halves of the ask
// -- a real href at the row's confirm URL and an hx-get of that same URL
// into the row's OWN region. A destructive control that posts on click is
// the thing this FR exists to remove (FR ab55875e).
func TestCredentialRevokeRegion_DefaultStateAsksAndPostsNothing(t *testing.T) {
	body := renderBody(t, CredentialRevokeRegion(liveRow("a", "ci runner", "", "", "", "")))

	assert.NotContains(t, body, "<form", "nothing may post before the operator confirms")
	assert.NotContains(t, body, "method=")
	assert.NotContains(t, body, `data-krill="credential-revoke-form"`)
	assert.NotContains(t, body, "Clients using it lose access immediately.",
		"the consequence is asked about, not stated on the control")

	assert.Contains(t, body, `data-krill="credential-revoke-region"`)
	assert.Contains(t, body, `id="`+CredentialRevokeRegionID("a")+`"`)
	assert.Contains(t, body, `data-krill="credential-revoke"`)
	assert.Contains(t, body, `href="/account/credentials/a/revoke"`,
		"a no-JS click follows a real link")
	assert.Contains(t, body, `hx-get="/account/credentials/a/revoke"`)
	assert.Contains(t, body, `hx-target="#`+CredentialRevokeRegionID("a")+`"`,
		"the confirmation is asked in the row, not over the list")
	assert.Equal(t, []string{"div"}, topLevelElements(body),
		"the fragment must be exactly its swap target")
	assert.NotContains(t, body, "<script")
}

// The confirmation names the credential and its consequence in the FR's own
// wording, and the action row is htmxui.Confirm's: destructive intent (the
// error-variant submit, no hand-rolled chrome), a dismiss affordance, and
// an app-owned doubled form around it (FR 21f5f0d0, htmxui §9).
func TestCredentialRevokeRegion_ConfirmNamesTheCredentialAndTheConsequence(t *testing.T) {
	body := renderBody(t, CredentialRevokeRegion(revokeRow("a", "ci runner")))

	assert.Contains(t, body, "Revoke ci runner? Clients using it lose access immediately.")
	assert.NotContains(t, body, "Revoke a?", "the confirmation names the credential, not its id")

	// The action row is Confirm's, not this page's: its submit is the
	// error variant a destructive intent renders, beside a dismiss link.
	// CancelAttrs is passed here too, because the assertion is that the row
	// is the one Confirm renders from these props -- so the props have to be
	// the ones the page actually passes.
	confirm := renderBody(t, htmxui.Confirm(htmxui.ConfirmProps{
		Intent:      htmxui.ConfirmDestructive,
		Summary:     "Revoke ci runner? Clients using it lose access immediately.",
		SubmitLabel: "Revoke",
		CancelHref:  "/account/credentials/a",
		CancelLabel: "Dismiss",
		CancelAttrs: templ.Attributes{
			"hx-get":    "/account/credentials/a",
			"hx-target": "#" + CredentialRevokeRegionID("a"),
			"hx-swap":   "outerHTML",
		},
	}, nil))
	assert.Contains(t, body, confirm, "the action row must be htmxui.Confirm's own markup")

	// The doubled form stays app-owned: no-JS path and htmx path, one
	// route, and the swap lands on the whole list so the revoked row leaves.
	assert.Contains(t, body, `data-krill="credential-revoke-form"`)
	assert.Contains(t, body, `method="post"`)
	assert.Contains(t, body, `action="/account/credentials/a/revoke"`)
	assert.Contains(t, body, `hx-post="/account/credentials/a/revoke"`)
	assert.Contains(t, body, `hx-target="#credentials-results"`)
	assert.Contains(t, body, `hx-swap="outerHTML"`)
	assert.NotContains(t, body, "<script")
}

// Dismiss returns the row by asking for it, so it works with JavaScript
// off: the href is the row's own URL, which answers with the row at rest.
func TestCredentialRevokeRegion_DismissReturnsTheRowByHref(t *testing.T) {
	body := renderBody(t, CredentialRevokeRegion(revokeRow("a", "ci runner")))

	assert.Contains(t, body, `href="/account/credentials/a"`)
	assert.Contains(t, body, ">Dismiss<")
	assert.NotContains(t, body, `hx-post="/account/credentials/a"`,
		"dismissal must not be able to answer the question")
}

// Two regions, two ids: a confirmation that appeared in every row, or in
// a row other than the one asked about, is an answer to a question nobody
// asked.
func TestCredentialRevokeRegion_EachRowHasItsOwnRegionID(t *testing.T) {
	assert.NotEqual(t, CredentialRevokeRegionID("a"), CredentialRevokeRegionID("b"))

	body := renderBody(t, CredentialsResults(CredentialsData{
		Rows: []CredentialRow{revokeRow("a", "ci runner"), liveRow("b", "claude-code laptop", "", "", "", "")},
	}))

	assert.Contains(t, body, `id="`+CredentialRevokeRegionID("a")+`"`)
	assert.Contains(t, body, `id="`+CredentialRevokeRegionID("b")+`"`)
	assert.Equal(t, 1, strings.Count(body, "Clients using it lose access immediately."),
		"only the row that was asked about may confirm")
	assert.Equal(t, 1, strings.Count(body, `data-krill="credential-revoke"`),
		"the row at rest still offers its Revoke control")
	assert.Equal(t, 1, strings.Count(body, `data-krill="credential-revoke-form"`),
		"and the asked row is the one showing the confirmation")
}

// Nothing here needs JavaScript, and a <script> would mean the ask is a
// scripted state flip rather than something the server renders.
func TestCredentialsResults_RevokeStepNeedsNoScript(t *testing.T) {
	body := renderBody(t, CredentialsResults(CredentialsData{
		Rows: []CredentialRow{liveRow("a", "ci runner", "", "", "", ""), revokeRow("b", "claude-code laptop")},
	}))

	assert.NotContains(t, body, "<script")
	assert.NotContains(t, body, "hx-confirm",
		"the confirmation is a rendered region, not a browser dialog")
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

// The dismiss is a doubled control on one anchor: the href is the no-JS
// destination, and the hx-get/hx-target/hx-swap are the in-place one, so
// cancelling swaps the row back rather than navigating the page away from
// the table it was asked in. The swap targets the row's OWN region --
// putting the row at rest back where the question was, which is also the
// only target the next Revoke has.
func TestCredentialRevokeRegion_DismissSwapsTheRowBackInPlace(t *testing.T) {
	body := renderBody(t, CredentialRevokeRegion(revokeRow("a", "ci runner")))

	assert.Contains(t, body, `href="/account/credentials/a"`,
		"the no-JS path still follows a real link")
	assert.Contains(t, body, `hx-get="/account/credentials/a"`,
		"an htmx browser's dismissal asks the same route rather than navigating")
	assert.Contains(t, body, `hx-target="#`+CredentialRevokeRegionID("a")+`"`,
		"the row goes back into its own region, not over the whole list")
	assert.Contains(t, body, `hx-swap="outerHTML"`,
		"the region's id must survive the swap or the next Revoke has no target")
	assert.Contains(t, body, ">Dismiss<")
	assert.NotContains(t, body, "<script",
		"the in-place dismiss is server-rendered markup, not a scripted state flip")
}
