//go:build integration

// Postgres-backed coverage for GET /channels/{id}'s recent-activity
// dashboard section (issue #2038, FR23-FR26, C20): the dashboard renders
// real per-Channel counts and outcome trend from DashboardStore.
// ChannelActivity, FR26's zero-activity Channel still returns 200 with a
// zeroed section, FR25's link to /channels/{id}/videos, NFR3's bounded
// query count, NFR4's three-tier visibility plus strict per-Channel
// scoping, and the pre-existing connection-status card's regression-free
// coexistence with the added section.
//
// Split into its own file rather than growing channels_integration_test.go
// further -- same package/build tag/harness, so newChannelsTestStack/
// sessionCookie/do/findCookie/channelsQueryCounter/tracedChannelsStack
// (channels_integration_test.go) are reused directly.
//
// Run it explicitly (requires a working Docker daemon):
//
//	bazel test //audience_score_system/web:web_channels_integration_test --test_output=all
package main

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/whale-net/everything/audience_score_system/store"
)

// setResearchNoteCreatedAt backdates a research_note's created_at directly
// via SQL -- SaveNote has no created_at input, mirroring store package's
// store_integration_test.go helper of the same name (different package, no
// collision) so the dashboard fixtures below land in deterministic,
// distinctly-windowed positions rather than whatever wall-clock time
// back-to-back SaveNote calls happen to land on.
func (s *channelsTestStack) setResearchNoteCreatedAt(t *testing.T, ctx context.Context, noteID uuid.UUID, at time.Time) {
	t.Helper()
	_, err := s.db.Pool.Exec(ctx, `UPDATE research_note SET created_at = $1 WHERE id = $2`, at, noteID)
	require.NoError(t, err)
}

// publishedVideoWithMetrics upserts a published synced_video on ch at
// publishedAt and attaches a video_metrics row -- FR24's outcome fixture:
// published alone is not enough, a recorded metrics row is required.
func (s *channelsTestStack) publishedVideoWithMetrics(t *testing.T, ctx context.Context, ch store.Channel, label string, publishedAt time.Time) {
	t.Helper()

	ytID := "yt-" + label + "-" + uuid.NewString()
	require.NoError(t, s.store.Sync().UpsertVideos(ctx, ch.ID, []store.SyncedVideo{{
		YouTubeVideoID: ytID, Title: label,
		PrivacyStatus: store.PrivacyStatusPublic, PublishedAt: &publishedAt, LastSyncedAt: time.Now(),
	}}))
	var syncedID uuid.UUID
	require.NoError(t, s.db.Pool.QueryRow(ctx, `SELECT id FROM synced_video WHERE youtube_video_id = $1`, ytID).Scan(&syncedID))

	views := int64(1)
	require.NoError(t, s.store.Sync().UpsertMetrics(ctx, []store.VideoMetrics{{
		SyncedVideoID: syncedID, Views: &views, MeasuredAt: time.Now(),
	}}))
}

// ── FR23/FR24: dashboard renders real counts + trend ────────────────────────

// TestHandleChannelDetail_FR23FR24_RendersActivityCountsAndOutcomeTrend
// proves the wiring end to end: real store data lands in the right table
// cells. Asserts on the exact `<td>Label</td><td>24h</td><td>7d</td>`
// sequence channel_detail.templ emits (verified against the compiled
// templ output, which places each count immediately between its own <td>
// tags with no intervening whitespace) rather than a loose "contains the
// digit" check, since a loose check could false-positive on an unrelated
// number elsewhere on the page (e.g. inside the Channel's own UUID).
func TestHandleChannelDetail_FR23FR24_RendersActivityCountsAndOutcomeTrend(t *testing.T) {
	ctx := context.Background()
	s := newChannelsTestStack(t)

	person := s.newPerson(t, ctx, "person")
	ch, err := s.store.Channels().Create(ctx, "yt-"+uuid.NewString(), "Dashboard Channel", person.ID)
	require.NoError(t, err)

	now := time.Now().UTC()

	// Two research notes inside 24h -- both windows must read 2 (7d is a
	// superset of 24h).
	n1, err := s.store.Research().SaveNote(ctx, store.SaveNoteInput{ChannelID: ch.ID, ThreadTitle: "N1", Text: "n1", AuthorPersonID: person.ID})
	require.NoError(t, err)
	s.setResearchNoteCreatedAt(t, ctx, n1.ID, now.Add(-1*time.Hour))
	n2, err := s.store.Research().SaveNote(ctx, store.SaveNoteInput{ChannelID: ch.ID, ThreadTitle: "N2", Text: "n2", AuthorPersonID: person.ID})
	require.NoError(t, err)
	s.setResearchNoteCreatedAt(t, ctx, n2.ID, now.Add(-2*time.Hour))

	// One outcome (published + metrics) inside 24h, zero in the
	// immediately preceding 24h period -- FR24's simplest non-Same trend:
	// Better.
	s.publishedVideoWithMetrics(t, ctx, ch, "outcome-1", now.Add(-1*time.Hour))

	cookie := s.sessionCookie(t, ctx, person.ID)
	w := s.do(t, http.MethodGet, "/channels/"+ch.ID.String(), cookie)
	require.Equal(t, http.StatusOK, w.Code, "body: %s", w.Body.String())

	body := w.Body.String()
	assert.Contains(t, body, "Recent activity")
	assert.Contains(t, body, "<td>Research notes</td><td>2</td><td>2</td>",
		"both research notes are inside 24h, so both windows must read 2")
	assert.Contains(t, body, "<td>Outcomes</td><td>1</td><td>1</td>",
		"the single published+metriced video is inside both windows")
	assert.Contains(t, body, "24h Better", "1 current outcome vs. 0 prior must render as Better")
	assert.Contains(t, body, "7d Better")
}

// ── FR26: zero-activity Channel ─────────────────────────────────────────────

// TestHandleChannelDetail_FR26_ZeroActivityRendersZeroedDashboard proves the
// regression this FR guards against directly: a brand-new Channel with no
// activity at all must return 200 with every count rendered as 0 -- never
// an error, and never an omitted section.
func TestHandleChannelDetail_FR26_ZeroActivityRendersZeroedDashboard(t *testing.T) {
	ctx := context.Background()
	s := newChannelsTestStack(t)

	person := s.newPerson(t, ctx, "person")
	ch, err := s.store.Channels().Create(ctx, "yt-"+uuid.NewString(), "Brand New Channel", person.ID)
	require.NoError(t, err)

	cookie := s.sessionCookie(t, ctx, person.ID)
	w := s.do(t, http.MethodGet, "/channels/"+ch.ID.String(), cookie)
	require.Equal(t, http.StatusOK, w.Code, "a zero-activity Channel must render 200, never an error, body: %s", w.Body.String())

	body := w.Body.String()
	assert.Contains(t, body, "Recent activity", "the dashboard section must render even with zero activity, never be omitted")
	for _, row := range []string{"Research notes", "Ideas", "Verdicts", "Video scripts", "Newly linked videos", "Outcomes"} {
		assert.Contains(t, body, "<td>"+row+"</td><td>0</td><td>0</td>", "row %q must render zero in both windows, not be blank or omitted", row)
	}
	assert.Contains(t, body, "24h Same")
	assert.Contains(t, body, "7d Same")
}

// ── FR25: link to the videos page ───────────────────────────────────────────

func TestHandleChannelDetail_FR25_LinksToVideosPage(t *testing.T) {
	ctx := context.Background()
	s := newChannelsTestStack(t)

	person := s.newPerson(t, ctx, "person")
	ch, err := s.store.Channels().Create(ctx, "yt-"+uuid.NewString(), "Linked Channel", person.ID)
	require.NoError(t, err)

	cookie := s.sessionCookie(t, ctx, person.ID)
	w := s.do(t, http.MethodGet, "/channels/"+ch.ID.String(), cookie)
	require.Equal(t, http.StatusOK, w.Code, "body: %s", w.Body.String())

	assert.Contains(t, w.Body.String(), `href="/channels/`+ch.ID.String()+`/videos"`,
		"FR25: the dashboard must link to /channels/{id}/videos")
}

// ── NFR4: three-tier visibility, strict per-Channel scoping ─────────────────

// TestHandleChannelDetail_NFR4_AllThreeTiersSeeDashboard proves the
// dashboard adds no new authorization tier: Founder, Co-Creator, and
// Analyst all see the same "Recent activity" section via the existing
// store.CanRead gate.
func TestHandleChannelDetail_NFR4_AllThreeTiersSeeDashboard(t *testing.T) {
	ctx := context.Background()
	s := newChannelsTestStack(t)

	founder := s.newPerson(t, ctx, "founder")
	ch, err := s.store.Channels().Create(ctx, "yt-"+uuid.NewString(), "Tiered Channel", founder.ID)
	require.NoError(t, err)

	coCreator := s.newPerson(t, ctx, "co-creator")
	require.NoError(t, s.store.Roles().AddRole(ctx, ch.ID, coCreator.ID, store.RoleCoCreator, founder.ID))
	analyst := s.newPerson(t, ctx, "analyst")
	require.NoError(t, s.store.Roles().AddRole(ctx, ch.ID, analyst.ID, store.RoleAnalyst, founder.ID))

	for _, p := range []store.Person{founder, coCreator, analyst} {
		cookie := s.sessionCookie(t, ctx, p.ID)
		w := s.do(t, http.MethodGet, "/channels/"+ch.ID.String(), cookie)
		require.Equal(t, http.StatusOK, w.Code, "person %s: body: %s", p.DisplayName, w.Body.String())
		assert.Contains(t, w.Body.String(), "Recent activity", "person %s must see the dashboard section", p.DisplayName)
	}
}

// TestHandleChannelDetail_NFR4_NonMemberForbidden extends the pre-existing
// TestHandleChannelDetail_NoRoleOnChannel_Forbidden by naming it against
// this task's NFR4 explicitly: a Person with no role gets 403, and the
// dashboard's added query never runs in a way that would leak data past
// that gate.
func TestHandleChannelDetail_NFR4_NonMemberForbidden(t *testing.T) {
	ctx := context.Background()
	s := newChannelsTestStack(t)

	founder := s.newPerson(t, ctx, "founder")
	ch, err := s.store.Channels().Create(ctx, "yt-"+uuid.NewString(), "Members Only Channel", founder.ID)
	require.NoError(t, err)

	outsider := s.newPerson(t, ctx, "outsider")
	cookie := s.sessionCookie(t, ctx, outsider.ID)

	w := s.do(t, http.MethodGet, "/channels/"+ch.ID.String(), cookie)
	assert.Equal(t, http.StatusForbidden, w.Code, "a Person with no open role on the Channel must be rejected, body: %s", w.Body.String())
}

// TestHandleChannelDetail_NFR4_CountsScopedToViewedChannelOnly proves the
// dashboard never aggregates across Channels: activity seeded on chB must
// never surface on chA's dashboard, even for the same Person holding a
// role on both.
func TestHandleChannelDetail_NFR4_CountsScopedToViewedChannelOnly(t *testing.T) {
	ctx := context.Background()
	s := newChannelsTestStack(t)

	person := s.newPerson(t, ctx, "person")
	chA, err := s.store.Channels().Create(ctx, "yt-a-"+uuid.NewString(), "Channel A", person.ID)
	require.NoError(t, err)
	chB, err := s.store.Channels().Create(ctx, "yt-b-"+uuid.NewString(), "Channel B", person.ID)
	require.NoError(t, err)

	now := time.Now().UTC()
	noteB, err := s.store.Research().SaveNote(ctx, store.SaveNoteInput{ChannelID: chB.ID, ThreadTitle: "B Note", Text: "b", AuthorPersonID: person.ID})
	require.NoError(t, err)
	s.setResearchNoteCreatedAt(t, ctx, noteB.ID, now.Add(-1*time.Hour))

	cookie := s.sessionCookie(t, ctx, person.ID)
	w := s.do(t, http.MethodGet, "/channels/"+chA.ID.String(), cookie)
	require.Equal(t, http.StatusOK, w.Code, "body: %s", w.Body.String())

	assert.Contains(t, w.Body.String(), "<td>Research notes</td><td>0</td><td>0</td>",
		"Channel B's note must never contribute to Channel A's dashboard counts")
}

// ── NFR3: bounded query count ────────────────────────────────────────────────

// TestHandleChannelDetail_NFR3_IssuesBoundedQueries proves the dashboard's
// added round trips don't grow with how much a Channel has accumulated: the
// same query count for a Channel with a few research notes as for one with
// many.
func TestHandleChannelDetail_NFR3_IssuesBoundedQueries(t *testing.T) {
	ctx := context.Background()
	s := newChannelsTestStack(t)

	makeChannel := func(noteCount int) (store.Channel, uuid.UUID) {
		person := s.newPerson(t, ctx, "p-"+uuid.NewString())
		ch, err := s.store.Channels().Create(ctx, "yt-"+uuid.NewString(), "Channel "+uuid.NewString(), person.ID)
		require.NoError(t, err)
		now := time.Now().UTC()
		for i := 0; i < noteCount; i++ {
			note, err := s.store.Research().SaveNote(ctx, store.SaveNoteInput{ChannelID: ch.ID, ThreadTitle: "T", Text: "note", AuthorPersonID: person.ID})
			require.NoError(t, err)
			s.setResearchNoteCreatedAt(t, ctx, note.ID, now.Add(-1*time.Hour))
		}
		return ch, person.ID
	}

	fewCh, fewPersonID := makeChannel(1)
	manyCh, manyPersonID := makeChannel(15)

	fewCounter := &channelsQueryCounter{}
	fewStack := s.tracedChannelsStack(t, ctx, fewCounter)
	fewCookie := fewStack.sessionCookie(t, ctx, fewPersonID)
	w := fewStack.do(t, http.MethodGet, "/channels/"+fewCh.ID.String(), fewCookie)
	require.Equal(t, http.StatusOK, w.Code, "body: %s", w.Body.String())

	manyCounter := &channelsQueryCounter{}
	manyStack := s.tracedChannelsStack(t, ctx, manyCounter)
	manyCookie := manyStack.sessionCookie(t, ctx, manyPersonID)
	w = manyStack.do(t, http.MethodGet, "/channels/"+manyCh.ID.String(), manyCookie)
	require.Equal(t, http.StatusOK, w.Code, "body: %s", w.Body.String())

	assert.Equal(t, fewCounter.n, manyCounter.n,
		"GET /channels/{id} must issue the same number of SQL statements regardless of activity volume (NFR3); 1 note issued %d, 15 issued %d", fewCounter.n, manyCounter.n)
}

// ── Regression: connection-status card unaffected ───────────────────────────

// TestHandleChannelDetail_Regression_ConnectionStatusCardStillRenders proves
// the dashboard is an ADDED section, never a replacement: the pre-existing
// connection-status card (title, Status badge, and its action links) still
// renders unchanged alongside the new "Recent activity" section.
func TestHandleChannelDetail_Regression_ConnectionStatusCardStillRenders(t *testing.T) {
	ctx := context.Background()
	s := newChannelsTestStack(t)

	person := s.newPerson(t, ctx, "person")
	ch, err := s.store.Channels().Create(ctx, "yt-"+uuid.NewString(), "Regression Channel", person.ID)
	require.NoError(t, err)

	cookie := s.sessionCookie(t, ctx, person.ID)
	w := s.do(t, http.MethodGet, "/channels/"+ch.ID.String(), cookie)
	require.Equal(t, http.StatusOK, w.Code, "body: %s", w.Body.String())

	body := w.Body.String()
	assert.Contains(t, body, "Regression Channel")
	assert.Contains(t, body, "Status:")
	assert.Contains(t, body, "Connected", "the connection-status card's badge must still render")
	assert.Contains(t, body, "Recent activity", "the added dashboard section must coexist with the card, not replace it")
}
