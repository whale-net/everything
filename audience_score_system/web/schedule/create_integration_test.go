//go:build integration

// create_integration_test.go covers `web/schedule`'s video-script AUTHORING
// surface (#2036, FR13-FR15, FR21, plus the create half of FR18/FR19): GET
// /channels/{id}/scripts/new, POST /channels/{id}/scripts, and GET
// /channels/{id}/scripts/{scriptID}. It shares newScheduleTestStack and its
// fixtures with schedule_integration_test.go (that file's router already
// registers all three routes) -- see this package's schedule.go doc comment
// for why authoring lives in this same package rather than a sibling.
package schedule_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/whale-net/everything/audience_score_system/store"
)

// doFormRequest posts urlencoded form values, mirroring schedule_integration_
// test.go's do helper but for a POST with a body and the correct content
// type -- that file's do only covers nil-bodied requests (GETs and the
// three no-body mutating POSTs), so authoring's form-bearing create POST
// needs its own.
func (s *scheduleTestStack) doFormRequest(t *testing.T, target string, form url.Values, cookie *http.Cookie) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, target, strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	if cookie != nil {
		req.AddCookie(cookie)
	}
	w := httptest.NewRecorder()
	s.router.ServeHTTP(w, req)
	return w
}

// ── FR13: a CanWrite persona (Founder or Analyst) creates a script ─────────

func TestHandleCreateScript_FounderAndAnalyst_CreatesProposedScript(t *testing.T) {
	for _, roleName := range []string{"Founder", "Analyst"} {
		t.Run(roleName, func(t *testing.T) {
			ctx := context.Background()
			s := newScheduleTestStack(t)
			ch, creator, verdict, strategy := s.setupVideoScriptFixture(t, ctx, "Create-"+roleName)

			actor := creator
			if roleName == "Analyst" {
				actor = s.newPerson(t, ctx, "analyst")
				require.NoError(t, s.store.Roles().AddRole(ctx, ch.ID, actor.ID, store.RoleAnalyst, creator.ID))
			}
			cookie := s.sessionCookie(t, ctx, actor.ID)

			form := url.Values{
				"verdict_id":      {verdict.ID.String()},
				"strategy_id":     {strategy.ID.String()},
				"title":           {"My New Script"},
				"script_text":     {"# Hello\n\nBody text."},
				"submit_action":   {"create"},
				"idempotency_key": {uuid.NewString()},
			}
			w := s.doFormRequest(t, "/channels/"+ch.ID.String()+"/scripts", form, cookie)
			require.Equal(t, http.StatusSeeOther, w.Code, "body: %s", w.Body.String())

			loc := w.Header().Get("Location")
			require.Contains(t, loc, "/channels/"+ch.ID.String()+"/scripts/")

			scriptID, err := uuid.Parse(loc[len("/channels/"+ch.ID.String()+"/scripts/"):])
			require.NoError(t, err)

			got, err := s.store.VideoScripts().GetByID(ctx, scriptID)
			require.NoError(t, err)
			assert.Equal(t, verdict.ID, got.VerdictID)
			assert.Equal(t, strategy.ID, got.StrategyID)
			assert.Equal(t, store.VideoScriptStatusProposed, got.Status)
			assert.Equal(t, "My New Script", got.Title)
		})
	}
}

// ── FR13: only viable verdicts are offered; a POST naming a not-viable or
// needs-more-research verdict is rejected server-side with no write ────────

func TestHandleCreateScript_NonViableVerdict_RejectedNoWrite(t *testing.T) {
	for _, tc := range []struct {
		name    string
		verdict store.VerdictValue
	}{
		{"NotViable", store.VerdictNotViable},
		{"NeedsMoreResearch", store.VerdictNeedsMoreResearch},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			s := newScheduleTestStack(t)
			ch, creator := s.setupChannel(t, ctx)

			idea, err := s.store.Ideas().Create(ctx, ch.ID, "Idea "+tc.name, creator.ID)
			require.NoError(t, err)
			badVerdict, err := s.store.Verdicts().Append(ctx, store.AppendVerdictInput{
				IdeaID: idea.ID, Verdict: tc.verdict, Reasoning: "reasoning", AuthorPersonID: creator.ID,
			})
			require.NoError(t, err)

			// A separate viable idea/verdict/strategy on the SAME Channel so
			// it has an active Strategy (otherwise the zero-Strategy block,
			// not this verdict rejection, would fire first).
			siblingIdea, err := s.store.Ideas().Create(ctx, ch.ID, "Sibling Idea "+tc.name, creator.ID)
			require.NoError(t, err)
			viableVerdict, err := s.store.Verdicts().Append(ctx, store.AppendVerdictInput{
				IdeaID: siblingIdea.ID, Verdict: store.VerdictViable, Reasoning: "viable", AuthorPersonID: creator.ID,
			})
			require.NoError(t, err)
			strategy, err := s.store.Strategies().Save(ctx, store.SaveStrategyInput{
				ChannelID: ch.ID, Title: "Sibling Strategy " + tc.name, Active: true,
				VerdictIDs: []uuid.UUID{viableVerdict.ID}, CreatedByPersonID: creator.ID,
			})
			require.NoError(t, err)

			cookie := s.sessionCookie(t, ctx, creator.ID)

			// GET the create form: the non-viable verdict must not appear as
			// an option.
			getW := s.do(t, http.MethodGet, "/channels/"+ch.ID.String()+"/scripts/new", cookie)
			require.Equal(t, http.StatusOK, getW.Code, "body: %s", getW.Body.String())
			assert.NotContains(t, getW.Body.String(), badVerdict.ID.String(), "a %s verdict must not appear in the picker", tc.verdict)

			form := url.Values{
				"verdict_id":      {badVerdict.ID.String()},
				"strategy_id":     {strategy.ID.String()},
				"title":           {"Should Not Exist"},
				"script_text":     {"body"},
				"submit_action":   {"create"},
				"idempotency_key": {uuid.NewString()},
			}
			w := s.doFormRequest(t, "/channels/"+ch.ID.String()+"/scripts", form, cookie)
			assert.Equal(t, http.StatusBadRequest, w.Code, "a %s verdict must be rejected server-side", tc.verdict)

			count, err := s.countVideoScripts(ctx, ch.ID)
			require.NoError(t, err)
			assert.Equal(t, 0, count, "no video_script row must be written")
		})
	}
}

// ── FR13: only active Strategies are offered; a POST naming an inactive or
// cross-Channel Strategy is rejected with no write ──────────────────────────

func TestHandleCreateScript_InactiveOrCrossChannelStrategy_RejectedNoWrite(t *testing.T) {
	ctx := context.Background()
	s := newScheduleTestStack(t)
	ch, creator, verdict, activeStrategy := s.setupVideoScriptFixture(t, ctx, "StratMain")
	_ = activeStrategy
	cookie := s.sessionCookie(t, ctx, creator.ID)

	// An inactive Strategy on the SAME Channel, grounded on the same
	// viable verdict.
	inactiveStrategy, err := s.store.Strategies().Save(ctx, store.SaveStrategyInput{
		ChannelID: ch.ID, Title: "Inactive Strategy", Active: false,
		VerdictIDs: []uuid.UUID{verdict.ID}, CreatedByPersonID: creator.ID,
	})
	require.NoError(t, err)

	// An active Strategy on a DIFFERENT Channel.
	otherCh, otherCreator, otherVerdict, _ := s.setupVideoScriptFixture(t, ctx, "OtherChannel")
	crossChannelStrategy, err := s.store.Strategies().Save(ctx, store.SaveStrategyInput{
		ChannelID: otherCh.ID, Title: "Cross-Channel Strategy", Active: true,
		VerdictIDs: []uuid.UUID{otherVerdict.ID}, CreatedByPersonID: otherCreator.ID,
	})
	require.NoError(t, err)

	for _, tc := range []struct {
		name       string
		strategyID uuid.UUID
	}{
		{"Inactive", inactiveStrategy.ID},
		{"CrossChannel", crossChannelStrategy.ID},
	} {
		t.Run(tc.name, func(t *testing.T) {
			form := url.Values{
				"verdict_id":      {verdict.ID.String()},
				"strategy_id":     {tc.strategyID.String()},
				"title":           {"Should Not Exist"},
				"script_text":     {"body"},
				"submit_action":   {"create"},
				"idempotency_key": {uuid.NewString()},
			}
			w := s.doFormRequest(t, "/channels/"+ch.ID.String()+"/scripts", form, cookie)
			assert.Equal(t, http.StatusBadRequest, w.Code, "an %s strategy must be rejected server-side, body: %s", tc.name, w.Body.String())
		})
	}

	count, err := s.countVideoScripts(ctx, ch.ID)
	require.NoError(t, err)
	assert.Equal(t, 0, count, "no video_script row must be written")
}

// ── FR13 (load-bearing): a Channel with zero active Strategies renders the
// blocked form with the exact save_strategy message, 200 not 500, and a
// POST attempted anyway writes nothing. The message must not say/imply
// "Creator" -- any CanWrite persona (Founder OR Analyst) can resolve it
// themselves via save_strategy. ───────────────────────────────────────────

func TestHandleNewScript_ZeroActiveStrategy_BlockedFormMessage(t *testing.T) {
	ctx := context.Background()
	s := newScheduleTestStack(t)
	ch, creator := s.setupChannel(t, ctx)

	idea, err := s.store.Ideas().Create(ctx, ch.ID, "Idea", creator.ID)
	require.NoError(t, err)
	verdict, err := s.store.Verdicts().Append(ctx, store.AppendVerdictInput{
		IdeaID: idea.ID, Verdict: store.VerdictViable, Reasoning: "viable", AuthorPersonID: creator.ID,
	})
	require.NoError(t, err)
	// No Strategy created at all -- zero active Strategies on ch.

	cookie := s.sessionCookie(t, ctx, creator.ID)

	getW := s.do(t, http.MethodGet, "/channels/"+ch.ID.String()+"/scripts/new", cookie)
	require.Equal(t, http.StatusOK, getW.Code, "the blocked form must render 200, not 500, body: %s", getW.Body.String())
	body := getW.Body.String()
	assert.Contains(t, body, "save_strategy", "the blocked message must name the MCP tool path")
	// Isolate the rendered message's own paragraph rather than scanning the
	// whole page: the signed-in person in this fixture is itself literally
	// named "Creator" (setupChannel's display name), so the page's navbar
	// account menu legitimately renders that word -- scanning the full body
	// for "Creator" would false-positive on that, not on the message text
	// this assertion actually cares about.
	msgStart := strings.Index(body, "This Channel has no active Strategy")
	require.NotEqual(t, -1, msgStart, "the zero-active-Strategy message must render, body: %s", body)
	msgEnd := strings.Index(body[msgStart:], "</p>")
	require.NotEqual(t, -1, msgEnd, "the message must be inside a <p> element, body: %s", body)
	message := body[msgStart : msgStart+msgEnd]
	assert.NotContains(t, message, "Creator", "the blocked message must not say or imply \"ask your Creator\" -- any CanWrite persona can resolve it")

	form := url.Values{
		"verdict_id":      {verdict.ID.String()},
		"strategy_id":     {uuid.NewString()},
		"title":           {"Should Not Exist"},
		"script_text":     {"body"},
		"submit_action":   {"create"},
		"idempotency_key": {uuid.NewString()},
	}
	postW := s.doFormRequest(t, "/channels/"+ch.ID.String()+"/scripts", form, cookie)
	assert.Equal(t, http.StatusBadRequest, postW.Code, "a POST attempted anyway must be rejected, body: %s", postW.Body.String())

	count, err := s.countVideoScripts(ctx, ch.ID)
	require.NoError(t, err)
	assert.Equal(t, 0, count)
}

// ── FR14: raw markdown in edit mode, rendered HTML in preview mode;
// toggling preserves unsaved body text; a body containing a literal
// <script> tag is escaped/neutralized in the preview output ───────────────

func TestHandleCreateScript_PreviewEditToggle_PreservesTextAndEscapesScript(t *testing.T) {
	ctx := context.Background()
	s := newScheduleTestStack(t)
	ch, creator, verdict, strategy := s.setupVideoScriptFixture(t, ctx, "Preview")
	cookie := s.sessionCookie(t, ctx, creator.ID)

	maliciousBody := "# Heading\n\n<script>alert(1)</script>\n\nSome text."

	previewForm := url.Values{
		"verdict_id":      {verdict.ID.String()},
		"strategy_id":     {strategy.ID.String()},
		"title":           {"Preview Test"},
		"script_text":     {maliciousBody},
		"submit_action":   {"preview"},
		"idempotency_key": {uuid.NewString()},
	}
	previewW := s.doFormRequest(t, "/channels/"+ch.ID.String()+"/scripts", previewForm, cookie)
	require.Equal(t, http.StatusOK, previewW.Code, "toggling to preview must re-render 200, body: %s", previewW.Body.String())
	previewBody := previewW.Body.String()
	assert.NotContains(t, previewBody, "<script>alert(1)</script>", "a raw <script> tag must never execute/render verbatim in the preview")
	// The unsaved body text must be preserved (as the hidden field's value)
	// so a later "Edit" or "Create script" submit is not lost.
	assert.Contains(t, previewBody, "alert(1)", "the toggle must preserve the submitted body text (escaped/neutralized, not dropped)")

	// No store call is made just from previewing/editing.
	count, err := s.countVideoScripts(ctx, ch.ID)
	require.NoError(t, err)
	assert.Equal(t, 0, count, "toggling preview/edit must never write to the store")

	editForm := url.Values{
		"verdict_id":      {verdict.ID.String()},
		"strategy_id":     {strategy.ID.String()},
		"title":           {"Preview Test"},
		"script_text":     {maliciousBody},
		"submit_action":   {"edit"},
		"idempotency_key": {uuid.NewString()},
	}
	editW := s.doFormRequest(t, "/channels/"+ch.ID.String()+"/scripts", editForm, cookie)
	require.Equal(t, http.StatusOK, editW.Code, "body: %s", editW.Body.String())
	// The editable <textarea> renders the raw markdown source via templ's
	// standard (and load-bearing) HTML-escaping -- a literal "<script>" in
	// an interpolated text node must come out as "&lt;script&gt;" so the
	// browser's HTML parser reproduces the exact original characters inside
	// the textarea rather than treating them as markup; unescaped output
	// here would itself be an XSS bug, not fidelity. Content equivalence
	// (not byte-for-byte match to the submitted string) is what "text is
	// preserved" means for an HTML response.
	assert.Contains(t, editW.Body.String(), "&lt;script&gt;alert(1)&lt;/script&gt;", "toggling back to edit mode must preserve the unsaved body text (HTML-escaped, since it renders inside a textarea's text node)")
}

// ── FR15: the page renders a link to the bound verdict, resolving to the
// right Idea/Channel ────────────────────────────────────────────────────────

func TestHandleScriptDetail_LinksToVerdict(t *testing.T) {
	ctx := context.Background()
	s := newScheduleTestStack(t)
	ch, creator, verdict, strategy := s.setupVideoScriptFixture(t, ctx, "DetailLink")
	script := s.proposeScript(t, ctx, ch, creator, verdict, strategy, "Detail Script")
	cookie := s.sessionCookie(t, ctx, creator.ID)

	idea, err := s.store.Ideas().GetByID(ctx, script.IdeaID)
	require.NoError(t, err)

	w := s.do(t, http.MethodGet, "/channels/"+ch.ID.String()+"/scripts/"+script.ID.String(), cookie)
	require.Equal(t, http.StatusOK, w.Code, "body: %s", w.Body.String())
	body := w.Body.String()
	assert.Contains(t, body, "/channels/"+ch.ID.String()+"/research/ideas/"+idea.ID.String(), "must link back to the bound verdict's Idea page")
	assert.Contains(t, body, idea.Title)
	assert.Contains(t, body, "v1", "must show the bound verdict's version")
	assert.Contains(t, body, string(verdict.Verdict), "must show the bound verdict's value")
}

// ── FR19 authz: non-member 403 with no write; unknown Channel 404;
// malformed UUID 400; signed-out redirect ───────────────────────────────────

func TestHandleCreateScript_NonMember_ForbiddenNoWrite(t *testing.T) {
	ctx := context.Background()
	s := newScheduleTestStack(t)
	ch, _, verdict, strategy := s.setupVideoScriptFixture(t, ctx, "NonMemberCreate")
	outsider := s.newPerson(t, ctx, "outsider")
	cookie := s.sessionCookie(t, ctx, outsider.ID)

	form := url.Values{
		"verdict_id":      {verdict.ID.String()},
		"strategy_id":     {strategy.ID.String()},
		"title":           {"Should Not Exist"},
		"script_text":     {"body"},
		"submit_action":   {"create"},
		"idempotency_key": {uuid.NewString()},
	}
	w := s.doFormRequest(t, "/channels/"+ch.ID.String()+"/scripts", form, cookie)
	assert.Equal(t, http.StatusForbidden, w.Code, "body: %s", w.Body.String())

	count, err := s.countVideoScripts(ctx, ch.ID)
	require.NoError(t, err)
	assert.Equal(t, 0, count)
}

func TestHandleNewScript_UnknownChannel_NotFound(t *testing.T) {
	ctx := context.Background()
	s := newScheduleTestStack(t)
	_, creator := s.setupChannel(t, ctx)
	cookie := s.sessionCookie(t, ctx, creator.ID)

	w := s.do(t, http.MethodGet, "/channels/"+uuid.NewString()+"/scripts/new", cookie)
	assert.Equal(t, http.StatusNotFound, w.Code, "body: %s", w.Body.String())
}

func TestHandleScriptDetail_UnknownChannel_NotFound(t *testing.T) {
	ctx := context.Background()
	s := newScheduleTestStack(t)
	_, creator := s.setupChannel(t, ctx)
	cookie := s.sessionCookie(t, ctx, creator.ID)

	w := s.do(t, http.MethodGet, "/channels/"+uuid.NewString()+"/scripts/"+uuid.NewString(), cookie)
	assert.Equal(t, http.StatusNotFound, w.Code, "body: %s", w.Body.String())
}

func TestHandleNewScript_MalformedChannelUUID_BadRequest(t *testing.T) {
	ctx := context.Background()
	s := newScheduleTestStack(t)
	_, creator := s.setupChannel(t, ctx)
	cookie := s.sessionCookie(t, ctx, creator.ID)

	w := s.do(t, http.MethodGet, "/channels/not-a-uuid/scripts/new", cookie)
	assert.Equal(t, http.StatusBadRequest, w.Code)
}

func TestHandleScriptDetail_MalformedScriptUUID_BadRequest(t *testing.T) {
	ctx := context.Background()
	s := newScheduleTestStack(t)
	ch, creator := s.setupChannel(t, ctx)
	cookie := s.sessionCookie(t, ctx, creator.ID)

	w := s.do(t, http.MethodGet, "/channels/"+ch.ID.String()+"/scripts/not-a-uuid", cookie)
	assert.Equal(t, http.StatusBadRequest, w.Code)
}

func TestAuthoringRoutes_SignedOut_RedirectsToLogin(t *testing.T) {
	ctx := context.Background()
	s := newScheduleTestStack(t)
	ch, creator, verdict, strategy := s.setupVideoScriptFixture(t, ctx, "SignedOut")
	script := s.proposeScript(t, ctx, ch, creator, verdict, strategy, "Script")

	for _, tc := range []struct {
		name   string
		method string
		target string
	}{
		{"New", http.MethodGet, "/channels/" + ch.ID.String() + "/scripts/new"},
		{"Create", http.MethodPost, "/channels/" + ch.ID.String() + "/scripts"},
		{"Detail", http.MethodGet, "/channels/" + ch.ID.String() + "/scripts/" + script.ID.String()},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := s.do(t, tc.method, tc.target, nil)
			assert.Equal(t, http.StatusFound, w.Code)
			assert.Equal(t, "/login", w.Header().Get("Location"))
		})
	}
}

// ── FR18/NFR2: replaying the same POST body (same idempotency key) returns
// the existing script and does not create a second row ─────────────────────

func TestHandleCreateScript_IdempotentReplay_NoDuplicate(t *testing.T) {
	ctx := context.Background()
	s := newScheduleTestStack(t)
	ch, creator, verdict, strategy := s.setupVideoScriptFixture(t, ctx, "Idempotent")
	cookie := s.sessionCookie(t, ctx, creator.ID)

	key := uuid.NewString()
	form := url.Values{
		"verdict_id":      {verdict.ID.String()},
		"strategy_id":     {strategy.ID.String()},
		"title":           {"Idempotent Script"},
		"script_text":     {"body text"},
		"submit_action":   {"create"},
		"idempotency_key": {key},
	}

	w1 := s.doFormRequest(t, "/channels/"+ch.ID.String()+"/scripts", form, cookie)
	require.Equal(t, http.StatusSeeOther, w1.Code, "body: %s", w1.Body.String())
	loc1 := w1.Header().Get("Location")

	// Replay: a back-button/refresh double-submit carrying the SAME
	// idempotency_key.
	w2 := s.doFormRequest(t, "/channels/"+ch.ID.String()+"/scripts", form, cookie)
	require.Equal(t, http.StatusSeeOther, w2.Code, "the replay must not error, body: %s", w2.Body.String())
	loc2 := w2.Header().Get("Location")

	assert.Equal(t, loc1, loc2, "the replay must resolve to the SAME script, not mint a second one")

	count, err := s.countVideoScripts(ctx, ch.ID)
	require.NoError(t, err)
	assert.Equal(t, 1, count, "a replayed idempotency key must never create a second row")
}

// ── FR21: the routes respond under /channels/{id}/scripts/..., and there is
// no top-level authoring route ──────────────────────────────────────────────

func TestAuthoringRoutes_MountedUnderChannelPrefix_NoTopLevelRoute(t *testing.T) {
	ctx := context.Background()
	s := newScheduleTestStack(t)
	ch, creator, verdict, strategy := s.setupVideoScriptFixture(t, ctx, "Mount")
	cookie := s.sessionCookie(t, ctx, creator.ID)

	w := s.do(t, http.MethodGet, "/channels/"+ch.ID.String()+"/scripts/new", cookie)
	assert.Equal(t, http.StatusOK, w.Code, "the Channel-scoped create form must respond, body: %s", w.Body.String())

	// No top-level (Channel-less) authoring route is registered.
	topLevelNew := s.do(t, http.MethodGet, "/scripts/new", cookie)
	assert.Equal(t, http.StatusNotFound, topLevelNew.Code, "there must be no top-level GET /scripts/new route")

	form := url.Values{
		"verdict_id":      {verdict.ID.String()},
		"strategy_id":     {strategy.ID.String()},
		"title":           {"x"},
		"script_text":     {"y"},
		"submit_action":   {"create"},
		"idempotency_key": {uuid.NewString()},
	}
	topLevelCreate := s.doFormRequest(t, "/scripts", form, cookie)
	assert.Equal(t, http.StatusNotFound, topLevelCreate.Code, "there must be no top-level POST /scripts route")
}

// ── Cross-Channel guard: a verdict on another Channel cannot be used ───────

func TestHandleCreateScript_CrossChannelVerdict_RejectedNoWrite(t *testing.T) {
	ctx := context.Background()
	s := newScheduleTestStack(t)
	ch, creator, _, strategy := s.setupVideoScriptFixture(t, ctx, "CrossVerdictMain")
	_, _, otherVerdict, _ := s.setupVideoScriptFixture(t, ctx, "CrossVerdictOther")
	cookie := s.sessionCookie(t, ctx, creator.ID)

	form := url.Values{
		"verdict_id":      {otherVerdict.ID.String()},
		"strategy_id":     {strategy.ID.String()},
		"title":           {"Should Not Exist"},
		"script_text":     {"body"},
		"submit_action":   {"create"},
		"idempotency_key": {uuid.NewString()},
	}
	w := s.doFormRequest(t, "/channels/"+ch.ID.String()+"/scripts", form, cookie)
	assert.Equal(t, http.StatusBadRequest, w.Code, "a verdict belonging to another Channel must be rejected, body: %s", w.Body.String())

	count, err := s.countVideoScripts(ctx, ch.ID)
	require.NoError(t, err)
	assert.Equal(t, 0, count)
}

// ── shared helpers ───────────────────────────────────────────────────────

// countVideoScripts counts video_script rows for channelID directly, since
// store.VideoScriptStore exposes no Count method -- mirrors this package's
// existing direct-SQL fixture helpers (e.g. recordPublishedMatch).
func (s *scheduleTestStack) countVideoScripts(ctx context.Context, channelID uuid.UUID) (int, error) {
	var count int
	err := s.db.Pool.QueryRow(ctx, `SELECT count(*) FROM video_script WHERE channel_id = $1`, channelID).Scan(&count)
	return count, err
}
