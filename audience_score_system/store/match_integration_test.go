//go:build integration

// match_integration_test.go covers MatchStore.ListCandidates and
// MatchStore.Record's `video_script` re-anchor (FR43, issue #1829): the
// `greenlit`-only candidate-generation restriction, Channel scoping, the
// "no existing live match" filter, and Record persisting video_script_id
// rather than schedule_entry_id. Split into its own file rather than
// growing store_integration_test.go further (already large before this
// milestone) or video_script_integration_test.go (that file's scope is
// VideoScriptStore itself, not MatchStore) -- same package/build
// tag/harness, so newStore/setupChannel/ptrTime
// (store_integration_test.go) and setupVideoScriptChannel/
// proposeVideoScript (video_script_integration_test.go) are reused
// directly. MatchStore.Resolve's video_script re-anchor (FR44) is #1830's
// scope, not this file's -- see store_integration_test.go's existing
// Resolve tests, which are deliberately left untouched here.
package store_test

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/whale-net/everything/audience_score_system/store"
)

// greenlitVideoScript builds a full Idea -> viable Verdict -> Strategy ->
// Propose -> Greenlight chain directly on ch (FR36/FR37) -- deliberately
// NOT setupVideoScriptChannel (video_script_integration_test.go), which
// creates its own fresh Channel per call: ListCandidates/Record tests
// below need every script fixture on the exact same Channel they query
// against. Returns the greenlit VideoScript -- the shared starting point
// for every ListCandidates test below that needs an actual candidate.
func greenlitVideoScript(t *testing.T, ctx context.Context, s *store.Store, ch store.Channel, creator store.Person, title string) store.VideoScript {
	t.Helper()

	idea, err := s.Ideas().Create(ctx, ch.ID, title, creator.ID)
	require.NoError(t, err)
	v, err := s.Verdicts().Append(ctx, store.AppendVerdictInput{
		IdeaID: idea.ID, Verdict: store.VerdictViable, Reasoning: title + " looks strong", AuthorPersonID: creator.ID,
	})
	require.NoError(t, err)
	st, err := s.Strategies().Save(ctx, store.SaveStrategyInput{
		ChannelID: ch.ID, Title: title + " Strategy", Active: true,
		VerdictIDs: []uuid.UUID{v.ID}, CreatedByPersonID: creator.ID,
	})
	require.NoError(t, err)

	script, err := s.VideoScripts().Propose(ctx, store.ProposeVideoScriptInput{
		ChannelID: ch.ID, VerdictID: v.ID, StrategyID: st.ID,
		Title: title, ScriptText: "script text for " + title, CreatedByPersonID: creator.ID,
	})
	require.NoError(t, err)
	require.NoError(t, s.VideoScripts().Greenlight(ctx, script.ID, creator.ID))

	got, err := s.VideoScripts().GetByID(ctx, script.ID)
	require.NoError(t, err)
	return got
}

// proposedVideoScript is greenlitVideoScript's counterpart left in status
// 'proposed' -- for the "never a candidate" cases.
func proposedVideoScript(t *testing.T, ctx context.Context, s *store.Store, ch store.Channel, creator store.Person, title string) store.VideoScript {
	t.Helper()

	idea, err := s.Ideas().Create(ctx, ch.ID, title, creator.ID)
	require.NoError(t, err)
	v, err := s.Verdicts().Append(ctx, store.AppendVerdictInput{
		IdeaID: idea.ID, Verdict: store.VerdictViable, Reasoning: title + " looks strong", AuthorPersonID: creator.ID,
	})
	require.NoError(t, err)
	st, err := s.Strategies().Save(ctx, store.SaveStrategyInput{
		ChannelID: ch.ID, Title: title + " Strategy", Active: true,
		VerdictIDs: []uuid.UUID{v.ID}, CreatedByPersonID: creator.ID,
	})
	require.NoError(t, err)

	script, err := s.VideoScripts().Propose(ctx, store.ProposeVideoScriptInput{
		ChannelID: ch.ID, VerdictID: v.ID, StrategyID: st.ID,
		Title: title, ScriptText: "script text for " + title, CreatedByPersonID: creator.ID,
	})
	require.NoError(t, err)
	return script
}

// ── MatchStore.ListCandidates (FR43, issue #1829) ───────────────────────────

func TestMatchStore_ListCandidates_OnlyGreenlitScripts(t *testing.T) {
	ctx := context.Background()
	s, _ := newStore(t)
	ch, creator := setupChannel(t, ctx, s)

	greenlit := greenlitVideoScript(t, ctx, s, ch, creator, "Greenlit Candidate")
	proposed := proposedVideoScript(t, ctx, s, ch, creator, "Proposed Not Candidate")

	denied := proposedVideoScript(t, ctx, s, ch, creator, "Denied Not Candidate")
	require.NoError(t, s.VideoScripts().Deny(ctx, denied.ID, creator.ID))

	archived := greenlitVideoScript(t, ctx, s, ch, creator, "Archived Not Candidate")
	require.NoError(t, s.VideoScripts().Archive(ctx, archived.ID, creator.ID))

	candidates, err := s.Matches().ListCandidates(ctx, ch.ID)
	require.NoError(t, err)

	ids := make(map[uuid.UUID]bool, len(candidates))
	for _, c := range candidates {
		ids[c.VideoScriptID] = true
	}
	assert.True(t, ids[greenlit.ID], "a greenlit script must be a candidate")
	assert.False(t, ids[proposed.ID], "a proposed (not yet decided) script must never be a candidate")
	assert.False(t, ids[denied.ID], "a denied script must never be a candidate")
	assert.False(t, ids[archived.ID], "an archived script must never be a candidate")
}

func TestMatchStore_ListCandidates_OnlyForQueriedChannel(t *testing.T) {
	ctx := context.Background()
	s, _ := newStore(t)
	chA, creatorA := setupChannel(t, ctx, s)
	chB, creatorB := setupChannel(t, ctx, s)

	inA := greenlitVideoScript(t, ctx, s, chA, creatorA, "On Channel A")
	inB := greenlitVideoScript(t, ctx, s, chB, creatorB, "On Channel B")

	candidatesA, err := s.Matches().ListCandidates(ctx, chA.ID)
	require.NoError(t, err)
	idsA := make(map[uuid.UUID]bool, len(candidatesA))
	for _, c := range candidatesA {
		idsA[c.VideoScriptID] = true
	}
	assert.True(t, idsA[inA.ID], "Channel A's own candidate must be returned")
	assert.False(t, idsA[inB.ID], "Channel B's candidate must never leak into Channel A's candidate pool")
}

func TestMatchStore_ListCandidates_ExcludesScriptsWithLiveMatch(t *testing.T) {
	ctx := context.Background()
	s, _ := newStore(t)
	ch, creator := setupChannel(t, ctx, s)

	stillCandidate := greenlitVideoScript(t, ctx, s, ch, creator, "Still A Candidate")
	autoMatched := greenlitVideoScript(t, ctx, s, ch, creator, "Auto Matched Already")
	confirmedMatched := greenlitVideoScript(t, ctx, s, ch, creator, "Confirmed Matched Already")
	pendingMatched := greenlitVideoScript(t, ctx, s, ch, creator, "Pending Matched Still Candidate")

	videoFor := func(youtubeVideoID string) store.SyncedVideo {
		require.NoError(t, s.Sync().UpsertVideos(ctx, ch.ID, []store.SyncedVideo{{
			YouTubeVideoID: youtubeVideoID, Title: "A Video",
			PrivacyStatus: store.PrivacyStatusPublic, PublishedAt: ptrTime(time.Now()), LastSyncedAt: time.Now(),
		}}))
		rows, _, err := s.Sync().ListSchedule(ctx, ch.ID, nil, nil, true, 0)
		require.NoError(t, err)
		for _, r := range rows {
			if r.YouTubeVideoID == youtubeVideoID {
				return r
			}
		}
		t.Fatalf("synced_video %s not found", youtubeVideoID)
		return store.SyncedVideo{}
	}

	autoVideo := videoFor("yt-cand-auto-" + uuid.NewString())
	require.NoError(t, s.Matches().Record(ctx, store.VideoScheduleMatch{
		SyncedVideoID: autoVideo.ID, VideoScriptID: &autoMatched.ID, Confidence: 0.95, State: store.MatchStateAuto,
	}))

	confirmedVideo := videoFor("yt-cand-confirmed-" + uuid.NewString())
	require.NoError(t, s.Matches().Record(ctx, store.VideoScheduleMatch{
		SyncedVideoID: confirmedVideo.ID, VideoScriptID: &confirmedMatched.ID, Confidence: 0.5, State: store.MatchStatePending,
	}))
	pending, _, err := s.Matches().ListPending(ctx, ch.ID, nil, 0)
	require.NoError(t, err)
	var confirmedMatchID uuid.UUID
	for _, m := range pending {
		if m.VideoScriptID != nil && *m.VideoScriptID == confirmedMatched.ID {
			confirmedMatchID = m.ID
		}
	}
	require.NotEqual(t, uuid.Nil, confirmedMatchID)
	require.NoError(t, s.Matches().Resolve(ctx, confirmedMatchID, creator.ID, true, nil))

	pendingVideo := videoFor("yt-cand-pending-" + uuid.NewString())
	require.NoError(t, s.Matches().Record(ctx, store.VideoScheduleMatch{
		SyncedVideoID: pendingVideo.ID, VideoScriptID: &pendingMatched.ID, Confidence: 0.5, State: store.MatchStatePending,
	}))

	candidates, err := s.Matches().ListCandidates(ctx, ch.ID)
	require.NoError(t, err)
	ids := make(map[uuid.UUID]bool, len(candidates))
	for _, c := range candidates {
		ids[c.VideoScriptID] = true
	}
	assert.True(t, ids[stillCandidate.ID], "a greenlit script with no match at all must remain a candidate")
	assert.False(t, ids[autoMatched.ID], "a script with a live 'auto' match must not be offered as a candidate again")
	assert.False(t, ids[confirmedMatched.ID], "a script with a live 'confirmed' match must not be offered as a candidate again")
	assert.True(t, ids[pendingMatched.ID], "a script whose only match is 'pending' (not yet live/settled) must remain a candidate")
}

// ── MatchStore.Record (FR43, issue #1829) ───────────────────────────────────

func TestMatchStore_Record_PersistsVideoScriptID(t *testing.T) {
	ctx := context.Background()
	s, db := newStore(t)
	ch, creator := setupChannel(t, ctx, s)

	script := greenlitVideoScript(t, ctx, s, ch, creator, "Recorded Script")

	require.NoError(t, s.Sync().UpsertVideos(ctx, ch.ID, []store.SyncedVideo{{
		YouTubeVideoID: "yt-record-" + uuid.NewString(), Title: "Recorded Video",
		PrivacyStatus: store.PrivacyStatusPublic, PublishedAt: ptrTime(time.Now()), LastSyncedAt: time.Now(),
	}}))
	synced, _, err := s.Sync().ListSchedule(ctx, ch.ID, nil, nil, true, 0)
	require.NoError(t, err)
	require.Len(t, synced, 1)
	video := synced[0]

	require.NoError(t, s.Matches().Record(ctx, store.VideoScheduleMatch{
		SyncedVideoID: video.ID, VideoScriptID: &script.ID, Confidence: 0.9, State: store.MatchStateAuto,
	}))

	var id uuid.UUID
	require.NoError(t, db.Pool.QueryRow(ctx, `SELECT id FROM video_schedule_match WHERE synced_video_id = $1`, video.ID).Scan(&id))
	got, err := s.Matches().GetByID(ctx, id)
	require.NoError(t, err)
	require.NotNil(t, got.VideoScriptID)
	assert.Equal(t, script.ID, *got.VideoScriptID, "Record must persist video_script_id")
}
