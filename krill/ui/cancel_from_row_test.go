// Testing-lane coverage for FR 336335f1: Cancel from a Needs attention row
// is a browser confirmation when JavaScript is present, the existing confirm
// page when it is not, and both halves carry the claim or escalation the row
// observed.
//
// The tests render the row through the tab's OWN loader (needsAttentionResults),
// read the control's action, its return_to and its observed-state guard OFF THAT
// MARKUP -- never spelling any of them -- and then take the path the browser
// would, so what is exercised is the control the operator is actually looking
// at rather than a URL the test invented.
//
// The four things this lane owes:
//
//   - JS accept and JS dismiss: hx-confirm gates the htmx POST (dismiss issues
//     no request) and the only native submission is a GET to the confirm page,
//     so nothing can post a cancel without an explicit confirmation.
//   - the no-JS page carrying the ids: the confirm page's own form hands the
//     observed claim or escalation on to the cancel POST.
//   - a Done-lane row offering no Cancel.
//   - the FR's fourth bullet: a refused Cancel's re-rendered confirmation is
//     rebuilt from FRESH state with FRESH ids, and the two refusal origins are
//     answered into the region each came from.
package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/whale-net/everything/krill/store"
	"github.com/whale-net/everything/krill/ui/pages"
)

// ---------------------------------------------------------------------------
// fixture
// ---------------------------------------------------------------------------

// cancelRowFixture is the world these tests walk: the harness's real signed-in
// operator and real write client against a fake api, with the Needs attention
// page's own task store carrying rows a test chooses and the fresh task a
// refused cancel is rebuilt from.
type cancelRowFixture struct {
	app     *App
	mux     *http.ServeMux
	api     *fakeAPI
	session *http.Cookie
	tasks   *fakeFragmentTasks
	pid     uuid.UUID
}

func newCancelRowFixture(t *testing.T) *cancelRowFixture {
	t.Helper()

	api := newFakeAPI(t)
	// newHtmxInterventionApp's fake task already holds harnessObservedClaimID,
	// so a card rebuilt from fresh state has a non-zero id to carry -- the
	// difference between "rebuilt from fresh state" and "rebuilt from the
	// refused request's ids" is observable at all only because the two differ.
	app, sessionCookie, _ := newHtmxInterventionApp(t, api, uuid.NewString())
	tasks, ok := app.tasks.(*fakeFragmentTasks)
	require.True(t, ok, "the harness must wire its fragment task store")
	return &cancelRowFixture{
		app: app, mux: newInterventionMux(app), api: api, session: sessionCookie,
		tasks: tasks, pid: testScopeID,
	}
}

// region renders one tab's results region through the tab's own loader, the
// same function the tab's GET and a post-write re-derivation both call.
func (f *cancelRowFixture) region(t *testing.T, tab string) string {
	t.Helper()
	view, err := f.app.needsAttentionResults(context.Background(), f.pid,
		needsAttentionFilter{}, "", tab, store.PageParams{},
		needsAttentionTabHref(f.pid, tab), f.app.clock(), "")
	require.NoError(t, err)
	return mustRenderComponent(view.Results)
}

// claimedRow is one claimed task whose claim id the row's controls must carry.
func (f *cancelRowFixture) claimedRow(lane store.Lane) store.ClaimedTaskRow {
	return store.ClaimedTaskRow{
		TaskID:      uuid.MustParse("a1111111-1111-2222-3333-444444444444"),
		Title:       "Paginate ListClaimedTasks",
		CurrentLane: lane,
		ClaimID:     uuid.MustParse("a2222222-1111-2222-3333-444444444444"),
	}
}

// escalatedRow is one escalated task whose escalation id the row's controls
// must carry.
func (f *cancelRowFixture) escalatedRow(lane store.Lane) store.EscalatedTaskRow {
	return store.EscalatedTaskRow{
		TaskID:       uuid.MustParse("a3333333-1111-2222-3333-444444444444"),
		Title:        "Requeue drops the escalation",
		Reason:       store.EscalationReasonThrashCap,
		Lane:         lane,
		EscalationID: uuid.MustParse("a4444444-1111-2222-3333-444444444444"),
	}
}

// ---------------------------------------------------------------------------
// reading a control off the markup
// ---------------------------------------------------------------------------

// formContaining is the whole <form>...</form> element whose markup contains
// needle. The Needs attention tables never nest forms, so the last <form>
// before the needle and the first </form> after it bound the element.
func formContaining(t *testing.T, markup, needle string) string {
	t.Helper()
	i := strings.Index(markup, needle)
	require.GreaterOrEqual(t, i, 0, "no markup contains %q", needle)
	start := strings.LastIndex(markup[:i], "<form")
	require.GreaterOrEqual(t, start, 0, "no <form> opens before %q", needle)
	end := strings.Index(markup[i:], "</form>")
	require.GreaterOrEqual(t, end, 0, "the form around %q is not closed", needle)
	return markup[start : i+end+len("</form>")]
}

// formAttr is one attribute's value off a rendered form.
func formAttr(t *testing.T, form, attr string) string {
	t.Helper()
	key := attr + `="`
	i := strings.Index(form, key)
	require.GreaterOrEqual(t, i, 0, "the form carries no %s attribute", attr)
	rest := form[i+len(key):]
	j := strings.Index(rest, `"`)
	require.GreaterOrEqual(t, j, 0)
	return rest[:j]
}

// hiddenInputs reads every hidden input a form renders, name -> value. This is
// exactly what a browser serialises -- as a GET query string or a POST body --
// so a test can submit the control's own fields without re-spelling them.
func hiddenInputs(form string) url.Values {
	values := url.Values{}
	const open = `type="hidden" name="`
	rest := form
	for {
		i := strings.Index(rest, open)
		if i < 0 {
			return values
		}
		rest = rest[i+len(open):]
		j := strings.Index(rest, `" value="`)
		if j < 0 {
			return values
		}
		name := rest[:j]
		rest = rest[j+len(`" value="`):]
		k := strings.Index(rest, `"`)
		if k < 0 {
			return values
		}
		values.Set(name, rest[:k])
		rest = rest[k:]
	}
}

// hasHiddenInput reports whether a form renders a hidden input under name.
func hasHiddenInput(form, name string) bool {
	_, ok := hiddenInputs(form)[name]
	return ok
}

// cancelControl is a tab region's Cancel control: the doubled form whose no-JS
// half is the confirm page.
func cancelControl(t *testing.T, region string) string {
	t.Helper()
	return formContaining(t, region, cancelConfirmSuffix)
}

// cancelCardForm is the confirm card's own form -- the one that POSTs the
// cancel route directly for taskID. It is read by that route rather than by the
// "/cancel/confirm" string the ROW's no-JS half uses, because the card is the
// half that has already been confirmed and posts the cancel itself.
func cancelCardForm(t *testing.T, markup, taskID string) string {
	t.Helper()
	return formContaining(t, markup, "/ops/tasks/"+taskID+`/cancel"`)
}

// guardValues is the observed-state guard a rendered fragment carries, claim
// and escalation each, empty where absent.
func guardValues(form string) (claim, escalation string) {
	values := hiddenInputs(form)
	return values.Get(expectedClaimIDParam), values.Get(escalatedGuardField)
}

// ---------------------------------------------------------------------------
// 1. JS accept and JS dismiss
// ---------------------------------------------------------------------------

// TestCancelFromARowConfirmsInTheBrowserBeforeItPosts is the FR's first
// sentence: with JavaScript the Cancel control confirms (hx-confirm, the FR's
// exact copy, naming the task) and nothing is posted until accepted; a dismiss
// therefore issues no request at all, because the only request the control can
// issue is the htmx POST the confirmation gates.
//
// "Dismiss posts nothing" is a property of the markup this binary controls: the
// native submission is a GET to the confirmation PAGE (which posts nothing by
// itself), and the cancel route is reachable only through the hx-post that
// hx-confirm gates. The confirm() dialog itself is htmx's own behaviour, not
// something this binary reimplements -- so the test pins the wiring, not a
// browser.
func TestCancelFromARowConfirmsInTheBrowserBeforeItPosts(t *testing.T) {
	f := newCancelRowFixture(t)
	row := f.claimedRow(store.LaneImplementation)
	f.tasks.claimed = []store.ClaimedTaskRow{row}

	region := f.region(t, needsAttentionTabClaimed)
	control := cancelControl(t, region)
	taskID := row.TaskID.String()

	// The htmx half is a POST of the cancel route, issued only after htmx's
	// confirmation is accepted.
	assert.Equal(t, "/ops/tasks/"+taskID+"/cancel", formAttr(t, control, "hx-post"),
		"the htmx half posts the cancel route")
	assert.Equal(t, "#"+pages.OpsResultsAnchor, formAttr(t, control, "hx-target"),
		"the htmx half swaps the results region")
	// hx-confirm is the FR's copy, EQUALITY not containment: an implementation
	// that appended anything to the sentence would fail here.
	assert.Equal(t,
		"Cancel "+row.Title+"? It moves to Cancelled and cannot be claimed again.",
		formAttr(t, control, "hx-confirm"),
		"the browser confirmation is exactly the FR's copy, naming the task")

	// The no-JS half: a GET to the confirm page, so a browser without
	// JavaScript opens a page and posts nothing until that page's own form is
	// submitted. This is also what makes a dismiss safe -- with no accepted
	// confirmation there is no POST anywhere on this control.
	assert.Equal(t, "get", formAttr(t, control, "method"),
		"the native half must not POST the irreversible route")
	assert.Equal(t, "/ops/tasks/"+taskID+cancelConfirmSuffix, formAttr(t, control, "action"),
		"the native half opens the confirmation page")

	// There is no plain form that POSTs the cancel route: the irreversible
	// write has exactly one reachable path, and it is gated.
	assert.NotContains(t, region, `<form method="post" action="/ops/tasks/`+taskID+`/cancel"`,
		"no control may post the cancel route without a confirmation")

	// Rendering the page -- the operator deciding, then dismissing -- issues no
	// request of any kind.
	assert.Empty(t, f.api.recorded(), "rendering the control posts nothing; a dismiss posts nothing")
}

// TestCancelFromARowOnAcceptCancelsAndConfirmsWithAToast takes the htmx half
// as an accepted confirmation does: the row's own hx-post, its own return_to,
// its own observed claim id. On accept the write reaches the api's cancel
// endpoint guarded by the id the row observed, the operator is navigated back
// to the tab they acted from (the row leaves it for the Cancelled tab), and the
// "Task cancelled." confirmation rides the one-shot flash the landing page
// renders as a toast.
func TestCancelFromARowOnAcceptCancelsAndConfirmsWithAToast(t *testing.T) {
	f := newCancelRowFixture(t)
	row := f.claimedRow(store.LaneImplementation)
	f.tasks.claimed = []store.ClaimedTaskRow{row}

	region := f.region(t, needsAttentionTabClaimed)
	control := cancelControl(t, region)
	action := formAttr(t, control, "hx-post")
	form := hiddenInputs(control)
	form.Set("reason", "dead-lettered from the row")

	rec := hxFormPost(f.mux, action, form, f.session)

	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.Equal(t, needsAttentionTabHref(f.pid, needsAttentionTabClaimed), rec.Header().Get("HX-Redirect"),
		"the operator lands back on the tab they acted from, re-read fresh")
	assert.Empty(t, rec.Header().Get("Location"), "an htmx request navigates with HX-Redirect, not a 303")
	assert.Equal(t, "Task cancelled.", flashMessageOf(t, rec),
		"the accept confirms with the cancel toast the landing page renders")

	// The write is the api cancel, guarded by the claim THIS row observed.
	write := f.api.writeRequest(t)
	assert.Equal(t, "/tasks/"+row.TaskID.String()+"/cancel", write.Path)
	var body map[string]any
	require.NoError(t, json.Unmarshal(write.Body, &body))
	assert.Equal(t, row.ClaimID.String(), body[expectedClaimIDParam],
		"the accepted cancel carries the claim the row observed")
	assert.Equal(t, "dead-lettered from the row", body["reason"])
}

// flashMessageOf decodes the one-shot toast cookie a successful cancel arms, so
// the confirmation is asserted as the exact string the operator sees.
func flashMessageOf(t *testing.T, rec *httptest.ResponseRecorder) string {
	t.Helper()
	for _, c := range rec.Result().Cookies() {
		if c.Name != toastCookieName {
			continue
		}
		decoded, err := base64.RawURLEncoding.DecodeString(c.Value)
		require.NoError(t, err, "the flash cookie must carry a decodable message")
		return string(decoded)
	}
	return ""
}

// ---------------------------------------------------------------------------
// 2. the no-JS page carries the observed ids
// ---------------------------------------------------------------------------

// TestNoJSConfirmPageCarriesTheObservedGuardForBothRowKinds walks the no-JS
// Cancel affordance end to end for both row kinds: the row's native GET opens
// the confirm page carrying the id the row observed, that page's own form hands
// the same id on to the cancel POST, and the resulting write reaches the api
// guarded -- so a claim or escalation that changed since the row was read is
// refused on the no-JS path exactly as on the htmx one.
func TestNoJSConfirmPageCarriesTheObservedGuardForBothRowKinds(t *testing.T) {
	for _, tc := range []struct {
		name   string
		tab    string
		field  string
		row    func(*cancelRowFixture) (taskID, observed string)
		absent string
	}{
		{
			name: "a Claimed row carries its claim id",
			tab:  needsAttentionTabClaimed,
			row: func(f *cancelRowFixture) (string, string) {
				r := f.claimedRow(store.LaneImplementation)
				f.tasks.claimed = []store.ClaimedTaskRow{r}
				return r.TaskID.String(), r.ClaimID.String()
			},
			field:  expectedClaimIDParam,
			absent: escalatedGuardField,
		},
		{
			name: "an Escalated row carries its escalation id",
			tab:  needsAttentionTabEscalated,
			row: func(f *cancelRowFixture) (string, string) {
				r := f.escalatedRow(store.LaneImplementation)
				f.tasks.escalated = []store.EscalatedTaskRow{r}
				return r.TaskID.String(), r.EscalationID.String()
			},
			field:  escalatedGuardField,
			absent: expectedClaimIDParam,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newCancelRowFixture(t)
			taskID, observed := tc.row(f)

			region := f.region(t, tc.tab)
			control := cancelControl(t, region)
			rowForm := hiddenInputs(control)
			assert.Equal(t, observed, rowForm.Get(tc.field),
				"the row's own control hands the page the id it observed")
			assert.False(t, hasHiddenInput(control, tc.absent),
				"the row observed only one kind of state, so only one guard is rendered")

			// The native half as a browser submits it: action + the form's own
			// hidden inputs as a query string.
			getHref := formAttr(t, control, "action") + "?" + rowForm.Encode()
			getRec := serveWithCookie(f.mux, http.MethodGet, getHref, "", f.session)
			require.Equal(t, http.StatusOK, getRec.Code, getRec.Body.String())

			page := getRec.Body.String()
			card := cancelCardForm(t, page, taskID)
			assert.Contains(t, page, `id="`+pages.CancelConfirmAnchor+`"`)
			assert.Equal(t, observed, hiddenInputs(card).Get(tc.field),
				"the confirm page hands the row's observed guard on to its own form")
			assert.False(t, hasHiddenInput(card, tc.absent),
				"the page must not invent a guard field the row never observed")
			assert.Equal(t, "/ops/tasks/"+taskID+"/cancel", formAttr(t, card, "action"))
			assert.Equal(t, "#"+pages.CancelConfirmAnchor, formAttr(t, card, "hx-target"),
				"the card's form swaps the card itself")

			// Confirming posts the page's own fields, and the guard survives to
			// the api.
			post := hiddenInputs(card)
			post.Set("reason", "dead-lettered after confirmation")
			postRec := serveFormPost(f.mux, formAttr(t, card, "action"), post, f.session)
			require.Equal(t, http.StatusSeeOther, postRec.Code, postRec.Body.String())

			write := f.api.writeRequest(t)
			assert.Equal(t, "/tasks/"+taskID+"/cancel", write.Path)
			var body map[string]any
			require.NoError(t, json.Unmarshal(write.Body, &body))
			assert.Equal(t, observed, body[tc.field],
				"the no-JS cancel is guarded by the same observed id the row rendered")
			assert.NotContains(t, body, tc.absent,
				"no guard field the row never observed is posted")
		})
	}
}

// TestNoJSNonDestructiveFormsCarryTheObservedGuard is the FR's third bullet:
// the no-JavaScript Release, Escalate and Requeue forms carry the observed
// claim id (Release, Escalate) or escalation id (Requeue), not just the cancel
// control. The hidden field is read off each verb's own form, so a form that
// carried the guard only on its htmx half would fail.
func TestNoJSNonDestructiveFormsCarryTheObservedGuard(t *testing.T) {
	t.Run("Claimed tab: Release and Escalate carry the claim id", func(t *testing.T) {
		f := newCancelRowFixture(t)
		row := f.claimedRow(store.LaneImplementation)
		f.tasks.claimed = []store.ClaimedTaskRow{row}

		region := f.region(t, needsAttentionTabClaimed)
		for _, verb := range []string{actionRelease, actionEscalate} {
			form := formContaining(t, region, "/"+verb+`"`)
			assert.Equal(t, "post", formAttr(t, form, "method"))
			assert.Equal(t, row.ClaimID.String(), hiddenInputs(form).Get(expectedClaimIDParam),
				"the %s form carries the claim the row observed", verb)
			assert.False(t, hasHiddenInput(form, escalatedGuardField))
		}
	})

	t.Run("Escalated tab: Requeue carries the escalation id", func(t *testing.T) {
		f := newCancelRowFixture(t)
		row := f.escalatedRow(store.LaneImplementation)
		f.tasks.escalated = []store.EscalatedTaskRow{row}

		region := f.region(t, needsAttentionTabEscalated)
		form := formContaining(t, region, "/"+actionRequeue+`"`)
		assert.Equal(t, "post", formAttr(t, form, "method"))
		assert.Equal(t, row.EscalationID.String(), hiddenInputs(form).Get(escalatedGuardField),
			"the requeue form carries the escalation the row observed")
		assert.False(t, hasHiddenInput(form, expectedClaimIDParam))
	})
}

// ---------------------------------------------------------------------------
// 3. FR bullet 4: a refused cancel is rebuilt from fresh state
// ---------------------------------------------------------------------------

// TestCancelCardRefusalIsRebuiltFromFreshState is the load-bearing assertion of
// this lane. A refusal means the state the acting row observed is not the state
// the store now holds, so the re-rendered confirmation must be built from a
// fresh read of the task -- carrying the FRESH claim id -- and must NOT re-offer
// the STALE id the refused request carried, which is exactly the guard the store
// just rejected.
//
// The test first proves the request really carried the stale id (so the
// assertion discriminates), then requires the response to carry the fresh one
// and not the stale one. Under the pre-Implementation behaviour -- rebuilding
// the card from cancelObservedFrom(r), the request's own ids -- the response
// would carry the stale id and both of the last two assertions would fail.
func TestCancelCardRefusalIsRebuiltFromFreshState(t *testing.T) {
	f := newCancelRowFixture(t)
	stale := uuid.NewString()
	fresh := harnessObservedClaimID
	require.NotEqual(t, stale, fresh.String(), "the stale and fresh ids must differ for this to mean anything")

	// The store refuses the guard the row observed (it changed since the page
	// loaded), the way krill api maps store.ErrObservedStateMismatch.
	f.api.rejectWrite(http.StatusConflict,
		`{"error":`+strconv.Quote(store.ErrObservedStateMismatch.Error())+`}`)

	taskID := uuid.NewString()
	returnTo := needsAttentionTabHref(f.pid, needsAttentionTabClaimed)
	// A card-origin refusal: the confirm card's own form posts, with no
	// HX-Target (the card targets itself and this request came from the card).
	rec := hxFormPost(f.mux, "/ops/tasks/"+taskID+"/cancel", url.Values{
		"reason":             {"force"},
		"return_to":          {returnTo},
		expectedClaimIDParam: {stale},
	}, f.session)

	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

	// The refused request really carried the stale id: without this the
	// NotContains below could pass for the wrong reason.
	write := f.api.writeRequest(t)
	var sent map[string]any
	require.NoError(t, json.Unmarshal(write.Body, &sent))
	require.Equal(t, stale, sent[expectedClaimIDParam],
		"the refused request carried the stale guard, so rebuilding from it would re-offer it")

	got := rec.Body.String()
	require.Contains(t, got, `id="`+pages.CancelConfirmAnchor+`"`, "the card is re-rendered")
	assert.Contains(t, got, "changed since the page was loaded",
		"the refusal is explained inline in krill's own words (FR c69a42b4)")
	assert.NotContains(t, got, store.ErrObservedStateMismatch.Error(),
		"never as the store's own package-qualified text")

	card := cancelCardForm(t, got, taskID)
	claim, escalation := guardValues(card)
	assert.Equal(t, fresh.String(), claim,
		"the re-rendered confirmation carries the claim the task holds NOW (FR bullet 4)")
	assert.Empty(t, escalation, "the fresh task holds no escalation, so none is rendered")
	assert.NotContains(t, got, stale,
		"the stale guard the store just refused must never be re-offered")

	// The pre-Implementation render is distinguishable: rebuilt from the refused
	// request's own ids -- what cancelObservedFrom(r) would have supplied -- the
	// card carries the stale guard. The handler's response above is NOT that
	// render, which is what makes the two assertions discriminating rather than
	// vacuous.
	staleCard := mustRenderComponent(pages.CancelConfirmCard(
		cancelConfirmData(taskID, returnTo, "", cancelObservedIDs{claimID: stale})))
	require.Contains(t, staleCard, stale,
		"rebuilt from the request's ids the card would carry the stale guard")
}

// ---------------------------------------------------------------------------
// 4. the two refusal origins are answered into their own region
// ---------------------------------------------------------------------------

// TestCancelRefusalIsAnsweredIntoTheRegionItCameFrom pins the split the two
// halves of a row's Cancel need: both POST the same route, so HX-Target -- the
// region htmx was swapping -- is the only thing that says which origin a refused
// cancel came from. A row-origin refusal re-renders the results region with the
// inline alert the other three verbs use (it must NOT swap the card into, and so
// destroy, id="ops-results"); a card-origin refusal re-renders the card (it must
// NOT render a results region it never targeted).
func TestCancelRefusalIsAnsweredIntoTheRegionItCameFrom(t *testing.T) {
	storeRefusal := store.ErrObservedStateMismatch.Error()
	// The sentence the operator reads instead of the store's -- krill's own
	// wording for a claim or escalation that changed since the page loaded
	// (FR c69a42b4).
	refusal := "changed since the page was loaded"

	cancelRefusal := func(t *testing.T, target string) string {
		t.Helper()
		f := newCancelRowFixture(t)
		f.api.rejectWrite(http.StatusConflict, `{"error":`+strconv.Quote(storeRefusal)+`}`)

		form := url.Values{
			"reason":             {"force"},
			"return_to":          {needsAttentionTabHref(f.pid, needsAttentionTabClaimed)},
			expectedClaimIDParam: {uuid.NewString()},
		}
		rec := hxPostWithTarget(f.mux, "/ops/tasks/"+uuid.NewString()+"/cancel", form, target, f.session)
		require.Equal(t, http.StatusOK, rec.Code, "a refusal is presented, not status-coded")
		require.Contains(t, rec.Body.String(), refusal, "the refusal reaches the operator")
		require.Contains(t, rec.Body.String(), `role="alert"`, "and it rides inline")
		require.Empty(t, rec.Header().Get("HX-Redirect"), "a refused cancel never navigates away")
		return rec.Body.String()
	}

	t.Run("a row-origin refusal re-renders the results region", func(t *testing.T) {
		got := cancelRefusal(t, pages.OpsResultsAnchor)
		assert.Contains(t, got, `id="`+pages.OpsResultsAnchor+`"`,
			"the region the row's control targeted is re-derived under the refusal")
		assert.NotContains(t, got, `id="`+pages.CancelConfirmAnchor+`"`,
			"the card must not be swapped into, and destroy, the results region")
	})

	t.Run("a card-origin refusal re-renders the card", func(t *testing.T) {
		// No HX-Target is the card's own form as htmx sends it when the card
		// targets itself; the two subtests below pin the same outcome for the
		// explicit anchor.
		got := cancelRefusal(t, "")
		assert.Contains(t, got, `id="`+pages.CancelConfirmAnchor+`"`,
			"the card is re-rendered into its own target")
		assert.NotContains(t, got, `id="`+pages.OpsResultsAnchor+`"`,
			"the card's route must not render a results region it never targeted")
	})

	t.Run("an explicit card anchor is a card-origin refusal too", func(t *testing.T) {
		got := cancelRefusal(t, pages.CancelConfirmAnchor)
		assert.Contains(t, got, `id="`+pages.CancelConfirmAnchor+`"`)
		assert.NotContains(t, got, `id="`+pages.OpsResultsAnchor+`"`)
	})
}

// hxPostWithTarget issues an htmx form POST declaring the region htmx resolved
// as its swap target. An empty target sends no HX-Target header at all, which is
// what the confirm card's own form produces when htmx resolves "#cancel-confirm"
// -- htmx omits the header only when the target IS the element itself, so a
// header-less request is the card-origin shape the existing
// TestCancelConfirmHTMXRefusalReRendersTheCard drives.
func hxPostWithTarget(mux *http.ServeMux, target string, form url.Values, hxTarget string, cookies ...*http.Cookie) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, target, strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("HX-Request", "true")
	if hxTarget != "" {
		req.Header.Set("HX-Target", hxTarget)
	}
	for _, c := range cookies {
		req.AddCookie(c)
	}
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec
}

// ---------------------------------------------------------------------------
// 5. a Done-lane row offers no Cancel
// ---------------------------------------------------------------------------

// TestDoneLaneRowOffersNoCancel pins the legality subtraction at the rows an
// operator actually reads: a task in the Done lane is finished, so it offers
// neither Escalate nor Cancel whatever its state, while Release (a finished task
// can still hold a claim) and Requeue (the recovery the queue exists for)
// survive. An escalated task never offers Release -- it holds no claim.
//
// The predicate is asserted directly beside the rendered rows so the row's verb
// set and the rule are pinned to each other, not to two restatements.
func TestDoneLaneRowOffersNoCancel(t *testing.T) {
	t.Run("the predicate", func(t *testing.T) {
		assert.Contains(t, legalInterventions(taskInterventionClaimed, store.LaneDone), actionRelease)
		assert.NotContains(t, legalInterventions(taskInterventionClaimed, store.LaneDone), actionCancel)
		assert.NotContains(t, legalInterventions(taskInterventionClaimed, store.LaneDone), actionEscalate)
		assert.Equal(t, []string{actionRequeue}, legalInterventions(taskInterventionEscalated, store.LaneDone))
		assert.NotContains(t, legalInterventions(taskInterventionEscalated, store.LaneImplementation), actionRelease,
			"an escalated task holds no claim, so there is nothing to release")
	})

	t.Run("a Done-lane claimed row", func(t *testing.T) {
		f := newCancelRowFixture(t)
		row := f.claimedRow(store.LaneDone)
		f.tasks.claimed = []store.ClaimedTaskRow{row}

		region := f.region(t, needsAttentionTabClaimed)
		assert.Contains(t, region, ">"+actionLabel(actionRelease)+"<", "the claim it still holds is releasable")
		assert.NotContains(t, region, ">"+actionLabel(actionCancel)+"<", "a finished task offers no Cancel")
		assert.NotContains(t, region, cancelConfirmSuffix, "nor the confirm page it would be reached through")
		assert.NotContains(t, region, "hx-confirm=", "nor any browser confirmation")
		assert.NotContains(t, region, "/"+actionEscalate+`"`, "and nothing left to flag for attention")
	})

	t.Run("a Done-lane escalated row", func(t *testing.T) {
		f := newCancelRowFixture(t)
		row := f.escalatedRow(store.LaneDone)
		f.tasks.escalated = []store.EscalatedTaskRow{row}

		region := f.region(t, needsAttentionTabEscalated)
		assert.Contains(t, region, ">"+actionLabel(actionRequeue)+"<", "recovery survives the Done subtraction")
		assert.NotContains(t, region, ">"+actionLabel(actionCancel)+"<", "a finished task offers no Cancel")
		assert.NotContains(t, region, cancelConfirmSuffix)
		assert.NotContains(t, region, "hx-confirm=")
		assert.NotContains(t, region, "/"+actionRelease+`"`, "an escalated task never offers Release")
	})
}
