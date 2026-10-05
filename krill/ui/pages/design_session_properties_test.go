package pages

// FR d8146d9e at the component level: the rail's properties card, and the
// one fact this file exists to pin -- which id the copy chip carries.
//
// The card was built with the chip bound to opened_by_krill_session_id while
// its <dt>, its aria-label and its data-krill hook all said "design session".
// Every label was true and the value was not, so nothing on the page read as
// broken to a reader and to a test alike: the rendered chip simply carried a
// different id than the FR promises. A live check missed it. These cases are
// therefore written against two ids that are deliberately far apart and
// deliberately BOTH set -- the case that was missed is the case where the
// krill session id is present, because that is the only configuration in
// which the two could be confused.

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Two fixed ids that share no prefix, so "the chip shows an id" can never be
// satisfied by the wrong one.
const (
	designSessionID = "3a7c1e90-4b2d-4f8a-9c15-6d0e2b8a7f43"
	krillSessionID  = "17f0f92f-dfa9-452d-b443-cd71abae586b"
)

// renderPropertiesCard is the properties card as the detail rail renders it,
// with every id set -- the configuration in which the chip could bind to
// either id.
func renderPropertiesCard(t *testing.T) string {
	t.Helper()
	return renderBody(t, designSessionPropertiesCard(DesignSessionDetailPage{
		ID:                     designSessionID,
		OpeningRequest:         "Facelift the operator UI onto a workspace shell",
		OpenedBy:               "alex",
		OpenedByTitle:          "alex@https://idp.test",
		OpenedByKrillSessionID: krillSessionID,
	}))
}

// The chip's data-task-id IS the design session's own id -- the id this page
// is about and the one the URL addresses -- and the visible text is its first
// eight characters, per the shared chip contract.
func TestDesignSessionPropertiesChipCarriesTheDesignSessionID(t *testing.T) {
	card := renderPropertiesCard(t)

	assert.Contains(t, card, `data-task-id="`+designSessionID+`"`,
		"the chip must carry the design session's own id, the one the page and URL are about")
	assert.Equal(t, designSessionID[:8], chipText(t, card),
		"the chip shows the design session id's first eight characters")
}

// The regression assertion, and the case the live check missed: with the
// krill session id SET, the chip still must not carry it.
//
// Scoped to the chip's own <dd>, because the krill session id DOES render
// on this card -- as its own labelled provenance row, per FR d8146d9e. The
// claim under test is which id the CHIP copies, not whether the page knows
// both of them.
func TestDesignSessionPropertiesChipDoesNotCarryTheKrillSessionID(t *testing.T) {
	require.NotEmpty(t, krillSessionID, "this case is meaningless unless a krill session id is set")
	card := renderPropertiesCard(t)
	chip := chipBlock(t, card)

	assert.NotContains(t, chip, krillSessionID,
		"the chip must not carry opened_by_krill_session_id even when it is set: "+
			"the operator copies a different id than the aria-label, the data-krill hook and the FR promise")
	assert.Contains(t, chip, "Design session",
		"the <dt> beside the chip names the design session, which is what it copies")
}

// FR d8146d9e asks for the krill session too -- "Opened by ... plus the
// krill session it was opened through" -- and get_design_session returns
// it. Rebinding the chip must not have cost the page that field: it
// renders as its own row, labelled as what it is.
func TestDesignSessionPropertiesStillRendersTheKrillSessionAsProvenance(t *testing.T) {
	card := renderPropertiesCard(t)

	assert.Contains(t, card, krillSessionID,
		"the krill session the design session was opened through is still on the page")
	assert.Contains(t, card, `data-krill="design-session-opened-by-krill-session"`,
		"it renders as its own provenance row, not as the chip's value")
	provenance := betweenTags(t, card, `data-krill="design-session-opened-by-krill-session"`, "</dd>")
	assert.Equal(t, krillSessionID, strings.TrimSpace(provenance),
		"the row carries the whole krill session id, not a truncated prefix")
}

// The shared chip contract is untouched: this hook is app-wide (task detail,
// milestone detail, spec blade) and forking it would break every other chip.
func TestDesignSessionPropertiesChipKeepsTheSharedCopyContract(t *testing.T) {
	card := renderPropertiesCard(t)
	chip := chipBlock(t, card)

	assert.Contains(t, chip, `data-krill="copy-task-id"`,
		"the chip keeps the app-wide clipboard hook")
	assert.Contains(t, chip, `data-krill="copy-task-id-status"`,
		"the sibling status live region is what lets the shared script confirm a copy")
	assert.Contains(t, chip, `aria-label="Copy design session id"`,
		"the aria-label tells the truth about which id is copied")
	assert.Contains(t, chip, "disabled",
		"the chip ships disabled; the shared head script is what enables it")
	assert.Contains(t, chip, "Copying the id needs JavaScript",
		"a disabled chip keeps the title fallback explaining itself")
}

// The Opened by row is untouched by the chip's rebinding -- it still names the
// identity the server read and still offers no input for one (LB4).
func TestDesignSessionPropertiesOpenedByRowIsUnchanged(t *testing.T) {
	card := renderPropertiesCard(t)

	assert.Contains(t, card, `data-krill="design-session-opened-by"`,
		"the Opened by row keeps its own hook")
	assert.Contains(t, card, "alex", "the row names the subject the server read")
	assert.Contains(t, card, `title="alex@https://idp.test"`,
		"the full (sub, iss) pair stays on the title")
	for _, forbidden := range []string{
		`name="operator"`, `name="subject"`, `name="acting"`, `name="identity"`,
	} {
		assert.NotContains(t, card, forbidden,
			"the page must offer no input for operator identity (LB4): found %s", forbidden)
	}
}

// chipText is the button's visible text: everything the chip renders between
// its tags, trimmed of templ's whitespace.
func chipText(t *testing.T, card string) string {
	t.Helper()
	start := strings.Index(card, `<button type="button"`)
	require.GreaterOrEqual(t, start, 0, "the properties card renders no copy chip")
	rest := card[start:]
	open := strings.Index(rest, ">")
	closeAt := strings.Index(rest, "</button>")
	require.Greater(t, open, -1, "the chip's opening tag is unterminated")
	require.Greater(t, closeAt, open, "the chip's closing tag precedes its opening tag")
	return strings.TrimSpace(rest[open+1 : closeAt])
}

// chipBlock is the chip's own row -- its <dt> label and its <dd> button --
// so a claim about the chip is never satisfied, or falsified, by text that
// belongs to a different row. It starts at the enclosing <div> because the
// label is a sibling of the <dd>, rendered before it.
func chipBlock(t *testing.T, card string) string {
	t.Helper()
	dd := strings.Index(card, `<dd data-krill="design-session-id">`)
	require.GreaterOrEqual(t, dd, 0, "the properties card renders no design-session-id <dd>")
	row := strings.LastIndex(card[:dd], "<div")
	require.GreaterOrEqual(t, row, 0, "the chip's <dd> is not inside a row of its own")
	closeAt := strings.Index(card[dd:], "</dd>")
	require.Greater(t, closeAt, 0, "the chip's <dd> is unterminated")
	return card[row : dd+closeAt]
}

// betweenTags is the element's inner text: everything after the tag carrying
// marker up to the next closeTag.
func betweenTags(t *testing.T, html, marker, closeTag string) string {
	t.Helper()
	at := strings.Index(html, marker)
	require.GreaterOrEqual(t, at, 0, "no element carries %s", marker)
	rest := html[at:]
	open := strings.Index(rest, ">")
	closeAt := strings.Index(rest, closeTag)
	require.Greater(t, open, -1, "%s is an unterminated tag", marker)
	require.Greater(t, closeAt, open, "%s appears before its opening tag", closeTag)
	return rest[open+1 : closeAt]
}
