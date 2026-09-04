// C7's pacing-policy + schedule-drafting MCP tool group (issue #1579,
// FR16-FR18): set_pacing_policy/get_pacing_policy (FR17), get_drafting_context
// (FR18's context half), save_schedule_draft (FR16 + FR18's flagging half),
// and list_schedule_entries. Also C8's commit_schedule_draft (FR19, issue
// #1648), added later than the rest of this group -- see
// ../../ARCHITECTURE.md's "NFR3 interface allocation" note for why C8 was
// originally web-only and what changed. See ../../ARCHITECTURE.md's "FR17
// authority" note for who may call set_pacing_policy, and
// ../../store/pacing.go / ../../store/schedule.go (PacingStore/
// ScheduleStore, already real from #1569) for the storage this group reads
// and writes.
package tools

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/whale-net/everything/audience_score_system/mcp/server"
	"github.com/whale-net/everything/audience_score_system/store"
)

// -- shared rendering ---------------------------------------------------------

// PacingPolicyOutput is one `pacing_policy` row (FR17), as
// set_pacing_policy, get_pacing_policy, and get_drafting_context all render
// it.
type PacingPolicyOutput struct {
	ChannelID            string   `json:"channel_id" jsonschema:"the Channel this policy governs, as a UUID string"`
	TargetUploadsPerWeek float64  `json:"target_uploads_per_week" jsonschema:"target number of uploads per calendar week (Monday-Sunday UTC)"`
	PreferredDays        []string `json:"preferred_days" jsonschema:"full English weekday names (e.g. Monday, Thursday) this Channel prefers to publish on; empty means no day preference"`
	UpdatedAt            string   `json:"updated_at" jsonschema:"when this policy was last set or changed, RFC3339"`
	UpdatedByPersonID    string   `json:"updated_by_person_id" jsonschema:"the Person who last set this policy, as a UUID string"`
	UpdatedByDisplayName string   `json:"updated_by_display_name" jsonschema:"that Person's display name"`
}

func toPacingPolicyOutput(p store.PacingPolicy, updatedByDisplayName string) PacingPolicyOutput {
	return PacingPolicyOutput{
		ChannelID:            p.ChannelID.String(),
		TargetUploadsPerWeek: p.TargetUploadsPerWeek,
		PreferredDays:        p.PreferredDays,
		UpdatedAt:            p.UpdatedAt.Format(time.RFC3339),
		UpdatedByPersonID:    p.UpdatedByPersonID.String(),
		UpdatedByDisplayName: updatedByDisplayName,
	}
}

// renderPacingPolicy resolves p's updater's display name and renders the
// result as PacingPolicyOutput -- the single call site every tool that
// renders a pacing policy shares, so they can never disagree on shape.
func renderPacingPolicy(ctx context.Context, persons store.PersonStore, p store.PacingPolicy) (PacingPolicyOutput, error) {
	updater, err := persons.GetByID(ctx, p.UpdatedByPersonID)
	if err != nil {
		return PacingPolicyOutput{}, fmt.Errorf("load pacing policy updater: %w", err)
	}
	return toPacingPolicyOutput(p, updater.DisplayName), nil
}

// weekdayNames maps a lowercased, trimmed weekday name to its canonical
// full English form -- the only vocabulary set_pacing_policy's
// preferred_days accepts and PacingPolicy.PreferredDays ever stores, so a
// stored value and time.Time.Weekday().String() (used by
// computeScheduleFlags's off_preferred_day check) always compare equal for
// a matching day.
var weekdayNames = map[string]string{
	"sunday":    "Sunday",
	"monday":    "Monday",
	"tuesday":   "Tuesday",
	"wednesday": "Wednesday",
	"thursday":  "Thursday",
	"friday":    "Friday",
	"saturday":  "Saturday",
}

// parseWeekdayName validates raw against weekdayNames case-insensitively,
// returning the canonical full English form.
func parseWeekdayName(raw string) (string, error) {
	canon, ok := weekdayNames[strings.ToLower(strings.TrimSpace(raw))]
	if !ok {
		return "", fmt.Errorf("preferred_days contains an invalid weekday %q -- must be a full English weekday name (Monday..Sunday)", raw)
	}
	return canon, nil
}

// ScheduleEntryOutput is one `schedule_entry` row, as save_schedule_draft,
// get_drafting_context, and list_schedule_entries all render it -- the
// idea, the exact verdict version it's bound to (LB3), state, approver, and
// timestamps an agent needs without a follow-up call.
type ScheduleEntryOutput struct {
	ScheduleEntryID string `json:"schedule_entry_id" jsonschema:"this schedule_entry's ID, as a UUID string"`
	ChannelID       string `json:"channel_id" jsonschema:"the Channel this entry belongs to, as a UUID string"`
	IdeaID          string `json:"idea_id" jsonschema:"the Idea this entry schedules, as a UUID string"`
	IdeaTitle       string `json:"idea_title" jsonschema:"that Idea's title"`
	VerdictID       string `json:"verdict_id" jsonschema:"the specific viability_verdict version this entry is bound to (LB3), as a UUID string -- never a copy of the verdict text, never just the idea_id"`
	VerdictVersion  int    `json:"verdict_version" jsonschema:"that verdict version's number, so a caller can tell a stale binding from a fresh one"`
	// ProposedPublishAt is formatted with sub-second precision
	// (RFC3339Nano, still a valid RFC3339 string) rather than plain
	// RFC3339 -- list_schedule_entries' since/before pagination (issue
	// #1812) round-trips this value back as a cursor, mirroring
	// ResearchNoteOutput.CreatedAt's #1808 fix.
	ProposedPublishAt     string  `json:"proposed_publish_at" jsonschema:"the proposed (or, once committed, the approved) publish time, RFC3339 (with sub-second precision -- use verbatim as a since/before pagination cursor)"`
	State                 string  `json:"state" jsonschema:"draft or committed"`
	ApprovedByPersonID    *string `json:"approved_by_person_id,omitempty" jsonschema:"the Founder or Co-Creator who approved this entry, as a UUID string, if committed"`
	ApprovedByDisplayName *string `json:"approved_by_display_name,omitempty" jsonschema:"that Person's display name, if committed"`
	ApprovedAt            *string `json:"approved_at,omitempty" jsonschema:"when this entry was approved, RFC3339, if committed"`
	CreatedByPersonID     string  `json:"created_by_person_id" jsonschema:"the Person who proposed this entry, as a UUID string"`
	CreatedByDisplayName  string  `json:"created_by_display_name" jsonschema:"that Person's display name"`
	CreatedAt             string  `json:"created_at" jsonschema:"when this entry was first proposed, RFC3339"`
	UpdatedAt             string  `json:"updated_at" jsonschema:"when this entry was last changed, RFC3339"`
}

func toScheduleEntryOutput(e store.ScheduleEntry, ideaTitle string, verdictVersion int, createdByDisplayName string, approvedByDisplayName *string) ScheduleEntryOutput {
	out := ScheduleEntryOutput{
		ScheduleEntryID:      e.ID.String(),
		ChannelID:            e.ChannelID.String(),
		IdeaID:               e.IdeaID.String(),
		IdeaTitle:            ideaTitle,
		VerdictID:            e.VerdictID.String(),
		VerdictVersion:       verdictVersion,
		ProposedPublishAt:    e.ProposedPublishAt.Format(time.RFC3339Nano),
		State:                string(e.State),
		CreatedByPersonID:    e.CreatedByPersonID.String(),
		CreatedByDisplayName: createdByDisplayName,
		CreatedAt:            e.CreatedAt.Format(time.RFC3339),
		UpdatedAt:            e.UpdatedAt.Format(time.RFC3339),
	}
	if e.ApprovedByPersonID != nil {
		id := e.ApprovedByPersonID.String()
		out.ApprovedByPersonID = &id
		out.ApprovedByDisplayName = approvedByDisplayName
	}
	if e.ApprovedAt != nil {
		at := e.ApprovedAt.Format(time.RFC3339)
		out.ApprovedAt = &at
	}
	return out
}

// renderScheduleEntry resolves e's idea title, bound verdict version,
// author display name, and (if committed) approver display name, and
// renders the result as ScheduleEntryOutput -- the single call site every
// tool that renders a schedule entry shares.
func renderScheduleEntry(ctx context.Context, ideas store.IdeaStore, verdicts store.VerdictStore, persons store.PersonStore, e store.ScheduleEntry) (ScheduleEntryOutput, error) {
	idea, err := ideas.GetByID(ctx, e.IdeaID)
	if err != nil {
		return ScheduleEntryOutput{}, fmt.Errorf("load schedule entry's idea: %w", err)
	}

	verdict, err := verdicts.GetByID(ctx, e.VerdictID)
	if err != nil {
		return ScheduleEntryOutput{}, fmt.Errorf("load schedule entry's bound verdict: %w", err)
	}

	creator, err := persons.GetByID(ctx, e.CreatedByPersonID)
	if err != nil {
		return ScheduleEntryOutput{}, fmt.Errorf("load schedule entry's author: %w", err)
	}

	var approvedByDisplayName *string
	if e.ApprovedByPersonID != nil {
		approver, err := persons.GetByID(ctx, *e.ApprovedByPersonID)
		if err != nil {
			return ScheduleEntryOutput{}, fmt.Errorf("load schedule entry's approver: %w", err)
		}
		approvedByDisplayName = &approver.DisplayName
	}

	return toScheduleEntryOutput(e, idea.Title, verdict.Version, creator.DisplayName, approvedByDisplayName), nil
}

// -- set_pacing_policy --------------------------------------------------------

// SetPacingPolicyInput is set_pacing_policy's argument schema (FR17).
// ChannelID is a JSON-wire string, not a uuid.UUID field directly -- see
// ../server/fakes_test.go's scopedInput doc for why.
type SetPacingPolicyInput struct {
	ChannelID            string   `json:"channel_id" jsonschema:"Channel this policy governs, as a UUID string"`
	TargetUploadsPerWeek float64  `json:"target_uploads_per_week" jsonschema:"target number of uploads per calendar week; must be greater than 0"`
	PreferredDays        []string `json:"preferred_days,omitempty" jsonschema:"full English weekday names (e.g. Monday, Thursday) this Channel prefers to publish on; may be empty for no preference"`
	// IdempotencyKeyArg backs IdempotencyKey() below -- named ...Arg because
	// a Go type cannot declare both a field and a method named
	// IdempotencyKey. Accepted for uniformity with every other write tool,
	// but not required: Upsert's (channel_id) natural key already makes
	// this safe under replay by construction (NFR2).
	IdempotencyKeyArg string `json:"idempotency_key,omitempty" jsonschema:"optional caller-supplied idempotency key; not required -- repeated calls with the same channel_id converge to one row regardless"`
}

// ChannelScopeID implements server.ChannelScoped.
func (i SetPacingPolicyInput) ChannelScopeID() uuid.UUID {
	id, _ := uuid.Parse(i.ChannelID)
	return id
}

// IdempotencyKey implements server.IdempotencyKeyed.
func (i SetPacingPolicyInput) IdempotencyKey() string { return i.IdempotencyKeyArg }

// registerSetPacingPolicy registers set_pacing_policy via
// server.RegisterWrite -- Channel-scoped via store.CanWrite (Founder,
// Co-Creator, and Analyst may all call it; see ../../ARCHITECTURE.md's
// "FR17 authority" note for why).
func registerSetPacingPolicy(reg *server.Registry, pacing store.PacingStore, persons store.PersonStore) {
	server.RegisterWrite(reg, &mcp.Tool{
		Name: "set_pacing_policy",
		Description: "Define or update a Channel's pacing policy: target uploads/week and optional preferred weekdays " +
			"(FR17). System-owned -- never synced from or overwritten by YouTube's own schedule. Upserts by Channel " +
			"(natural key): repeated calls with the same channel_id converge to one row with the latest values " +
			"(NFR2 by construction) -- idempotency_key is accepted for uniformity but not required. Founder, " +
			"Co-Creator, and Analyst may all call this tool.",
	}, setPacingPolicyMutate(pacing), setPacingPolicyRender(pacing, persons))
}

func setPacingPolicyMutate(pacing store.PacingStore) server.WriteMutate[SetPacingPolicyInput] {
	return func(ctx context.Context, in SetPacingPolicyInput) (uuid.UUID, error) {
		if in.TargetUploadsPerWeek <= 0 {
			return uuid.Nil, fmt.Errorf("target_uploads_per_week must be greater than 0")
		}

		channelID, err := uuid.Parse(in.ChannelID)
		if err != nil {
			return uuid.Nil, fmt.Errorf("channel_id is not a valid UUID: %w", err)
		}

		days := make([]string, 0, len(in.PreferredDays))
		for _, raw := range in.PreferredDays {
			day, err := parseWeekdayName(raw)
			if err != nil {
				return uuid.Nil, err
			}
			days = append(days, day)
		}

		person := server.PersonFromContext(ctx)
		if person == nil {
			return uuid.Nil, fmt.Errorf("unauthenticated: no caller credential resolved")
		}

		if _, err := pacing.Upsert(ctx, channelID, store.PacingPolicy{
			TargetUploadsPerWeek: in.TargetUploadsPerWeek,
			PreferredDays:        days,
			UpdatedByPersonID:    person.ID,
		}); err != nil {
			return uuid.Nil, err
		}
		// ref = channelID, the natural key PacingStore.Get reads by -- there
		// is exactly one pacing_policy row per Channel, so this uniquely
		// identifies the row setPacingPolicyRender must reload.
		return channelID, nil
	}
}

// setPacingPolicyRender always re-reads the policy (and its updater's
// current display name) from Postgres rather than trusting anything cached
// from mutate -- see server.RegisterWrite's doc on why render runs on
// every call, replay included.
func setPacingPolicyRender(pacing store.PacingStore, persons store.PersonStore) server.WriteRender[PacingPolicyOutput] {
	return func(ctx context.Context, ref uuid.UUID) (*mcp.CallToolResult, PacingPolicyOutput, error) {
		p, ok, err := pacing.Get(ctx, ref)
		if err != nil {
			return nil, PacingPolicyOutput{}, fmt.Errorf("load saved pacing policy: %w", err)
		}
		if !ok {
			return nil, PacingPolicyOutput{}, fmt.Errorf("pacing policy not found immediately after save")
		}
		out, err := renderPacingPolicy(ctx, persons, p)
		if err != nil {
			return nil, PacingPolicyOutput{}, err
		}
		return nil, out, nil
	}
}

// -- get_pacing_policy ---------------------------------------------------------

// GetPacingPolicyInput is get_pacing_policy's argument schema.
type GetPacingPolicyInput struct {
	ChannelID string `json:"channel_id" jsonschema:"Channel to read the pacing policy for, as a UUID string"`
}

// ChannelScopeID implements server.ChannelScoped.
func (i GetPacingPolicyInput) ChannelScopeID() uuid.UUID {
	id, _ := uuid.Parse(i.ChannelID)
	return id
}

// GetPacingPolicyOutput is get_pacing_policy's structured result. Policy is
// nil (not a zero-valued PacingPolicyOutput, which would misleadingly read
// as "0 uploads/week") when the Channel has never had one set.
type GetPacingPolicyOutput struct {
	Policy *PacingPolicyOutput `json:"policy" jsonschema:"the Channel's pacing policy, or null if none has been set yet"`
}

func registerGetPacingPolicy(reg *server.Registry, pacing store.PacingStore, persons store.PersonStore) {
	server.RegisterRead(reg, &mcp.Tool{
		Name: "get_pacing_policy",
		Description: "Read a Channel's pacing policy (FR17). Returns policy: null -- not a zero-valued policy -- when " +
			"none has been set yet.",
	}, getPacingPolicyHandler(pacing, persons))
}

func getPacingPolicyHandler(pacing store.PacingStore, persons store.PersonStore) mcp.ToolHandlerFor[GetPacingPolicyInput, GetPacingPolicyOutput] {
	return func(ctx context.Context, _ *mcp.CallToolRequest, in GetPacingPolicyInput) (*mcp.CallToolResult, GetPacingPolicyOutput, error) {
		channelID, err := uuid.Parse(in.ChannelID)
		if err != nil {
			return nil, GetPacingPolicyOutput{}, fmt.Errorf("channel_id is not a valid UUID: %w", err)
		}

		p, ok, err := pacing.Get(ctx, channelID)
		if err != nil {
			return nil, GetPacingPolicyOutput{}, err
		}
		if !ok {
			return nil, GetPacingPolicyOutput{}, nil
		}

		out, err := renderPacingPolicy(ctx, persons, p)
		if err != nil {
			return nil, GetPacingPolicyOutput{}, err
		}
		return nil, GetPacingPolicyOutput{Policy: &out}, nil
	}
}

// -- get_drafting_context -------------------------------------------------------

// draftingContextWindow bounds get_drafting_context's optional `around`
// window on either side -- generous enough to see the surrounding weeks'
// pacing pressure without dumping a Channel's entire history when a caller
// only wants to plan around one date.
const draftingContextWindow = 14 * 24 * time.Hour

// GetDraftingContextInput is get_drafting_context's argument schema
// (FR18's context half).
type GetDraftingContextInput struct {
	ChannelID string     `json:"channel_id" jsonschema:"Channel to build drafting context for, as a UUID string"`
	Around    *time.Time `json:"around,omitempty" jsonschema:"optional center of a +/-14 day window restricting the synced schedule and existing schedule_entry rows returned; omit to see everything on file"`

	// Limit caps synced_schedule and schedule_entries independently, on
	// top of any Around window -- without it, both fetch a Channel's
	// entire synced_video/schedule_entry history and can exceed an MCP
	// client's response-size cap (issue #1812).
	Limit int `json:"limit,omitempty" jsonschema:"Maximum rows to return for EACH of synced_schedule and schedule_entries (default 50, applied independently to each list). The response's truncated flag is set when either list has more matching rows -- narrow via around to page through the rest."`
}

// defaultDraftingContextLimit bounds get_drafting_context's synced_schedule
// and schedule_entries lists (independently) when a caller supplies limit
// <= 0 -- without this, a Channel with enough history in either exceeds
// the calling MCP client's response-size cap and the tool call fails
// outright with no way to retrieve the data in pages (issue #1812).
const defaultDraftingContextLimit = 50

// ChannelScopeID implements server.ChannelScoped.
func (i GetDraftingContextInput) ChannelScopeID() uuid.UUID {
	id, _ := uuid.Parse(i.ChannelID)
	return id
}

// GetDraftingContextOutput is get_drafting_context's structured result --
// everything an agent needs to propose a slot that isn't blind (FR18).
type GetDraftingContextOutput struct {
	Policy          *PacingPolicyOutput   `json:"policy" jsonschema:"the Channel's pacing policy (FR17), or null if none has been set yet"`
	SyncedSchedule  []ScheduleVideo       `json:"synced_schedule" jsonschema:"the Channel's synced YouTube schedule (FR15), including scheduled/private drafts"`
	ScheduleEntries []ScheduleEntryOutput `json:"schedule_entries" jsonschema:"the Channel's existing schedule_entry rows (draft and committed)"`
	Truncated       bool                  `json:"truncated" jsonschema:"True if synced_schedule and/or schedule_entries was capped at limit"`
}

func registerGetDraftingContext(reg *server.Registry, pacing store.PacingStore, sync store.SyncStore, schedules store.ScheduleStore, ideas store.IdeaStore, verdicts store.VerdictStore, persons store.PersonStore) {
	server.RegisterRead(reg, &mcp.Tool{
		Name: "get_drafting_context",
		Description: "Read everything needed to propose a schedule slot that isn't blind (FR18): the Channel's pacing " +
			"policy (FR17), synced YouTube schedule (FR15), and existing draft/committed schedule_entry rows. Optionally " +
			"restrict to a +/-14 day window around `around`. synced_schedule and schedule_entries are each independently " +
			"capped at limit (default 50); see truncated. Narrow the around window to page through the rest of a " +
			"truncated response.",
	}, getDraftingContextHandler(pacing, sync, schedules, ideas, verdicts, persons))
}

func getDraftingContextHandler(pacing store.PacingStore, sync store.SyncStore, schedules store.ScheduleStore, ideas store.IdeaStore, verdicts store.VerdictStore, persons store.PersonStore) mcp.ToolHandlerFor[GetDraftingContextInput, GetDraftingContextOutput] {
	return func(ctx context.Context, _ *mcp.CallToolRequest, in GetDraftingContextInput) (*mcp.CallToolResult, GetDraftingContextOutput, error) {
		channelID := in.ChannelScopeID()
		var out GetDraftingContextOutput

		p, ok, err := pacing.Get(ctx, channelID)
		if err != nil {
			return nil, out, fmt.Errorf("get_drafting_context: load pacing policy: %w", err)
		}
		if ok {
			rendered, err := renderPacingPolicy(ctx, persons, p)
			if err != nil {
				return nil, out, err
			}
			out.Policy = &rendered
		}

		// from/before: the +/-14 day window's upper edge is passed as
		// ScheduleStore.ListByChannel's exclusive `before` bound rather
		// than an inclusive one (this handler's pre-#1808/#1812 Go-side
		// filtering treated it as inclusive) -- an entry landing on the
		// exact nanosecond of that boundary is not a real-world case this
		// window is meant to catch.
		var from, before *time.Time
		if in.Around != nil {
			f := in.Around.Add(-draftingContextWindow)
			b := in.Around.Add(draftingContextWindow)
			from, before = &f, &b
		}

		limit := in.Limit
		if limit <= 0 {
			limit = defaultDraftingContextLimit
		}

		vids, syncedTruncated, err := sync.ListSchedule(ctx, channelID, from, before, true, limit)
		if err != nil {
			return nil, out, fmt.Errorf("get_drafting_context: list synced schedule: %w", err)
		}
		out.SyncedSchedule = make([]ScheduleVideo, 0, len(vids))
		for _, v := range vids {
			out.SyncedSchedule = append(out.SyncedSchedule, ScheduleVideo{
				YouTubeVideoID:   v.YouTubeVideoID,
				Title:            v.Title,
				PrivacyStatus:    string(v.PrivacyStatus),
				PublishAt:        v.PublishAt,
				PublishedAt:      v.PublishedAt,
				IsScheduledDraft: v.IsScheduledDraft,
				LastSyncedAt:     v.LastSyncedAt,
			})
		}

		entries, entriesTruncated, err := schedules.ListByChannel(ctx, channelID, from, before, limit)
		if err != nil {
			return nil, out, fmt.Errorf("get_drafting_context: list schedule entries: %w", err)
		}
		out.ScheduleEntries = make([]ScheduleEntryOutput, 0, len(entries))
		for _, e := range entries {
			rendered, err := renderScheduleEntry(ctx, ideas, verdicts, persons, e)
			if err != nil {
				return nil, out, err
			}
			out.ScheduleEntries = append(out.ScheduleEntries, rendered)
		}

		out.Truncated = syncedTruncated || entriesTruncated

		return nil, out, nil
	}
}

// -- save_schedule_draft --------------------------------------------------------

// collisionWindow is the default proximity window save_schedule_draft
// flags a proposed slot within of an existing entry (FR18) -- stated here
// and in the tool's description since M1 does not expose it as a per-call
// argument.
const collisionWindow = 12 * time.Hour

// ScheduleFlag is one FR18 advisory flag on a proposed slot -- never
// blocking (the Validation criterion: no code path refuses a draft
// because of a flag).
type ScheduleFlag struct {
	Type               string   `json:"type" jsonschema:"cadence_exceeded, off_preferred_day, or collision"`
	Explanation        string   `json:"explanation" jsonschema:"human-readable explanation of why this flag was raised"`
	ConflictingEntries []string `json:"conflicting_entries,omitempty" jsonschema:"human-readable identifiers of the schedule_entry/synced-video rows this flag is about, if any"`
}

// SaveScheduleDraftInput is save_schedule_draft's argument schema (FR16).
type SaveScheduleDraftInput struct {
	ChannelID         string `json:"channel_id" jsonschema:"Channel this draft belongs to, as a UUID string"`
	IdeaID            string `json:"idea_id" jsonschema:"the Idea being scheduled, as a UUID string; must have a viable verdict (FR16)"`
	ProposedPublishAt string `json:"proposed_publish_at" jsonschema:"the proposed publish slot, RFC3339"`
	VerdictID         string `json:"verdict_id,omitempty" jsonschema:"pin the draft to a specific viability verdict version (as a UUID string) rather than idea_id's current verdict; must belong to idea_id and be viable"`
	IdempotencyKeyArg string `json:"idempotency_key,omitempty" jsonschema:"caller-supplied idempotency key. If omitted, a retry with the identical channel_id/idea_id/proposed_publish_at converges on the same draft rather than duplicating it (NFR2)."`
}

// ChannelScopeID implements server.ChannelScoped.
func (i SaveScheduleDraftInput) ChannelScopeID() uuid.UUID {
	id, _ := uuid.Parse(i.ChannelID)
	return id
}

// IdempotencyKey implements server.IdempotencyKeyed.
func (i SaveScheduleDraftInput) IdempotencyKey() string { return i.IdempotencyKeyArg }

// SaveScheduleDraftOutput is save_schedule_draft's structured result: the
// saved entry (embedding ScheduleEntryOutput, so a caller can see exactly
// which verdict version it's bound to) plus FR18's non-blocking flags.
type SaveScheduleDraftOutput struct {
	ScheduleEntryOutput
	CadenceExceeded bool           `json:"cadence_exceeded" jsonschema:"true if this slot's week's committed+draft+synced entries exceed the pacing policy's target_uploads_per_week (FR18); always false if no policy is set"`
	OffPreferredDay bool           `json:"off_preferred_day" jsonschema:"true if this slot's weekday is not one of the pacing policy's preferred_days (FR18); always false if no policy is set or preferred_days is empty"`
	Collision       bool           `json:"collision" jsonschema:"true if this slot falls within 12h of an existing synced or schedule_entry slot (FR18)"`
	Flags           []ScheduleFlag `json:"flags" jsonschema:"detail for each true flag above, with a human-readable explanation and the conflicting entries; empty if none apply. The draft is saved regardless of any flag -- flags are advisory, never blocking."`
}

// registerSaveScheduleDraft registers save_schedule_draft via
// server.RegisterWrite, so the idempotency middleware (NFR2) applies
// automatically -- see ../server/idempotency.go. Founder, Co-Creator, and
// Analyst may all propose a draft (store.CanWrite, applied by
// RegisterWrite via ChannelScoped) -- FR16 does not name a single
// proposer persona.
func registerSaveScheduleDraft(reg *server.Registry, schedules store.ScheduleStore, ideas store.IdeaStore, verdicts store.VerdictStore, persons store.PersonStore, pacing store.PacingStore, sync store.SyncStore) {
	server.RegisterWrite(reg, &mcp.Tool{
		Name: "save_schedule_draft",
		Description: "Save a draft schedule entry for an Idea with a viable verdict (FR16), bound by foreign key to " +
			"the exact verdict version that judged it viable (LB3) -- omit verdict_id to bind the idea's current " +
			"verdict, or supply one to pin an older version. Rejects an idea whose resolved verdict is not-viable, " +
			"needs-more-research, or absent -- no row is written in that case. Otherwise always saves the draft and " +
			"separately reports non-blocking FR18 flags (cadence_exceeded, off_preferred_day, collision) -- a flag " +
			"never prevents the save. Always supply idempotency_key: a keyless retry converges on the same draft only " +
			"if channel_id/idea_id/proposed_publish_at are byte-identical to a prior call.",
	}, saveScheduleDraftMutate(schedules, ideas, verdicts), saveScheduleDraftRender(schedules, ideas, verdicts, persons, pacing, sync))
}

func saveScheduleDraftMutate(schedules store.ScheduleStore, ideas store.IdeaStore, verdicts store.VerdictStore) server.WriteMutate[SaveScheduleDraftInput] {
	return func(ctx context.Context, in SaveScheduleDraftInput) (uuid.UUID, error) {
		channelID, err := uuid.Parse(in.ChannelID)
		if err != nil {
			return uuid.Nil, fmt.Errorf("channel_id is not a valid UUID: %w", err)
		}

		ideaID, err := uuid.Parse(in.IdeaID)
		if err != nil {
			return uuid.Nil, fmt.Errorf("idea_id is not a valid UUID: %w", err)
		}

		proposedPublishAt, err := time.Parse(time.RFC3339, in.ProposedPublishAt)
		if err != nil {
			return uuid.Nil, fmt.Errorf("proposed_publish_at is not a valid RFC3339 timestamp: %w", err)
		}

		idea, err := ideas.GetByID(ctx, ideaID)
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return uuid.Nil, fmt.Errorf("idea_id does not exist")
			}
			return uuid.Nil, fmt.Errorf("load idea: %w", err)
		}
		if idea.ChannelID != channelID {
			return uuid.Nil, fmt.Errorf("idea_id does not belong to channel_id")
		}

		// Resolve the verdict this draft will bind to -- an explicit
		// verdict_id must belong to idea_id; otherwise use idea_id's current
		// verdict. Either way, reject anything but VerdictViable before
		// ever calling SaveDraft, so a rejected proposal writes nothing
		// (FR16).
		var verdict store.Verdict
		if strings.TrimSpace(in.VerdictID) != "" {
			verdictID, err := uuid.Parse(in.VerdictID)
			if err != nil {
				return uuid.Nil, fmt.Errorf("verdict_id is not a valid UUID: %w", err)
			}
			verdict, err = verdicts.GetByID(ctx, verdictID)
			if err != nil {
				if errors.Is(err, pgx.ErrNoRows) {
					return uuid.Nil, fmt.Errorf("verdict_id does not exist")
				}
				return uuid.Nil, fmt.Errorf("load verdict: %w", err)
			}
			if verdict.IdeaID != ideaID {
				return uuid.Nil, fmt.Errorf("verdict_id belongs to a different idea")
			}
		} else {
			verdict, err = verdicts.Current(ctx, ideaID)
			if err != nil {
				if errors.Is(err, pgx.ErrNoRows) {
					return uuid.Nil, fmt.Errorf("idea has no viability verdict yet -- record one via save_viability_verdict before drafting a schedule entry (FR16)")
				}
				return uuid.Nil, fmt.Errorf("load current verdict: %w", err)
			}
		}
		if verdict.Verdict != store.VerdictViable {
			return uuid.Nil, fmt.Errorf("idea's %s verdict (version %d) is %q, not %q -- save_schedule_draft only accepts a viable verdict (FR16)",
				ideaID, verdict.Version, verdict.Verdict, store.VerdictViable)
		}

		person := server.PersonFromContext(ctx)
		if person == nil {
			return uuid.Nil, fmt.Errorf("unauthenticated: no caller credential resolved")
		}

		entry, err := schedules.SaveDraft(ctx, store.SaveDraftInput{
			ChannelID:         channelID,
			IdeaID:            ideaID,
			VerdictID:         verdict.ID,
			ProposedPublishAt: proposedPublishAt,
			CreatedByPersonID: person.ID,
			IdempotencyKey:    in.IdempotencyKeyArg,
		})
		if err != nil {
			if errors.Is(err, store.ErrVerdictNotViable) {
				return uuid.Nil, fmt.Errorf("idea's verdict is not viable -- save_schedule_draft only accepts a viable verdict (FR16): %w", err)
			}
			return uuid.Nil, err
		}
		return entry.ID, nil
	}
}

// saveScheduleDraftRender always re-reads the entry (by ref, the entry.ID
// mutate returned) from Postgres, then computes FR18's flags fresh against
// current pacing/schedule/synced state -- both the first run and every
// replay alike, so nothing about flags is itself cached (LB4).
func saveScheduleDraftRender(schedules store.ScheduleStore, ideas store.IdeaStore, verdicts store.VerdictStore, persons store.PersonStore, pacing store.PacingStore, sync store.SyncStore) server.WriteRender[SaveScheduleDraftOutput] {
	return func(ctx context.Context, ref uuid.UUID) (*mcp.CallToolResult, SaveScheduleDraftOutput, error) {
		entry, err := schedules.GetByID(ctx, ref)
		if err != nil {
			return nil, SaveScheduleDraftOutput{}, fmt.Errorf("load saved schedule draft: %w", err)
		}

		base, err := renderScheduleEntry(ctx, ideas, verdicts, persons, entry)
		if err != nil {
			return nil, SaveScheduleDraftOutput{}, err
		}

		policyRow, ok, err := pacing.Get(ctx, entry.ChannelID)
		if err != nil {
			return nil, SaveScheduleDraftOutput{}, fmt.Errorf("load pacing policy for flags: %w", err)
		}
		var policy *store.PacingPolicy
		if ok {
			policy = &policyRow
		}

		// Unbounded (limit 0): computeScheduleFlags' cadence/collision
		// detection below needs every other schedule_entry and every
		// synced video, not a capped page.
		otherEntries, _, err := schedules.ListByChannel(ctx, entry.ChannelID, nil, nil, 0)
		if err != nil {
			return nil, SaveScheduleDraftOutput{}, fmt.Errorf("list schedule entries for flags: %w", err)
		}

		synced, _, err := sync.ListSchedule(ctx, entry.ChannelID, nil, nil, true, 0)
		if err != nil {
			return nil, SaveScheduleDraftOutput{}, fmt.Errorf("list synced schedule for flags: %w", err)
		}

		flags := computeScheduleFlags(entry, policy, otherEntries, synced)

		return nil, SaveScheduleDraftOutput{
			ScheduleEntryOutput: base,
			CadenceExceeded:     flags.cadenceExceeded,
			OffPreferredDay:     flags.offPreferredDay,
			Collision:           flags.collision,
			Flags:               flags.flags,
		}, nil
	}
}

// effectiveSyncedVideoTime mirrors SyncStore.ListSchedule's (store/sync.go)
// notion of a SyncedVideo's effective timestamp -- PublishAt while still a
// scheduled/private draft, else PublishedAt, nil if neither is set.
func effectiveSyncedVideoTime(v store.SyncedVideo) *time.Time {
	if v.PublishAt != nil {
		return v.PublishAt
	}
	return v.PublishedAt
}

// weekBounds returns the [Monday 00:00 UTC, next Monday 00:00 UTC) window
// containing t (converted to UTC first) -- the calendar week both
// set_pacing_policy's target_uploads_per_week and save_schedule_draft's
// cadence_exceeded flag (FR18) reason about.
func weekBounds(t time.Time) (start, end time.Time) {
	t = t.UTC()
	daysSinceMonday := (int(t.Weekday()) + 6) % 7 // time.Sunday==0 -> 6 days after Monday, time.Monday==1 -> 0, ...
	start = time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, time.UTC).AddDate(0, 0, -daysSinceMonday)
	end = start.AddDate(0, 0, 7)
	return start, end
}

// scheduleFlagsResult is computeScheduleFlags's return shape: the three
// FR18 booleans plus their detailed ScheduleFlag explanations, kept
// together so a caller (saveScheduleDraftRender) can never report a true
// boolean with no matching Flags entry or vice versa.
type scheduleFlagsResult struct {
	cadenceExceeded bool
	offPreferredDay bool
	collision       bool
	flags           []ScheduleFlag
}

// computeScheduleFlags derives FR18's non-blocking flags for entry
// (already persisted) against policy (nil if none set on the Channel),
// every other schedule_entry on the Channel, and its synced schedule.
// Never rejects anything -- every flag here is purely advisory (the
// Validation criterion: no code path refuses a draft because of a flag).
func computeScheduleFlags(entry store.ScheduleEntry, policy *store.PacingPolicy, otherEntries []store.ScheduleEntry, synced []store.SyncedVideo) scheduleFlagsResult {
	var result scheduleFlagsResult

	weekStart, weekEnd := weekBounds(entry.ProposedPublishAt)
	cadenceCount := 1 // entry itself, already persisted.
	var cadenceConflicts []string
	for _, e := range otherEntries {
		if e.ID == entry.ID {
			continue
		}
		t := e.ProposedPublishAt.UTC()
		if !t.Before(weekStart) && t.Before(weekEnd) {
			cadenceCount++
			cadenceConflicts = append(cadenceConflicts, fmt.Sprintf("schedule_entry %s (%s, idea %s, %s)", e.ID, e.State, e.IdeaID, t.Format(time.RFC3339)))
		}
	}
	for _, v := range synced {
		ts := effectiveSyncedVideoTime(v)
		if ts == nil {
			continue
		}
		t := ts.UTC()
		if !t.Before(weekStart) && t.Before(weekEnd) {
			cadenceCount++
			cadenceConflicts = append(cadenceConflicts, fmt.Sprintf("synced video %s %q (%s, %s)", v.YouTubeVideoID, v.Title, v.PrivacyStatus, t.Format(time.RFC3339)))
		}
	}
	if policy != nil && float64(cadenceCount) > policy.TargetUploadsPerWeek {
		result.cadenceExceeded = true
		result.flags = append(result.flags, ScheduleFlag{
			Type: "cadence_exceeded",
			Explanation: fmt.Sprintf("this slot's week (%s to %s UTC) now holds %d entries against a target of %g/week",
				weekStart.Format("2006-01-02"), weekEnd.Format("2006-01-02"), cadenceCount, policy.TargetUploadsPerWeek),
			ConflictingEntries: cadenceConflicts,
		})
	}

	if policy != nil && len(policy.PreferredDays) > 0 {
		weekday := entry.ProposedPublishAt.UTC().Weekday().String()
		preferred := false
		for _, d := range policy.PreferredDays {
			if d == weekday {
				preferred = true
				break
			}
		}
		if !preferred {
			result.offPreferredDay = true
			result.flags = append(result.flags, ScheduleFlag{
				Type:        "off_preferred_day",
				Explanation: fmt.Sprintf("%s is not one of the Channel's preferred days (%s)", weekday, strings.Join(policy.PreferredDays, ", ")),
			})
		}
	}

	lower := entry.ProposedPublishAt.Add(-collisionWindow)
	upper := entry.ProposedPublishAt.Add(collisionWindow)
	var collisions []string
	for _, e := range otherEntries {
		if e.ID == entry.ID {
			continue
		}
		if !e.ProposedPublishAt.Before(lower) && !e.ProposedPublishAt.After(upper) {
			collisions = append(collisions, fmt.Sprintf("schedule_entry %s (%s, idea %s, %s)", e.ID, e.State, e.IdeaID, e.ProposedPublishAt.Format(time.RFC3339)))
		}
	}
	for _, v := range synced {
		ts := effectiveSyncedVideoTime(v)
		if ts == nil {
			continue
		}
		if !ts.Before(lower) && !ts.After(upper) {
			collisions = append(collisions, fmt.Sprintf("synced video %s %q (%s, %s)", v.YouTubeVideoID, v.Title, v.PrivacyStatus, ts.Format(time.RFC3339)))
		}
	}
	if len(collisions) > 0 {
		result.collision = true
		result.flags = append(result.flags, ScheduleFlag{
			Type:               "collision",
			Explanation:        fmt.Sprintf("another entry falls within %s of this proposed slot", collisionWindow),
			ConflictingEntries: collisions,
		})
	}

	return result
}

// -- list_schedule_entries -------------------------------------------------------

// defaultListScheduleEntriesLimit bounds list_schedule_entries' response
// when a caller supplies limit <= 0 -- without this, a Channel with enough
// draft+committed schedule_entry history exceeds the calling MCP client's
// response-size cap and the tool call fails outright with no way to
// retrieve the data in pages (issue #1812).
const defaultListScheduleEntriesLimit = 50

// ListScheduleEntriesInput is list_schedule_entries's argument schema.
type ListScheduleEntriesInput struct {
	ChannelID string `json:"channel_id" jsonschema:"Channel to list schedule entries for, as a UUID string"`

	// Since/Before bound the window by each entry's proposed_publish_at,
	// the same field the response is ordered by. Together they let a
	// caller page through a truncated response (issue #1812): since
	// (inclusive) resumes forward past the last returned entry's
	// proposed_publish_at (mirroring list_pending_matches' oldest-first
	// paging), or before (exclusive) narrows to what came earlier.
	Since  *time.Time `json:"since,omitempty" jsonschema:"Only include entries whose proposed_publish_at is at or after this time -- page forward past a truncated response by setting this to the last returned entry's proposed_publish_at"`
	Before *time.Time `json:"before,omitempty" jsonschema:"Only include entries whose proposed_publish_at is strictly before this time"`
	Limit  int        `json:"limit,omitempty" jsonschema:"Maximum entries to return, ordered by proposed_publish_at ascending (default 50). The response's truncated flag is set when more matching rows exist."`
}

// ChannelScopeID implements server.ChannelScoped.
func (i ListScheduleEntriesInput) ChannelScopeID() uuid.UUID {
	id, _ := uuid.Parse(i.ChannelID)
	return id
}

// ListScheduleEntriesOutput is list_schedule_entries's structured result.
type ListScheduleEntriesOutput struct {
	Entries   []ScheduleEntryOutput `json:"entries" jsonschema:"schedule_entry rows for this Channel (draft and committed), ordered by proposed publish time"`
	Truncated bool                  `json:"truncated" jsonschema:"True if more matching entries exist beyond limit"`
}

func registerListScheduleEntries(reg *server.Registry, schedules store.ScheduleStore, ideas store.IdeaStore, verdicts store.VerdictStore, persons store.PersonStore) {
	server.RegisterRead(reg, &mcp.Tool{
		Name: "list_schedule_entries",
		Description: "List a Channel's schedule_entry rows (draft and committed), each with its idea, the specific " +
			"verdict version it's bound to (LB3), state, approver, and timestamps, ordered by proposed_publish_at " +
			"ascending. Response is capped at limit (default 50); see truncated. Page forward past truncation by " +
			"re-calling with since set to the last returned entry's proposed_publish_at, or narrow with before.",
	}, listScheduleEntriesHandler(schedules, ideas, verdicts, persons))
}

func listScheduleEntriesHandler(schedules store.ScheduleStore, ideas store.IdeaStore, verdicts store.VerdictStore, persons store.PersonStore) mcp.ToolHandlerFor[ListScheduleEntriesInput, ListScheduleEntriesOutput] {
	return func(ctx context.Context, _ *mcp.CallToolRequest, in ListScheduleEntriesInput) (*mcp.CallToolResult, ListScheduleEntriesOutput, error) {
		channelID, err := uuid.Parse(in.ChannelID)
		if err != nil {
			return nil, ListScheduleEntriesOutput{}, fmt.Errorf("channel_id is not a valid UUID: %w", err)
		}

		limit := in.Limit
		if limit <= 0 {
			limit = defaultListScheduleEntriesLimit
		}
		entries, truncated, err := schedules.ListByChannel(ctx, channelID, in.Since, in.Before, limit)
		if err != nil {
			return nil, ListScheduleEntriesOutput{}, err
		}

		out := ListScheduleEntriesOutput{Entries: make([]ScheduleEntryOutput, 0, len(entries)), Truncated: truncated}
		for _, e := range entries {
			rendered, err := renderScheduleEntry(ctx, ideas, verdicts, persons, e)
			if err != nil {
				return nil, ListScheduleEntriesOutput{}, err
			}
			out.Entries = append(out.Entries, rendered)
		}
		return nil, out, nil
	}
}

// -- commit_schedule_draft -----------------------------------------------------

// CommitScheduleDraftInput is commit_schedule_draft's argument schema
// (FR19, issue #1648).
type CommitScheduleDraftInput struct {
	ChannelID         string `json:"channel_id" jsonschema:"Channel the draft belongs to, as a UUID string"`
	ScheduleEntryID   string `json:"schedule_entry_id" jsonschema:"the draft schedule_entry to commit, as a UUID string (from save_schedule_draft or list_schedule_entries)"`
	IdempotencyKeyArg string `json:"idempotency_key,omitempty" jsonschema:"Caller-supplied idempotency key. Strongly recommended: a replay with the same key is a no-op returning the original result (NFR2); committing an already-committed entry without one is rejected as a conflict, never a silent no-op."`
}

// ChannelScopeID implements server.ChannelScoped.
func (i CommitScheduleDraftInput) ChannelScopeID() uuid.UUID {
	id, _ := uuid.Parse(i.ChannelID)
	return id
}

// IdempotencyKey implements server.IdempotencyKeyed.
func (i CommitScheduleDraftInput) IdempotencyKey() string { return i.IdempotencyKeyArg }

// registerCommitScheduleDraft registers commit_schedule_draft via
// server.RegisterWrite. RegisterWrite's automatic ChannelScoped gate only
// enforces store.CanWrite (Founder, Co-Creator, and Analyst) -- FR19
// requires the stricter store.CanApprove (Creator-tier: Founder or
// Co-Creator, symmetrically per FR32, matching web/schedule.go's
// HandleApprove), so commitScheduleDraftMutate checks it explicitly before
// calling store.ScheduleStore.Approve, the same store method the web UI's
// approve button already calls (issue #1580, C8).
func registerCommitScheduleDraft(reg *server.Registry, schedules store.ScheduleStore, ideas store.IdeaStore, verdicts store.VerdictStore, persons store.PersonStore, roles store.RoleStore) {
	server.RegisterWrite(reg, &mcp.Tool{
		Name: "commit_schedule_draft",
		Description: "Approve a draft schedule_entry, transitioning it draft -> committed and recording the approving " +
			"Founder or Co-Creator (FR19). Requires Creator-tier authority (store.CanApprove -- Founder or " +
			"Co-Creator, symmetrically per FR32) -- an Analyst calling this is rejected with a permission " +
			"error, even though an Analyst may create the draft via save_schedule_draft. Committing is required before " +
			"the outcome matcher will ever consider this entry a candidate: list_pending_matches/resolve_pending_match " +
			"(FR22/FR23) and the sync worker's auto-match only ever match against committed entries, never drafts. " +
			"Rejected with a conflict if schedule_entry_id is not currently a draft, or if it is already frozen by a " +
			"live match to a published video (FR20). Always supply idempotency_key: committing an already-committed " +
			"entry without one is rejected as a conflict, not treated as a no-op replay.",
	}, commitScheduleDraftMutate(schedules, roles), commitScheduleDraftRender(schedules, ideas, verdicts, persons))
}

func commitScheduleDraftMutate(schedules store.ScheduleStore, roles store.RoleStore) server.WriteMutate[CommitScheduleDraftInput] {
	return func(ctx context.Context, in CommitScheduleDraftInput) (uuid.UUID, error) {
		channelID, err := uuid.Parse(in.ChannelID)
		if err != nil {
			return uuid.Nil, fmt.Errorf("channel_id is not a valid UUID: %w", err)
		}

		entryID, err := uuid.Parse(in.ScheduleEntryID)
		if err != nil {
			return uuid.Nil, fmt.Errorf("schedule_entry_id is not a valid UUID: %w", err)
		}

		entry, err := schedules.GetByID(ctx, entryID)
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return uuid.Nil, fmt.Errorf("schedule_entry_id does not exist")
			}
			return uuid.Nil, fmt.Errorf("load schedule_entry: %w", err)
		}
		if entry.ChannelID != channelID {
			return uuid.Nil, fmt.Errorf("schedule_entry_id does not belong to channel_id")
		}

		person := server.PersonFromContext(ctx)
		if person == nil {
			return uuid.Nil, fmt.Errorf("unauthenticated: no caller credential resolved")
		}

		canApprove, err := store.CanApprove(ctx, roles, channelID, person.ID)
		if err != nil {
			return uuid.Nil, fmt.Errorf("check approval authority: %w", err)
		}
		if !canApprove {
			return uuid.Nil, fmt.Errorf("permission denied: only a Channel's Founder or Co-Creator may commit a schedule draft (FR19)")
		}

		if err := schedules.Approve(ctx, entryID, person.ID); err != nil {
			if errors.Is(err, store.ErrScheduleEntryPublished) {
				return uuid.Nil, fmt.Errorf("cannot commit: %w", err)
			}
			return uuid.Nil, err
		}
		return entryID, nil
	}
}

// commitScheduleDraftRender always re-reads the entry (by ref, the entry
// ID mutate returned) from Postgres rather than trusting anything cached
// from mutate -- see server.RegisterWrite's doc on why render runs on
// every call, replay included.
func commitScheduleDraftRender(schedules store.ScheduleStore, ideas store.IdeaStore, verdicts store.VerdictStore, persons store.PersonStore) server.WriteRender[ScheduleEntryOutput] {
	return func(ctx context.Context, ref uuid.UUID) (*mcp.CallToolResult, ScheduleEntryOutput, error) {
		entry, err := schedules.GetByID(ctx, ref)
		if err != nil {
			return nil, ScheduleEntryOutput{}, fmt.Errorf("load committed schedule entry: %w", err)
		}
		out, err := renderScheduleEntry(ctx, ideas, verdicts, persons, entry)
		if err != nil {
			return nil, ScheduleEntryOutput{}, err
		}
		return nil, out, nil
	}
}

// -- uncommit_schedule_draft ---------------------------------------------------

// UncommitScheduleDraftInput is uncommit_schedule_draft's argument schema
// (FR20, issue #1648's full-parity follow-up).
type UncommitScheduleDraftInput struct {
	ChannelID         string `json:"channel_id" jsonschema:"Channel the entry belongs to, as a UUID string"`
	ScheduleEntryID   string `json:"schedule_entry_id" jsonschema:"the committed schedule_entry to revert to draft, as a UUID string"`
	IdempotencyKeyArg string `json:"idempotency_key,omitempty" jsonschema:"Caller-supplied idempotency key. Strongly recommended: a replay with the same key is a no-op returning the original result (NFR2); un-committing an already-draft entry without one is rejected as a conflict, never a silent no-op."`
}

// ChannelScopeID implements server.ChannelScoped.
func (i UncommitScheduleDraftInput) ChannelScopeID() uuid.UUID {
	id, _ := uuid.Parse(i.ChannelID)
	return id
}

// IdempotencyKey implements server.IdempotencyKeyed.
func (i UncommitScheduleDraftInput) IdempotencyKey() string { return i.IdempotencyKeyArg }

// registerUncommitScheduleDraft registers uncommit_schedule_draft via
// server.RegisterWrite. Like commit_schedule_draft, this checks
// store.CanApprove explicitly (Creator-tier: Founder or Co-Creator,
// symmetrically per FR32) rather than relying on RegisterWrite's
// automatic store.CanWrite gate, matching web/schedule.go's
// HandleUnapprove (FR20).
func registerUncommitScheduleDraft(reg *server.Registry, schedules store.ScheduleStore, ideas store.IdeaStore, verdicts store.VerdictStore, persons store.PersonStore, roles store.RoleStore) {
	server.RegisterWrite(reg, &mcp.Tool{
		Name: "uncommit_schedule_draft",
		Description: "Reverse commit_schedule_draft: transition a committed schedule_entry back to draft, clearing " +
			"its approver and approved_at (FR20). Requires Creator-tier authority (store.CanApprove -- Founder or " +
			"Co-Creator, symmetrically per FR32) -- an Analyst calling this is " +
			"rejected with a permission error. Rejected with a conflict if schedule_entry_id is not currently " +
			"committed, or if it is already frozen by a live match to a published video (FR20's freeze) -- once a " +
			"video has published against an entry, its commit can no longer be reversed here or in web. Always " +
			"supply idempotency_key: un-committing an already-draft entry without one is rejected as a conflict, " +
			"not treated as a no-op replay.",
	}, uncommitScheduleDraftMutate(schedules, roles), commitScheduleDraftRender(schedules, ideas, verdicts, persons))
}

func uncommitScheduleDraftMutate(schedules store.ScheduleStore, roles store.RoleStore) server.WriteMutate[UncommitScheduleDraftInput] {
	return func(ctx context.Context, in UncommitScheduleDraftInput) (uuid.UUID, error) {
		channelID, err := uuid.Parse(in.ChannelID)
		if err != nil {
			return uuid.Nil, fmt.Errorf("channel_id is not a valid UUID: %w", err)
		}

		entryID, err := uuid.Parse(in.ScheduleEntryID)
		if err != nil {
			return uuid.Nil, fmt.Errorf("schedule_entry_id is not a valid UUID: %w", err)
		}

		entry, err := schedules.GetByID(ctx, entryID)
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return uuid.Nil, fmt.Errorf("schedule_entry_id does not exist")
			}
			return uuid.Nil, fmt.Errorf("load schedule_entry: %w", err)
		}
		if entry.ChannelID != channelID {
			return uuid.Nil, fmt.Errorf("schedule_entry_id does not belong to channel_id")
		}

		person := server.PersonFromContext(ctx)
		if person == nil {
			return uuid.Nil, fmt.Errorf("unauthenticated: no caller credential resolved")
		}

		canApprove, err := store.CanApprove(ctx, roles, channelID, person.ID)
		if err != nil {
			return uuid.Nil, fmt.Errorf("check approval authority: %w", err)
		}
		if !canApprove {
			return uuid.Nil, fmt.Errorf("permission denied: only a Channel's Founder or Co-Creator may un-commit a schedule entry (FR20)")
		}

		if err := schedules.Unapprove(ctx, entryID, person.ID); err != nil {
			if errors.Is(err, store.ErrScheduleEntryPublished) {
				return uuid.Nil, fmt.Errorf("cannot un-commit: %w", err)
			}
			return uuid.Nil, err
		}
		return entryID, nil
	}
}

// -- update_schedule_draft -----------------------------------------------------

// UpdateScheduleDraftInput is update_schedule_draft's argument schema
// (FR20's edit route, issue #1648's full-parity follow-up).
type UpdateScheduleDraftInput struct {
	ChannelID         string `json:"channel_id" jsonschema:"Channel the entry belongs to, as a UUID string"`
	ScheduleEntryID   string `json:"schedule_entry_id" jsonschema:"the draft schedule_entry to reschedule, as a UUID string"`
	ProposedPublishAt string `json:"proposed_publish_at" jsonschema:"the new proposed publish slot, RFC3339"`
	IdempotencyKeyArg string `json:"idempotency_key,omitempty" jsonschema:"Caller-supplied idempotency key. Strongly recommended: a replay with the same key is a no-op returning the original result (NFR2); editing an entry that is no longer a draft without one is rejected as a conflict, never a silent no-op."`
}

// ChannelScopeID implements server.ChannelScoped.
func (i UpdateScheduleDraftInput) ChannelScopeID() uuid.UUID {
	id, _ := uuid.Parse(i.ChannelID)
	return id
}

// IdempotencyKey implements server.IdempotencyKeyed.
func (i UpdateScheduleDraftInput) IdempotencyKey() string { return i.IdempotencyKeyArg }

// registerUpdateScheduleDraft registers update_schedule_draft via
// server.RegisterWrite. Like commit_schedule_draft, this checks
// store.CanApprove explicitly (Creator-tier: Founder or Co-Creator,
// symmetrically per FR32) rather than relying on RegisterWrite's
// automatic store.CanWrite gate -- matching web/schedule.go's HandleEdit,
// which gates re-scheduling an *existing* draft more strictly than
// save_schedule_draft gates creating one (FR20).
func registerUpdateScheduleDraft(reg *server.Registry, schedules store.ScheduleStore, ideas store.IdeaStore, verdicts store.VerdictStore, persons store.PersonStore, pacing store.PacingStore, sync store.SyncStore, roles store.RoleStore) {
	server.RegisterWrite(reg, &mcp.Tool{
		Name: "update_schedule_draft",
		Description: "Change a draft schedule_entry's proposed_publish_at (FR20's edit route). Requires Creator-tier " +
			"authority (store.CanApprove -- Founder or Co-Creator, symmetrically per FR32) -- an Analyst calling " +
			"this is rejected with a permission error, even though an " +
			"Analyst may create the original draft via save_schedule_draft. Rejected with a conflict if " +
			"schedule_entry_id is not currently a draft (a committed entry must be un-committed first via " +
			"uncommit_schedule_draft), or if it is already frozen by a live match to a published video (FR20). " +
			"Recomputes and returns FR18's non-blocking flags (cadence_exceeded, off_preferred_day, collision) " +
			"against the new slot, same as save_schedule_draft. Always supply idempotency_key: editing a " +
			"non-draft entry without one is rejected as a conflict, not treated as a no-op replay.",
	}, updateScheduleDraftMutate(schedules, roles), saveScheduleDraftRender(schedules, ideas, verdicts, persons, pacing, sync))
}

func updateScheduleDraftMutate(schedules store.ScheduleStore, roles store.RoleStore) server.WriteMutate[UpdateScheduleDraftInput] {
	return func(ctx context.Context, in UpdateScheduleDraftInput) (uuid.UUID, error) {
		channelID, err := uuid.Parse(in.ChannelID)
		if err != nil {
			return uuid.Nil, fmt.Errorf("channel_id is not a valid UUID: %w", err)
		}

		entryID, err := uuid.Parse(in.ScheduleEntryID)
		if err != nil {
			return uuid.Nil, fmt.Errorf("schedule_entry_id is not a valid UUID: %w", err)
		}

		proposedPublishAt, err := time.Parse(time.RFC3339, in.ProposedPublishAt)
		if err != nil {
			return uuid.Nil, fmt.Errorf("proposed_publish_at is not a valid RFC3339 timestamp: %w", err)
		}

		entry, err := schedules.GetByID(ctx, entryID)
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return uuid.Nil, fmt.Errorf("schedule_entry_id does not exist")
			}
			return uuid.Nil, fmt.Errorf("load schedule_entry: %w", err)
		}
		if entry.ChannelID != channelID {
			return uuid.Nil, fmt.Errorf("schedule_entry_id does not belong to channel_id")
		}

		person := server.PersonFromContext(ctx)
		if person == nil {
			return uuid.Nil, fmt.Errorf("unauthenticated: no caller credential resolved")
		}

		canApprove, err := store.CanApprove(ctx, roles, channelID, person.ID)
		if err != nil {
			return uuid.Nil, fmt.Errorf("check approval authority: %w", err)
		}
		if !canApprove {
			return uuid.Nil, fmt.Errorf("permission denied: only a Channel's Founder or Co-Creator may reschedule a schedule entry (FR20)")
		}

		if err := schedules.Update(ctx, entryID, proposedPublishAt); err != nil {
			if errors.Is(err, store.ErrScheduleEntryPublished) {
				return uuid.Nil, fmt.Errorf("cannot reschedule: %w", err)
			}
			return uuid.Nil, err
		}
		return entryID, nil
	}
}

// -- registration ------------------------------------------------------------

// RegisterScheduleDraft registers set_pacing_policy, get_pacing_policy,
// get_drafting_context, save_schedule_draft, commit_schedule_draft,
// uncommit_schedule_draft, update_schedule_draft, and list_schedule_entries
// against reg (see ../server/registry.go), backed by st's
// PacingStore/ScheduleStore/SyncStore/IdeaStore/VerdictStore/PersonStore/
// RoleStore.
func RegisterScheduleDraft(reg *server.Registry, st *store.Store) {
	registerSetPacingPolicy(reg, st.Pacing(), st.Persons())
	registerGetPacingPolicy(reg, st.Pacing(), st.Persons())
	registerGetDraftingContext(reg, st.Pacing(), st.Sync(), st.Schedules(), st.Ideas(), st.Verdicts(), st.Persons())
	registerSaveScheduleDraft(reg, st.Schedules(), st.Ideas(), st.Verdicts(), st.Persons(), st.Pacing(), st.Sync())
	registerCommitScheduleDraft(reg, st.Schedules(), st.Ideas(), st.Verdicts(), st.Persons(), st.Roles())
	registerUncommitScheduleDraft(reg, st.Schedules(), st.Ideas(), st.Verdicts(), st.Persons(), st.Roles())
	registerUpdateScheduleDraft(reg, st.Schedules(), st.Ideas(), st.Verdicts(), st.Persons(), st.Pacing(), st.Sync(), st.Roles())
	registerListScheduleEntries(reg, st.Schedules(), st.Ideas(), st.Verdicts(), st.Persons())
}
