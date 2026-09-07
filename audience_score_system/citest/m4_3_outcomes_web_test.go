//go:build integration

// Package citest_test (M4.3 milestone acceptance test): the outcome
// sentence itself for M4.3 (root plan #1924, this task issue #1931,
// capabilities C9/C10/C14) -- "a Creator or Analyst can resolve a pending
// match (confirm/reject), compare predicted vs. actual outcomes, and
// configure calibration entirely from the web UI", cross-checked against
// MCP, run once as the Creator and once as the Analyst (shared
// Creator-or-Analyst authority, LB5). Modelled directly on
// m4_1_research_web_test.go's and m4_2_propose_web_test.go's own
// doc-comment/harness conventions: this file reuses the package's shared
// `world` (e2e_test.go), its mcpConnect/callTool/decode helpers, and
// e2e_test.go's own real-web-router mounting -- newWorld now also mounts
// web/matches.New(st) and web/outcomes.New(st) alongside every earlier
// milestone's own routes, mirroring web/main.go's setupRoutes exactly for
// the four routes this file needs (GET/POST /channels/{id}/matches[...],
// GET /channels/{id}/outcomes, POST /channels/{id}/outcome-bar).
//
// This is a citest, not a package test, precisely because no single
// task's own *_integration_test.go can make this file's claim: #1926/#1927
// each cover the matches page in isolation, #1928/#1929 each cover the
// outcomes page in isolation, but the milestone's own sentence is the
// END-TO-END loop across BOTH pages plus the cross-surface agreement
// LB5/NFR4 require -- a match resolved on `web` must be the row
// get_prediction_vs_outcome reads back, and the calibration trend `web`
// plots must be in the IDENTICAL bucket order get_calibration_trend
// returns.
//
// Steps 2-4 of this file's flow (resolve, compare, configure) are
// performed ONLY through `web` HTTP requests -- no direct store call
// substitutes for a user action there. Step 1 (seeding the calibration
// candidates) goes through MCP's save_video_script/greenlight_video_script
// plus a real worker/sync.ChannelSyncWorkflow cycle (w.runSyncCycle),
// exactly like m3_calibration_test.go's own seedCandidate precedent --
// that plumbing is not itself under test here, only the two web pages and
// their cross-surface agreement are.
//
// # Producing a pending match reliably
//
// seedM43Candidate's targetPublishDate parameter reuses
// m2_1_video_script_test.go step 9's own documented trick: an UNDATED
// greenlit script can only ever score a title-only match (FR43's 0.7 cap,
// worker/sync/matching.go), landing PENDING below
// worker/sync.MatchConfidenceThreshold -- exactly the fixture this file's
// step 2 (web resolve) needs. A second candidate, proposed WITH a
// target_publish_date equal to its eventual publish time, instead scores
// an exact title+date match and lands 'auto' -- reused here purely to give
// the calibration trend a SECOND calendar-month bucket, so step 5's
// bucket-order cross-check (NFR4) is not trivially true of a single-bucket
// trend.
//
// # Authorization boundary pointer
//
// FR7/NFR3 (matches) and FR5/NFR2 (outcomes) authorization-boundary edge
// cases (an outsider's forged POST) are already covered in isolation by
// web/matches/matches_integration_test.go and
// web/outcomes/outcomes_integration_test.go; this file's job is proving
// the steps connect end to end and agree cross-surface, not re-litigating
// those edge cases.
//
// Run it explicitly (requires a working Docker daemon):
//
//	bazel test //audience_score_system/citest:e2e_test --test_output=all
package citest_test

import (
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	mcptools "github.com/whale-net/everything/audience_score_system/mcp/tools"
	"github.com/whale-net/everything/audience_score_system/store"
	"github.com/whale-net/everything/audience_score_system/youtube"
	"github.com/whale-net/everything/audience_score_system/youtube/fake"
)

// resolveFormRE extracts a rendered pending-match row's resolve-form
// action (the match ID) and its hidden server-generated idempotency_key
// (FR10) straight out of GET /channels/{id}/matches' rendered HTML --
// this file's only way to obtain either value, since steps 2-4 must never
// substitute a store/MCP call for what a real browser session would read
// off the page it was just served.
var resolveFormRE = regexp.MustCompile(`/matches/([0-9a-f-]{36})/resolve"[\s\S]*?name="idempotency_key" value="([0-9a-f-]{36})"`)

// extractResolveForm parses matchID and idempotencyKey out of body (the
// rendered /channels/{id}/matches page), requiring exactly one match on
// the page -- true for every scenario in this file, each of which starts
// its own fresh Channel with exactly one pending match at a time.
func extractResolveForm(t *testing.T, body string) (matchID, idempotencyKey string) {
	t.Helper()
	m := resolveFormRE.FindStringSubmatch(body)
	require.NotNil(t, m, "expected exactly one resolve form in the rendered matches page: %s", body)
	return m[1], m[2]
}

// seedM43Candidate creates a fresh Idea (ideaTitle) with a viable verdict
// and an active Strategy bound to that verdict version, proposes a
// video_script (scriptTitle) bound to it via csProposer, greenlights it
// via csCreator, then runs one real sync cycle publishing a matching video
// at publishedAt with views -- mirrors m3_calibration_test.go's own
// viableVerdictAndStrategy + proposeAndGreenlightScript pair, collapsed
// into one helper since this file needs no other candidate-construction
// path.
//
// targetPublishDate == nil leaves the script undated, landing the
// resulting match PENDING (see this file's package doc comment); a
// non-nil targetPublishDate equal to publishedAt instead lands an exact
// title+date match 'auto', mirroring m3_calibration_test.go's own
// seedCandidate. Returns the created Idea's ID and the verdict MCP
// returned, so callers can assert the SAME bound verdict version (LB3)
// renders on both web and MCP later.
func (w *world) seedM43Candidate(t *testing.T, ch store.Channel, csProposer, csCreator *mcp.ClientSession, ideaTitle, scriptTitle string, targetPublishDate *time.Time, publishedAt time.Time, views int64) (ideaID uuid.UUID, verdict mcptools.VerdictOutput) {
	t.Helper()

	ideaOut := decode[mcptools.IdeaOutput](t, callTool(t, csProposer, "create_idea", mcptools.CreateIdeaInput{
		ChannelID: ch.ID.String(), Title: ideaTitle,
	}))
	ideaID = uuid.MustParse(ideaOut.IdeaID)

	verdict = decode[mcptools.VerdictOutput](t, callTool(t, csProposer, "save_viability_verdict", mcptools.SaveViabilityVerdictInput{
		ChannelID: ch.ID.String(), IdeaID: ideaID.String(), Verdict: string(store.VerdictViable),
		Reasoning: ideaTitle + " looks strong", IdempotencyKeyArg: uuid.NewString(),
	}))

	strategy := decode[mcptools.StrategyOutput](t, callTool(t, csProposer, "save_strategy", mcptools.SaveStrategyInput{
		ChannelID: ch.ID.String(), Title: ideaTitle + " Strategy", VerdictIDs: []string{verdict.ID}, IdempotencyKeyArg: uuid.NewString(),
	}))

	scriptIn := mcptools.SaveVideoScriptInput{
		ChannelID: ch.ID.String(), VerdictID: verdict.ID, StrategyID: strategy.StrategyID,
		Title: scriptTitle, ScriptText: "script text for " + scriptTitle, IdempotencyKeyArg: uuid.NewString(),
	}
	if targetPublishDate != nil {
		scriptIn.TargetPublishDate = targetPublishDate.Format(time.RFC3339)
	}
	script := decode[mcptools.VideoScriptOutput](t, callTool(t, csProposer, "save_video_script", scriptIn))

	greenlit := decode[mcptools.VideoScriptOutput](t, callTool(t, csCreator, "greenlight_video_script", mcptools.GreenlightVideoScriptInput{
		ChannelID: ch.ID.String(), VideoScriptID: script.VideoScriptID, IdempotencyKeyArg: uuid.NewString(),
	}))
	require.Equal(t, "greenlit", greenlit.Status)

	ytID := "yt-m43-" + uuid.NewString()
	fc := &fake.Client{
		Schedule: []youtube.Video{{
			YouTubeVideoID: ytID, Title: scriptTitle, PrivacyStatus: store.PrivacyStatusPublic, PublishedAt: &publishedAt,
		}},
		MetricsByVideoID: map[string]youtube.VideoMetrics{
			ytID: {YouTubeVideoID: ytID, Views: &views, MeasuredAt: publishedAt.Add(time.Hour)},
		},
	}
	_, err := w.runSyncCycle(ch.ID, fc)
	require.NoError(t, err)

	return ideaID, verdict
}

// runM43MilestoneFlow drives ch's whole M4.3 outcome sentence as actor
// (Creator on one call, Analyst on the other -- LB5's shared
// Creator-or-Analyst write authority, proven by literally running this
// same sequence twice under two different callers, not by inspecting a
// role check in isolation): seed a pending match below threshold (step
// 1), resolve it on web (step 2), watch the outcomes page transition from
// FR2's empty state to a real row purely through that web action (step
// 3), configure the outcome bar on web and watch FR4's not-configured
// pointer become FR3's chart (step 4), then cross-check every one of
// those web-observed facts against MCP (step 5).
func (w *world) runM43MilestoneFlow(t *testing.T, tag string, actorCookie *http.Cookie, csActor, csCreator *mcp.ClientSession, ch store.Channel) {
	t.Helper()

	matchesPath := "/channels/" + ch.ID.String() + "/matches"
	outcomesPath := "/channels/" + ch.ID.String() + "/outcomes"
	outcomeBarPath := "/channels/" + ch.ID.String() + "/outcome-bar"

	// ── 1: seed a pending match below threshold (undated script, FR43) ──
	pendingIdeaTitle := tag + " Pending Idea"
	pendingScriptTitle := tag + " Pending Video"
	pendingPublishedAt := time.Date(2024, time.February, 15, 12, 0, 0, 0, time.UTC)
	const pendingViews = 1500
	_, pendingVerdict := w.seedM43Candidate(t, ch, csActor, csCreator, pendingIdeaTitle, pendingScriptTitle, nil, pendingPublishedAt, pendingViews)

	pendingBefore := decode[mcptools.ListPendingMatchesOutput](t, callTool(t, csActor, "list_pending_matches", mcptools.ListPendingMatchesInput{ChannelID: ch.ID.String()}))
	require.Len(t, pendingBefore.Matches, 1, "%s: exactly one pending match must exist before any web resolution", tag)
	require.NotNil(t, pendingBefore.Matches[0].BestGuessScript)
	assert.Equal(t, pendingScriptTitle, pendingBefore.Matches[0].BestGuessScript.Title)

	// ── FR2/FR4 empty states must both render before any web action ─────
	t.Run(tag+"_0_empty_states_before_any_web_action", func(t *testing.T) {
		rec := w.get(actorCookie, outcomesPath)
		require.Equal(t, http.StatusOK, rec.Code, "body: %s", rec.Body.String())
		body := rec.Body.String()
		assert.Contains(t, body, "No prediction-vs-outcome rows yet", "FR2: the empty state must render -- the pending match has not been confirmed yet")
		assert.Contains(t, body, "No outcome bar is configured for this Channel yet", "FR4: the not-configured pointer must render -- no bar has been set yet")
		assert.NotContains(t, body, pendingScriptTitle, "a merely-pending match must never appear in the prediction-vs-outcome comparison")
	})

	// ── 2: resolve on web (FR9, FR10) ────────────────────────────────────
	var matchID, idemKey string
	t.Run(tag+"_1_resolve_on_web", func(t *testing.T) {
		listRec := w.get(actorCookie, matchesPath)
		require.Equal(t, http.StatusOK, listRec.Code, "body: %s", listRec.Body.String())
		listBody := listRec.Body.String()
		assert.Contains(t, listBody, pendingScriptTitle)
		matchID, idemKey = extractResolveForm(t, listBody)

		resolveRec := w.postForm(actorCookie, matchesPath+"/"+matchID+"/resolve", url.Values{
			"idempotency_key": {idemKey}, "confirm": {"true"},
		})
		require.Equal(t, http.StatusSeeOther, resolveRec.Code, "%s: body: %s", tag, resolveRec.Body.String())
		assert.Equal(t, matchesPath, resolveRec.Header().Get("Location"))

		afterRec := w.get(actorCookie, matchesPath)
		require.Equal(t, http.StatusOK, afterRec.Code, "body: %s", afterRec.Body.String())
		assert.Contains(t, afterRec.Body.String(), "No pending matches", "FR8: the empty state renders once nothing is pending")
	})

	// ── 3: compare on web -- the FR2 empty state becomes a real row ─────
	t.Run(tag+"_2_compare_on_web", func(t *testing.T) {
		rec := w.get(actorCookie, outcomesPath)
		require.Equal(t, http.StatusOK, rec.Code, "body: %s", rec.Body.String())
		body := rec.Body.String()
		assert.NotContains(t, body, "No prediction-vs-outcome rows yet", "the load-bearing transition: a real row must have replaced FR2's empty state")
		assert.Contains(t, body, pendingIdeaTitle)
		assert.Contains(t, body, "(v"+strconv.Itoa(pendingVerdict.Version)+",", "LB3: the BOUND verdict version must render, labeled explicitly")
		assert.Contains(t, body, pendingScriptTitle)
		assert.Contains(t, body, "Confirmed", "FR22/FR23: a web-resolved match's provenance must render as Confirmed")
	})

	// A second candidate, proposed WITH a matching target_publish_date, so
	// it auto-links in a DIFFERENT calendar month -- purely to give the
	// calibration trend a second bucket for step 5's order cross-check
	// (see this file's package doc comment).
	autoIdeaTitle := tag + " Auto Idea"
	autoScriptTitle := tag + " Auto Video"
	autoPublishedAt := time.Date(2024, time.April, 20, 12, 0, 0, 0, time.UTC)
	const autoViews = 200
	w.seedM43Candidate(t, ch, csActor, csCreator, autoIdeaTitle, autoScriptTitle, &autoPublishedAt, autoPublishedAt, autoViews)

	pendingAfterAuto := decode[mcptools.ListPendingMatchesOutput](t, callTool(t, csActor, "list_pending_matches", mcptools.ListPendingMatchesInput{ChannelID: ch.ID.String()}))
	require.Empty(t, pendingAfterAuto.Matches, "%s: the exact title+date candidate must auto-link, not queue pending", tag)

	// ── 4: configure on web -- FR4's pointer becomes FR3's chart ────────
	const threshold = 1000.0
	t.Run(tag+"_3_configure_on_web", func(t *testing.T) {
		before := w.get(actorCookie, outcomesPath)
		require.Equal(t, http.StatusOK, before.Code, "body: %s", before.Body.String())
		assert.Contains(t, before.Body.String(), "No outcome bar is configured for this Channel yet", "FR4: the pointer must still render right up until the POST below")

		setRec := w.postForm(actorCookie, outcomeBarPath, url.Values{
			"metric_name": {store.OutcomeBarMetricViews}, "threshold_value": {strconv.FormatFloat(threshold, 'f', -1, 64)},
		})
		require.Equal(t, http.StatusSeeOther, setRec.Code, "%s: body: %s", tag, setRec.Body.String())
		assert.Equal(t, outcomesPath, setRec.Header().Get("Location"))

		after := w.get(actorCookie, outcomesPath)
		require.Equal(t, http.StatusOK, after.Code, "body: %s", after.Body.String())
		afterBody := after.Body.String()
		assert.NotContains(t, afterBody, "No outcome bar is configured", "FR3: the chart must have replaced FR4's pointer")
		assert.Contains(t, afterBody, "<svg")

		// Scope the ordering check to the calibration-trend section only:
		// the prediction-vs-outcome cards ABOVE it render
		// most-recently-published-first (the opposite order), so a
		// whole-body index comparison would spuriously fail on their
		// "Published: 2024-04-20"/"Published: 2024-02-15" lines.
		calibIdx := strings.Index(afterBody, "Calibration trend")
		require.Greater(t, calibIdx, 0, "the calibration-trend section heading must render")
		calibSection := afterBody[calibIdx:]
		assert.Contains(t, calibSection, "2024-02")
		assert.Contains(t, calibSection, "2024-04")
		assert.Less(t, strings.Index(calibSection, "2024-02"), strings.Index(calibSection, "2024-04"),
			"NFR4: within the calibration-trend section, the chronologically earlier bucket must render before the later one")
	})

	// ── 5: cross-surface agreement (LB5, NFR4) ──────────────────────────
	t.Run(tag+"_4_cross_surface_agreement", func(t *testing.T) {
		cmp := decode[mcptools.GetPredictionVsOutcomeOutput](t, callTool(t, csActor, "get_prediction_vs_outcome", mcptools.GetPredictionVsOutcomeInput{ChannelID: ch.ID.String()}))
		require.Len(t, cmp.Rows, 2, "%s: MCP must return the identical row set the web outcomes page rendered", tag)
		var byTitle = map[string]mcptools.PredictionVsOutcomeRowOutput{}
		for _, r := range cmp.Rows {
			byTitle[r.IdeaTitle] = r
		}
		confirmedRow, ok := byTitle[pendingIdeaTitle]
		require.True(t, ok, "%s: the web-confirmed idea must be readable over MCP", tag)
		assert.Equal(t, pendingVerdict.Version, confirmedRow.Verdict.Version, "the BOUND verdict version must agree between web and MCP")
		assert.Equal(t, "confirmed", confirmedRow.MatchProvenance)

		autoRow, ok := byTitle[autoIdeaTitle]
		require.True(t, ok, "%s: the auto-matched fixture idea must also be readable over MCP", tag)
		assert.Equal(t, "auto", autoRow.MatchProvenance)

		trend := decode[mcptools.GetCalibrationTrendOutput](t, callTool(t, csActor, "get_calibration_trend", mcptools.GetCalibrationTrendInput{ChannelID: ch.ID.String()}))
		require.True(t, trend.OutcomeBar.Configured)
		require.NotNil(t, trend.OutcomeBar.ThresholdValue)
		assert.Equal(t, threshold, *trend.OutcomeBar.ThresholdValue)
		require.Len(t, trend.Buckets, 2, "%s: MCP must return the same two calendar-month buckets the chart plotted", tag)

		b0, err := time.Parse(time.RFC3339, trend.Buckets[0].BucketStart)
		require.NoError(t, err)
		b1, err := time.Parse(time.RFC3339, trend.Buckets[1].BucketStart)
		require.NoError(t, err)
		assert.Equal(t, "2024-02", b0.UTC().Format("2006-01"), "NFR4: the FIRST bucket MCP returns must be the chronologically earlier month, matching the chart's own plot order")
		assert.Equal(t, "2024-04", b1.UTC().Format("2006-01"), "NFR4: the SECOND bucket MCP returns must be the later month, in the SAME order the chart plotted it")

		pendingAfterResolve := decode[mcptools.ListPendingMatchesOutput](t, callTool(t, csActor, "list_pending_matches", mcptools.ListPendingMatchesInput{ChannelID: ch.ID.String()}))
		assert.Empty(t, pendingAfterResolve.Matches, "the match resolved on web in step 2 must no longer be listed as pending over MCP")

		bar := decode[mcptools.OutcomeBarOutput](t, callTool(t, csActor, "get_outcome_bar", mcptools.GetOutcomeBarInput{ChannelID: ch.ID.String()}))
		assert.True(t, bar.Configured)
		require.NotNil(t, bar.ThresholdValue)
		assert.Equal(t, threshold, *bar.ThresholdValue, "get_outcome_bar must reflect the threshold set on web in step 4")
	})
}

// TestE2E_M43_OutcomesResolveCompareConfigure is M4.3's own acceptance
// test (root plan #1924, this task issue #1931, milestone
// resolve-compare-configure): one Creator and one Analyst, each driving
// runM43MilestoneFlow's full resolve -> compare -> configure -> cross-
// check sequence on their OWN fresh Channel (cross-Channel isolation
// mirrors m3_calibration_test.go's own precedent, and keeps the two runs'
// calibration buckets/pending-match counts from interfering with each
// other) -- proving the milestone's shared Creator-or-Analyst authority by
// literally running the identical sequence twice under two different
// callers, not by inspecting a role check in isolation.
func TestE2E_M43_OutcomesResolveCompareConfigure(t *testing.T) {
	w := newWorld(t)
	ctx := w.ctx

	creator, _, err := w.st.Persons().UpsertByGoogleSubject(ctx, "sub-m43-creator", "m43-creator@example.com", "M4.3 Creator")
	require.NoError(t, err)
	analyst, _, err := w.st.Persons().UpsertByGoogleSubject(ctx, "sub-m43-analyst", "m43-analyst@example.com", "M4.3 Analyst")
	require.NoError(t, err)

	creatorCookie := w.establishSession(creator.ID)
	analystCookie := w.establishSession(analyst.ID)
	csCreator := w.mcpConnect(creator.ID)
	csAnalyst := w.mcpConnect(analyst.ID)

	t.Run("1_creator_full_loop", func(t *testing.T) {
		ch, err := w.st.Channels().Create(ctx, "yt-m43-creator-"+uuid.NewString(), "M4.3 Creator Channel", creator.ID)
		require.NoError(t, err)
		require.NoError(t, w.st.Roles().AddRole(ctx, ch.ID, analyst.ID, store.RoleAnalyst, creator.ID))
		w.runM43MilestoneFlow(t, "Creator", creatorCookie, csCreator, csCreator, ch)
	})

	// ── 6: authority parity -- the identical flow, as the Analyst ───────
	t.Run("2_analyst_full_loop_authority_parity", func(t *testing.T) {
		ch, err := w.st.Channels().Create(ctx, "yt-m43-analyst-"+uuid.NewString(), "M4.3 Analyst Channel", creator.ID)
		require.NoError(t, err)
		require.NoError(t, w.st.Roles().AddRole(ctx, ch.ID, analyst.ID, store.RoleAnalyst, creator.ID))
		w.runM43MilestoneFlow(t, "Analyst", analystCookie, csAnalyst, csCreator, ch)
	})
}
