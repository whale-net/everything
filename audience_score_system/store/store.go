// Package store is the pgx-based repository for Audience Score System's
// identity core: person, channel, the Persona<->Channel join table (LB2),
// and channel_invite codes (migration 001, see
// audience_score_system/migrate/schema/migrations/001_identity.up.sql) --
// plus the LB3 research/verdict/schedule/outcome record chain, its read
// models, and the mcp_idempotency ledger (migration 002, see
// .../002_research_schedule_outcome.up.sql and issue #1569); plus
// video_script (migration 010, .../010_video_script.up.sql, issues
// #1823/#1824), M2.1's replacement for schedule_entry as the record of a
// proposed video.
//
// Store is the single entry point, built over //libs/go/db's
// *pgxpool.Pool. Its Persons/Channels/Roles/Invites/Ideas/Research/
// Verdicts/Pacing/Schedules/Sync/Matches/Idempotency accessors
// hand back the per-entity Store implementations -- kept as separate
// concrete types,
// not all methods on Store itself, because e.g. PersonStore.GetByID and
// ChannelStore.GetByID share a method name but return different types; a
// single receiver type cannot implement both.
//
// Every authorization question in M1 is answered ONLY by
// CanApprove/CanInvite/CanReconnect/CanRead/CanWrite (see authz.go) --
// reading `role` off the open (valid_to IS NULL) channel_person row for a
// (channel_id, person_id) pair. No handler, tool, or workflow outside this
// package may reconstruct a role check from raw SQL (NFR5): there is no
// channel.owner_id column, and nothing here assumes a Channel has exactly
// one Creator.
package store

import "github.com/jackc/pgx/v5/pgxpool"

// Store is the pgx-backed repository over person/channel/channel_person/
// channel_invite (migration 001) plus idea/research_note/
// viability_verdict/verdict_citation/pacing_policy/schedule_entry/
// synced_video/video_metrics/video_schedule_match/mcp_idempotency
// (migration 002) plus video_script (migration 010).
type Store struct {
	pool *pgxpool.Pool
}

// New returns a Store backed by pool (see //libs/go/db.NewPool).
func New(pool *pgxpool.Pool) *Store {
	return &Store{pool: pool}
}

// Persons returns the PersonStore implementation.
func (s *Store) Persons() PersonStore { return personStore{pool: s.pool} }

// Channels returns the ChannelStore implementation.
func (s *Store) Channels() ChannelStore { return channelStore{pool: s.pool} }

// Roles returns the RoleStore implementation.
func (s *Store) Roles() RoleStore { return roleStore{pool: s.pool} }

// Invites returns the InviteStore implementation.
func (s *Store) Invites() InviteStore { return inviteStore{pool: s.pool} }

// Ideas returns the IdeaStore implementation (migration 002).
func (s *Store) Ideas() IdeaStore { return ideaStore{pool: s.pool} }

// Research returns the ResearchStore implementation (migration 002).
func (s *Store) Research() ResearchStore { return researchStore{pool: s.pool} }

// Verdicts returns the VerdictStore implementation (migration 002).
func (s *Store) Verdicts() VerdictStore { return verdictStore{pool: s.pool} }

// Pacing returns the PacingStore implementation (migration 002).
func (s *Store) Pacing() PacingStore { return pacingStore{pool: s.pool} }

// Schedules returns the ScheduleStore implementation (migration 002).
func (s *Store) Schedules() ScheduleStore { return scheduleStore{pool: s.pool} }

// Sync returns the SyncStore implementation (migration 002).
func (s *Store) Sync() SyncStore { return syncStore{pool: s.pool} }

// Matches returns the MatchStore implementation (migration 002).
func (s *Store) Matches() MatchStore { return matchStore{pool: s.pool} }

// Idempotency returns the Idempotency implementation (migration 002,
// NFR2/LB4).
func (s *Store) Idempotency() Idempotency { return idempotencyStore{pool: s.pool} }

// Browse returns the BrowseStore implementation (issue #1582, FR24) --
// C10's cross-entity Channel-overview and prediction-vs-outcome reads.
func (s *Store) Browse() BrowseStore { return browseStore{pool: s.pool} }

// Strategies returns the StrategyStore implementation (migration 008,
// issue #1637) -- a grouping of viable viability_verdict rows
// (many-to-many with Strategy), with no recurrence/pacing mechanics of
// its own (FR47, issue #1833).
func (s *Store) Strategies() StrategyStore { return strategyStore{pool: s.pool} }

// Access returns the AccessStore implementation (migration 009, issue
// #1716) -- M2's read side of the access model: which Channels a Person
// has a role on and at what tier (FR26), a Channel's roster (FR30/FR31/
// FR33), and its grant/revoke audit trail over v_channel_person_audit
// (FR35). Performs no authorization itself -- see AccessStore's doc
// comment.
func (s *Store) Access() AccessStore { return accessStore{pool: s.pool} }

// MyWork returns the MyWorkStore implementation (issue #1717, FR27/FR28,
// NFR9) -- the cross-Channel "my work" aggregate: for every Channel a
// Person currently holds an open role on, its latest research notes,
// verdict, schedule state, and outcome comparison, in a bounded number of
// queries regardless of Channel count.
func (s *Store) MyWork() MyWorkStore { return myWorkStore{pool: s.pool} }

// VideoScripts returns the VideoScriptStore implementation (migration
// 010, milestone video-script-model, issues #1823/#1824) -- M2.1's
// replacement for schedule_entry as the record of a proposed video: the
// propose/greenlit/denied/archived lifecycle (FR36-FR40) plus the
// publish-freeze predicate Archive consults (FR39). Performs no
// authorization itself -- see VideoScriptStore's doc comment.
func (s *Store) VideoScripts() VideoScriptStore { return videoScriptStore{pool: s.pool} }
