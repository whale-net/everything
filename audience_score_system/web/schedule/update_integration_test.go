//go:build integration

// update_integration_test.go covers `web/schedule`'s video-script EDIT
// surface (#2037, FR16-FR19, FR18/NFR2's edit-POST half): GET
// /channels/{id}/scripts/{scriptID}?edit=1's in-place edit mode and POST
// /channels/{id}/scripts/{scriptID} (HandleUpdateScript). It shares
// newScheduleTestStack, its fixtures, and doFormRequest with
// schedule_integration_test.go/create_integration_test.go (that file's
// router registers this package's POST /channels/{id}/scripts/{scriptID}
// route alongside the others) -- see this package's schedule.go doc comment
// for why authoring/editing lives in this same package rather than a
// sibling.
package schedule_test

import (
	"context"
	"net/http"
	"net/url"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/whale-net/everything/audience_score_system/store"
)

// updateForm builds the edit form's urlencoded POST body -- submit_action
// "save" is the actual "Save changes" attempt, mirroring
// scriptEditForm's rendered field set exactly (idempotency_key, title,
// script_text).
func updateForm(idempotencyKey, title, scriptText string) url.Values {
	return url.Values{
		"idempotency_key": {idempotencyKey},
		"title":           {title},
		"script_text":     {scriptText},
		"submit_action":   {"save"},
	}
}

// ── FR16: a Founder and an Analyst can both edit a proposed script; the
// change is visible on re-render ─────────────────────────────────────────

func TestHandleUpdateScript_FounderAndAnalyst_EditsProposedScript_VisibleOnRerender(t *testing.T) {
	for _, roleName := range []string{"Founder", "Analyst"} {
		t.Run(roleName, func(t *testing.T) {
			ctx := context.Background()
			s := newScheduleTestStack(t)
			ch, creator, verdict, strategy := s.setupVideoScriptFixture(t, ctx, "Edit-"+roleName)
			script := s.proposeScript(t, ctx, ch, creator, verdict, strategy, "Original Title")

			actor := creator
			if roleName == "Analyst" {
				actor = s.newPerson(t, ctx, "analyst")
				require.NoError(t, s.store.Roles().AddRole(ctx, ch.ID, actor.ID, store.RoleAnalyst, creator.ID))
			}
			cookie := s.sessionCookie(t, ctx, actor.ID)

			form := updateForm(uuid.NewString(), "Edited Title", "Edited body text.")
			w := s.doFormRequest(t, "/channels/"+ch.ID.String()+"/scripts/"+script.ID.String(), form, cookie)
			require.Equal(t, http.StatusSeeOther, w.Code, "body: %s", w.Body.String())

			got, err := s.store.VideoScripts().GetByID(ctx, script.ID)
			require.NoError(t, err)
			assert.Equal(t, "Edited Title", got.Title)
			assert.Equal(t, "Edited body text.", got.ScriptText)
			assert.Equal(t, store.VideoScriptStatusProposed, got.Status, "%s's edit must not itself change status", roleName)

			detailW := s.do(t, http.MethodGet, "/channels/"+ch.ID.String()+"/scripts/"+script.ID.String(), cookie)
			require.Equal(t, http.StatusOK, detailW.Code, "body: %s", detailW.Body.String())
			body := detailW.Body.String()
			assert.Contains(t, body, "Edited Title", "%s's edit must be visible on re-render", roleName)
			assert.Contains(t, body, "Edited body text.", "%s's edit must be visible on re-render", roleName)
		})
	}
}

// ── FR17: greenlit/denied/archived/published-while-proposed all render
// read-only with no edit affordance and the correct distinguishing
// indicator text ───────────────────────────────────────────────────────────

func TestHandleScriptDetail_FrozenStates_ReadOnlyWithDistinctIndicator(t *testing.T) {
	for _, tc := range []struct {
		name     string
		prepare  func(t *testing.T, ctx context.Context, s *scheduleTestStack, ch store.Channel, creator store.Person, scriptID uuid.UUID)
		wantText string
	}{
		{
			name: "Greenlit",
			prepare: func(t *testing.T, ctx context.Context, s *scheduleTestStack, ch store.Channel, creator store.Person, scriptID uuid.UUID) {
				require.NoError(t, s.store.VideoScripts().Greenlight(ctx, scriptID, creator.ID))
			},
			wantText: "approved and is awaiting publish",
		},
		{
			name: "Denied",
			prepare: func(t *testing.T, ctx context.Context, s *scheduleTestStack, ch store.Channel, creator store.Person, scriptID uuid.UUID) {
				require.NoError(t, s.store.VideoScripts().Deny(ctx, scriptID, creator.ID))
			},
			wantText: "was denied",
		},
		{
			name: "Archived",
			prepare: func(t *testing.T, ctx context.Context, s *scheduleTestStack, ch store.Channel, creator store.Person, scriptID uuid.UUID) {
				require.NoError(t, s.store.VideoScripts().Greenlight(ctx, scriptID, creator.ID))
				require.NoError(t, s.store.VideoScripts().Archive(ctx, scriptID, creator.ID))
			},
			wantText: "has been archived",
		},
		{
			// Still status='proposed' -- frozen solely via a live confirmed
			// match to a published video, mirroring the store-level
			// load-bearing "status='proposed' alone would let this through"
			// case (see store/video_script_integration_test.go).
			name: "PublishedWhileProposed",
			prepare: func(t *testing.T, ctx context.Context, s *scheduleTestStack, ch store.Channel, creator store.Person, scriptID uuid.UUID) {
				s.recordPublishedMatch(t, ctx, ch, scriptID, store.MatchStateConfirmed, true)
			},
			wantText: "video has already been published",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			s := newScheduleTestStack(t)
			ch, creator, verdict, strategy := s.setupVideoScriptFixture(t, ctx, "Frozen-"+tc.name)
			script := s.proposeScript(t, ctx, ch, creator, verdict, strategy, "Frozen Script "+tc.name)
			tc.prepare(t, ctx, s, ch, creator, script.ID)
			cookie := s.sessionCookie(t, ctx, creator.ID)

			w := s.do(t, http.MethodGet, "/channels/"+ch.ID.String()+"/scripts/"+script.ID.String(), cookie)
			require.Equal(t, http.StatusOK, w.Code, "body: %s", w.Body.String())
			body := w.Body.String()
			assert.Contains(t, body, tc.wantText, "%s: must render the state-specific freeze indicator text, not merely \"read-only\"", tc.name)
			assert.NotContains(t, body, "?edit=1", "%s: no edit affordance must be rendered", tc.name)
			assert.NotContains(t, body, "Save changes", "%s: no edit form must be rendered", tc.name)

			// A manually-appended ?edit=1 (bypassing the omitted affordance)
			// must still fall back to the read-only render, never expose the
			// edit form -- renderScriptDetail forces EditMode false whenever
			// !Editable, regardless of the caller's requested edit mode.
			editW := s.do(t, http.MethodGet, "/channels/"+ch.ID.String()+"/scripts/"+script.ID.String()+"?edit=1", cookie)
			require.Equal(t, http.StatusOK, editW.Code, "body: %s", editW.Body.String())
			assert.NotContains(t, editW.Body.String(), "Save changes", "%s: a manually-appended ?edit=1 must not expose the edit form on a frozen script", tc.name)
			assert.Contains(t, editW.Body.String(), tc.wantText, "%s: ?edit=1 must still render the freeze indicator", tc.name)
		})
	}
}

// ── FR17 enforcement: a POST forced at a greenlit script (bypassing the UI)
// returns a form error from ErrVideoScriptDecided and writes nothing ───────

func TestHandleUpdateScript_ForcedAtGreenlit_FormErrorNoWrite(t *testing.T) {
	ctx := context.Background()
	s := newScheduleTestStack(t)
	ch, creator, verdict, strategy := s.setupVideoScriptFixture(t, ctx, "ForcedGreenlit")
	script := s.proposeScript(t, ctx, ch, creator, verdict, strategy, "Forced Script")
	require.NoError(t, s.store.VideoScripts().Greenlight(ctx, script.ID, creator.ID))
	cookie := s.sessionCookie(t, ctx, creator.ID)

	form := updateForm(uuid.NewString(), "Should Not Apply", "should not apply")
	w := s.doFormRequest(t, "/channels/"+ch.ID.String()+"/scripts/"+script.ID.String(), form, cookie)
	assert.Equal(t, http.StatusConflict, w.Code, "a POST at a greenlit script bypassing the UI must 409 from ErrVideoScriptDecided, body: %s", w.Body.String())
	assert.Contains(t, w.Body.String(), "no longer be edited", "the response must surface ErrVideoScriptDecided as a visible form error")

	got, err := s.store.VideoScripts().GetByID(ctx, script.ID)
	require.NoError(t, err)
	assert.Equal(t, "Forced Script", got.Title, "the forced POST must write nothing")
	assert.Equal(t, store.VideoScriptStatusGreenlit, got.Status)
}

// ── FR19: non-member 403 with no write; cross-Channel scriptID 404;
// malformed UUID 400; signed-out redirect ───────────────────────────────────

func TestHandleUpdateScript_NonMember_ForbiddenNoWrite(t *testing.T) {
	ctx := context.Background()
	s := newScheduleTestStack(t)
	ch, creator, verdict, strategy := s.setupVideoScriptFixture(t, ctx, "NonMemberEdit")
	script := s.proposeScript(t, ctx, ch, creator, verdict, strategy, "Original Title")
	outsider := s.newPerson(t, ctx, "outsider")
	cookie := s.sessionCookie(t, ctx, outsider.ID)

	form := updateForm(uuid.NewString(), "Should Not Apply", "should not apply")
	w := s.doFormRequest(t, "/channels/"+ch.ID.String()+"/scripts/"+script.ID.String(), form, cookie)
	assert.Equal(t, http.StatusForbidden, w.Code, "body: %s", w.Body.String())

	got, err := s.store.VideoScripts().GetByID(ctx, script.ID)
	require.NoError(t, err)
	assert.Equal(t, "Original Title", got.Title, "a non-member's forged POST must not write")
}

func TestHandleUpdateScript_CrossChannelScriptID_NotFound(t *testing.T) {
	ctx := context.Background()
	s := newScheduleTestStack(t)
	ch1, creator1, verdict1, strategy1 := s.setupVideoScriptFixture(t, ctx, "CrossChannelMain")
	script1 := s.proposeScript(t, ctx, ch1, creator1, verdict1, strategy1, "Script One")

	ch2, creator2, _, _ := s.setupVideoScriptFixture(t, ctx, "CrossChannelOther")
	cookie := s.sessionCookie(t, ctx, creator2.ID)

	// script1 belongs to ch1, not ch2 -- the path's {id} (ch2) must 404,
	// mirroring HandleScriptDetail's identical cross-Channel rule.
	form := updateForm(uuid.NewString(), "Should Not Apply", "should not apply")
	w := s.doFormRequest(t, "/channels/"+ch2.ID.String()+"/scripts/"+script1.ID.String(), form, cookie)
	assert.Equal(t, http.StatusNotFound, w.Code, "body: %s", w.Body.String())

	got, err := s.store.VideoScripts().GetByID(ctx, script1.ID)
	require.NoError(t, err)
	assert.Equal(t, "Script One", got.Title, "a cross-Channel-scoped POST must not write")
}

func TestHandleUpdateScript_MalformedScriptUUID_BadRequest(t *testing.T) {
	ctx := context.Background()
	s := newScheduleTestStack(t)
	ch, creator := s.setupChannel(t, ctx)
	cookie := s.sessionCookie(t, ctx, creator.ID)

	form := updateForm(uuid.NewString(), "x", "y")
	w := s.doFormRequest(t, "/channels/"+ch.ID.String()+"/scripts/not-a-uuid", form, cookie)
	assert.Equal(t, http.StatusBadRequest, w.Code)
}

func TestHandleUpdateScript_SignedOut_RedirectsToLogin(t *testing.T) {
	ctx := context.Background()
	s := newScheduleTestStack(t)
	ch, creator, verdict, strategy := s.setupVideoScriptFixture(t, ctx, "SignedOutEdit")
	script := s.proposeScript(t, ctx, ch, creator, verdict, strategy, "Script")

	form := updateForm(uuid.NewString(), "x", "y")
	w := s.doFormRequest(t, "/channels/"+ch.ID.String()+"/scripts/"+script.ID.String(), form, nil)
	assert.Equal(t, http.StatusFound, w.Code)
	assert.Equal(t, "/login", w.Header().Get("Location"))
}

// ── FR19 orthogonality: the Creator who greenlit the script also cannot
// edit it -- CanWrite holder, frozen by status ──────────────────────────────

func TestHandleUpdateScript_CreatorWhoGreenlit_CannotEdit(t *testing.T) {
	ctx := context.Background()
	s := newScheduleTestStack(t)
	ch, creator, verdict, strategy := s.setupVideoScriptFixture(t, ctx, "CreatorGreenlit")
	script := s.proposeScript(t, ctx, ch, creator, verdict, strategy, "Creator Greenlit Script")
	require.NoError(t, s.store.VideoScripts().Greenlight(ctx, script.ID, creator.ID))
	cookie := s.sessionCookie(t, ctx, creator.ID)

	form := updateForm(uuid.NewString(), "Should Not Apply", "should not apply")
	w := s.doFormRequest(t, "/channels/"+ch.ID.String()+"/scripts/"+script.ID.String(), form, cookie)
	assert.Equal(t, http.StatusConflict, w.Code, "the Creator who greenlit their own script still holds CanWrite -- must be frozen by the status gate, not authorization, body: %s", w.Body.String())

	got, err := s.store.VideoScripts().GetByID(ctx, script.ID)
	require.NoError(t, err)
	assert.Equal(t, "Creator Greenlit Script", got.Title, "the Creator's own forced edit on their decided script must write nothing")
}

// ── FR18/NFR2: replaying the same edit POST body (same idempotency key)
// does not double-apply / re-run ────────────────────────────────────────────

func TestHandleUpdateScript_IdempotentReplay_NoDoubleApply(t *testing.T) {
	ctx := context.Background()
	s := newScheduleTestStack(t)
	ch, creator, verdict, strategy := s.setupVideoScriptFixture(t, ctx, "IdempotentEdit")
	script := s.proposeScript(t, ctx, ch, creator, verdict, strategy, "Original Title")
	cookie := s.sessionCookie(t, ctx, creator.ID)

	key := uuid.NewString()
	form := updateForm(key, "Edited Title", "Edited body.")

	w1 := s.doFormRequest(t, "/channels/"+ch.ID.String()+"/scripts/"+script.ID.String(), form, cookie)
	require.Equal(t, http.StatusSeeOther, w1.Code, "body: %s", w1.Body.String())

	got, err := s.store.VideoScripts().GetByID(ctx, script.ID)
	require.NoError(t, err)
	assert.Equal(t, "Edited Title", got.Title)

	// Replay: a back-button/refresh double-submit carrying the SAME
	// idempotency_key.
	w2 := s.doFormRequest(t, "/channels/"+ch.ID.String()+"/scripts/"+script.ID.String(), form, cookie)
	require.Equal(t, http.StatusSeeOther, w2.Code, "the replay must not error, body: %s", w2.Body.String())

	got, err = s.store.VideoScripts().GetByID(ctx, script.ID)
	require.NoError(t, err)
	assert.Equal(t, "Edited Title", got.Title, "a replayed edit POST must not double-apply or re-run")
	assert.Equal(t, "Edited body.", got.ScriptText)
}
