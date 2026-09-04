package store

import (
	"time"

	"github.com/google/uuid"
)

// Person is one row of `person` (migration 001) -- one per authenticated
// human, keyed on the Google OAuth `sub` claim (FR1/FR2), not email.
type Person struct {
	ID            uuid.UUID
	GoogleSubject string
	Email         string
	DisplayName   string
	CreatedAt     time.Time
}

// ConnectionState is `channel.connection_state` (migration 001, FR4).
type ConnectionState string

const (
	ConnectionStateConnected   ConnectionState = "connected"
	ConnectionStateNeedsReauth ConnectionState = "needs_reauth"
)

// Channel is one row of `channel` (migration 001) -- one per connected
// YouTube channel. Deliberately has no owner field: ownership, and every
// other role, lives only in channel_person (LB2) -- see RoleStore and
// authz.go.
type Channel struct {
	ID                       uuid.UUID
	YouTubeChannelID         string
	Title                    string
	ConnectionState          ConnectionState
	ConnectionStateChangedAt time.Time
	CreatedAt                time.Time
}

// Role is `channel_person.role` (migration 001, LB2; widened by migration
// 009 for M2). M1 only ever populated RoleCreator and RoleAnalyst rows,
// but nothing in this package assumes those are the only roles that will
// ever exist.
type Role string

const (
	// RoleCreator is the Founder tier (FR3/FR25/FR29) -- exactly one live
	// row per Channel, enforced by migration 009's
	// channel_person_channel_id_founder_current partial unique index.
	// Never granted or removed by any path other than Channel-connect.
	RoleCreator Role = "creator"
	// RoleCoCreator is M2's new tier (migration 009, FR29), sitting between
	// Founder and Analyst: it shares the Founder's approve/invite/
	// reconnect/read/write/audit authority (FR32/FR35) symmetrically, but
	// cannot be removed by another Co-Creator and can never remove a
	// Founder (FR33) -- see CanRemove's doc comment for the exact matrix.
	// NFR7: adding a tier is exactly one constant here plus one CHECK
	// value in the migration, and nothing else -- no ordering, no rank,
	// no `>=` comparison anywhere in this package.
	RoleCoCreator Role = "co_creator"
	RoleAnalyst   Role = "analyst"
)

// ChannelPerson is one row of `channel_person` -- the LB2 join table,
// SCD2 per AGENTS.md "SCD2". ValidTo == nil means this role is currently
// held; a non-nil ValidTo means it was closed (superseded or revoked) at
// that time.
type ChannelPerson struct {
	ID        uuid.UUID
	ChannelID uuid.UUID
	PersonID  uuid.UUID
	Role      Role
	ValidFrom time.Time
	ValidTo   *time.Time
}

// ChannelRole is one Channel paired with a Person's currently-held Role on
// it -- AccessStore.ChannelsWithRoleForPerson's row shape (FR26). Exactly
// one row per open channel_person row: migration 001's
// channel_person_channel_id_person_id_current partial unique index
// guarantees at most one open (channel_id, person_id) row, so a Person
// never has two ChannelRole entries for the same Channel.
type ChannelRole struct {
	Channel Channel
	Role    Role
}

// RosterEntry is one Person holding an open role on a Channel, as
// AccessStore.Roster returns it (FR30/FR31/FR33) -- the access-management
// page's list. GrantedByDisplayName is "" when granted_by_person_id is
// NULL (migration 009 does not backfill it on pre-M2 rows) -- render
// "unknown" upstream rather than inventing a granter here.
type RosterEntry struct {
	PersonID             uuid.UUID
	DisplayName          string
	Email                string
	Role                 Role
	GrantedAt            time.Time
	GrantedByDisplayName string
}

// AuditEvent is one row of `v_channel_person_audit` (migration 009, FR35),
// as AccessStore.AuditTrail returns it. ActorPersonID is nil and
// ActorDisplayName is "" for pre-M2 rows and for grants with no recorded
// actor (migration 009 does not backfill granted_by_person_id/
// revoked_by_person_id) -- render "unknown" upstream rather than
// inventing an actor.
type AuditEvent struct {
	Event              string
	OccurredAt         time.Time
	SubjectPersonID    uuid.UUID
	SubjectDisplayName string
	Role               Role
	ActorPersonID      *uuid.UUID
	ActorDisplayName   string
}

// Invite is one row of `channel_invite` (migration 001, FR5-FR8; widened by
// migration 009 for M2, FR29/FR30) -- a single-use, high-entropy code a
// Channel's Creator (or Co-Creator, FR32) generates to let another Person
// accept a specific tier (Co-Creator or Analyst).
type Invite struct {
	ID                 uuid.UUID
	ChannelID          uuid.UUID
	Code               string
	CreatedByPersonID  uuid.UUID
	CreatedAt          time.Time
	ConsumedAt         *time.Time
	ConsumedByPersonID *uuid.UUID
	InvalidatedAt      *time.Time
	// Role is the tier this invite grants on accept (migration 009, FR29/
	// FR30) -- RoleCoCreator or RoleAnalyst; RoleCreator is never a valid
	// invite role (FR25/FR29: only Channel-connect grants Founder).
	Role Role
}

// -- migration 002: idea -> verdict version -> schedule draft -> committed
// entry -> published video -> metrics (LB3 record chain, issue #1569).

// Idea is one row of `idea` (migration 002) -- the stable identity LB3
// requires. Everything downstream (research notes, verdicts, schedule
// entries) references an Idea by ID, never by copying its title.
type Idea struct {
	ID                uuid.UUID
	ChannelID         uuid.UUID
	Title             string
	CreatedByPersonID uuid.UUID
	CreatedAt         time.Time
}

// ResearchNote is one row of `research_note` (migration 002, FR9/FR10).
// IdeaID is nil when the note predates an Idea. SourceURL is nil for an
// uncited note (FR10) -- distinct from an empty string.
type ResearchNote struct {
	ID             uuid.UUID
	ChannelID      uuid.UUID
	IdeaID         *uuid.UUID
	Text           string
	SourceURL      *string
	AuthorPersonID uuid.UUID
	CreatedAt      time.Time
	IdempotencyKey string
}

// VerdictValue is `viability_verdict.verdict` (migration 002, FR12).
type VerdictValue string

const (
	VerdictViable            VerdictValue = "viable"
	VerdictNotViable         VerdictValue = "not-viable"
	VerdictNeedsMoreResearch VerdictValue = "needs-more-research"
)

// Verdict is one row of `viability_verdict` (migration 002) -- an
// append-only version log (FR12), never UPDATEd. See VerdictStore.Append.
type Verdict struct {
	ID             uuid.UUID
	IdeaID         uuid.UUID
	Version        int
	Verdict        VerdictValue
	Reasoning      string
	AuthorPersonID uuid.UUID
	CreatedAt      time.Time
	IdempotencyKey string
	// CitedResearchNoteIDs is FR11's cited-note list, backed by
	// `verdict_citation` -- populated on read by joining that table, not a
	// column on `viability_verdict` itself.
	CitedResearchNoteIDs []uuid.UUID
}

// PacingPolicy is one row of `pacing_policy` (migration 002, FR17) --
// natural key = Channel (channel_id UNIQUE), so PacingStore.Upsert
// converges on repeated calls with identical values (NFR2).
type PacingPolicy struct {
	ID                   uuid.UUID
	ChannelID            uuid.UUID
	TargetUploadsPerWeek float64
	PreferredDays        []string
	UpdatedAt            time.Time
	UpdatedByPersonID    uuid.UUID
}

// ScheduleState is `schedule_entry.state` (migration 002, FR16/FR19/FR20).
type ScheduleState string

const (
	ScheduleStateDraft     ScheduleState = "draft"
	ScheduleStateCommitted ScheduleState = "committed"
)

// ScheduleEntry is one row of `schedule_entry` (migration 002) -- the
// draft-and-committed record, one row per proposed slot. VerdictID is the
// FK to the specific Verdict *version* that judged the Idea viable --
// LB3's load-bearing link, never nil.
type ScheduleEntry struct {
	ID                 uuid.UUID
	ChannelID          uuid.UUID
	IdeaID             uuid.UUID
	VerdictID          uuid.UUID
	ProposedPublishAt  time.Time
	State              ScheduleState
	ApprovedByPersonID *uuid.UUID
	ApprovedAt         *time.Time
	CreatedByPersonID  uuid.UUID
	CreatedAt          time.Time
	UpdatedAt          time.Time
	IdempotencyKey     string
}

// ScheduleEntryDetail is one `schedule_entry` row joined with its bound
// Idea title, its bound Verdict version/value/reasoning (schedule_entry.
// verdict_id -- LB3), the approver's display name (empty when not
// approved), and -- when Published holds -- the identity of the video
// that fulfilled it (issue #1580, C8: FR19/FR20). Published mirrors
// ScheduleStore.IsPublished exactly: a live (auto/confirmed)
// video_schedule_match to a synced_video whose published_at is non-null.
// Backs `web`'s GET /channels/{id}/schedule page.
type ScheduleEntryDetail struct {
	Entry               ScheduleEntry
	IdeaTitle           string
	VerdictVersion      int
	Verdict             VerdictValue
	VerdictReasoning    string
	ApproverName        string
	Published           bool
	PublishedVideoID    string // YouTube video id; "" unless Published.
	PublishedVideoTitle string
}

// PrivacyStatus is `synced_video.privacy_status` (migration 002, FR14).
type PrivacyStatus string

const (
	PrivacyStatusPublic   PrivacyStatus = "public"
	PrivacyStatusPrivate  PrivacyStatus = "private"
	PrivacyStatusUnlisted PrivacyStatus = "unlisted"
)

// SyncedVideo is one row of `synced_video` (migration 002, FR14/FR21) --
// the read model of what YouTube actually says. UNIQUE(channel_id,
// youtube_video_id) is the natural key SyncStore.UpsertVideos upserts on.
type SyncedVideo struct {
	ID               uuid.UUID
	ChannelID        uuid.UUID
	YouTubeVideoID   string
	Title            string
	PrivacyStatus    PrivacyStatus
	PublishAt        *time.Time
	PublishedAt      *time.Time
	IsScheduledDraft bool
	LastSyncedAt     time.Time
}

// VideoMetrics is one row of `video_metrics` (migration 002, FR21) -- M1
// stores views + retention + CTR/impressions only. UNIQUE(synced_video_id,
// measured_at) is the natural key SyncStore.UpsertMetrics upserts on.
// Fields are pointers because YouTube Analytics can omit any of them.
type VideoMetrics struct {
	ID                         uuid.UUID
	SyncedVideoID              uuid.UUID
	Views                      *int64
	AverageViewDurationSeconds *float64
	AverageViewPercentage      *float64
	Impressions                *int64
	ImpressionCTR              *float64
	MeasuredAt                 time.Time
}

// MatchState is `video_schedule_match.state` (migration 002, FR22/FR23).
type MatchState string

const (
	MatchStateAuto      MatchState = "auto"
	MatchStatePending   MatchState = "pending"
	MatchStateConfirmed MatchState = "confirmed"
	MatchStateRejected  MatchState = "rejected"
)

// VideoScheduleMatch is one row of `video_schedule_match` (migration 002,
// FR22/FR23) -- the outcome link between a SyncedVideo and the
// ScheduleEntry it fulfilled. ScheduleEntryID is nil when a video has
// arrived with no confident match yet, pending MatchStore.Resolve.
type VideoScheduleMatch struct {
	ID                 uuid.UUID
	SyncedVideoID      uuid.UUID
	ScheduleEntryID    *uuid.UUID
	Confidence         float64
	State              MatchState
	ResolvedByPersonID *uuid.UUID
	ResolvedAt         *time.Time
	CreatedAt          time.Time
}

// MatchCandidate is one committed `schedule_entry` eligible for
// worker/sync's matcher (issue #1581, FR22/FR23): a committed entry on a
// Channel with no existing live (auto/confirmed) video_schedule_match,
// joined with its bound idea's title -- schedule_entry itself carries no
// title, only idea_id. MatchStore.ListCandidates returns these.
type MatchCandidate struct {
	ScheduleEntryID   uuid.UUID
	IdeaTitle         string
	ProposedPublishAt time.Time
}

// PredictionVsOutcome is one row of the `v_prediction_vs_outcome` view
// (migration 002, FR24) -- the pre-joined idea x current-verdict x
// committed-schedule-entry x resolved-match x synced-video x
// latest-metrics comparison read. See that view's SQL comment for exactly
// which rows qualify.
type PredictionVsOutcome struct {
	IdeaID                     uuid.UUID
	ChannelID                  uuid.UUID
	IdeaTitle                  string
	VerdictID                  uuid.UUID
	VerdictVersion             int
	Verdict                    VerdictValue
	VerdictReasoning           string
	ScheduleEntryID            uuid.UUID
	ProposedPublishAt          time.Time
	ApprovedAt                 *time.Time
	MatchID                    uuid.UUID
	MatchState                 MatchState
	MatchConfidence            float64
	SyncedVideoID              uuid.UUID
	YouTubeVideoID             string
	VideoTitle                 string
	PublishedAt                *time.Time
	Views                      *int64
	AverageViewDurationSeconds *float64
	AverageViewPercentage      *float64
	Impressions                *int64
	ImpressionCTR              *float64
	MetricsMeasuredAt          time.Time
}

// PredictionOutcome is one row of BrowseStore.PredictionVsOutcome (issue
// #1582, FR24, C10's comparison read) -- an idea's committed, matched
// (auto or confirmed), published video, alongside the SPECIFIC
// viability_verdict version bound to that schedule_entry
// (schedule_entry.verdict_id, LB3's FK chain) -- never the idea's current
// verdict. Deliberately NOT the same shape as PredictionVsOutcome above:
// see BrowseStore.PredictionVsOutcome's doc (browse.go) for why this is a
// distinct, corrected query rather than a read of migration 002's
// v_prediction_vs_outcome view, plus the verdict author fields that view
// does not carry.
type PredictionOutcome struct {
	IdeaID    uuid.UUID
	IdeaTitle string

	VerdictID                uuid.UUID
	VerdictVersion           int
	Verdict                  VerdictValue
	VerdictReasoning         string
	VerdictAuthorPersonID    uuid.UUID
	VerdictAuthorDisplayName string
	VerdictCreatedAt         time.Time

	ScheduleEntryID   uuid.UUID
	ProposedPublishAt time.Time
	ApprovedAt        *time.Time

	MatchID         uuid.UUID
	MatchState      MatchState // always MatchStateAuto or MatchStateConfirmed.
	MatchConfidence float64

	SyncedVideoID  uuid.UUID
	YouTubeVideoID string
	VideoTitle     string
	PublishedAt    *time.Time

	Views                      *int64
	AverageViewDurationSeconds *float64
	AverageViewPercentage      *float64
	Impressions                *int64
	ImpressionCTR              *float64
	MetricsMeasuredAt          time.Time
}

// Cadence is `strategy.cadence` (migration 008, issue #1637) -- a
// Strategy's own recurrence, independent of and finer-grained than the
// Channel-wide pacing_policy (FR17).
type Cadence string

const (
	CadenceWeekly   Cadence = "weekly"
	CadenceBiweekly Cadence = "biweekly"
	CadenceMonthly  Cadence = "monthly"
)

// Strategy is one row of `strategy` (migration 008, issue #1637) -- a
// cadence built directly from one or more viability_verdict rows (via
// strategy_verdict), sitting between viability verdicts and scheduling.
// PreferredWeekday is "" for no day preference, else a full English
// weekday name in the same vocabulary as PacingPolicy.PreferredDays.
type Strategy struct {
	ID                uuid.UUID
	ChannelID         uuid.UUID
	Title             string
	Cadence           Cadence
	PreferredWeekday  string
	Active            bool
	CreatedByPersonID uuid.UUID
	CreatedAt         time.Time
	UpdatedAt         time.Time
	IdempotencyKey    string
}

// StrategyVerdictDetail is one `strategy_verdict` row joined with its
// verdict's version and idea id/title -- what StrategyStore.GetByID/
// ListByChannel resolve into StrategyDetail.Verdicts, and what
// mcp/tools/strategy.go renders per linked verdict.
type StrategyVerdictDetail struct {
	VerdictID      uuid.UUID
	VerdictVersion int
	IdeaID         uuid.UUID
	IdeaTitle      string
}

// StrategyDetail is a Strategy plus every viability_verdict it's built
// from (StrategyVerdictDetail, migration 008) -- StrategyStore.GetByID/
// ListByChannel/Save's return shape. The same verdict may appear on more
// than one StrategyDetail (a verdict can ground multiple Strategies).
type StrategyDetail struct {
	Strategy
	Verdicts []StrategyVerdictDetail
}

// IdeaOverview is one Idea plus its current verdict (all four
// CurrentVerdict* fields nil if none has been recorded yet) --
// BrowseStore.IdeasWithCurrentVerdict's row shape, backing
// get_channel_overview's ideas section (issue #1582, FR24).
type IdeaOverview struct {
	Idea
	CurrentVerdictID        *uuid.UUID
	CurrentVerdictVersion   *int
	CurrentVerdict          *VerdictValue
	CurrentVerdictReasoning *string
}

// IdeaVerdictSummary is one Channel's single most-recently-recorded
// viability_verdict across ALL of its Ideas -- MyWorkStore.
// SummariesForPerson's ChannelWorkSummary.LatestVerdict field (issue
// #1717, FR27). Deliberately not the same thing as any one Idea's
// *current* verdict (v_current_verdict/IdeaOverview above): each Idea
// keeps its own independent version sequence, so this is a cross-Idea
// "what did this Channel most recently judge" read, ordered by
// viability_verdict.created_at, not by version.
type IdeaVerdictSummary struct {
	IdeaID    uuid.UUID
	IdeaTitle string

	VerdictID uuid.UUID
	Version   int
	Verdict   VerdictValue
	Reasoning string
	CreatedAt time.Time
}

// ScheduleDraftState is one Channel's schedule_entry counts --
// MyWorkStore.SummariesForPerson's ChannelWorkSummary.ScheduleState field
// (issue #1717, FR27). NextProposedPublishAt is the earliest
// proposed_publish_at still at or after the query time across BOTH draft
// and committed entries, nil if none is upcoming -- the zero value (all
// fields zero/nil) is the correct "no schedule_entry rows yet" state, not
// a sentinel error.
type ScheduleDraftState struct {
	DraftCount            int
	CommittedCount        int
	NextProposedPublishAt *time.Time
}

// ChannelWorkSummary is one Channel's cross-section "what's going on
// here" summary -- MyWorkStore.SummariesForPerson's row shape (issue
// #1717, FR27/FR28). Content-parity with BrowseStore/get_channel_overview
// (C10, issue #1582/FR24): this is the same four-section summary,
// assembled across every Channel a Person currently holds an open role
// on, rather than for one Channel a caller already picked.
type ChannelWorkSummary struct {
	Channel Channel
	Role    Role

	// LatestNotes is most-recent-first, capped at SummariesForPerson's
	// notesPerChannel argument. Empty (never nil) when the Channel has no
	// research notes yet.
	LatestNotes []ResearchNote
	// LatestVerdict is nil when the Channel has no Idea with a recorded
	// verdict yet.
	LatestVerdict *IdeaVerdictSummary
	ScheduleState ScheduleDraftState
	// LatestOutcome is nil when the Channel has no published,
	// live-matched video against a committed schedule entry yet -- see
	// BrowseStore.PredictionVsOutcome's qualifying-row rule, which this
	// mirrors exactly (LB3: the verdict version bound to the schedule
	// entry, not the Idea's current verdict).
	LatestOutcome *PredictionOutcome
}
