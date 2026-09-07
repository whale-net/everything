//go:build integration

// Package citest_test (M4.2 milestone half): the milestone acceptance test
// for M4.2 (root plan #1914, this task issue #1917) -- one Channel driven
// through *propose a video script on `web` (#1915) -> see it on
// `web/schedule`'s list (already shipped, #1834) -> read the identical row
// back over real MCP -> greenlight it on `web`*, end to end, proving M4.2's
// own outcome sentence ("a Creator or Analyst can propose, review/approve/
// deny, and browse video scripts entirely from the web UI"), not any one
// task's own slice -- #1915 already has its own
// web/research/research_integration_test.go coverage for its propose-form
// slice in isolation. Modelled directly on m4_1_research_web_test.go's own
// doc-comment/harness conventions: this file reuses the package's shared
// `world` (e2e_test.go), its mcpConnect/callTool/decode helpers, and
// e2e_test.go's own real-web-router mounting (web/research and web/schedule
// are both already mounted there, per M4.1's #1903 and #1911 precedent --
// verified below, not re-wired).
//
// The load-bearing claim only this file can make is M4.2's own outcome
// sentence: a video_script proposed on `web` and one proposed via
// `save_video_script` are indistinguishable in storage and browsing -- same
// store, same rules, no second-class record. Phase 5 asserts this in the
// web-write/MCP-read direction, then the mirror, MCP-write/web-read.
//
// # FR/NFR coverage inventory (root plan #1914)
//
//   - FR1 (Idea detail page renders the propose form whenever the Idea's
//     current verdict is viable, exposing exactly save_video_script's own
//     fields and no others, verdict_id never a form field) -- phase 1.
//   - FR2 (the Strategy picker is populated from
//     store.StrategyStore.ListByChannel(activeOnly=true) -- an inactive
//     Strategy or one on a different Channel can never appear) -- phase 1.
//   - FR3 (submitting calls the identical store.VideoScriptStore.Propose
//     method save_video_script's mutate step calls; redirects to
//     web/schedule's existing list) -- phase 2.
//   - FR4 (CanWrite gate, re-derived fresh on every POST -- Founder,
//     Co-Creator, or Analyst; a POST from a caller without CanWrite is
//     rejected regardless of what was rendered) -- phase 2 (Analyst tier
//     proof: the propose succeeds as Analyst, not Creator-only) and phase 7
//     (a non-member's forged POST is rejected with nothing written, and the
//     propose tier is shown to be genuinely distinct from the review tier).
//   - FR5 (server-generated idempotency key, hidden field, passed through
//     to Propose's existing dedupe -- a double-submit creates no second
//     row) -- phase 3.
//   - FR6 (ARCHITECTURE.md NFR3 amendment) -- docs-only (#1916); not
//     exercised by a test.
//   - NFR1 (FR3's write path satisfies LB4's idempotency contract, no
//     session-held state) -- phase 3.
//   - NFR2 (authorization is re-derived server-side on every request that
//     renders or accepts the propose form, never a client-supplied flag) --
//     phase 7.
//   - NFR3 (a Strategy or verdict that stops qualifying between render and
//     submit is rejected by Propose's own existing checks, not assumed
//     valid) -- not re-exercised here; already covered in isolation by
//     web/research/research_integration_test.go's
//     TestHandleProposeVideoScript_StrategyDeletedBeforeSubmit_BadRequest_NoRow
//     and _VerdictBecomesNonViableBeforeSubmit_BadRequest_NoRow. This
//     file's job is proving the steps connect across web and MCP, not
//     re-litigating #1915's own edge-case surface.
//
// Run it explicitly (requires a working Docker daemon):
//
//	bazel test //audience_score_system/citest:e2e_test --test_output=all
package citest_test

import (
	"net/http"
	"net/url"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	mcptools "github.com/whale-net/everything/audience_score_system/mcp/tools"
	"github.com/whale-net/everything/audience_score_system/store"
)

// TestE2E_M42_ProposeWebBrowseReview is M4.2's own acceptance test (root
// plan #1914, this task issue #1917): one Channel, one Idea, one viable
// verdict, and three Strategies (one active on the Channel, one inactive on
// it, one active on a SECOND Channel -- so the propose picker's filtering
// is meaningful), driven through a web propose, a browser-replay
// idempotency check, a web browse, a cross-surface MCP read and mirror
// write, a web review (greenlight + deny), and an authorization-boundary
// check -- sharing this function's state across every phase in order,
// mirrored from TestE2E_M41_ResearchWebSaveBrowse's own sequential,
// state-sharing t.Run structure.
func TestE2E_M42_ProposeWebBrowseReview(t *testing.T) {
	w := newWorld(t)
	ctx := w.ctx

	creator, _, err := w.st.Persons().UpsertByGoogleSubject(ctx, "sub-m42-creator", "m42-creator@example.com", "M4.2 Creator")
	require.NoError(t, err)
	analyst, _, err := w.st.Persons().UpsertByGoogleSubject(ctx, "sub-m42-analyst", "m42-analyst@example.com", "M4.2 Analyst")
	require.NoError(t, err)
	outsider, _, err := w.st.Persons().UpsertByGoogleSubject(ctx, "sub-m42-outsider", "m42-outsider@example.com", "M4.2 Outsider")
	require.NoError(t, err)

	// Two Channels for the SAME Founder, no second UpsertByGoogleSubject
	// call -- mirrors m2_multi_channel_test.go's own "no second signup"
	// precedent -- so ch2's Strategy below is a genuinely different
	// Channel's data, not a different Person's.
	ch, err := w.st.Channels().Create(ctx, "yt-m42-channel", "M4.2 Propose Channel", creator.ID)
	require.NoError(t, err)
	require.NoError(t, w.st.Roles().AddRole(ctx, ch.ID, analyst.ID, store.RoleAnalyst, creator.ID))
	ch2, err := w.st.Channels().Create(ctx, "yt-m42-channel-2", "M4.2 Second Channel", creator.ID)
	require.NoError(t, err)

	creatorCookie := w.establishSession(creator.ID)
	analystCookie := w.establishSession(analyst.ID)
	outsiderCookie := w.establishSession(outsider.ID)

	csCreator := w.mcpConnect(creator.ID)
	csAnalyst := w.mcpConnect(analyst.ID)

	ideaOut := decode[mcptools.IdeaOutput](t, callTool(t, csAnalyst, "create_idea", mcptools.CreateIdeaInput{
		ChannelID: ch.ID.String(), Title: "M4.2 Propose Idea",
	}))
	ideaID := uuid.MustParse(ideaOut.IdeaID)

	verdictOut := decode[mcptools.VerdictOutput](t, callTool(t, csAnalyst, "save_viability_verdict", mcptools.SaveViabilityVerdictInput{
		ChannelID: ch.ID.String(), IdeaID: ideaID.String(), Verdict: string(store.VerdictViable),
		Reasoning:         "M4.2: strong evidence to proceed",
		IdempotencyKeyArg: uuid.NewString(),
	}))

	// The active same-Channel Strategy -- the only one FR2's picker should
	// ever offer.
	activeStrategy := decode[mcptools.StrategyOutput](t, callTool(t, csAnalyst, "save_strategy", mcptools.SaveStrategyInput{
		ChannelID: ch.ID.String(), Title: "M4.2 Active Strategy For Propose",
		VerdictIDs: []string{verdictOut.ID}, IdempotencyKeyArg: uuid.NewString(),
	}))

	// An inactive same-Channel Strategy -- FR2's picker must never offer
	// this one.
	inactive := false
	inactiveStrategy := decode[mcptools.StrategyOutput](t, callTool(t, csAnalyst, "save_strategy", mcptools.SaveStrategyInput{
		ChannelID: ch.ID.String(), Title: "M4.2 Inactive Strategy Never Offered",
		VerdictIDs: []string{verdictOut.ID}, Active: &inactive, IdempotencyKeyArg: uuid.NewString(),
	}))

	// An active Strategy on a SECOND Channel -- FR2's picker must never
	// leak this across the Channel boundary, mirroring
	// m2_multi_channel_test.go's own step 12 Channel-isolation precedent
	// for video_script.
	otherIdeaOut := decode[mcptools.IdeaOutput](t, callTool(t, csCreator, "create_idea", mcptools.CreateIdeaInput{
		ChannelID: ch2.ID.String(), Title: "M4.2 Other Channel Idea",
	}))
	otherIdeaID := uuid.MustParse(otherIdeaOut.IdeaID)
	otherVerdictOut := decode[mcptools.VerdictOutput](t, callTool(t, csCreator, "save_viability_verdict", mcptools.SaveViabilityVerdictInput{
		ChannelID: ch2.ID.String(), IdeaID: otherIdeaID.String(), Verdict: string(store.VerdictViable),
		Reasoning:         "M4.2: second channel's own verdict",
		IdempotencyKeyArg: uuid.NewString(),
	}))
	otherChannelStrategy := decode[mcptools.StrategyOutput](t, callTool(t, csCreator, "save_strategy", mcptools.SaveStrategyInput{
		ChannelID: ch2.ID.String(), Title: "M4.2 Other Channel Strategy Never Leaks",
		VerdictIDs: []string{otherVerdictOut.ID}, IdempotencyKeyArg: uuid.NewString(),
	}))

	// ── 1: FR1, FR2 -- the propose form is offered, exposing exactly its
	// five fields, and the picker offers ONLY the active same-Channel
	// Strategy ──────────────────────────────────────────────────────────
	t.Run("1_fr1_fr2_propose_form_offered", func(t *testing.T) {
		rec := w.get(analystCookie, "/channels/"+ch.ID.String()+"/research/ideas/"+ideaID.String())
		require.Equal(t, http.StatusOK, rec.Code, "body: %s", rec.Body.String())
		body := rec.Body.String()

		assert.Contains(t, body, `name="idempotency_key"`)
		assert.Contains(t, body, `name="strategy_id"`)
		assert.Contains(t, body, `name="title"`)
		assert.Contains(t, body, `name="script_text"`)
		assert.Contains(t, body, `name="target_publish_date"`)

		assert.Contains(t, body, activeStrategy.Title, "FR2: the active same-Channel Strategy must appear in the picker")
		assert.NotContains(t, body, inactiveStrategy.Title, "FR2: an inactive Strategy must never appear in the picker")
		assert.NotContains(t, body, otherChannelStrategy.Title, "FR2: a Strategy on a different Channel must never appear in the picker")
	})

	proposeKey := uuid.NewString()
	const (
		webProposedTitle      = "M4.2 Web-Proposed Script"
		webProposedScriptText = "Script text proposed on web for M4.2."
	)
	proposeForm := url.Values{
		"idempotency_key":     {proposeKey},
		"strategy_id":         {activeStrategy.StrategyID},
		"title":               {webProposedTitle},
		"script_text":         {webProposedScriptText},
		"target_publish_date": {"2026-04-01"},
	}
	var webScriptID uuid.UUID

	// ── 2: FR3, FR4 -- propose over web, as the ANALYST (proving
	// store.CanWrite, not Creator-only) ─────────────────────────────────
	t.Run("2_fr3_fr4_propose_over_web", func(t *testing.T) {
		rec := w.postForm(analystCookie, "/channels/"+ch.ID.String()+"/research/ideas/"+ideaID.String()+"/video-scripts", proposeForm)
		require.Equal(t, http.StatusSeeOther, rec.Code, "body: %s", rec.Body.String())
		assert.Equal(t, "/channels/"+ch.ID.String()+"/schedule", rec.Header().Get("Location"), "FR3: redirects to web/schedule's existing list")

		scripts, err := w.st.VideoScripts().ListByChannel(ctx, ch.ID)
		require.NoError(t, err)
		require.Len(t, scripts, 1, "exactly one video_script must exist on ch after this POST")
		script := scripts[0]
		webScriptID = script.ID

		assert.Equal(t, store.VideoScriptStatusProposed, script.Status)
		assert.Equal(t, verdictOut.ID, script.VerdictID.String())
		assert.Equal(t, ideaID, script.IdeaID, "FR3: idea_id is derived from the Idea's current verdict, never a form field")
		assert.Equal(t, activeStrategy.StrategyID, script.StrategyID.String())
		assert.Equal(t, analyst.ID, script.CreatedByPersonID, "FR4: an Analyst's CanWrite propose, not Creator-only")
	})

	// ── 3: FR5, NFR1 -- replaying the identical POST body (same
	// idempotency_key) creates no duplicate row ─────────────────────────
	t.Run("3_fr5_nfr1_idempotent_replay", func(t *testing.T) {
		rec := w.postForm(analystCookie, "/channels/"+ch.ID.String()+"/research/ideas/"+ideaID.String()+"/video-scripts", proposeForm)
		require.Equal(t, http.StatusSeeOther, rec.Code, "a replayed submit must still redirect, not error, body: %s", rec.Body.String())
		assert.Equal(t, "/channels/"+ch.ID.String()+"/schedule", rec.Header().Get("Location"))

		scripts, err := w.st.VideoScripts().ListByChannel(ctx, ch.ID)
		require.NoError(t, err)
		assert.Len(t, scripts, 1, "FR5/NFR1: replaying the identical idempotency_key must not create a second video_script")
	})

	// ── 4: C10 (already shipped) -- the proposed script browses on
	// web/schedule, regression guard ─────────────────────────────────────
	t.Run("4_c10_browse_on_web_regression_guard", func(t *testing.T) {
		rec := w.get(analystCookie, "/channels/"+ch.ID.String()+"/schedule")
		require.Equal(t, http.StatusOK, rec.Code, "body: %s", rec.Body.String())
		body := rec.Body.String()
		assert.Contains(t, body, webProposedTitle)
		assert.Contains(t, body, "Proposed")
	})

	var mcpScriptID uuid.UUID
	const (
		mcpProposedTitle      = "M4.2 MCP-Proposed Script"
		mcpProposedScriptText = "Script text proposed via MCP for M4.2."
	)

	// ── 5: the load-bearing cross-surface claim -- a script proposed on
	// web reads back identically over MCP, and the mirror direction: a
	// script proposed via save_video_script renders identically on
	// web/schedule ────────────────────────────────────────────────────────
	t.Run("5_mcp_cross_surface_same_row_and_mirror", func(t *testing.T) {
		overview := decode[mcptools.GetChannelOverviewOutput](t, callTool(t, csCreator, "get_channel_overview", mcptools.GetChannelOverviewInput{
			ChannelID: ch.ID.String(), Sections: []string{"video_scripts"},
		}))
		var found *mcptools.VideoScriptOverviewOutput
		for i := range overview.VideoScripts {
			if overview.VideoScripts[i].VideoScriptID == webScriptID.String() {
				found = &overview.VideoScripts[i]
			}
		}
		require.NotNil(t, found, "the web-proposed script must be readable over MCP -- same store, same rules, no second-class record")
		assert.Equal(t, webProposedTitle, found.Title)
		assert.Equal(t, "proposed", found.Status)
		assert.Equal(t, verdictOut.Version, found.VerdictVersion)
		assert.Equal(t, string(store.VerdictViable), found.Verdict)

		// Mirror direction: propose a second script via MCP, then confirm
		// it renders identically on GET /channels/{id}/schedule, right
		// alongside the web-proposed one.
		scriptRes := decode[mcptools.VideoScriptOutput](t, callTool(t, csAnalyst, "save_video_script", mcptools.SaveVideoScriptInput{
			ChannelID: ch.ID.String(), VerdictID: verdictOut.ID, StrategyID: activeStrategy.StrategyID,
			Title: mcpProposedTitle, ScriptText: mcpProposedScriptText, IdempotencyKeyArg: uuid.NewString(),
		}))
		mcpScriptID = uuid.MustParse(scriptRes.VideoScriptID)
		assert.Equal(t, "proposed", scriptRes.Status)

		rec := w.get(analystCookie, "/channels/"+ch.ID.String()+"/schedule")
		require.Equal(t, http.StatusOK, rec.Code, "body: %s", rec.Body.String())
		body := rec.Body.String()
		assert.Contains(t, body, mcpProposedTitle, "a script proposed via MCP must render on web/schedule identically to one proposed via web")
		assert.Contains(t, body, webProposedTitle, "both scripts must appear side by side, neither a second-class record")
	})

	// ── 6: C19 (already shipped) -- review on web: the Founder
	// greenlights the web-proposed script, then denies the MCP-proposed
	// one -- closes the milestone's outcome sentence entirely over web ───
	t.Run("6_c19_review_on_web_regression_guard", func(t *testing.T) {
		rec := w.postForm(creatorCookie, "/schedule/"+webScriptID.String()+"/approve", nil)
		require.Equal(t, http.StatusSeeOther, rec.Code, "body: %s", rec.Body.String())
		script, err := w.st.VideoScripts().GetByID(ctx, webScriptID)
		require.NoError(t, err)
		assert.Equal(t, store.VideoScriptStatusGreenlit, script.Status)
		require.NotNil(t, script.DecidedByPersonID)
		assert.Equal(t, creator.ID, *script.DecidedByPersonID)
		require.NotNil(t, script.DecidedAt)

		listRec := w.get(analystCookie, "/channels/"+ch.ID.String()+"/schedule")
		require.Equal(t, http.StatusOK, listRec.Code, "body: %s", listRec.Body.String())
		assert.Contains(t, listRec.Body.String(), "Greenlit")

		denyRec := w.postForm(creatorCookie, "/schedule/"+mcpScriptID.String()+"/deny", nil)
		require.Equal(t, http.StatusSeeOther, denyRec.Code, "body: %s", denyRec.Body.String())
		script2, err := w.st.VideoScripts().GetByID(ctx, mcpScriptID)
		require.NoError(t, err)
		assert.Equal(t, store.VideoScriptStatusDenied, script2.Status)
	})

	// ── 7: FR4, NFR2 -- authorization boundary: a non-member is rejected
	// on both the GET and the forged POST with nothing written; the
	// Analyst's propose tier (CanWrite) is genuinely distinct from the
	// review tier (CanApprove) ──────────────────────────────────────────
	t.Run("7_fr4_nfr2_authorization_boundary", func(t *testing.T) {
		getIdea := w.get(outsiderCookie, "/channels/"+ch.ID.String()+"/research/ideas/"+ideaID.String())
		assert.Equal(t, http.StatusForbidden, getIdea.Code)

		before, err := w.st.VideoScripts().ListByChannel(ctx, ch.ID)
		require.NoError(t, err)

		forgedForm := url.Values{
			"idempotency_key": {uuid.NewString()}, "strategy_id": {activeStrategy.StrategyID},
			"title": {"forged script"}, "script_text": {"forged script text"},
		}
		postForged := w.postForm(outsiderCookie, "/channels/"+ch.ID.String()+"/research/ideas/"+ideaID.String()+"/video-scripts", forgedForm)
		assert.Equal(t, http.StatusForbidden, postForged.Code, "body: %s", postForged.Body.String())

		after, err := w.st.VideoScripts().ListByChannel(ctx, ch.ID)
		require.NoError(t, err)
		assert.Equal(t, len(before), len(after), "the outsider's rejected POST must not have created a video_script")

		// A fresh proposed video_script fixture (direct store call, not
		// itself under test -- matching e2e_test.go step 7's own "fixture
		// volume" precedent) proves the Analyst -- who legitimately holds
		// CanWrite and just proposed two scripts above -- is still
		// rejected by CanApprove: the propose tier and the review tier are
		// genuinely different, both enforced server-side.
		fixtureScript, err := w.st.VideoScripts().Propose(ctx, store.ProposeVideoScriptInput{
			ChannelID:         ch.ID,
			VerdictID:         uuid.MustParse(verdictOut.ID),
			StrategyID:        uuid.MustParse(activeStrategy.StrategyID),
			Title:             "M4.2 Tier-Check Fixture Script",
			ScriptText:        "fixture script text, not itself under test",
			CreatedByPersonID: creator.ID,
			IdempotencyKey:    uuid.NewString(),
		})
		require.NoError(t, err)

		rejectApprove := w.postForm(analystCookie, "/schedule/"+fixtureScript.ID.String()+"/approve", nil)
		assert.Equal(t, http.StatusForbidden, rejectApprove.Code, "body: %s", rejectApprove.Body.String())

		reloaded, err := w.st.VideoScripts().GetByID(ctx, fixtureScript.ID)
		require.NoError(t, err)
		assert.Equal(t, store.VideoScriptStatusProposed, reloaded.Status, "the Analyst's rejected approve must not change status")
	})
}
