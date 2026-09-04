// MyWorkStore is FR27/FR28's cross-Channel "my work" aggregate read model
// (issue #1717): for every Channel a Person currently holds an open role
// on, the latest research notes, the most recently recorded viability
// verdict, the current schedule-draft state, and the most recent outcome
// comparison -- the same four sections BrowseStore/get_channel_overview
// (C10, issue #1582/FR24) surfaces for one Channel a caller already
// picked, assembled here across every Channel a Person is on instead.
package store

import (
	"context"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

// MyWorkStore covers the cross-Channel aggregate behind FR27/FR28.
type MyWorkStore interface {
	// SummariesForPerson returns one ChannelWorkSummary per Channel
	// personID currently holds an open (valid_to IS NULL) role on --
	// ordered channel.title, channel.id, matching
	// AccessStore.ChannelsWithRoleForPerson exactly, since that method is
	// this one's Channel-set source. Every such Channel appears even when
	// it has no notes/verdict/schedule/outcome data yet (LatestNotes
	// empty, LatestVerdict/LatestOutcome nil, ScheduleState zero-valued)
	// -- never a missing entry. notesPerChannel caps LatestNotes per
	// Channel (most-recent-first); <= 0 returns none.
	//
	// FR28: the Channel set is re-derived from
	// AccessStore.ChannelsWithRoleForPerson on every call -- nothing
	// cached, nothing session-scoped, nothing computed once at sign-in.
	// A role revoked between two calls drops that Channel out of the very
	// next call's result with no re-auth and no new connection required.
	// Do not memoize or cache this method's result across calls; that is
	// exactly the shortcut FR28 forbids.
	//
	// NFR9: issues at most 5 SQL statements total, regardless of how many
	// Channels personID is on -- one to list the Channels
	// (AccessStore.ChannelsWithRoleForPerson), then one each for notes,
	// latest verdict, schedule state, and latest outcome, every one of
	// those four scoped to `channel_id = ANY(...)` across the whole
	// Channel set at once and reduced to "top-N/top-1 per Channel" with a
	// window function, never looped per Channel. If personID holds no
	// open role anywhere, returns an empty slice after only the first
	// query.
	SummariesForPerson(ctx context.Context, personID uuid.UUID, notesPerChannel int) ([]ChannelWorkSummary, error)
}

// myWorkStore implements MyWorkStore over research_note, viability_verdict/
// idea, schedule_entry (loadScheduleState only -- #1835's retirement task
// still owns that section), and the predictionOutcomeJoin chain (browse.go,
// video_script-anchored per #1830's FR44 re-anchor), scoped by
// AccessStore.ChannelsWithRoleForPerson's Channel set.
type myWorkStore struct{ pool *pgxpool.Pool }

var _ MyWorkStore = myWorkStore{}

// SummariesForPerson assembles ChannelWorkSummary rows in Go by
// channel_id, from at most 5 SQL statements (NFR9) -- see the interface
// doc comment above for the exact contract. No `for` loop here issues a
// query: channelRoles/notes/verdicts/scheduleState/outcomes are each
// exactly one round trip regardless of len(channelIDs).
func (s myWorkStore) SummariesForPerson(ctx context.Context, personID uuid.UUID, notesPerChannel int) ([]ChannelWorkSummary, error) {
	// Statement 1/5: the Channel set itself (FR28 -- re-read live on every
	// call, never cached).
	channelRoles, err := accessStore{pool: s.pool}.ChannelsWithRoleForPerson(ctx, personID)
	if err != nil {
		return nil, fmt.Errorf("my work: list channels for person: %w", err)
	}
	if len(channelRoles) == 0 {
		return nil, nil
	}

	channelIDs := make([]uuid.UUID, len(channelRoles))
	summaries := make([]ChannelWorkSummary, len(channelRoles))
	index := make(map[uuid.UUID]int, len(channelRoles))
	for i, cr := range channelRoles {
		channelIDs[i] = cr.Channel.ID
		summaries[i] = ChannelWorkSummary{
			Channel:     cr.Channel,
			Role:        cr.Role,
			LatestNotes: []ResearchNote{},
		}
		index[cr.Channel.ID] = i
	}

	// Statements 2-5/5: one bounded, all-Channels-at-once query per
	// section.
	if err := s.loadLatestNotes(ctx, channelIDs, notesPerChannel, summaries, index); err != nil {
		return nil, err
	}
	if err := s.loadLatestVerdicts(ctx, channelIDs, summaries, index); err != nil {
		return nil, err
	}
	if err := s.loadScheduleState(ctx, channelIDs, summaries, index); err != nil {
		return nil, err
	}
	if err := s.loadLatestOutcomes(ctx, channelIDs, summaries, index); err != nil {
		return nil, err
	}

	return summaries, nil
}

// loadLatestNotes runs one query over research_note for every Channel in
// channelIDs at once, using ROW_NUMBER() PARTITION BY channel_id to take
// each Channel's top notesPerChannel most-recent notes in a single
// statement (NFR9) rather than one ListByChannel call per Channel.
func (s myWorkStore) loadLatestNotes(ctx context.Context, channelIDs []uuid.UUID, notesPerChannel int, summaries []ChannelWorkSummary, index map[uuid.UUID]int) error {
	if notesPerChannel <= 0 {
		return nil
	}

	rows, err := s.pool.Query(ctx, `
		SELECT id, channel_id, idea_id, text, source_url, author_person_id, created_at, COALESCE(idempotency_key, '')
		FROM (
			SELECT id, channel_id, idea_id, text, source_url, author_person_id, created_at, idempotency_key,
			       ROW_NUMBER() OVER (PARTITION BY channel_id ORDER BY created_at DESC) AS rn
			FROM research_note
			WHERE channel_id = ANY($1)
		) ranked
		WHERE rn <= $2
		ORDER BY channel_id, created_at DESC
	`, channelIDs, notesPerChannel)
	if err != nil {
		return fmt.Errorf("my work: list latest research notes: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		var n ResearchNote
		if err := rows.Scan(&n.ID, &n.ChannelID, &n.IdeaID, &n.Text, &n.SourceURL, &n.AuthorPersonID, &n.CreatedAt, &n.IdempotencyKey); err != nil {
			return fmt.Errorf("my work: scan research_note: %w", err)
		}
		if i, ok := index[n.ChannelID]; ok {
			summaries[i].LatestNotes = append(summaries[i].LatestNotes, n)
		}
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("my work: list latest research notes: %w", err)
	}
	return nil
}

// loadLatestVerdicts runs one query over viability_verdict joined to idea
// for every Channel in channelIDs at once, taking each Channel's single
// most-recently-created verdict (across all of its Ideas) via
// ROW_NUMBER() PARTITION BY idea.channel_id (NFR9).
func (s myWorkStore) loadLatestVerdicts(ctx context.Context, channelIDs []uuid.UUID, summaries []ChannelWorkSummary, index map[uuid.UUID]int) error {
	rows, err := s.pool.Query(ctx, `
		SELECT channel_id, idea_id, idea_title, verdict_id, version, verdict, reasoning, created_at
		FROM (
			SELECT i.channel_id, i.id AS idea_id, i.title AS idea_title,
			       vv.id AS verdict_id, vv.version, vv.verdict, vv.reasoning, vv.created_at,
			       ROW_NUMBER() OVER (PARTITION BY i.channel_id ORDER BY vv.created_at DESC) AS rn
			FROM viability_verdict vv
			JOIN idea i ON i.id = vv.idea_id
			WHERE i.channel_id = ANY($1)
		) ranked
		WHERE rn = 1
	`, channelIDs)
	if err != nil {
		return fmt.Errorf("my work: list latest verdicts: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		var channelID uuid.UUID
		var v IdeaVerdictSummary
		if err := rows.Scan(&channelID, &v.IdeaID, &v.IdeaTitle, &v.VerdictID, &v.Version, &v.Verdict, &v.Reasoning, &v.CreatedAt); err != nil {
			return fmt.Errorf("my work: scan latest verdict: %w", err)
		}
		if i, ok := index[channelID]; ok {
			v := v
			summaries[i].LatestVerdict = &v
		}
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("my work: list latest verdicts: %w", err)
	}
	return nil
}

// loadScheduleState runs one aggregate query over schedule_entry, grouped
// by channel_id, for every Channel in channelIDs at once (NFR9).
func (s myWorkStore) loadScheduleState(ctx context.Context, channelIDs []uuid.UUID, summaries []ChannelWorkSummary, index map[uuid.UUID]int) error {
	rows, err := s.pool.Query(ctx, `
		SELECT channel_id,
		       COUNT(*) FILTER (WHERE state = 'draft'),
		       COUNT(*) FILTER (WHERE state = 'committed'),
		       MIN(proposed_publish_at) FILTER (WHERE proposed_publish_at >= NOW())
		FROM schedule_entry
		WHERE channel_id = ANY($1)
		GROUP BY channel_id
	`, channelIDs)
	if err != nil {
		return fmt.Errorf("my work: aggregate schedule state: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		var channelID uuid.UUID
		var st ScheduleDraftState
		if err := rows.Scan(&channelID, &st.DraftCount, &st.CommittedCount, &st.NextProposedPublishAt); err != nil {
			return fmt.Errorf("my work: scan schedule state: %w", err)
		}
		if i, ok := index[channelID]; ok {
			summaries[i].ScheduleState = st
		}
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("my work: aggregate schedule state: %w", err)
	}
	return nil
}

// loadLatestOutcomes runs one query, built on predictionOutcomeJoin
// (browse.go) -- the same join BrowseStore.PredictionVsOutcome uses,
// including its LB3 "bound verdict version, not current" semantics --
// for every Channel in channelIDs at once, taking each Channel's single
// most-recently-published qualifying row via
// ROW_NUMBER() PARTITION BY idea.channel_id (NFR9).
func (s myWorkStore) loadLatestOutcomes(ctx context.Context, channelIDs []uuid.UUID, summaries []ChannelWorkSummary, index map[uuid.UUID]int) error {
	rows, err := s.pool.Query(ctx, `
		SELECT channel_id, idea_id, idea_title,
		       verdict_id, version, verdict, reasoning, author_person_id, author_display_name, verdict_created_at,
		       video_script_id, script_title, script_status, target_publish_date, decided_at,
		       match_id, match_state, match_confidence,
		       synced_video_id, youtube_video_id, video_title, published_at,
		       views, average_view_duration_seconds, average_view_percentage, impressions, impression_ctr, measured_at
		FROM (
			SELECT
				i.channel_id,
				i.id AS idea_id, i.title AS idea_title,
				vv.id AS verdict_id, vv.version, vv.verdict, vv.reasoning, vv.author_person_id,
				COALESCE(NULLIF(author.display_name, ''), COALESCE(author.email, '')) AS author_display_name, vv.created_at AS verdict_created_at,
				vs.id AS video_script_id, vs.title AS script_title, vs.status AS script_status, vs.target_publish_date, vs.decided_at,
				vsm.id AS match_id, vsm.state AS match_state, vsm.confidence AS match_confidence,
				sv.id AS synced_video_id, sv.youtube_video_id, COALESCE(sv.title, '') AS video_title, sv.published_at,
				vm.views, vm.average_view_duration_seconds, vm.average_view_percentage,
				vm.impressions, vm.impression_ctr, vm.measured_at,
				ROW_NUMBER() OVER (PARTITION BY i.channel_id ORDER BY sv.published_at DESC NULLS LAST) AS rn
			`+predictionOutcomeJoin+`
			WHERE i.channel_id = ANY($1)
		) ranked
		WHERE rn = 1
	`, channelIDs)
	if err != nil {
		return fmt.Errorf("my work: list latest outcomes: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		var channelID uuid.UUID
		var r PredictionOutcome
		if err := rows.Scan(
			&channelID, &r.IdeaID, &r.IdeaTitle,
			&r.VerdictID, &r.VerdictVersion, &r.Verdict, &r.VerdictReasoning, &r.VerdictAuthorPersonID,
			&r.VerdictAuthorDisplayName, &r.VerdictCreatedAt,
			&r.VideoScriptID, &r.ScriptTitle, &r.ScriptStatus, &r.TargetPublishDate, &r.DecidedAt,
			&r.MatchID, &r.MatchState, &r.MatchConfidence,
			&r.SyncedVideoID, &r.YouTubeVideoID, &r.VideoTitle, &r.PublishedAt,
			&r.Views, &r.AverageViewDurationSeconds, &r.AverageViewPercentage,
			&r.Impressions, &r.ImpressionCTR, &r.MetricsMeasuredAt,
		); err != nil {
			return fmt.Errorf("my work: scan latest outcome: %w", err)
		}
		if i, ok := index[channelID]; ok {
			r := r
			summaries[i].LatestOutcome = &r
		}
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("my work: list latest outcomes: %w", err)
	}
	return nil
}
