//go:build integration

// thread_integration_test.go covers ThreadStore (migration 016, natural-
// key unique index added by migration 017, issue #1937, root plan
// #1934): FindOrCreate's natural-key convergence on
// (channel_id, idea_id, lower(btrim(title))) -- specifically the
// IS NOT DISTINCT FROM regression this task calls out by name for the
// nullable idea_id component -- and ListByChannel's discovery shape
// (note count, most-recent note timestamp, Idea scoping). Same package/
// build tag/harness as outcome_bar_integration_test.go -- newStore/
// setupChannel (store_integration_test.go) are reused directly.
package store_test

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/whale-net/everything/audience_score_system/store"
	"github.com/whale-net/everything/libs/go/dbtest"
)

// ── FindOrCreate natural-key convergence (FR4) ──────────────────────────────

func TestThreadStore_FindOrCreate_IdenticalChannelIdeaTitleTwice_ReturnsSameThread(t *testing.T) {
	ctx := context.Background()
	s, _ := newStore(t)
	ch, creator := setupChannel(t, ctx, s)
	idea, err := s.Ideas().Create(ctx, ch.ID, "Idea one", creator.ID)
	require.NoError(t, err)

	first, err := s.Threads().FindOrCreate(ctx, store.FindOrCreateThreadInput{
		ChannelID: ch.ID, IdeaID: &idea.ID, Title: "Format saturation", CreatedByPersonID: creator.ID,
	})
	require.NoError(t, err)

	second, err := s.Threads().FindOrCreate(ctx, store.FindOrCreateThreadInput{
		ChannelID: ch.ID, IdeaID: &idea.ID, Title: "Format saturation", CreatedByPersonID: creator.ID,
	})
	require.NoError(t, err)

	assert.Equal(t, first.ID, second.ID, "an identical (channel, idea, title) triple must converge on one thread (FR4)")
}

// TestThreadStore_FindOrCreate_NilIdeaID_IdenticalTitleTwice_ReturnsSameThread
// is the regression test this task calls out by name: research_thread.
// idea_id is nullable, and a lookup using idea_id = $2 would treat two
// NULL-idea_id rows as never matching, breaking convergence for the "note
// predates an Idea" bucket specifically. FindOrCreate must use
// idea_id IS NOT DISTINCT FROM $2 instead.
func TestThreadStore_FindOrCreate_NilIdeaID_IdenticalTitleTwice_ReturnsSameThread(t *testing.T) {
	ctx := context.Background()
	s, db := newStore(t)
	ch, creator := setupChannel(t, ctx, s)

	first, err := s.Threads().FindOrCreate(ctx, store.FindOrCreateThreadInput{
		ChannelID: ch.ID, IdeaID: nil, Title: "Pre-idea research", CreatedByPersonID: creator.ID,
	})
	require.NoError(t, err)

	second, err := s.Threads().FindOrCreate(ctx, store.FindOrCreateThreadInput{
		ChannelID: ch.ID, IdeaID: nil, Title: "Pre-idea research", CreatedByPersonID: creator.ID,
	})
	require.NoError(t, err, "a bare idea_id = $2 lookup would return pgx.ErrNoRows here on the second call, even though the unique index already prevented a duplicate insert")

	assert.Equal(t, first.ID, second.ID, "two NULL-idea_id FindOrCreate calls with the same (channel, title) must converge on one thread")

	var count int
	require.NoError(t, db.Pool.QueryRow(ctx, `SELECT count(*) FROM research_thread WHERE channel_id = $1 AND idea_id IS NULL`, ch.ID).Scan(&count))
	assert.Equal(t, 1, count, "repeated identical NULL-idea_id FindOrCreate calls must leave exactly one research_thread row")
}

func TestThreadStore_FindOrCreate_CaseAndWhitespaceInsensitive_Converges(t *testing.T) {
	ctx := context.Background()
	s, _ := newStore(t)
	ch, creator := setupChannel(t, ctx, s)

	first, err := s.Threads().FindOrCreate(ctx, store.FindOrCreateThreadInput{
		ChannelID: ch.ID, Title: "Format saturation", CreatedByPersonID: creator.ID,
	})
	require.NoError(t, err)

	second, err := s.Threads().FindOrCreate(ctx, store.FindOrCreateThreadInput{
		ChannelID: ch.ID, Title: "  format SATURATION  ", CreatedByPersonID: creator.ID,
	})
	require.NoError(t, err)

	assert.Equal(t, first.ID, second.ID, "title comparison must be case/whitespace-insensitive")
	assert.Equal(t, "Format saturation", second.Title, "the persisted title is the FIRST caller's trimmed title, not the second caller's casing")
}

func TestThreadStore_FindOrCreate_SameTitleDifferentIdeas_ProducesDistinctThreads(t *testing.T) {
	ctx := context.Background()
	s, _ := newStore(t)
	ch, creator := setupChannel(t, ctx, s)
	idea1, err := s.Ideas().Create(ctx, ch.ID, "Idea one", creator.ID)
	require.NoError(t, err)
	idea2, err := s.Ideas().Create(ctx, ch.ID, "Idea two", creator.ID)
	require.NoError(t, err)

	t1, err := s.Threads().FindOrCreate(ctx, store.FindOrCreateThreadInput{
		ChannelID: ch.ID, IdeaID: &idea1.ID, Title: "Research", CreatedByPersonID: creator.ID,
	})
	require.NoError(t, err)

	t2, err := s.Threads().FindOrCreate(ctx, store.FindOrCreateThreadInput{
		ChannelID: ch.ID, IdeaID: &idea2.ID, Title: "Research", CreatedByPersonID: creator.ID,
	})
	require.NoError(t, err)

	assert.NotEqual(t, t1.ID, t2.ID, "the same title under two different Ideas must produce two distinct threads")
}

func TestThreadStore_FindOrCreate_SameTitleDifferentChannels_ProducesDistinctThreads(t *testing.T) {
	ctx := context.Background()
	s, _ := newStore(t)
	ch1, creator1 := setupChannel(t, ctx, s)
	ch2, creator2 := setupChannel(t, ctx, s)

	t1, err := s.Threads().FindOrCreate(ctx, store.FindOrCreateThreadInput{
		ChannelID: ch1.ID, Title: "Research", CreatedByPersonID: creator1.ID,
	})
	require.NoError(t, err)

	t2, err := s.Threads().FindOrCreate(ctx, store.FindOrCreateThreadInput{
		ChannelID: ch2.ID, Title: "Research", CreatedByPersonID: creator2.ID,
	})
	require.NoError(t, err)

	assert.NotEqual(t, t1.ID, t2.ID, "the same title on two different Channels must produce two distinct threads")
}

func TestThreadStore_FindOrCreate_IdeaFromAnotherChannel_RejectedNothingWritten(t *testing.T) {
	ctx := context.Background()
	s, db := newStore(t)
	ch1, creator1 := setupChannel(t, ctx, s)
	ch2, creator2 := setupChannel(t, ctx, s)
	foreignIdea, err := s.Ideas().Create(ctx, ch2.ID, "Foreign idea", creator2.ID)
	require.NoError(t, err)

	_, err = s.Threads().FindOrCreate(ctx, store.FindOrCreateThreadInput{
		ChannelID: ch1.ID, IdeaID: &foreignIdea.ID, Title: "Research", CreatedByPersonID: creator1.ID,
	})
	assert.Error(t, err, "an idea_id belonging to a different Channel must be rejected")

	var count int
	require.NoError(t, db.Pool.QueryRow(ctx, `SELECT count(*) FROM research_thread WHERE channel_id = $1`, ch1.ID).Scan(&count))
	assert.Equal(t, 0, count, "a rejected cross-Channel idea_id must not create any thread row")
}

// ── ListByChannel discovery (FR3) ────────────────────────────────────────────

func TestThreadStore_ListByChannel_NoteCountAndLatestNoteAt(t *testing.T) {
	ctx := context.Background()
	s, db := newStore(t)
	ch, creator := setupChannel(t, ctx, s)

	emptyThread, err := s.Threads().FindOrCreate(ctx, store.FindOrCreateThreadInput{
		ChannelID: ch.ID, Title: "Empty thread", CreatedByPersonID: creator.ID,
	})
	require.NoError(t, err)

	activeThread, err := s.Threads().FindOrCreate(ctx, store.FindOrCreateThreadInput{
		ChannelID: ch.ID, Title: "Active thread", CreatedByPersonID: creator.ID,
	})
	require.NoError(t, err)

	note1, err := s.Research().SaveNote(ctx, store.SaveNoteInput{ThreadTitle: "Research",
		ChannelID: ch.ID, Text: "first note", AuthorPersonID: creator.ID,
	})
	require.NoError(t, err)
	attachNoteToThread(t, ctx, db, note1.ID, activeThread.ID)

	note2, err := s.Research().SaveNote(ctx, store.SaveNoteInput{ThreadTitle: "Research",
		ChannelID: ch.ID, Text: "second note", AuthorPersonID: creator.ID,
	})
	require.NoError(t, err)
	attachNoteToThread(t, ctx, db, note2.ID, activeThread.ID)

	summaries, err := s.Threads().ListByChannel(ctx, ch.ID, nil)
	require.NoError(t, err)

	byID := make(map[string]store.ThreadSummary, len(summaries))
	for _, sm := range summaries {
		byID[sm.ID.String()] = sm
	}

	active, ok := byID[activeThread.ID.String()]
	require.True(t, ok)
	assert.Equal(t, 2, active.NoteCount)
	require.NotNil(t, active.LatestNoteAt, "a thread with notes must have a non-nil LatestNoteAt")

	empty, ok := byID[emptyThread.ID.String()]
	require.True(t, ok)
	assert.Equal(t, 0, empty.NoteCount)
	assert.Nil(t, empty.LatestNoteAt, "a thread with no notes must have a nil LatestNoteAt")
}

func TestThreadStore_ListByChannel_IdeaFilter_IncludesAndExcludesNullIdeaThreads(t *testing.T) {
	ctx := context.Background()
	s, _ := newStore(t)
	ch, creator := setupChannel(t, ctx, s)
	idea, err := s.Ideas().Create(ctx, ch.ID, "Idea one", creator.ID)
	require.NoError(t, err)

	attached, err := s.Threads().FindOrCreate(ctx, store.FindOrCreateThreadInput{
		ChannelID: ch.ID, IdeaID: &idea.ID, Title: "Attached thread", CreatedByPersonID: creator.ID,
	})
	require.NoError(t, err)

	unattached, err := s.Threads().FindOrCreate(ctx, store.FindOrCreateThreadInput{
		ChannelID: ch.ID, Title: "Unattached thread", CreatedByPersonID: creator.ID,
	})
	require.NoError(t, err)

	// ideaID == nil: every thread on the Channel, including idea_id IS NULL.
	all, err := s.Threads().ListByChannel(ctx, ch.ID, nil)
	require.NoError(t, err)
	assert.Len(t, all, 2, "ideaID=nil must return every thread on the Channel, including NULL-idea ones")

	// ideaID != nil: only that Idea's threads.
	scoped, err := s.Threads().ListByChannel(ctx, ch.ID, &idea.ID)
	require.NoError(t, err)
	require.Len(t, scoped, 1)
	assert.Equal(t, attached.ID, scoped[0].ID)
	for _, sm := range scoped {
		assert.NotEqual(t, unattached.ID, sm.ID, "a NULL-idea thread must not appear when ideaID is set")
	}
}

// attachNoteToThread points note noteID at threadID directly via SQL --
// SaveNote itself carries no ThreadID parameter (that wiring is a
// separate task, #1938), so this test-only helper reaches past the
// store's public API purely to set up ListByChannel's note-count/
// latest-note-at fixture.
func attachNoteToThread(t *testing.T, ctx context.Context, db *dbtest.Postgres, noteID, threadID uuid.UUID) {
	t.Helper()
	_, err := db.Pool.Exec(ctx, `UPDATE research_note SET thread_id = $1 WHERE id = $2`, threadID, noteID)
	require.NoError(t, err)
}
