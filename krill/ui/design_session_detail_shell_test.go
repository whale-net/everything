// The session detail page's SHELL: the breadcrumb, the h1 with its stage
// badge, the two-column layout's regions, and the properties card
// (FR a77852a9, FR d8146d9e).
//
// This file asserts through components' own mappers and through the
// regions' stable ids, never against literal class strings: the claims
// under test are "the badge beside the h1 is the same badge the list
// shows" and "the rail's cards sit in these regions", and both survive a
// restyle that a class-literal assertion would not (htmxui ARCHITECTURE
// §14).
//
// What is deliberately NOT here: what goes IN the timeline or the rail.
// design_page_test.go already pins the log and the open-question table
// against get_design_session's and list_open_questions's own wire
// responses, and those cases keep running -- this page moved their markup
// into the new regions, it did not rewrite it.
package main

import (
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/whale-net/everything/krill/store"
	"github.com/whale-net/everything/krill/ui/components"
)

// The detail page's region ids. These are the contract the next task's
// timeline and rail fill, so they are named here as constants rather than
// repeated as literals across the cases.
const (
	regionDetailShell = `id="design-session-detail"`
	// The breadcrumb is marked by a data attribute rather than an id: it is
	// a landmark, not a swap target, and a test should slice on the same
	// marker the markup carries.
	regionDetailBread   = `data-krill="design-session-breadcrumb"`
	regionDetailOpening = `id="session-opening"`
	regionDetailProps   = `id="session-properties"`
	markerOpenedByLabel = `data-krill="design-session-opened-by"`
	markerStageBadge    = `data-krill="design-session-stage"`
	markerCopyChip      = `data-krill="copy-task-id"`
	markerH1            = `data-krill="design-session-title"`
)

// detailShellFixture is one rendered session detail: the served body plus
// the ids a case asserts on.
type detailShellFixture struct {
	body       string
	sessionID  uuid.UUID
	productID  uuid.UUID
	krillSess  string
	crumbTrail string
}

// renderDetailShell serves one session's detail over fakes and returns the
// body. openedBy is the identity the aggregate reports; the zero Subject
// is how "the krill_session row could not be read" reaches the view.
func renderDetailShell(t *testing.T, productID, sessionID uuid.UUID, openedBy store.Subject, stage store.Stage) detailShellFixture {
	t.Helper()
	const submission = "Facelift the operator UI onto a workspace shell\n\nIt should use the postgres flag table."
	krillSessionID := store.SessionID(uuid.MustParse("d90a77e3-1111-2222-3333-444455556666"))

	summary := designSummary(sessionID, productID, submission, stage, time.Now(), 0)
	summary.OpenedBy = openedBy
	summary.OpenedByKrillSessionID = krillSessionID

	ds := fakeDesignSessions{
		byID:      map[uuid.UUID]store.DesignSession{sessionID: {ID: sessionID, ProductID: productID}},
		summaries: designSummaries(productID, summary),
	}
	app := newDesignReadApp(ds, fakeRevisionEvents{
		bySession: map[uuid.UUID][]store.RevisionEvent{sessionID: {event(1, store.EventTypeDraft)}},
	})
	rec := get(designReadMux(app), designSessionPath(productID, sessionID))
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

	return detailShellFixture{
		body:       rec.Body.String(),
		sessionID:  sessionID,
		productID:  productID,
		krillSess:  krillSessionID.String(),
		crumbTrail: firstLine(submission),
	}
}

// operatorAlex is the opening identity the properties card is about: a
// real Subject, minted by the server's read rather than typed anywhere.
var operatorAlex = store.Subject{Iss: "https://idp.test", Sub: "alex", Kind: store.SubjectKindHuman}

func TestDesignSessionDetail_Shell_H1CarriesTheFirstLineAndTheStageBadge(t *testing.T) {
	// Every stage the store can derive, so the badge beside the h1 is
	// pinned for the whole vocabulary rather than one representative.
	for _, stage := range []store.Stage{
		store.StageOpened, store.StageInDraft, store.StageArchitectReview,
		store.StageAnswered, store.StageRuled, store.StageChangesRequested,
		store.StageApproved,
	} {
		t.Run(string(stage), func(t *testing.T) {
			productID, sessionID := uuid.New(), uuid.New()
			f := renderDetailShell(t, productID, sessionID, operatorAlex, stage)

			// The h1 is the opening request's FIRST LINE -- the same string
			// the list's Opening request cell links by, so a reader who
			// clicked through sees the row's own wording, not the whole
			// multi-paragraph submission.
			shell := pageSection(t, f.body, regionDetailShell, "")
			h1 := betweenTags(t, shell, markerH1, "</h1>")
			assert.Equal(t, "Facelift the operator UI onto a workspace shell", h1,
				"the h1 is the opening request's first line")

			// The badge's wording and colour are resolved through the SAME
			// components mapper the sessions list uses -- asserted against
			// the mapper's own answer, not against a class literal, so a
			// restyle of either page keeps this true. The helper is the
			// list test's own, so "the same mapper" is enforced by both
			// pages sharing one assertion rather than by two copies of it.
			badge := betweenTags(t, shell, markerStageBadge, "</span>")
			assert.Equal(t, components.DesignSessionStageLabel(string(stage)), badge,
				"the stage badge must carry the list's own label for this stage")
			assert.NotEmpty(t, badge, "a stage badge is never blank, known or not")
			assertStageBadgeMatchesMapper(t, f.body, stage)
		})
	}
}

// betweenTags returns the text content of the element that carries the
// given attribute marker: everything between the end of that element's own
// opening tag and its closing tag.
//
// It skips to the opening tag's closing ">" first, because the marker is
// one attribute among several and the rest of them (a title, a data-task-id)
// is not the element's content -- reading from the marker to the closing
// tag would return the element's own attributes as its text. It exists so
// an assertion names an attribute and a boundary rather than a slice of the
// whole page, which is what keeps these assertions about the element they
// name.
func betweenTags(t *testing.T, body, marker, end string) string {
	t.Helper()
	i := strings.Index(body, marker)
	require.NotEqual(t, -1, i, "body must contain %q", marker)
	rest := body[i+len(marker):]
	open := strings.Index(rest, ">")
	require.NotEqual(t, -1, open, "the element carrying %q must have an opening tag", marker)
	rest = rest[open+1:]
	j := strings.Index(rest, end)
	require.NotEqual(t, -1, j, "body must contain %q after %q", end, marker)
	return strings.TrimSpace(rest[:j])
}

// detailRegion returns the slice of body from the region marker to the
// FIRST end marker after it.
//
// It does not reuse design_page_test.go's pageSection, which takes the
// LAST occurrence of the boundary: that works for a page whose region ends
// at the end of its <section>, but a <nav> closes once inside the main
// column and again around the sidebar, and LastIndex would hand this page's
// breadcrumb an assertion scope running through the whole chrome. First is
// the correct reading for a section that is not the last of its kind.
func detailRegion(t *testing.T, body, marker, end string) string {
	t.Helper()
	i := strings.Index(body, marker)
	require.NotEqual(t, -1, i, "body must contain %q", marker)
	rest := body[i+len(marker):]
	if end == "" {
		return rest
	}
	j := strings.Index(rest, end)
	require.NotEqual(t, -1, j, "body must contain %q after %q", end, marker)
	return rest[:j]
}

// TestDesignSessionDetail_Shell_BreadcrumbNamesProductListAndSession is FR
// a77852a9's breadcrumb: the way back to the product, the way back to its
// session list, and then this session by name.
//
// The first two are links and the third is not, because the reader is
// already on it -- asserted by the third crumb being a <span> rather than
// an <a>, since a self-link in a breadcrumb is a navigation affordance that
// goes nowhere.
func TestDesignSessionDetail_Shell_BreadcrumbNamesProductListAndSession(t *testing.T) {
	productID, sessionID := uuid.New(), uuid.New()
	f := renderDetailShell(t, productID, sessionID, operatorAlex, store.StageInDraft)

	nav := detailRegion(t, f.body, regionDetailBread, "</nav>")

	// The Design sessions crumb links to this product's own list URL -- the
	// canonical one the route serves, not the legacy unscoped spelling.
	assert.Contains(t, nav, `href="`+designProductSessionsPath(productID)+`"`,
		"the Design sessions crumb must link to the product's own session list")
	assert.Contains(t, nav, "Design sessions")

	// The session's own title closes the trail, as plain text.
	assert.Contains(t, nav, f.crumbTrail,
		"the breadcrumb must name the session by its opening request")
	lastCrumb := nav[strings.LastIndex(nav, "<li"):]
	assert.Contains(t, lastCrumb, "<span>"+f.crumbTrail+"</span>",
		"the session's own crumb is the page the reader is already on, so it is text, not a link")
	assert.NotContains(t, lastCrumb, "<a ",
		"the final breadcrumb must not be a link to the page already open")

	// A product whose name the server could not read contributes no crumb
	// rather than an empty link: a blank breadcrumb entry looks like a
	// rendering fault. The list crumb is the one that navigates, so it is
	// the one that must always be there.
	t.Run("an unreadable product name drops the product crumb", func(t *testing.T) {
		bare := newDesignReadApp(
			fakeDesignSessions{
				byID: map[uuid.UUID]store.DesignSession{sessionID: {ID: sessionID, ProductID: productID}},
				summaries: designSummaries(productID, designSummary(
					sessionID, productID, "an idea", store.StageOpened, time.Now(), 0)),
			},
			fakeRevisionEvents{},
		)
		rec := get(designReadMux(bare), designSessionPath(productID, sessionID))
		require.Equal(t, http.StatusOK, rec.Code)
		nav := detailRegion(t, rec.Body.String(), regionDetailBread, "</nav>")
		assert.Equal(t, 2, strings.Count(nav, "breadcrumb-crumb"),
			"the list and the session's own title are the only two crumbs when the product has no readable name")
		assert.Contains(t, nav, "Design sessions")
	})
}

// TestDesignSessionDetail_Shell_TwoColumnRegions pins the layout's
// structure by region id: the log and the opening statement in the main
// column, the questions and the properties in the rail, and the grid that
// puts a column beside a column at lg.
//
// The rail assertion is positional, not a class check: "Open questions
// sits BESIDE the timeline" is the claim, and a class-literal assertion
// against the grid template would break on any restyle of an unrelated
// class on that div.
func TestDesignSessionDetail_Shell_TwoColumnRegions(t *testing.T) {
	productID, sessionID := uuid.New(), uuid.New()
	f := renderDetailShell(t, productID, sessionID, operatorAlex, store.StageAnswered)

	shell := pageSection(t, f.body, regionDetailShell, "")

	for _, region := range []string{regionDetailOpening, regionRevisionLog, regionOpenQuestion, regionDetailProps} {
		assert.Contains(t, shell, region, "the shell must carry region %s", region)
	}

	// The main column holds the opening statement then the log; the rail
	// holds the questions then the properties. Ordering within each column
	// is asserted by byte position, which is what "above" means here.
	openAt := strings.Index(shell, regionDetailOpening)
	logAt := strings.Index(shell, regionRevisionLog)
	qAt := strings.Index(shell, regionOpenQuestion)
	propsAt := strings.Index(shell, regionDetailProps)
	require.NotEqual(t, -1, openAt)
	require.NotEqual(t, -1, logAt)
	require.NotEqual(t, -1, qAt)
	require.NotEqual(t, -1, propsAt)
	assert.Less(t, openAt, logAt, "the opening statement reads above the timeline")
	assert.Less(t, logAt, qAt, "the timeline is in the column BEFORE the rail, not after it")
	assert.Less(t, qAt, propsAt, "the properties card follows the questions in the rail")

	// The rail's markup is inside an <aside> -- the structural claim that
	// it is a rail rather than another block in the flow.
	aside := betweenTags(t, shell, "<aside", "</aside>")
	assert.Contains(t, aside, regionOpenQuestion, "the questions card is in the rail")
	assert.Contains(t, aside, regionDetailProps, "the properties card is in the rail")
	assert.NotContains(t, aside, regionRevisionLog, "the log is NOT in the rail")

	// The full opening submission renders as prose in the main column -- it
	// is the session's opening statement and this page is the only place it
	// is readable in full, so both paragraphs must be there.
	opening := detailRegion(t, shell, regionDetailOpening, "</section>")
	assert.Contains(t, opening, "Facelift the operator UI onto a workspace shell")
	assert.Contains(t, opening, "It should use the postgres flag table.",
		"the whole submission renders, not just the h1's first line")
	assert.Contains(t, opening, "<p>",
		"the submission renders as markdown paragraphs, not as one run-on line of raw text")
}

// TestDesignSessionDetail_Shell_PropertiesCardShowsWhoOpenedIt is FR
// d8146d9e: an "Opened by" line naming the operator the server read, and
// the design session's own id as a copy chip.
func TestDesignSessionDetail_Shell_PropertiesCardShowsWhoOpenedIt(t *testing.T) {
	productID, sessionID := uuid.New(), uuid.New()
	f := renderDetailShell(t, productID, sessionID, operatorAlex, store.StageInDraft)
	card := detailRegion(t, f.body, regionDetailProps, "</section>")

	t.Run("Opened by names the subject the server read", func(t *testing.T) {
		dd := betweenTags(t, card, markerOpenedByLabel, "</dd>")
		assert.Equal(t, operatorAlex.Sub, dd,
			"the label is the acting subject, not a composite that buries it")
		// The full (sub, iss) pair is on the title, for the case where one
		// sub means different people at different issuers.
		assert.Contains(t, card, `title="`+operatorAlex.Sub+"@"+operatorAlex.Iss+`"`,
			"the full (sub, iss) pair is available on the element's title")
	})

	t.Run("the design session id is a copy chip carrying the whole id", func(t *testing.T) {
		chip := betweenTags(t, card, markerCopyChip, "</button>")
		// The chip shows a readable prefix; the whole value is what the
		// clipboard receives, which is the attribute the shared head script
		// reads.
		assert.Equal(t, f.sessionID.String()[:8], strings.TrimSpace(chip),
			"the chip shows the id's first eight characters, not all 36")
		assert.Contains(t, card, `data-task-id="`+f.sessionID.String()+`"`,
			"the full id is what the chip copies")
		// The krill session it was opened through is a DIFFERENT id, and it
		// is set in this fixture: a chip that carried it would satisfy every
		// label on the page while copying the wrong value, which is the
		// defect this assertion exists to keep fixed.
		require.NotEqual(t, f.sessionID.String(), f.krillSess,
			"the two ids must differ or this case cannot detect the wrong binding")
		assert.NotContains(t, card, `data-task-id="`+f.krillSess+`"`,
			"the chip must not carry the krill session id the design session was opened through")
		// The krill session is not lost by that -- FR d8146d9e asks for it
		// too, and get_design_session returns it. It renders as its own
		// labelled row, which is the one place it belongs now that the chip
		// is not wearing it.
		assert.Contains(t, card, `data-krill="design-session-opened-by-krill-session"`,
			"the krill session still renders, as provenance beside the chip")
		assert.Contains(t, card, f.krillSess,
			"the provenance row carries the whole krill session id")
		// Ships disabled, like every other chip in this app: a control that
		// cannot work until the head script binds it must not look live.
		assert.Contains(t, card, "disabled",
			"the chip ships disabled; the shared head script is what enables it")
		assert.Contains(t, card, `data-krill="copy-task-id-status"`,
			"the chip keeps the shared script's confirmation span, so there is one clipboard implementation")
	})

	t.Run("no field anywhere asks for an operator identity", func(t *testing.T) {
		shell := pageSection(t, f.body, regionDetailShell, "")
		for _, forbidden := range []string{
			`name="operator"`, `name="subject"`, `name="acting"`,
			`name="on_behalf_of"`, `name="identity"`, `name="user"`,
		} {
			assert.NotContains(t, shell, forbidden,
				"the page must offer no input for operator identity (LB4): found %s", forbidden)
		}
		// And no form anywhere posts an identity to this session.
		for _, form := range regexp.MustCompile(`(?s)<form[^>]*>.*?</form>`).FindAllString(shell, -1) {
			assert.NotContains(t, form, `name="operator"`)
			assert.NotContains(t, form, `name="acting"`)
		}
	})
}

// TestDesignSessionDetail_Shell_MissingKrillSessionRendersNoLabel is the
// boundary case FR d8146d9e names: when the krill_session row behind
// opened_by_krill_session_id cannot be read, the card shows the id chip and
// NO "Opened by" line at all.
//
// Not a blank line and not a placeholder -- a page that records who did
// what must never put an invented operator on a session nobody signed, and
// a rendered-but-empty "Opened by" reads to an operator as "we know who
// this was and won't tell you", which is the same lie in a quieter register.
func TestDesignSessionDetail_Shell_MissingKrillSessionRendersNoLabel(t *testing.T) {
	productID, sessionID := uuid.New(), uuid.New()
	f := renderDetailShell(t, productID, sessionID, store.Subject{}, store.StageOpened)
	card := detailRegion(t, f.body, regionDetailProps, "</section>")

	assert.NotContains(t, card, "Opened by",
		"an unreadable krill_session must produce no Opened by line, not an empty one")
	assert.NotContains(t, card, markerOpenedByLabel,
		"the label element itself must be absent, so a test cannot find an identity to render")

	// The id chip still renders: it is a fact the design_session row does
	// hold, and dropping it would hide the session's own provenance.
	assert.Contains(t, card, markerCopyChip, "the design session id chip renders even without an operator")
	assert.Contains(t, card, `data-task-id="`+f.sessionID.String()+`"`,
		"the chip still carries the whole design session id")
	assert.NotContains(t, card, `data-task-id="`+f.krillSess+`"`,
		"an unreadable krill_session must not change which id the chip carries")
	assert.Contains(t, card, `data-krill="design-session-opened-by-krill-session"`,
		"and the provenance row still renders: the design_session row holds this id "+
			"whether or not the krill_session behind it could be read")

	// And the card invents no substitute: no issuer, no bare subject, no
	// placeholder text standing in for the operator it could not read.
	// Scoped to the card on purpose -- a revision event's own acting subject
	// names its own issuer, and "who drafted round 3" is a different fact
	// from "who opened this session".
	for _, fabricated := range []string{"@" + operatorAlex.Iss, operatorAlex.Sub, "unknown", "n/a", "—"} {
		assert.NotContains(t, card, fabricated,
			"the properties card must render no stand-in identity (%q)", fabricated)
	}
}

// TestDesignSessionDetail_Shell_ListAndDetailAgreeOnStageAndTitle is the
// cross-page claim this whole shell exists to keep true: the row an
// operator clicks and the page they land on name the same session the same
// way.
//
// Both pages are served from ONE fake, which is what makes this a real
// check rather than two independent assertions -- if the detail re-derived
// the stage, or spelled the title differently from the list, the two pages
// would be driven by different inputs and this test would still pass.
func TestDesignSessionDetail_Shell_ListAndDetailAgreeOnStageAndTitle(t *testing.T) {
	const submission = "Facelift the operator UI onto a workspace shell\n\nThe rest of the opening statement."
	productID, sessionID := uuid.New(), uuid.New()

	summary := designSummary(sessionID, productID, submission, store.StageArchitectReview, time.Now(), 0)
	summary.OpenedBy = operatorAlex
	ds := fakeDesignSessions{
		byID:      map[uuid.UUID]store.DesignSession{sessionID: {ID: sessionID, ProductID: productID}},
		summaries: designSummaries(productID, summary),
	}
	mux := designReadMux(newDesignReadApp(ds, fakeRevisionEvents{
		bySession: map[uuid.UUID][]store.RevisionEvent{sessionID: {event(1, store.EventTypeDraft)}},
	}))

	list := get(mux, designProductSessionsPath(productID))
	require.Equal(t, http.StatusOK, list.Code, list.Body.String())
	detail := get(mux, designSessionPath(productID, sessionID))
	require.Equal(t, http.StatusOK, detail.Code, detail.Body.String())

	wantTitle := firstLine(submission)
	wantStage := components.DesignSessionStageLabel(string(store.StageArchitectReview))

	// The list's own Opening request cell, and the detail's h1, are the
	// same string.
	assert.Contains(t, list.Body.String(), ">"+wantTitle+"</a>",
		"the list's Opening request cell links by the first line")
	assert.Equal(t, wantTitle, betweenTags(t, detail.Body.String(), markerH1, "</h1>"),
		"the detail's h1 is that same first line")

	// And the stage badge beside the h1 is the badge the list's Stage cell
	// shows -- one mapper, one vocabulary.
	assert.Contains(t, list.Body.String(), ">"+wantStage+"</span>",
		"the list shows the stage through the shared label mapper")
	assert.Equal(t, wantStage, betweenTags(t, detail.Body.String(), markerStageBadge, "</span>"),
		"the detail's stage badge is the same badge the list showed")
}

// TestDesignSessionDetail_Shell_LogAndQuestionsStillRender is the
// regression this shell could plausibly have broken: moving the log and the
// question list into new regions must not lose a round or a question of
// either.
//
// It asserts presence rather than re-parsing: design_page_test.go already
// pins both against get_design_session's and list_open_questions's own
// wire responses, byte for byte. This case exists to fail loudly if a
// future layout change drops a region from the template and the pinned
// parity cases stop finding what they parse.
func TestDesignSessionDetail_Shell_LogAndQuestionsStillRender(t *testing.T) {
	productID, sessionID := uuid.New(), uuid.New()

	draft := event(1, store.EventTypeDraft)
	draft.VerifiedAgainst = strptr("spec.md#1")
	draft.EntityDeltas = []store.EntityDelta{{
		EntityID:    uuid.New(),
		Change:      store.EntityDeltaChangeCreated,
		SummaryLine: "added the rollback requirement",
	}}
	draft.OpenQuestionsDelta.Opened = []store.OpenQuestionOpened{{
		QuestionID: "q1", Blocking: true, Text: "which store holds the flag?",
	}}

	ds := fakeDesignSessions{
		byID:      map[uuid.UUID]store.DesignSession{sessionID: {ID: sessionID, ProductID: productID}},
		summaries: designSummaries(productID, designSummary(sessionID, productID, "an idea", store.StageInDraft, time.Now(), 1)),
	}
	app := newDesignReadApp(ds, fakeRevisionEvents{
		bySession: map[uuid.UUID][]store.RevisionEvent{sessionID: {draft}},
		openQuestions: map[uuid.UUID][]store.OpenQuestion{sessionID: {
			{QuestionID: "blocker", Text: "needs a decision", Blocking: true, OpenedAtSeqNo: 1},
		}},
	})
	rec := get(designReadMux(app), designSessionPath(productID, sessionID))
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

	log := detailRegion(t, rec.Body.String(), regionRevisionLog, "</section>")
	assert.Contains(t, log, `data-krill-seq-no="1"`, "the log region still holds the session's rounds")
	assert.Contains(t, log, "added the rollback requirement")
	assert.Contains(t, log, "spec.md#1")

	questions := detailRegion(t, rec.Body.String(), regionOpenQuestion, "</section>")
	assert.Contains(t, questions, "needs a decision", "the questions region still holds the open questions")
	assert.Contains(t, questions, components.QuestionBlockingLabel("blocking"))

	// The rail's resolve boxes read the SAME question set, so it must still
	// be offered for this session.
	assert.Contains(t, rec.Body.String(), `value="blocker"`,
		"the resolve checkbox set survives the relayout")
}

// TestDesignSessionDetail_Shell_SidebarFollowsTheURLsProduct guards the
// detail against resolving its product from the last-viewed cookie: a
// session under product B, opened while the cookie names product A, must
// select B in the switcher and record B as last viewed.
func TestDesignSessionDetail_Shell_SidebarFollowsTheURLsProduct(t *testing.T) {
	productA := store.Product{ID: uuid.MustParse("aaaaaaaa-0000-0000-0000-000000000001"), Name: "alpha"}
	productB := store.Product{ID: uuid.MustParse("bbbbbbbb-0000-0000-0000-000000000002"), Name: "bravo"}
	sessionID := uuid.MustParse("cccccccc-0000-0000-0000-000000000003")

	summary := designSummary(sessionID, productB.ID, "Bravo's session", store.StageOpened, time.Now(), 0)
	ds := fakeDesignSessions{
		byID:      map[uuid.UUID]store.DesignSession{sessionID: {ID: sessionID, ProductID: productB.ID}},
		summaries: designSummaries(productB.ID, summary),
	}
	app := newDesignReadApp(ds, fakeRevisionEvents{})
	app.spec = scopedProductsReader{specReadClient: emptyScopeSpecReader{}, products: []store.Product{productA, productB}}

	req := httptest.NewRequest(http.MethodGet, designSessionPath(productB.ID, sessionID), nil)
	req.AddCookie(&http.Cookie{Name: lastViewedProductCookie, Value: productA.ID.String()})
	rec := httptest.NewRecorder()
	designReadMux(app).ServeHTTP(rec, req)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

	body := rec.Body.String()
	assert.Contains(t, body, `value="`+productB.ID.String()+`" selected`,
		"the switcher selects the product the URL names")
	assert.NotContains(t, body, `value="`+productA.ID.String()+`" selected`)
	assert.Contains(t, body, `href="/products/`+productB.ID.String()+`/overview"`,
		"the sidebar links stay on the URL's product")
	assert.Contains(t, rec.Header().Get("Set-Cookie"), productB.ID.String(),
		"the URL's product becomes the last viewed")
}
