//go:build integration

// Package citest_test (M3 calibration milestone): the milestone acceptance
// test for M3 (`outcome-bar-calibration`, root plan #1880, this task issue
// #1887) -- one Channel driven through *set a bar -> publish outcomes ->
// read a calibration trend* end to end over real MCP, real Postgres, and
// (for the candidate chain) the real sync worker, proving M3's outcome
// sentence itself: "a Creator or Analyst can see how well the system's
// predictions have tracked reality across every idea on a Channel, not
// just one at a time." Modelled directly on m2_1_video_script_test.go
// (this file's closest analogue -- a milestone test against the
// video_script chain) and e2e_test.go's harness/doc-comment conventions;
// candidate-chain construction reuses m2_1_video_script_test.go's
// setupVideoScriptChannel and this package's shared world helpers
// (mcpConnect, callTool, decode, runSyncCycle) rather than writing new
// fixture plumbing from scratch.
//
// Every set_outcome_bar/get_outcome_bar/get_calibration_trend call below
// goes through a real MCP client against the real in-process MCP server
// and real Postgres (never a direct store.OutcomeBarStore/
// CalibrationStore call) -- that trio is the actual tool under test here.
// Building the calibration CANDIDATES themselves -- the full post-M2.1
// chain (idea -> viable verdict -> video_script bound to that verdict
// version, greenlit -> live video_schedule_match -> synced_video with a
// published_at -> video_metrics) -- goes through save_video_script/
// greenlight_video_script over MCP plus the real worker/sync.
// ChannelSyncWorkflow (via w.runSyncCycle, exactly like
// m2_1_video_script_test.go's own step 1), with idea/verdict/strategy
// creation direct against the store (setupVideoScriptChannel's own
// precedent: that plumbing is not itself under test here). The three FR3
// exclusion fixtures (phase 5) are each built the ONE way that actually
// produces their excluded shape -- see notViableCalibrationScript's and
// publishNoMetrics's own doc comments for why each needs its own
// construction path.
//
// # FR/NFR coverage inventory
//
//   - FR1 (set_outcome_bar) -- phase 3 (Analyst write succeeds) and phase
//     9 (unsupported metric_name rejected, bar left intact).
//   - FR2 (get_outcome_bar not-configured) -- phase 2.
//   - FR3 (calibration-candidate filter: viable verdict, greenlit/archived
//     script bound to it, live match, published video, synced metrics) --
//     phase 5, by count; the not-viable-verdict, still-proposed, and
//     no-metrics fixtures are built in this file's setup section.
//   - FR4 (classify against the CURRENT bar, no historical snapshot) --
//     phase 6 (hand-computed classification) and phase 7 (reclassification
//     on a raised bar, same underlying data, no migration/backfill/second
//     write).
//   - FR5 (calendar-month bucketing, chronological) -- phase 6.
//   - FR6 (get_calibration_trend's not-configured result, before any bar
//     exists) -- phase 1.
//   - FR7 (limit/truncation, backward paging) -- phase 8.
//   - NFR1 (write convergence, no double count) -- phase 4.
//   - NFR2 (Analyst write authority + Creator/Analyst mutual visibility;
//     outsider rejected by all three tools) -- phase 3 (Analyst writes,
//     Creator reads it back) and phase 10 (outsider rejected).
//   - NFR3 (MCP-only surface, no web/CLI equivalent) and NFR4 (docs) --
//     not exercised here; covered by #1886 per this task's own Validation
//     section.
//
// Cross-Channel isolation (phase 11) and the neighbouring
// get_prediction_vs_outcome surface's non-regression (phase 12) round out
// the milestone-level claims that don't map onto a single FR/NFR number.
//
// Run it explicitly (requires a working Docker daemon):
//
//	bazel test //audience_score_system/citest:e2e_test --test_output=all
package citest_test

import (
	"fmt"
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

// assertBucketStart parses got (a bucket_start RFC3339 string) and asserts
// it names the same instant as want, comparing in UTC -- mirrors
// calibration_integration_test.go's own BucketStart.UTC().Equal(...)
// pattern (store package). A raw string comparison against a hardcoded
// "...Z" literal is NOT safe here: get_calibration_trend's
// CalibrationBucketOutput.Format(time.RFC3339) renders whatever zone
// pgx/Postgres hands back for a `timestamptz` column, which can be a
// non-UTC offset (e.g. "2023-12-31T19:00:00-05:00") that names the exact
// same instant as "2024-01-01T00:00:00Z" -- so comparing formatted strings
// directly is a false negative waiting to happen, not a real bug in the
// tool.
func assertBucketStart(t *testing.T, want time.Time, got string, msgAndArgs ...any) {
	t.Helper()
	parsed, err := time.Parse(time.RFC3339, got)
	require.NoError(t, err)
	if !want.Equal(parsed) {
		assert.Fail(t, fmt.Sprintf("bucket_start mismatch: want instant %s, got %s", want, parsed), msgAndArgs...)
	}
}

// viableVerdictAndStrategy creates a fresh Idea with a viable verdict and a
// Strategy bound to that verdict version on ch -- store-direct, exactly
// like setupVideoScriptChannel's own construction (see that helper's doc
// comment on why idea/verdict/strategy creation goes straight through the
// store rather than MCP). Each calibration candidate below needs its own
// independent Idea, not the single shared one setupVideoScriptChannel
// already returns.
func (w *world) viableVerdictAndStrategy(t *testing.T, ch store.Channel, creator store.Person, title string) (store.Verdict, uuid.UUID) {
	t.Helper()
	ctx := w.ctx

	idea, err := w.st.Ideas().Create(ctx, ch.ID, title, creator.ID)
	require.NoError(t, err)
	v, err := w.st.Verdicts().Append(ctx, store.AppendVerdictInput{
		IdeaID: idea.ID, Verdict: store.VerdictViable, Reasoning: title + " looks strong", AuthorPersonID: creator.ID,
	})
	require.NoError(t, err)
	strat, err := w.st.Strategies().Save(ctx, store.SaveStrategyInput{
		ChannelID: ch.ID, Title: title + " Strategy", Active: true,
		VerdictIDs: []uuid.UUID{v.ID}, CreatedByPersonID: creator.ID,
	})
	require.NoError(t, err)
	return v, strat.ID
}

// proposeAndGreenlightScript drives save_video_script then
// greenlight_video_script over real MCP (csAnalyst proposes, csCreator
// greenlights -- the same two-caller split every other file in this
// package uses), bound to verdictID/strategyID and targeted at
// publishedAt so the real matching algorithm (worker/sync/matching.go)
// scores an exact title + exact date match once w.runSyncCycle actually
// publishes it -- the same TargetPublishDate == actual publish time
// pattern m2_1_video_script_test.go's own step 1 uses to land an 'auto'
// match deterministically.
func (w *world) proposeAndGreenlightScript(t *testing.T, csAnalyst, csCreator *mcp.ClientSession, ch store.Channel, verdictID, strategyID uuid.UUID, title string, publishedAt time.Time) mcptools.VideoScriptOutput {
	t.Helper()

	script := decode[mcptools.VideoScriptOutput](t, callTool(t, csAnalyst, "save_video_script", mcptools.SaveVideoScriptInput{
		ChannelID: ch.ID.String(), VerdictID: verdictID.String(), StrategyID: strategyID.String(),
		Title: title, ScriptText: "script text for " + title,
		TargetPublishDate: publishedAt.Format(time.RFC3339), IdempotencyKeyArg: uuid.NewString(),
	}))
	greenlit := decode[mcptools.VideoScriptOutput](t, callTool(t, csCreator, "greenlight_video_script", mcptools.GreenlightVideoScriptInput{
		ChannelID: ch.ID.String(), VideoScriptID: script.VideoScriptID, IdempotencyKeyArg: uuid.NewString(),
	}))
	require.Equal(t, "greenlit", greenlit.Status)
	return greenlit
}

// notViableCalibrationScript builds an Idea with a not-viable verdict and a
// raw-inserted 'greenlit' video_script bound directly to it, via a raw SQL
// INSERT rather than VideoScriptStore.Propose (which rejects binding a
// script to a non-viable verdict outright, ErrVerdictNotViable/FR36) --
// mirrors calibration_integration_test.go's rawScriptOnNonViableVerdict
// (store package) but against this package's real w.pg.Pool: the only way
// to construct FR3a's exclusion fixture, and proof that
// get_calibration_trend's own vv.verdict = 'viable' predicate is real
// defense-in-depth over the wire, not merely an invariant enforced
// elsewhere. strategyID need only be a valid Strategy row on ch (the FK
// target) -- it does not need to be bound to verdict, so the Channel's own
// setupVideoScriptChannel strategy is reused rather than building another
// one. targetPublishDate is set explicitly (unlike the store package's
// version, which never runs this script through the real matching
// algorithm) so w.runSyncCycle's real matcher scores an exact-date match
// and lands it 'auto', just like every other candidate in this file.
func (w *world) notViableCalibrationScript(t *testing.T, ch store.Channel, strategyID uuid.UUID, creator store.Person, title string, targetPublishDate time.Time) store.VideoScript {
	t.Helper()
	ctx := w.ctx

	idea, err := w.st.Ideas().Create(ctx, ch.ID, title, creator.ID)
	require.NoError(t, err)
	v, err := w.st.Verdicts().Append(ctx, store.AppendVerdictInput{
		IdeaID: idea.ID, Verdict: store.VerdictNotViable, Reasoning: title + " reasoning", AuthorPersonID: creator.ID,
	})
	require.NoError(t, err)

	var scriptID uuid.UUID
	require.NoError(t, w.pg.Pool.QueryRow(ctx, `
		INSERT INTO video_script (channel_id, idea_id, verdict_id, strategy_id, title, script_text, status, target_publish_date, created_by_person_id)
		VALUES ($1, $2, $3, $4, $5, 'seed script text', 'greenlit', $6, $7)
		RETURNING id
	`, ch.ID, idea.ID, v.ID, strategyID, title, targetPublishDate, creator.ID).Scan(&scriptID))

	got, err := w.st.VideoScripts().GetByID(ctx, scriptID)
	require.NoError(t, err)
	return got
}

// publishNoMetrics publishes a synced video for scriptID and records a
// confirmed match to it WITHOUT ever writing a video_metrics row --
// FR3c's "no synced metrics yet" exclusion case. Driving this through the
// real worker/sync.Activities.SyncOutcomes (w.runSyncCycle) instead would
// always write a placeholder video_metrics row, never leave the table
// with zero rows for this video: youtube/fake.Client.Metrics' documented
// "missing data for one video is not an error for the whole request"
// fallback returns a zero-valued (but present) youtube.VideoMetrics for
// any requested video ID with no MetricsByVideoID entry, and
// SyncOutcomes.syncMetrics upserts every result it gets back regardless.
// So this fixture is built directly against the store instead -- mirrors
// calibration_integration_test.go's publishAndMatchNoMetrics (store
// package); it is a setup step, not the tool under test.
func (w *world) publishNoMetrics(t *testing.T, ch store.Channel, scriptID uuid.UUID, title string, publishedAt time.Time) {
	t.Helper()
	ctx := w.ctx

	ytID := "yt-m3-nometrics-" + uuid.NewString()
	require.NoError(t, w.st.Sync().UpsertVideos(ctx, ch.ID, []store.SyncedVideo{{
		YouTubeVideoID: ytID, Title: title,
		PrivacyStatus: store.PrivacyStatusPublic, PublishedAt: &publishedAt, LastSyncedAt: time.Now(),
	}}))
	synced, _, err := w.st.Sync().ListSchedule(ctx, ch.ID, nil, nil, true, 0)
	require.NoError(t, err)
	var syncedID uuid.UUID
	for _, sv := range synced {
		if sv.YouTubeVideoID == ytID {
			syncedID = sv.ID
			break
		}
	}
	require.NotEqual(t, uuid.Nil, syncedID, "the just-synced video must be found by its unique YouTube id")

	require.NoError(t, w.st.Matches().Record(ctx, store.VideoScheduleMatch{
		SyncedVideoID: syncedID, VideoScriptID: &scriptID, Confidence: 0.9, State: store.MatchStateConfirmed,
	}))
}

// TestE2E_M3_CalibrationTrend is M3's own end-to-end acceptance test
// (issue #1887, milestone outcome-bar-calibration): one continuous
// scenario on a single Channel (plus a second Channel for phase 11's
// isolation check), sharing this function's Creator/Co-Creator/Analyst/
// Outsider Persons and MCP sessions across every phase below -- mirrored
// from TestE2E_ThreeLoopsEndToEnd's (e2e_test.go) own sequential,
// state-sharing t.Run structure, since this file's phases build on each
// other's bar/candidate state in order rather than each getting a fresh
// Channel the way TestE2E_M21_VideoScriptMilestone's independent scenarios
// do.
func TestE2E_M3_CalibrationTrend(t *testing.T) {
	w := newWorld(t)
	ctx := w.ctx

	creator, _, err := w.st.Persons().UpsertByGoogleSubject(ctx, "sub-m3-creator", "m3-creator@example.com", "M3 Creator")
	require.NoError(t, err)
	coCreator, _, err := w.st.Persons().UpsertByGoogleSubject(ctx, "sub-m3-cocreator", "m3-cocreator@example.com", "M3 Co-Creator")
	require.NoError(t, err)
	analyst, _, err := w.st.Persons().UpsertByGoogleSubject(ctx, "sub-m3-analyst", "m3-analyst@example.com", "M3 Analyst")
	require.NoError(t, err)
	outsider, _, err := w.st.Persons().UpsertByGoogleSubject(ctx, "sub-m3-outsider", "m3-outsider@example.com", "M3 Outsider")
	require.NoError(t, err)

	vsc := w.setupVideoScriptChannel(t, creator, coCreator, analyst, "M3 Calibration")
	csCreator := w.mcpConnect(creator.ID)
	csAnalyst := w.mcpConnect(analyst.ID)
	csOutsider := w.mcpConnect(outsider.ID)

	const (
		lowThreshold  = 1000.0
		highThreshold = 2000.0
	)

	jan10 := time.Date(2024, time.January, 10, 12, 0, 0, 0, time.UTC)
	jan20 := time.Date(2024, time.January, 20, 12, 0, 0, 0, time.UTC)
	feb10 := time.Date(2024, time.February, 10, 12, 0, 0, 0, time.UTC)
	mar05 := time.Date(2024, time.March, 5, 12, 0, 0, 0, time.UTC)

	// ── Seed every candidate (real + FR3 exclusions) BEFORE any bar exists
	// -- phase 1 depends on candidates already being present while the
	// Channel is still unconfigured. ──────────────────────────────────────
	fc := &fake.Client{MetricsByVideoID: map[string]youtube.VideoMetrics{}}
	seedCandidate := func(title string, publishedAt time.Time, views int64) {
		v, stID := w.viableVerdictAndStrategy(t, vsc.ch, creator, title)
		w.proposeAndGreenlightScript(t, csAnalyst, csCreator, vsc.ch, v.ID, stID, title, publishedAt)
		ytID := "yt-m3-" + uuid.NewString()
		fc.Schedule = append(fc.Schedule, youtube.Video{
			YouTubeVideoID: ytID, Title: title, PrivacyStatus: store.PrivacyStatusPublic, PublishedAt: &publishedAt,
		})
		fc.MetricsByVideoID[ytID] = youtube.VideoMetrics{YouTubeVideoID: ytID, Views: &views, MeasuredAt: publishedAt.Add(time.Hour)}
	}

	// The 4 true calibration candidates (FR3), spread over three calendar
	// months (January x2, February, March -- FR5), split roughly evenly
	// around lowThreshold=1000 (FR1's "threshold chosen so the seeded
	// candidates split") -- see phase 6/7's hand-computed expectations.
	seedCandidate("M3 Jan Above", jan10, 2500) // calibrated at both 1000 and 2000
	seedCandidate("M3 Jan Below", jan20, 400)  // miscalibrated at both
	seedCandidate("M3 Feb Above", feb10, 1800) // calibrated at 1000, miscalibrated at 2000 -- phase 7's reclassification
	seedCandidate("M3 Mar Below", mar05, 100)  // miscalibrated at both

	// FR3a: not-viable verdict, identical downstream chain -- must never
	// count, in particular never as miscalibrated.
	notViableDate := time.Date(2024, time.January, 15, 12, 0, 0, 0, time.UTC)
	notViable := w.notViableCalibrationScript(t, vsc.ch, vsc.strategy.ID, creator, "M3 Not Viable", notViableDate)
	notViableViews := int64(9999)
	fc.Schedule = append(fc.Schedule, youtube.Video{
		YouTubeVideoID: "yt-m3-not-viable", Title: notViable.Title, PrivacyStatus: store.PrivacyStatusPublic, PublishedAt: &notViableDate,
	})
	fc.MetricsByVideoID["yt-m3-not-viable"] = youtube.VideoMetrics{YouTubeVideoID: "yt-m3-not-viable", Views: &notViableViews, MeasuredAt: notViableDate.Add(time.Hour)}

	// FR3b: viable verdict, script left 'proposed' -- never greenlit, never
	// synced, never in fc.Schedule at all -- must never count, "not even as
	// miscalibrated" (the milestone's own emphasis).
	vProposed, stProposed := w.viableVerdictAndStrategy(t, vsc.ch, creator, "M3 Proposed Unpublished")
	proposedOnly := decode[mcptools.VideoScriptOutput](t, callTool(t, csAnalyst, "save_video_script", mcptools.SaveVideoScriptInput{
		ChannelID: vsc.ch.ID.String(), VerdictID: vProposed.ID.String(), StrategyID: stProposed.String(),
		Title: "M3 Proposed Unpublished", ScriptText: "never greenlit, never synced", IdempotencyKeyArg: uuid.NewString(),
	}))
	require.Equal(t, "proposed", proposedOnly.Status)

	// Run ONE real sync cycle -- the genuine worker/sync.ChannelSyncWorkflow,
	// via Temporal's testsuite -- covering every real candidate above and
	// the not-viable one: schedule sync, outcome/metrics sync, and the real
	// title+date matching algorithm auto-linking each (never for the
	// still-proposed script, which was never added to fc.Schedule, and
	// never for the no-metrics candidate below, built directly against the
	// store instead -- see publishNoMetrics's doc comment for why).
	_, err = w.runSyncCycle(vsc.ch.ID, fc)
	require.NoError(t, err)

	// FR3c: a published video with a live match but genuinely NO
	// video_metrics row -- must never count.
	vNoMetrics, stNoMetrics := w.viableVerdictAndStrategy(t, vsc.ch, creator, "M3 No Metrics")
	noMetricsScript := w.proposeAndGreenlightScript(t, csAnalyst, csCreator, vsc.ch, vNoMetrics.ID, stNoMetrics, "M3 No Metrics", mar05)
	w.publishNoMetrics(t, vsc.ch, uuid.MustParse(noMetricsScript.VideoScriptID), "M3 No Metrics", mar05)

	// Sanity check before any calibration assertion below: every candidate
	// synced through the real matcher above landed 'auto', not 'pending' --
	// otherwise phase 6's hand-computed expectations would be meaningless.
	pending := decode[mcptools.ListPendingMatchesOutput](t, callTool(t, csCreator, "list_pending_matches", mcptools.ListPendingMatchesInput{ChannelID: vsc.ch.ID.String()}))
	require.Empty(t, pending.Matches, "every real/not-viable candidate above was built with an exact title+date match -- none should have landed pending")

	// ── 1: FR6 -- unconfigured trend, before any bar, with candidates already seeded ──
	t.Run("1_fr6_unconfigured_trend_before_any_bar_succeeds", func(t *testing.T) {
		res := callTool(t, csCreator, "get_calibration_trend", mcptools.GetCalibrationTrendInput{ChannelID: vsc.ch.ID.String()})
		require.False(t, res.IsError, "FR6: not-configured is a successful response, never an error: %s", textOf(res))
		out := decode[mcptools.GetCalibrationTrendOutput](t, res)
		assert.False(t, out.OutcomeBar.Configured, "FR6: no outcome bar has ever been set for this Channel")
		assert.Empty(t, out.Buckets, "no buckets computed while unconfigured, even though candidates already exist")
		assert.False(t, out.Truncated)
	})

	// ── 2: FR2 -- unconfigured get_outcome_bar ──────────────────────────
	t.Run("2_fr2_unconfigured_get_outcome_bar_succeeds", func(t *testing.T) {
		res := callTool(t, csCreator, "get_outcome_bar", mcptools.GetOutcomeBarInput{ChannelID: vsc.ch.ID.String()})
		require.False(t, res.IsError, "FR2: not-configured is a successful response, never an error: %s", textOf(res))
		out := decode[mcptools.OutcomeBarOutput](t, res)
		assert.Equal(t, mcptools.OutcomeBarOutput{Configured: false}, out)
	})

	// ── 3: FR1 + NFR2 -- Analyst writes the bar, Creator reads it back ──
	t.Run("3_fr1_nfr2_analyst_sets_bar_creator_reads_it_back", func(t *testing.T) {
		setRes := callTool(t, csAnalyst, "set_outcome_bar", mcptools.SetOutcomeBarInput{
			ChannelID: vsc.ch.ID.String(), MetricName: store.OutcomeBarMetricViews, ThresholdValue: lowThreshold,
		})
		require.False(t, setRes.IsError, "FR1/NFR2: an Analyst -- not just the Creator -- must be able to set the outcome bar: %s", textOf(setRes))
		analystSet := decode[mcptools.OutcomeBarOutput](t, setRes)
		assert.True(t, analystSet.Configured)
		assert.Equal(t, store.OutcomeBarMetricViews, analystSet.MetricName)
		require.NotNil(t, analystSet.ThresholdValue)
		assert.Equal(t, lowThreshold, *analystSet.ThresholdValue)
		assert.Equal(t, analyst.ID.String(), analystSet.UpdatedByPersonID)

		creatorRead := decode[mcptools.OutcomeBarOutput](t, callTool(t, csCreator, "get_outcome_bar", mcptools.GetOutcomeBarInput{ChannelID: vsc.ch.ID.String()}))
		assert.Equal(t, analystSet, creatorRead, "NFR2 mutual visibility: the Creator must see exactly what the Analyst just set")
	})

	// ── 4: NFR1 -- identical repeat write converges, no double count ───
	t.Run("4_nfr1_identical_second_call_converges_no_double_count", func(t *testing.T) {
		before := decode[mcptools.GetCalibrationTrendOutput](t, callTool(t, csCreator, "get_calibration_trend", mcptools.GetCalibrationTrendInput{ChannelID: vsc.ch.ID.String()}))

		second := decode[mcptools.OutcomeBarOutput](t, callTool(t, csAnalyst, "set_outcome_bar", mcptools.SetOutcomeBarInput{
			ChannelID: vsc.ch.ID.String(), MetricName: store.OutcomeBarMetricViews, ThresholdValue: lowThreshold,
		}))
		got := decode[mcptools.OutcomeBarOutput](t, callTool(t, csCreator, "get_outcome_bar", mcptools.GetOutcomeBarInput{ChannelID: vsc.ch.ID.String()}))
		assert.Equal(t, second, got, "NFR1: an identical repeat call converges -- get_outcome_bar must be unchanged")

		after := decode[mcptools.GetCalibrationTrendOutput](t, callTool(t, csCreator, "get_calibration_trend", mcptools.GetCalibrationTrendInput{ChannelID: vsc.ch.ID.String()}))
		assert.Equal(t, before, after, "NFR1: the trend must not double-count after an identical repeat write")
	})

	// ── 5: FR3 -- candidate filter excludes all three fixtures, by count ──
	t.Run("5_fr3_candidate_filter_excludes_not_viable_unpublished_and_no_metrics", func(t *testing.T) {
		out := decode[mcptools.GetCalibrationTrendOutput](t, callTool(t, csCreator, "get_calibration_trend", mcptools.GetCalibrationTrendInput{ChannelID: vsc.ch.ID.String()}))
		var totalCandidates int
		for _, b := range out.Buckets {
			totalCandidates += b.Candidates
		}
		assert.Equal(t, 4, totalCandidates,
			"FR3: only the 4 true candidates count -- the not-viable-verdict script, the still-proposed unpublished "+
				"script, and the published-but-no-metrics video must all be excluded, the unpublished-but-viable idea "+
				"included")
	})

	// ── 6: FR4 + FR5 -- monthly buckets, chronological, hand-computed ───
	t.Run("6_fr4_fr5_monthly_buckets_chronological_hand_computed", func(t *testing.T) {
		out := decode[mcptools.GetCalibrationTrendOutput](t, callTool(t, csCreator, "get_calibration_trend", mcptools.GetCalibrationTrendInput{ChannelID: vsc.ch.ID.String()}))
		require.Len(t, out.Buckets, 3, "January, February, March -- one bucket per calendar month with a candidate")

		jan, feb, mar := out.Buckets[0], out.Buckets[1], out.Buckets[2]
		assertBucketStart(t, time.Date(2024, time.January, 1, 0, 0, 0, 0, time.UTC), jan.BucketStart)
		assertBucketStart(t, time.Date(2024, time.February, 1, 0, 0, 0, 0, time.UTC), feb.BucketStart)
		assertBucketStart(t, time.Date(2024, time.March, 1, 0, 0, 0, 0, time.UTC), mar.BucketStart)

		assert.Equal(t, 2, jan.Candidates)
		assert.Equal(t, 1, jan.Calibrated, "2500 clears 1000, 400 does not")
		assert.Equal(t, 1, jan.Miscalibrated)
		assert.InDelta(t, 0.5, jan.CalibrationRate, 1e-9)

		assert.Equal(t, 1, feb.Candidates)
		assert.Equal(t, 1, feb.Calibrated, "1800 clears 1000")
		assert.Equal(t, 0, feb.Miscalibrated)
		assert.InDelta(t, 1.0, feb.CalibrationRate, 1e-9)

		assert.Equal(t, 1, mar.Candidates)
		assert.Equal(t, 0, mar.Calibrated, "100 does not clear 1000")
		assert.Equal(t, 1, mar.Miscalibrated)
		assert.InDelta(t, 0.0, mar.CalibrationRate, 1e-9)
	})

	// ── 7: FR4 -- raising the bar reclassifies, no migration/second write ──
	t.Run("7_fr4_raising_bar_reclassifies_with_no_migration_or_second_write", func(t *testing.T) {
		setRes := callTool(t, csCreator, "set_outcome_bar", mcptools.SetOutcomeBarInput{
			ChannelID: vsc.ch.ID.String(), MetricName: store.OutcomeBarMetricViews, ThresholdValue: highThreshold,
		})
		require.False(t, setRes.IsError)

		out := decode[mcptools.GetCalibrationTrendOutput](t, callTool(t, csCreator, "get_calibration_trend", mcptools.GetCalibrationTrendInput{ChannelID: vsc.ch.ID.String()}))
		require.Len(t, out.Buckets, 3)
		jan, feb, mar := out.Buckets[0], out.Buckets[1], out.Buckets[2]

		assert.Equal(t, 1, jan.Calibrated, "2500 views still clears the raised 2000 threshold")
		assert.Equal(t, 0, feb.Calibrated, "1800 views no longer clears the raised 2000 threshold -- FR4 reclassifies with no migration/backfill")
		assert.Equal(t, 0, mar.Calibrated)
		assert.Equal(t, 4, jan.Candidates+feb.Candidates+mar.Candidates, "the candidate COUNT itself never changes with the bar")
	})

	// ── 8: FR7 -- truncation and backward paging ─────────────────────────
	t.Run("8_fr7_truncation_and_backward_paging", func(t *testing.T) {
		limited := decode[mcptools.GetCalibrationTrendOutput](t, callTool(t, csCreator, "get_calibration_trend", mcptools.GetCalibrationTrendInput{ChannelID: vsc.ch.ID.String(), Limit: 2}))
		assert.True(t, limited.Truncated, "3 months exist, limit 2 must report truncation")
		require.Len(t, limited.Buckets, 2)
		assertBucketStart(t, time.Date(2024, time.February, 1, 0, 0, 0, 0, time.UTC), limited.Buckets[0].BucketStart, "the two MOST RECENT months, kept chronological between themselves")
		assertBucketStart(t, time.Date(2024, time.March, 1, 0, 0, 0, 0, time.UTC), limited.Buckets[1].BucketStart)

		oldestReturned, err := time.Parse(time.RFC3339, limited.Buckets[0].BucketStart)
		require.NoError(t, err)
		older := decode[mcptools.GetCalibrationTrendOutput](t, callTool(t, csCreator, "get_calibration_trend", mcptools.GetCalibrationTrendInput{ChannelID: vsc.ch.ID.String(), Before: &oldestReturned}))
		require.Len(t, older.Buckets, 1, "paging backward past the truncated response with before=oldest-returned-bucket_start must surface January")
		assertBucketStart(t, time.Date(2024, time.January, 1, 0, 0, 0, 0, time.UTC), older.Buckets[0].BucketStart)
		assert.False(t, older.Truncated, "nothing older than January exists")
	})

	// ── 9: FR1 -- unsupported metric_name rejected, bar left intact ─────
	t.Run("9_fr1_unsupported_metric_rejected_bar_intact", func(t *testing.T) {
		before := decode[mcptools.OutcomeBarOutput](t, callTool(t, csCreator, "get_outcome_bar", mcptools.GetOutcomeBarInput{ChannelID: vsc.ch.ID.String()}))

		res := callTool(t, csCreator, "set_outcome_bar", mcptools.SetOutcomeBarInput{
			ChannelID: vsc.ch.ID.String(), MetricName: "ctr", ThresholdValue: 42,
		})
		assert.True(t, res.IsError, "FR1: only \"views\" is accepted in this milestone")

		after := decode[mcptools.OutcomeBarOutput](t, callTool(t, csCreator, "get_outcome_bar", mcptools.GetOutcomeBarInput{ChannelID: vsc.ch.ID.String()}))
		assert.Equal(t, before, after, "the rejected call must leave the previously configured bar untouched")
	})

	// ── 10: NFR2 -- a Person with no open role is rejected by all three ──
	t.Run("10_nfr2_person_with_no_role_rejected_by_all_three_tools", func(t *testing.T) {
		setRes := callTool(t, csOutsider, "set_outcome_bar", mcptools.SetOutcomeBarInput{
			ChannelID: vsc.ch.ID.String(), MetricName: store.OutcomeBarMetricViews, ThresholdValue: 1,
		})
		assert.True(t, setRes.IsError, "a Person with no open role must be rejected by set_outcome_bar")

		getRes := callTool(t, csOutsider, "get_outcome_bar", mcptools.GetOutcomeBarInput{ChannelID: vsc.ch.ID.String()})
		assert.True(t, getRes.IsError, "a Person with no open role must be rejected by get_outcome_bar")

		trendRes := callTool(t, csOutsider, "get_calibration_trend", mcptools.GetCalibrationTrendInput{ChannelID: vsc.ch.ID.String()})
		assert.True(t, trendRes.IsError, "a Person with no open role must be rejected by get_calibration_trend")

		stillConfigured := decode[mcptools.OutcomeBarOutput](t, callTool(t, csCreator, "get_outcome_bar", mcptools.GetOutcomeBarInput{ChannelID: vsc.ch.ID.String()}))
		assert.Equal(t, highThreshold, *stillConfigured.ThresholdValue, "the outsider's rejected write attempt must not have changed anything")
	})

	// ── 11: cross-Channel isolation -- an independent bar and trend ─────
	t.Run("11_cross_channel_isolation_independent_trend", func(t *testing.T) {
		vscB := w.setupVideoScriptChannel(t, creator, coCreator, analyst, "M3 Calibration Channel B")

		vB, stB := w.viableVerdictAndStrategy(t, vscB.ch, creator, "M3 Channel B Candidate")
		novDate := time.Date(2024, time.November, 12, 12, 0, 0, 0, time.UTC)
		w.proposeAndGreenlightScript(t, csAnalyst, csCreator, vscB.ch, vB.ID, stB, "M3 Channel B Candidate", novDate)

		views := int64(5000)
		fcB := &fake.Client{
			Schedule: []youtube.Video{{
				YouTubeVideoID: "yt-m3b-candidate", Title: "M3 Channel B Candidate",
				PrivacyStatus: store.PrivacyStatusPublic, PublishedAt: &novDate,
			}},
			MetricsByVideoID: map[string]youtube.VideoMetrics{
				"yt-m3b-candidate": {YouTubeVideoID: "yt-m3b-candidate", Views: &views, MeasuredAt: novDate.Add(time.Hour)},
			},
		}
		_, err := w.runSyncCycle(vscB.ch.ID, fcB)
		require.NoError(t, err)

		setRes := callTool(t, csCreator, "set_outcome_bar", mcptools.SetOutcomeBarInput{
			ChannelID: vscB.ch.ID.String(), MetricName: store.OutcomeBarMetricViews, ThresholdValue: lowThreshold,
		})
		require.False(t, setRes.IsError, "body: %s", textOf(setRes))

		trendB := decode[mcptools.GetCalibrationTrendOutput](t, callTool(t, csCreator, "get_calibration_trend", mcptools.GetCalibrationTrendInput{ChannelID: vscB.ch.ID.String()}))
		require.Len(t, trendB.Buckets, 1, "Channel B's own single November candidate, independent of Channel A's three months")
		assert.Equal(t, 1, trendB.Buckets[0].Candidates, "Channel A's candidates must never leak into Channel B's trend")
		assert.Equal(t, 1, trendB.Buckets[0].Calibrated)

		trendA := decode[mcptools.GetCalibrationTrendOutput](t, callTool(t, csCreator, "get_calibration_trend", mcptools.GetCalibrationTrendInput{ChannelID: vsc.ch.ID.String()}))
		require.Len(t, trendA.Buckets, 3, "Channel A's trend must be unaffected by Channel B's candidate/bar")
		require.NotNil(t, trendA.OutcomeBar.ThresholdValue)
		assert.Equal(t, highThreshold, *trendA.OutcomeBar.ThresholdValue, "Channel A's own bar must be untouched by Channel B's set_outcome_bar call")
	})

	// ── 12: no regression -- get_prediction_vs_outcome still per-idea ───
	t.Run("12_no_regression_get_prediction_vs_outcome_still_returns_per_idea_rows", func(t *testing.T) {
		cmp := decode[mcptools.GetPredictionVsOutcomeOutput](t, callTool(t, csCreator, "get_prediction_vs_outcome", mcptools.GetPredictionVsOutcomeInput{ChannelID: vsc.ch.ID.String()}))
		require.Len(t, cmp.Rows, 5,
			"C14 complements C10, it does not replace it: get_prediction_vs_outcome still returns every published+matched "+
				"idea, INCLUDING the not-viable one calibration excludes (4 real candidates + the not-viable fixture)")

		var titles []string
		for _, r := range cmp.Rows {
			titles = append(titles, r.IdeaTitle)
		}
		assert.Contains(t, titles, "M3 Not Viable", "get_prediction_vs_outcome does not filter on verdict viability -- only calibration does")
		assert.NotContains(t, titles, "M3 Proposed Unpublished", "an unpublished script never reaches this comparison either")
		assert.NotContains(t, titles, "M3 No Metrics", "a video with no metrics snapshot never reaches this comparison either")
	})
}
