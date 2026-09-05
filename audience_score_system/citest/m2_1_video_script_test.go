//go:build integration

// Package citest_test (M2.1 half): end-to-end coverage for milestone
// M2.1 (`video-script-model`, root plan #1823, this task issue #1837)
// across MCP, web, and the sync worker -- the cross-surface behavior no
// single task's own test exercises. Modelled exactly on
// TestE2E_ThreeLoopsEndToEnd (e2e_test.go) and
// TestE2E_M2_MultiChannelMultiTier (m2_multi_channel_test.go): every step
// below runs through the real store/handler/tool/worker surfaces sharing
// this package's `world` (see e2e_test.go's newWorld), never fixtures
// poked directly into tables for the behavior actually under test.
//
// # What this file does NOT re-cover
//
// #1835's own Implementation phase already retargeted e2e_test.go's and
// m2_multi_channel_test.go's schedule loops onto video_script
// (save_video_script/greenlight_video_script and the rebuilt web/schedule
// routes) as a side effect of its own work, and both files already prove:
// the MCP save->greenlight->sync->outcome loop once (e2e_test.go step 5-7),
// Analyst-403/Creator-succeeds on the web greenlight route (e2e_test.go
// step 6), the web publish freeze for an auto-matched script
// (e2e_test.go step 8), and Co-Creator's web-surface greenlight authority
// (m2_multi_channel_test.go step 6). This file's job is everything else
// the milestone's own Testing section calls for that those two files
// don't already exercise: MCP-tool-level lifecycle authorization (not
// just web), deny_video_script and archive_video_script (not just
// greenlight), cross-surface agreement beyond the single greenlight case,
// the freeze's MCP half, the "pending does not freeze" counter-case, the
// undated-script matching cap end to end, resolving against an archived
// script, and the milestone's retired-surface/payload-shape assertions.
// Every task's own *_integration_test.go already proves its own slice in
// isolation (mcp/tools/video_script_integration_test.go in particular
// proves FR36-FR40's full matrix and NFR12's idempotency replay on all
// four tools against fixture-inserted matches) -- this file's own
// distinguishing job, per this task's Validation section, is proving the
// freeze, the undated-cap, and the archived-override cases end to end,
// through the REAL sync worker (worker/sync.ChannelSyncWorkflow via
// Temporal's testsuite, exactly like e2e_test.go's runSyncCycle), not
// only at the unit level where a surface-local test could pass while the
// assembled system misbehaves.
//
// # Bullet -> test-case map (this task's Testing section)
//
//   - "Full lifecycle, MCP path ... C9's loop closing with no
//     schedule/calendar step" -- TestE2E_M21_VideoScriptMilestone/
//     1_full_lifecycle_mcp_path_no_schedule_step (also covers "the sync
//     job still ingests published videos and metrics" via its own
//     explicit FR46 assertion).
//   - "A second script proposed and then deny_video_script'd never
//     appears as a match candidate ..." -- .../2_denied_script_excluded_from_candidates_and_outcomes
//   - "A greenlit script archived before any video publishes ..." --
//     .../3_archived_before_publish_excluded_from_next_sync
//   - "Propose via MCP, greenlight via web, confirm via MCP ..." --
//     .../4_cross_surface_propose_mcp_greenlight_web_overview_agrees
//   - "Propose via MCP, deny via web, archive attempt via MCP ..." --
//     .../5_cross_surface_deny_web_archive_mcp_rejected_invalid_transition
//   - "An Analyst can save_video_script but is rejected by all three
//     lifecycle tools and by all three web routes" --
//     .../6_analyst_rejected_by_all_lifecycle_tools_and_web_routes
//   - "A Co-Creator has authority symmetric with the Founder ..." --
//     .../7_co_creator_symmetric_authority_greenlight_deny_archive_mcp_and_web
//   - "a person with a role on Channel A cannot read or mutate Channel
//     B's scripts ..." -- m2_multi_channel_test.go's own step 12 (this
//     task extends that file directly, per its Testing section).
//   - "Greenlit script -> sync a published video that auto-links ...
//     archive_video_script over MCP returns the freeze error, and the web
//     archive route 409s" -- .../8_freeze_auto_matched_rejected_by_mcp_and_web_both
//   - "Same setup but with the match still pending -> archive succeeds on
//     both paths" -- .../9_pending_match_does_not_freeze_archive_succeeds_mcp_and_web
//   - "A greenlit script with no target date ... never auto-links ...",
//     "resolve_pending_match ... confirms it, and the row then appears in
//     get_prediction_vs_outcome", and "resolve_pending_match confirms
//     successfully against an archived script" -- all three combine into
//     one coherent story in .../10_undated_script_pending_then_resolved_against_archived_script
//     (the same script that was pending's best guess is archived before
//     resolution, exactly the "since-pulled-back script" case FR40's note
//     describes).
//   - "A registry-level assertion listing every retired tool name ...
//     asserting none is registered" -- .../11_retired_tools_never_registered_ten_names
//   - "get_channel_overview's response carries no synced_schedule key and
//     no cadence anywhere in a Strategy payload" --
//     .../12_no_leftover_schedule_or_cadence_fields_in_payloads
//   - "Idempotency (NFR12): each of the four video_script write tools
//     replayed with the same key ..." -- already exhaustively covered per
//     tool, in isolation, by mcp/tools/video_script_integration_test.go's
//     TestSaveVideoScript_Replay_SameKeySameArgs_ReturnsOriginal_ExactlyOneRow,
//     TestGreenlightVideoScript_Replay_SameKeyReturnsOriginal_NoDoubleTransition,
//     TestDenyVideoScript_Replay_SameKeyReturnsOriginal_NoDoubleTransition, and
//     TestArchiveVideoScript_Replay_SameKeyReturnsOriginal_NoDoubleTransition --
//     not re-duplicated here per this task's own instruction to close
//     gaps, not re-cover ground #1835 (and, for this bullet, an earlier
//     task) already proved.
//
// Run it explicitly (requires a working Docker daemon):
//
//	bazel test //audience_score_system/citest:e2e_test --test_output=all
package citest_test

import (
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	mcptools "github.com/whale-net/everything/audience_score_system/mcp/tools"
	"github.com/whale-net/everything/audience_score_system/store"
	"github.com/whale-net/everything/audience_score_system/youtube"
	"github.com/whale-net/everything/audience_score_system/youtube/fake"
)

// videoScriptChannel bundles a fresh Channel (Founder = creator, plus
// Co-Creator/Analyst roles granted directly rather than through the
// invite flow -- invite mechanics are e2e_test.go step 2's and
// m2_multi_channel_test.go's own job, not this file's) with a viable
// verdict and active Strategy already saved, ready for save_video_script.
type videoScriptChannel struct {
	ch       store.Channel
	verdict  store.Verdict
	strategy store.StrategyDetail
}

// setupVideoScriptChannel creates a fresh, isolated Channel per scenario
// below -- isolation matters here specifically because worker/sync's
// matcher candidate pool (store.MatchStore.ListCandidates) is scoped by
// Channel, not by Idea: two scenarios sharing one Channel could have their
// greenlit-but-not-yet-resolved scripts accidentally compete as candidates
// for each other's synced videos. idea/verdict/strategy creation goes
// straight through the store rather than MCP -- covering create_idea/
// save_viability_verdict/save_strategy's own tool-level behavior is
// e2e_test.go step 3/4/5's job (and this file's own step 1 exercises the
// full MCP chain once, for the milestone's headline no-schedule-step
// claim); every other scenario below only needs a viable verdict + active
// Strategy to exist, not to re-prove how one gets made.
func (w *world) setupVideoScriptChannel(t *testing.T, creator, coCreator, analyst store.Person, label string) videoScriptChannel {
	t.Helper()
	ctx := w.ctx

	ch, err := w.st.Channels().Create(ctx, "yt-"+uuid.NewString(), label+" Channel", creator.ID)
	require.NoError(t, err)
	require.NoError(t, w.st.Roles().AddRole(ctx, ch.ID, coCreator.ID, store.RoleCoCreator, creator.ID))
	require.NoError(t, w.st.Roles().AddRole(ctx, ch.ID, analyst.ID, store.RoleAnalyst, creator.ID))

	idea, err := w.st.Ideas().Create(ctx, ch.ID, label+" Idea", creator.ID)
	require.NoError(t, err)
	verdict, err := w.st.Verdicts().Append(ctx, store.AppendVerdictInput{
		IdeaID: idea.ID, Verdict: store.VerdictViable, Reasoning: "viable for " + label, AuthorPersonID: creator.ID,
	})
	require.NoError(t, err)
	strategy, err := w.st.Strategies().Save(ctx, store.SaveStrategyInput{
		ChannelID: ch.ID, Title: label + " Strategy", Active: true,
		VerdictIDs: []uuid.UUID{verdict.ID}, CreatedByPersonID: creator.ID,
	})
	require.NoError(t, err)

	return videoScriptChannel{ch: ch, verdict: verdict, strategy: strategy}
}

// TestE2E_M21_VideoScriptMilestone is M2.1's own end-to-end acceptance
// test (issue #1837, milestone video-script-model): every t.Run below is
// an independent scenario (its own fresh Channel via
// setupVideoScriptChannel), sharing only this function's Founder/
// Co-Creator/Analyst Persons and their already-open MCP sessions/web
// cookies -- a Person's role is per-Channel, so reusing the same three
// Persons across many Channels needs no extra signup/invite ceremony
// (mirrored from TestE2E_M2_MultiChannelMultiTier's own "no second
// signup" pattern, one level further: no second Person at all).
func TestE2E_M21_VideoScriptMilestone(t *testing.T) {
	w := newWorld(t)
	ctx := w.ctx

	creator, _, err := w.st.Persons().UpsertByGoogleSubject(ctx, "sub-m21-creator", "m21-creator@example.com", "M2.1 Creator")
	require.NoError(t, err)
	coCreator, _, err := w.st.Persons().UpsertByGoogleSubject(ctx, "sub-m21-cocreator", "m21-cocreator@example.com", "M2.1 Co-Creator")
	require.NoError(t, err)
	analyst, _, err := w.st.Persons().UpsertByGoogleSubject(ctx, "sub-m21-analyst", "m21-analyst@example.com", "M2.1 Analyst")
	require.NoError(t, err)

	creatorCookie := w.establishSession(creator.ID)
	coCreatorCookie := w.establishSession(coCreator.ID)
	analystCookie := w.establishSession(analyst.ID)

	// ── 1: full lifecycle, MCP path only (FR36-FR40, C18/C19) ───────────
	t.Run("1_full_lifecycle_mcp_path_no_schedule_step", func(t *testing.T) {
		vsc := w.setupVideoScriptChannel(t, creator, coCreator, analyst, "Full Loop")
		csAnalyst := w.mcpConnect(analyst.ID)

		idea := decode[mcptools.IdeaOutput](t, callTool(t, csAnalyst, "create_idea", mcptools.CreateIdeaInput{
			ChannelID: vsc.ch.ID.String(), Title: "Full Loop Video Idea",
		}))
		ideaID := idea.IdeaID

		note := decode[mcptools.ResearchNoteOutput](t, callTool(t, csAnalyst, "save_research_note", mcptools.SaveResearchNoteInput{
			ChannelID: vsc.ch.ID.String(), IdeaID: ideaID,
			Text: "Strong comps support this.", SourceURL: "https://example.com/comps",
			IdempotencyKeyArg: uuid.NewString(),
		}))
		require.True(t, note.Cited)

		verdict := decode[mcptools.VerdictOutput](t, callTool(t, csAnalyst, "save_viability_verdict", mcptools.SaveViabilityVerdictInput{
			ChannelID: vsc.ch.ID.String(), IdeaID: ideaID, Verdict: string(store.VerdictViable),
			Reasoning: "Comps confirm viability.", CitedResearchNoteIDs: []string{note.ID},
			IdempotencyKeyArg: uuid.NewString(),
		}))

		strategy := decode[mcptools.StrategyOutput](t, callTool(t, csAnalyst, "save_strategy", mcptools.SaveStrategyInput{
			ChannelID: vsc.ch.ID.String(), Title: "Full Loop Strategy", VerdictIDs: []string{verdict.ID},
			IdempotencyKeyArg: uuid.NewString(),
		}))

		publishAt := time.Now().Add(-time.Hour)
		script := decode[mcptools.VideoScriptOutput](t, callTool(t, csAnalyst, "save_video_script", mcptools.SaveVideoScriptInput{
			ChannelID: vsc.ch.ID.String(), VerdictID: verdict.ID, StrategyID: strategy.StrategyID,
			Title: "Full Loop Video", ScriptText: "Script text for the full loop.",
			TargetPublishDate: publishAt.Format(time.RFC3339), IdempotencyKeyArg: uuid.NewString(),
		}))
		require.Equal(t, "proposed", script.Status)

		// FR37/C9: greenlight over MCP -- no web/schedule step anywhere in
		// this loop, the milestone's headline claim.
		greenlit := decode[mcptools.VideoScriptOutput](t, callTool(t, w.mcpConnect(creator.ID), "greenlight_video_script", mcptools.GreenlightVideoScriptInput{
			ChannelID: vsc.ch.ID.String(), VideoScriptID: script.VideoScriptID, IdempotencyKeyArg: uuid.NewString(),
		}))
		require.Equal(t, "greenlit", greenlit.Status)

		views := int64(999)
		fc := &fake.Client{
			Schedule: []youtube.Video{{
				YouTubeVideoID: "yt-full-loop", Title: "Full Loop Video",
				PrivacyStatus: store.PrivacyStatusPublic, PublishedAt: &publishAt,
			}},
			MetricsByVideoID: map[string]youtube.VideoMetrics{
				"yt-full-loop": {YouTubeVideoID: "yt-full-loop", Views: &views, MeasuredAt: publishAt.Add(time.Hour)},
			},
		}
		counts, err := w.runSyncCycle(vsc.ch.ID, fc)
		require.NoError(t, err)
		assert.EqualValues(t, 1, counts.syncSchedule)
		assert.EqualValues(t, 1, counts.syncOutcomes,
			"FR46: the sync job still ingests published videos and metrics -- FR46 removed read surfaces, not the sync")

		cmp := decode[mcptools.GetPredictionVsOutcomeOutput](t, callTool(t, w.mcpConnect(creator.ID), "get_prediction_vs_outcome", mcptools.GetPredictionVsOutcomeInput{ChannelID: vsc.ch.ID.String()}))
		require.Len(t, cmp.Rows, 1, "C9's loop closes with no schedule/calendar step anywhere in it")
		assert.Equal(t, "yt-full-loop", cmp.Rows[0].Video.YouTubeVideoID)
		assert.Equal(t, verdict.ID, cmp.Rows[0].Verdict.VerdictID)
		assert.Equal(t, "auto", cmp.Rows[0].MatchProvenance)
		require.NotNil(t, cmp.Rows[0].Metrics.Views, "FR46: metrics genuinely ingested, not just the schedule half of sync")
		assert.EqualValues(t, 999, *cmp.Rows[0].Metrics.Views)
	})

	// ── 2: a denied script never becomes a match candidate ──────────────
	t.Run("2_denied_script_excluded_from_candidates_and_outcomes", func(t *testing.T) {
		vsc := w.setupVideoScriptChannel(t, creator, coCreator, analyst, "Denied Candidate")
		csAnalyst := w.mcpConnect(analyst.ID)

		script := decode[mcptools.VideoScriptOutput](t, callTool(t, csAnalyst, "save_video_script", mcptools.SaveVideoScriptInput{
			ChannelID: vsc.ch.ID.String(), VerdictID: vsc.verdict.ID.String(), StrategyID: vsc.strategy.ID.String(),
			Title: "Denied Candidate Video", ScriptText: "will be denied", IdempotencyKeyArg: uuid.NewString(),
		}))

		denied := decode[mcptools.VideoScriptOutput](t, callTool(t, w.mcpConnect(creator.ID), "deny_video_script", mcptools.DenyVideoScriptInput{
			ChannelID: vsc.ch.ID.String(), VideoScriptID: script.VideoScriptID, IdempotencyKeyArg: uuid.NewString(),
		}))
		require.Equal(t, "denied", denied.Status)

		publishAt := time.Now().Add(-time.Hour)
		fc := &fake.Client{Schedule: []youtube.Video{{
			YouTubeVideoID: "yt-denied-candidate", Title: "Denied Candidate Video",
			PrivacyStatus: store.PrivacyStatusPublic, PublishedAt: &publishAt,
		}}}
		_, err := w.runSyncCycle(vsc.ch.ID, fc)
		require.NoError(t, err)

		pending := decode[mcptools.ListPendingMatchesOutput](t, callTool(t, w.mcpConnect(creator.ID), "list_pending_matches", mcptools.ListPendingMatchesInput{ChannelID: vsc.ch.ID.String()}))
		require.Len(t, pending.Matches, 1, "with its only same-title script denied, the video has no plausible candidate at all")
		assert.Nil(t, pending.Matches[0].BestGuessScript, "a denied script must never surface as a match candidate")
		assert.Zero(t, pending.Matches[0].Confidence)

		cmp := decode[mcptools.GetPredictionVsOutcomeOutput](t, callTool(t, w.mcpConnect(creator.ID), "get_prediction_vs_outcome", mcptools.GetPredictionVsOutcomeInput{ChannelID: vsc.ch.ID.String()}))
		assert.Empty(t, cmp.Rows, "a denied script's video must never reach the prediction-vs-outcome comparison")
		assert.Equal(t, 1, cmp.PendingMatchCount)
	})

	// ── 3: an archived script stays out of the NEXT sync's candidates ───
	t.Run("3_archived_before_publish_excluded_from_next_sync", func(t *testing.T) {
		vsc := w.setupVideoScriptChannel(t, creator, coCreator, analyst, "Archived Before Publish")
		csAnalyst := w.mcpConnect(analyst.ID)
		csCreator := w.mcpConnect(creator.ID)

		script := decode[mcptools.VideoScriptOutput](t, callTool(t, csAnalyst, "save_video_script", mcptools.SaveVideoScriptInput{
			ChannelID: vsc.ch.ID.String(), VerdictID: vsc.verdict.ID.String(), StrategyID: vsc.strategy.ID.String(),
			Title: "Archived Before Publish Video", ScriptText: "will be archived before publishing", IdempotencyKeyArg: uuid.NewString(),
		}))
		greenlit := decode[mcptools.VideoScriptOutput](t, callTool(t, csCreator, "greenlight_video_script", mcptools.GreenlightVideoScriptInput{
			ChannelID: vsc.ch.ID.String(), VideoScriptID: script.VideoScriptID, IdempotencyKeyArg: uuid.NewString(),
		}))
		require.Equal(t, "greenlit", greenlit.Status)

		archived := decode[mcptools.VideoScriptOutput](t, callTool(t, csCreator, "archive_video_script", mcptools.ArchiveVideoScriptInput{
			ChannelID: vsc.ch.ID.String(), VideoScriptID: script.VideoScriptID, IdempotencyKeyArg: uuid.NewString(),
		}))
		require.Equal(t, "archived", archived.Status, "no live match exists yet, so archiving a still-unmatched greenlit script succeeds")

		publishAt := time.Now().Add(-time.Hour)
		fc := &fake.Client{Schedule: []youtube.Video{{
			YouTubeVideoID: "yt-archived-before-publish", Title: "Archived Before Publish Video",
			PrivacyStatus: store.PrivacyStatusPublic, PublishedAt: &publishAt,
		}}}
		_, err := w.runSyncCycle(vsc.ch.ID, fc)
		require.NoError(t, err)

		pending := decode[mcptools.ListPendingMatchesOutput](t, callTool(t, csCreator, "list_pending_matches", mcptools.ListPendingMatchesInput{ChannelID: vsc.ch.ID.String()}))
		require.Len(t, pending.Matches, 1)
		assert.Nil(t, pending.Matches[0].BestGuessScript, "an archived script must stay out of the candidate pool on the very next sync run")

		cmp := decode[mcptools.GetPredictionVsOutcomeOutput](t, callTool(t, csCreator, "get_prediction_vs_outcome", mcptools.GetPredictionVsOutcomeInput{ChannelID: vsc.ch.ID.String()}))
		assert.Empty(t, cmp.Rows)
	})

	// ── 4: propose MCP, greenlight web, confirm via MCP overview ────────
	t.Run("4_cross_surface_propose_mcp_greenlight_web_overview_agrees", func(t *testing.T) {
		vsc := w.setupVideoScriptChannel(t, creator, coCreator, analyst, "Cross Surface Greenlight")
		csAnalyst := w.mcpConnect(analyst.ID)

		script := decode[mcptools.VideoScriptOutput](t, callTool(t, csAnalyst, "save_video_script", mcptools.SaveVideoScriptInput{
			ChannelID: vsc.ch.ID.String(), VerdictID: vsc.verdict.ID.String(), StrategyID: vsc.strategy.ID.String(),
			Title: "Cross Surface Greenlight Video", ScriptText: "proposed over MCP, greenlit over web", IdempotencyKeyArg: uuid.NewString(),
		}))

		rec := w.postForm(creatorCookie, "/schedule/"+script.VideoScriptID+"/approve", nil)
		require.Equal(t, http.StatusSeeOther, rec.Code, "body: %s", rec.Body.String())

		overview := decode[mcptools.GetChannelOverviewOutput](t, callTool(t, w.mcpConnect(analyst.ID), "get_channel_overview", mcptools.GetChannelOverviewInput{ChannelID: vsc.ch.ID.String()}))
		var found bool
		for _, vs := range overview.VideoScripts {
			if vs.VideoScriptID == script.VideoScriptID {
				found = true
				assert.Equal(t, "greenlit", vs.Status, "the web greenlight must be visible through the MCP overview -- both surfaces address the same row")
			}
		}
		assert.True(t, found, "get_channel_overview's video_scripts section must include the just-greenlit script")
	})

	// ── 5: propose MCP, deny web, archive attempt MCP rejected ──────────
	t.Run("5_cross_surface_deny_web_archive_mcp_rejected_invalid_transition", func(t *testing.T) {
		vsc := w.setupVideoScriptChannel(t, creator, coCreator, analyst, "Cross Surface Deny")
		csAnalyst := w.mcpConnect(analyst.ID)

		script := decode[mcptools.VideoScriptOutput](t, callTool(t, csAnalyst, "save_video_script", mcptools.SaveVideoScriptInput{
			ChannelID: vsc.ch.ID.String(), VerdictID: vsc.verdict.ID.String(), StrategyID: vsc.strategy.ID.String(),
			Title: "Cross Surface Deny Video", ScriptText: "proposed over MCP, denied over web", IdempotencyKeyArg: uuid.NewString(),
		}))

		rec := w.postForm(creatorCookie, "/schedule/"+script.VideoScriptID+"/deny", nil)
		require.Equal(t, http.StatusSeeOther, rec.Code, "body: %s", rec.Body.String())

		scriptID := uuid.MustParse(script.VideoScriptID)
		got, err := w.st.VideoScripts().GetByID(ctx, scriptID)
		require.NoError(t, err)
		require.Equal(t, store.VideoScriptStatusDenied, got.Status)

		res := callTool(t, w.mcpConnect(creator.ID), "archive_video_script", mcptools.ArchiveVideoScriptInput{
			ChannelID: vsc.ch.ID.String(), VideoScriptID: script.VideoScriptID, IdempotencyKeyArg: uuid.NewString(),
		})
		assert.True(t, res.IsError, "FR40: archiving a denied (not greenlit) script must be rejected as an invalid transition")
		assert.Contains(t, textOf(res), "denied")

		got2, err := w.st.VideoScripts().GetByID(ctx, scriptID)
		require.NoError(t, err)
		assert.Equal(t, store.VideoScriptStatusDenied, got2.Status, "the rejected archive attempt must cause no state change")
	})

	// ── 6: Analyst rejected by every lifecycle tool and route ───────────
	t.Run("6_analyst_rejected_by_all_lifecycle_tools_and_web_routes", func(t *testing.T) {
		vsc := w.setupVideoScriptChannel(t, creator, coCreator, analyst, "Analyst Authz")
		csAnalyst := w.mcpConnect(analyst.ID)

		script := decode[mcptools.VideoScriptOutput](t, callTool(t, csAnalyst, "save_video_script", mcptools.SaveVideoScriptInput{
			ChannelID: vsc.ch.ID.String(), VerdictID: vsc.verdict.ID.String(), StrategyID: vsc.strategy.ID.String(),
			Title: "Analyst Authz Video", ScriptText: "an Analyst may propose but never decide", IdempotencyKeyArg: uuid.NewString(),
		}))
		require.Equal(t, "proposed", script.Status, "NFR13: Founder, Co-Creator, AND Analyst may all propose")
		scriptID := uuid.MustParse(script.VideoScriptID)

		assertUnchanged := func(t *testing.T) {
			t.Helper()
			got, err := w.st.VideoScripts().GetByID(ctx, scriptID)
			require.NoError(t, err)
			assert.Equal(t, store.VideoScriptStatusProposed, got.Status, "an Analyst's rejected attempt must never change state")
		}

		greenlightRes := callTool(t, csAnalyst, "greenlight_video_script", mcptools.GreenlightVideoScriptInput{
			ChannelID: vsc.ch.ID.String(), VideoScriptID: script.VideoScriptID, IdempotencyKeyArg: uuid.NewString(),
		})
		assert.True(t, greenlightRes.IsError, "FR37: an Analyst calling greenlight_video_script must be rejected")
		assertUnchanged(t)
		recGreenlight := w.postForm(analystCookie, "/schedule/"+script.VideoScriptID+"/approve", nil)
		assert.Equal(t, http.StatusForbidden, recGreenlight.Code)
		assertUnchanged(t)

		denyRes := callTool(t, csAnalyst, "deny_video_script", mcptools.DenyVideoScriptInput{
			ChannelID: vsc.ch.ID.String(), VideoScriptID: script.VideoScriptID, IdempotencyKeyArg: uuid.NewString(),
		})
		assert.True(t, denyRes.IsError, "FR38: an Analyst calling deny_video_script must be rejected")
		assertUnchanged(t)
		recDeny := w.postForm(analystCookie, "/schedule/"+script.VideoScriptID+"/deny", nil)
		assert.Equal(t, http.StatusForbidden, recDeny.Code)
		assertUnchanged(t)

		archiveRes := callTool(t, csAnalyst, "archive_video_script", mcptools.ArchiveVideoScriptInput{
			ChannelID: vsc.ch.ID.String(), VideoScriptID: script.VideoScriptID, IdempotencyKeyArg: uuid.NewString(),
		})
		assert.True(t, archiveRes.IsError, "FR39: an Analyst calling archive_video_script must be rejected")
		assertUnchanged(t)
		recArchive := w.postForm(analystCookie, "/schedule/"+script.VideoScriptID+"/archive", nil)
		assert.Equal(t, http.StatusForbidden, recArchive.Code)
		assertUnchanged(t)
	})

	// ── 7: Co-Creator symmetric authority, MCP and web, all three ───────
	t.Run("7_co_creator_symmetric_authority_greenlight_deny_archive_mcp_and_web", func(t *testing.T) {
		vsc := w.setupVideoScriptChannel(t, creator, coCreator, analyst, "Co-Creator Authz")
		csAnalyst := w.mcpConnect(analyst.ID)
		csCoCreator := w.mcpConnect(coCreator.ID)

		propose := func(t *testing.T, title string) mcptools.VideoScriptOutput {
			t.Helper()
			return decode[mcptools.VideoScriptOutput](t, callTool(t, csAnalyst, "save_video_script", mcptools.SaveVideoScriptInput{
				ChannelID: vsc.ch.ID.String(), VerdictID: vsc.verdict.ID.String(), StrategyID: vsc.strategy.ID.String(),
				Title: title, ScriptText: "script for " + title, IdempotencyKeyArg: uuid.NewString(),
			}))
		}

		// greenlight -- MCP.
		s1 := propose(t, "Co-Creator Greenlight MCP")
		g1 := decode[mcptools.VideoScriptOutput](t, callTool(t, csCoCreator, "greenlight_video_script", mcptools.GreenlightVideoScriptInput{
			ChannelID: vsc.ch.ID.String(), VideoScriptID: s1.VideoScriptID, IdempotencyKeyArg: uuid.NewString(),
		}))
		assert.Equal(t, "greenlit", g1.Status, "FR32/LB2: Co-Creator authority is symmetric with the Founder's, over MCP")

		// greenlight -- web.
		s2 := propose(t, "Co-Creator Greenlight Web")
		recG2 := w.postForm(coCreatorCookie, "/schedule/"+s2.VideoScriptID+"/approve", nil)
		require.Equal(t, http.StatusSeeOther, recG2.Code, "body: %s", recG2.Body.String())
		got2, err := w.st.VideoScripts().GetByID(ctx, uuid.MustParse(s2.VideoScriptID))
		require.NoError(t, err)
		assert.Equal(t, store.VideoScriptStatusGreenlit, got2.Status, "FR32/LB2: Co-Creator authority is symmetric with the Founder's, over web")

		// deny -- MCP.
		s3 := propose(t, "Co-Creator Deny MCP")
		d3 := decode[mcptools.VideoScriptOutput](t, callTool(t, csCoCreator, "deny_video_script", mcptools.DenyVideoScriptInput{
			ChannelID: vsc.ch.ID.String(), VideoScriptID: s3.VideoScriptID, IdempotencyKeyArg: uuid.NewString(),
		}))
		assert.Equal(t, "denied", d3.Status)

		// deny -- web.
		s4 := propose(t, "Co-Creator Deny Web")
		recD4 := w.postForm(coCreatorCookie, "/schedule/"+s4.VideoScriptID+"/deny", nil)
		require.Equal(t, http.StatusSeeOther, recD4.Code, "body: %s", recD4.Body.String())
		got4, err := w.st.VideoScripts().GetByID(ctx, uuid.MustParse(s4.VideoScriptID))
		require.NoError(t, err)
		assert.Equal(t, store.VideoScriptStatusDenied, got4.Status)

		// archive -- MCP (greenlight first so there is something to archive).
		s5 := propose(t, "Co-Creator Archive MCP")
		_ = decode[mcptools.VideoScriptOutput](t, callTool(t, csCoCreator, "greenlight_video_script", mcptools.GreenlightVideoScriptInput{
			ChannelID: vsc.ch.ID.String(), VideoScriptID: s5.VideoScriptID, IdempotencyKeyArg: uuid.NewString(),
		}))
		a5 := decode[mcptools.VideoScriptOutput](t, callTool(t, csCoCreator, "archive_video_script", mcptools.ArchiveVideoScriptInput{
			ChannelID: vsc.ch.ID.String(), VideoScriptID: s5.VideoScriptID, IdempotencyKeyArg: uuid.NewString(),
		}))
		assert.Equal(t, "archived", a5.Status)

		// archive -- web.
		s6 := propose(t, "Co-Creator Archive Web")
		_ = decode[mcptools.VideoScriptOutput](t, callTool(t, csCoCreator, "greenlight_video_script", mcptools.GreenlightVideoScriptInput{
			ChannelID: vsc.ch.ID.String(), VideoScriptID: s6.VideoScriptID, IdempotencyKeyArg: uuid.NewString(),
		}))
		recA6 := w.postForm(coCreatorCookie, "/schedule/"+s6.VideoScriptID+"/archive", nil)
		require.Equal(t, http.StatusSeeOther, recA6.Code, "body: %s", recA6.Body.String())
		got6, err := w.st.VideoScripts().GetByID(ctx, uuid.MustParse(s6.VideoScriptID))
		require.NoError(t, err)
		assert.Equal(t, store.VideoScriptStatusArchived, got6.Status)
	})

	// ── 8: publish freeze, MCP and web both, single auto-matched script ──
	t.Run("8_freeze_auto_matched_rejected_by_mcp_and_web_both", func(t *testing.T) {
		vsc := w.setupVideoScriptChannel(t, creator, coCreator, analyst, "Freeze Auto Match")
		csAnalyst := w.mcpConnect(analyst.ID)
		csCreator := w.mcpConnect(creator.ID)

		publishAt := time.Now().Add(-time.Hour)
		script := decode[mcptools.VideoScriptOutput](t, callTool(t, csAnalyst, "save_video_script", mcptools.SaveVideoScriptInput{
			ChannelID: vsc.ch.ID.String(), VerdictID: vsc.verdict.ID.String(), StrategyID: vsc.strategy.ID.String(),
			Title: "Freeze Auto Match Video", ScriptText: "will auto-match then try to be archived",
			TargetPublishDate: publishAt.Format(time.RFC3339), IdempotencyKeyArg: uuid.NewString(),
		}))
		greenlit := decode[mcptools.VideoScriptOutput](t, callTool(t, csCreator, "greenlight_video_script", mcptools.GreenlightVideoScriptInput{
			ChannelID: vsc.ch.ID.String(), VideoScriptID: script.VideoScriptID, IdempotencyKeyArg: uuid.NewString(),
		}))
		require.Equal(t, "greenlit", greenlit.Status)

		fc := &fake.Client{Schedule: []youtube.Video{{
			YouTubeVideoID: "yt-freeze-auto", Title: "Freeze Auto Match Video",
			PrivacyStatus: store.PrivacyStatusPublic, PublishedAt: &publishAt,
		}}}
		_, err := w.runSyncCycle(vsc.ch.ID, fc)
		require.NoError(t, err)

		scriptID := uuid.MustParse(script.VideoScriptID)
		published, err := w.st.VideoScripts().IsPublished(ctx, scriptID)
		require.NoError(t, err)
		require.True(t, published, "the greenlit script's video must have auto-linked (state = 'auto')")

		// MCP archive attempt: rejected as frozen, no state change.
		mcpRes := callTool(t, csCreator, "archive_video_script", mcptools.ArchiveVideoScriptInput{
			ChannelID: vsc.ch.ID.String(), VideoScriptID: script.VideoScriptID, IdempotencyKeyArg: uuid.NewString(),
		})
		assert.True(t, mcpRes.IsError, "FR39: archive_video_script over MCP must return the freeze error")
		assert.Contains(t, textOf(mcpRes), "already been published")
		gotAfterMCP, err := w.st.VideoScripts().GetByID(ctx, scriptID)
		require.NoError(t, err)
		assert.Equal(t, store.VideoScriptStatusGreenlit, gotAfterMCP.Status, "the rejected MCP archive attempt must cause no state change")

		// Web archive attempt: 409, no state change either.
		recWeb := w.postForm(creatorCookie, "/schedule/"+script.VideoScriptID+"/archive", nil)
		assert.Equal(t, http.StatusConflict, recWeb.Code, "FR39: the web archive route must 409 once published")
		gotAfterWeb, err := w.st.VideoScripts().GetByID(ctx, scriptID)
		require.NoError(t, err)
		assert.Equal(t, store.VideoScriptStatusGreenlit, gotAfterWeb.Status, "status stays greenlit on both rejected paths")
	})

	// ── 9: a PENDING match does not freeze -- archive succeeds, both paths ──
	t.Run("9_pending_match_does_not_freeze_archive_succeeds_mcp_and_web", func(t *testing.T) {
		vsc := w.setupVideoScriptChannel(t, creator, coCreator, analyst, "Pending No Freeze")
		csAnalyst := w.mcpConnect(analyst.ID)
		csCreator := w.mcpConnect(creator.ID)

		// Undated scripts land 'pending' by construction (FR43's 0.7 cap,
		// see worker/sync/matching.go's score doc comment) -- reused here
		// purely as a reliable way to produce a live 'pending' match with a
		// real best-guess candidate, distinct from step 3's "no candidate
		// at all" placeholder.
		mcpScript := decode[mcptools.VideoScriptOutput](t, callTool(t, csAnalyst, "save_video_script", mcptools.SaveVideoScriptInput{
			ChannelID: vsc.ch.ID.String(), VerdictID: vsc.verdict.ID.String(), StrategyID: vsc.strategy.ID.String(),
			Title: "Pending Archive MCP", ScriptText: "archived while its match is still pending", IdempotencyKeyArg: uuid.NewString(),
		}))
		_ = decode[mcptools.VideoScriptOutput](t, callTool(t, csCreator, "greenlight_video_script", mcptools.GreenlightVideoScriptInput{
			ChannelID: vsc.ch.ID.String(), VideoScriptID: mcpScript.VideoScriptID, IdempotencyKeyArg: uuid.NewString(),
		}))
		publishAt1 := time.Now().Add(-time.Hour)
		fc1 := &fake.Client{Schedule: []youtube.Video{{
			YouTubeVideoID: "yt-pending-archive-mcp", Title: "Pending Archive MCP",
			PrivacyStatus: store.PrivacyStatusPublic, PublishedAt: &publishAt1,
		}}}
		_, err := w.runSyncCycle(vsc.ch.ID, fc1)
		require.NoError(t, err)

		pending1 := decode[mcptools.ListPendingMatchesOutput](t, callTool(t, csCreator, "list_pending_matches", mcptools.ListPendingMatchesInput{ChannelID: vsc.ch.ID.String()}))
		require.Len(t, pending1.Matches, 1)
		require.NotNil(t, pending1.Matches[0].BestGuessScript)
		assert.Equal(t, mcpScript.VideoScriptID, pending1.Matches[0].BestGuessScript.VideoScriptID)

		mcpArchiveRes := callTool(t, csCreator, "archive_video_script", mcptools.ArchiveVideoScriptInput{
			ChannelID: vsc.ch.ID.String(), VideoScriptID: mcpScript.VideoScriptID, IdempotencyKeyArg: uuid.NewString(),
		})
		require.False(t, mcpArchiveRes.IsError, "the freeze applies only to a live auto/confirmed match, never to a pending one: %s", textOf(mcpArchiveRes))
		mcpArchived := decode[mcptools.VideoScriptOutput](t, mcpArchiveRes)
		assert.Equal(t, "archived", mcpArchived.Status)

		webScript := decode[mcptools.VideoScriptOutput](t, callTool(t, csAnalyst, "save_video_script", mcptools.SaveVideoScriptInput{
			ChannelID: vsc.ch.ID.String(), VerdictID: vsc.verdict.ID.String(), StrategyID: vsc.strategy.ID.String(),
			Title: "Pending Archive Web", ScriptText: "archived via web while its match is still pending", IdempotencyKeyArg: uuid.NewString(),
		}))
		_ = decode[mcptools.VideoScriptOutput](t, callTool(t, csCreator, "greenlight_video_script", mcptools.GreenlightVideoScriptInput{
			ChannelID: vsc.ch.ID.String(), VideoScriptID: webScript.VideoScriptID, IdempotencyKeyArg: uuid.NewString(),
		}))
		publishAt2 := time.Now().Add(-time.Hour)
		fc2 := &fake.Client{Schedule: []youtube.Video{{
			YouTubeVideoID: "yt-pending-archive-web", Title: "Pending Archive Web",
			PrivacyStatus: store.PrivacyStatusPublic, PublishedAt: &publishAt2,
		}}}
		_, err = w.runSyncCycle(vsc.ch.ID, fc2)
		require.NoError(t, err)

		// Archiving mcpScript above only changed the SCRIPT's status --
		// its own pending match row is untouched (still pending, its
		// best-guess script now reporting archived) until a human actually
		// resolves it -- so this list now carries both matches; find
		// webScript's by its own best-guess script id rather than assuming
		// a single-element list.
		pending2 := decode[mcptools.ListPendingMatchesOutput](t, callTool(t, csCreator, "list_pending_matches", mcptools.ListPendingMatchesInput{ChannelID: vsc.ch.ID.String()}))
		require.Len(t, pending2.Matches, 2)
		var webMatch *mcptools.PendingMatchOutput
		for i, m := range pending2.Matches {
			if m.BestGuessScript != nil && m.BestGuessScript.VideoScriptID == webScript.VideoScriptID {
				webMatch = &pending2.Matches[i]
			}
		}
		require.NotNil(t, webMatch, "the just-synced web video must have webScript as its best guess")

		recWeb := w.postForm(creatorCookie, "/schedule/"+webScript.VideoScriptID+"/archive", nil)
		require.Equal(t, http.StatusSeeOther, recWeb.Code, "body: %s", recWeb.Body.String())
		gotWeb, err := w.st.VideoScripts().GetByID(ctx, uuid.MustParse(webScript.VideoScriptID))
		require.NoError(t, err)
		assert.Equal(t, store.VideoScriptStatusArchived, gotWeb.Status)
	})

	// ── 10: undated script never auto-links; resolve against an archived script ──
	t.Run("10_undated_script_pending_then_resolved_against_archived_script", func(t *testing.T) {
		vsc := w.setupVideoScriptChannel(t, creator, coCreator, analyst, "Undated Resolve Archived")
		csAnalyst := w.mcpConnect(analyst.ID)
		csCreator := w.mcpConnect(creator.ID)

		// No TargetPublishDate: FR43's 0.7 cap means even a perfect title
		// match can never clear MatchConfidenceThreshold (0.8) alone.
		script := decode[mcptools.VideoScriptOutput](t, callTool(t, csAnalyst, "save_video_script", mcptools.SaveVideoScriptInput{
			ChannelID: vsc.ch.ID.String(), VerdictID: vsc.verdict.ID.String(), StrategyID: vsc.strategy.ID.String(),
			Title: "Undated Perfect Match Video", ScriptText: "no target date set", IdempotencyKeyArg: uuid.NewString(),
		}))
		require.Empty(t, script.TargetPublishDate)
		greenlit := decode[mcptools.VideoScriptOutput](t, callTool(t, csCreator, "greenlight_video_script", mcptools.GreenlightVideoScriptInput{
			ChannelID: vsc.ch.ID.String(), VideoScriptID: script.VideoScriptID, IdempotencyKeyArg: uuid.NewString(),
		}))
		require.Equal(t, "greenlit", greenlit.Status)

		publishAt := time.Now().Add(-time.Hour)
		fc := &fake.Client{Schedule: []youtube.Video{{
			YouTubeVideoID: "yt-undated-match", Title: "Undated Perfect Match Video",
			PrivacyStatus: store.PrivacyStatusPublic, PublishedAt: &publishAt,
		}}}
		_, err := w.runSyncCycle(vsc.ch.ID, fc)
		require.NoError(t, err)

		pendingOut := decode[mcptools.ListPendingMatchesOutput](t, callTool(t, csCreator, "list_pending_matches", mcptools.ListPendingMatchesInput{ChannelID: vsc.ch.ID.String()}))
		require.Len(t, pendingOut.Matches, 1, "FR43: an undated script, even with a perfect title match, must never auto-link -- it lands pending")
		require.NotNil(t, pendingOut.Matches[0].BestGuessScript)
		assert.Equal(t, script.VideoScriptID, pendingOut.Matches[0].BestGuessScript.VideoScriptID)
		assert.Less(t, pendingOut.Matches[0].Confidence, 0.8, "below MatchConfidenceThreshold")
		assert.InDelta(t, 0.7, pendingOut.Matches[0].Confidence, 0.01, "titleWeight(0.7)*1.0 title match + dateWeight*0(no date) = 0.7, the documented cap")
		matchID := pendingOut.Matches[0].MatchID

		// The Founder pulls the script back before the pending video gets
		// resolved -- succeeds, since the freeze only applies to a live
		// auto/confirmed match (step 9), never a pending one.
		archived := decode[mcptools.VideoScriptOutput](t, callTool(t, csCreator, "archive_video_script", mcptools.ArchiveVideoScriptInput{
			ChannelID: vsc.ch.ID.String(), VideoScriptID: script.VideoScriptID, IdempotencyKeyArg: uuid.NewString(),
		}))
		require.Equal(t, "archived", archived.Status)

		// FR40/FR44: resolve_pending_match's override accepts ANY
		// video_script on the Channel, including this now-archived one --
		// the case that keeps a video correctly recorded under a
		// since-pulled-back script.
		resolved := decode[mcptools.ResolvedMatchOutput](t, callTool(t, csCreator, "resolve_pending_match", mcptools.ResolvePendingMatchInput{
			ChannelID: vsc.ch.ID.String(), MatchID: matchID, Confirm: true,
			VideoScriptID: script.VideoScriptID, IdempotencyKeyArg: uuid.NewString(),
		}))
		assert.Equal(t, "confirmed", resolved.State)

		cmp := decode[mcptools.GetPredictionVsOutcomeOutput](t, callTool(t, csCreator, "get_prediction_vs_outcome", mcptools.GetPredictionVsOutcomeInput{ChannelID: vsc.ch.ID.String()}))
		require.Len(t, cmp.Rows, 1, "the just-confirmed match must now appear in the comparison")
		assert.Equal(t, script.VideoScriptID, cmp.Rows[0].Script.VideoScriptID)
		assert.Equal(t, "archived", cmp.Rows[0].Script.Status, "the video stays correctly recorded even though its script was since pulled back")
		assert.Equal(t, "confirmed", cmp.Rows[0].MatchProvenance)
	})

	// ── 11: the retired schedule/pacing surface, ten names, none registered ──
	t.Run("11_retired_tools_never_registered_ten_names", func(t *testing.T) {
		cs := w.mcpConnect(creator.ID)
		registered := map[string]bool{}
		for tool, err := range cs.Tools(ctx, nil) {
			require.NoError(t, err)
			registered[tool.Name] = true
		}
		require.NotEmpty(t, registered, "sanity check: registration must have produced at least one tool")
		assert.True(t, registered["save_video_script"], "sanity check: the video_script surface must be registered")

		// Every schedule-draft/pacing name FR41 retired (issue #1832),
		// plus generate_schedule_plan (FR47, issue #1833) and
		// get_channel_schedule (FR46, issue #1831) -- the full ten-name
		// retired surface across the whole milestone, not just #1832's own
		// eight (see mcp/server/registry_tools_test.go for that narrower,
		// task-scoped assertion).
		retiredToolNames := []string{
			"save_schedule_draft",
			"commit_schedule_draft",
			"uncommit_schedule_draft",
			"update_schedule_draft",
			"list_schedule_entries",
			"get_drafting_context",
			"get_pacing_policy",
			"set_pacing_policy",
			"generate_schedule_plan",
			"get_channel_schedule",
		}
		for _, name := range retiredToolNames {
			assert.False(t, registered[name], "%s must not be registered -- a re-add must fail this test loudly", name)
		}
	})

	// ── 12: no leftover synced_schedule/cadence shape in live payloads ──
	t.Run("12_no_leftover_schedule_or_cadence_fields_in_payloads", func(t *testing.T) {
		vsc := w.setupVideoScriptChannel(t, creator, coCreator, analyst, "Payload Shape")
		cs := w.mcpConnect(creator.ID)

		overviewRes := callTool(t, cs, "get_channel_overview", mcptools.GetChannelOverviewInput{ChannelID: vsc.ch.ID.String()})
		overviewJSON, err := json.Marshal(overviewRes.StructuredContent)
		require.NoError(t, err)
		assert.NotContains(t, string(overviewJSON), "synced_schedule",
			"FR46/FR49: get_channel_overview's response must carry no synced_schedule key")

		strategyRes := callTool(t, cs, "save_strategy", mcptools.SaveStrategyInput{
			ChannelID: vsc.ch.ID.String(), Title: "Payload Shape Strategy", VerdictIDs: []string{vsc.verdict.ID.String()},
			IdempotencyKeyArg: uuid.NewString(),
		})
		strategyJSON, err := json.Marshal(strategyRes.StructuredContent)
		require.NoError(t, err)
		assert.NotContains(t, string(strategyJSON), "cadence",
			"FR47: no cadence anywhere in a Strategy payload")
	})
}
