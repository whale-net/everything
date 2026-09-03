// Strategy MCP tool group (issue #1637): save_strategy/get_strategy/
// list_strategies manage a cadence built directly from one or more
// viable viability_verdict rows -- independent of, and finer-grained
// than, the Channel-wide pacing policy (FR17, schedule_draft.go). generate_schedule_plan
// is the "Plan" half: a read-only tool that reads active Strategies plus
// the existing schedule and proposes cadence-driven next slots, which a
// caller commits via the existing save_schedule_draft tool rather than a
// second write path. See ../../store/strategy.go (StrategyStore) and
// migration 008's comment for the schema and design rationale.
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

// StrategyVerdictOutput is one viability_verdict a Strategy is built
// from, as save_strategy/get_strategy/list_strategies all render it.
type StrategyVerdictOutput struct {
	VerdictID      string `json:"verdict_id" jsonschema:"this linked verdict's ID, as a UUID string"`
	VerdictVersion int    `json:"verdict_version" jsonschema:"that verdict version's number"`
	IdeaID         string `json:"idea_id" jsonschema:"the Idea this verdict judged, as a UUID string"`
	IdeaTitle      string `json:"idea_title" jsonschema:"that Idea's title"`
}

// StrategyOutput is one `strategy` row plus the verdicts it's built from,
// as save_strategy/get_strategy/list_strategies all render it.
type StrategyOutput struct {
	StrategyID           string                  `json:"strategy_id" jsonschema:"this Strategy's ID, as a UUID string"`
	ChannelID            string                  `json:"channel_id" jsonschema:"the Channel this Strategy belongs to, as a UUID string"`
	Title                string                  `json:"title" jsonschema:"this Strategy's short description"`
	Cadence              string                  `json:"cadence" jsonschema:"weekly, biweekly, or monthly"`
	PreferredWeekday     string                  `json:"preferred_weekday,omitempty" jsonschema:"full English weekday name this Strategy's slots roll onto; empty means no day preference"`
	Active               bool                    `json:"active" jsonschema:"whether this Strategy is currently active -- only active Strategies are read by generate_schedule_plan"`
	Verdicts             []StrategyVerdictOutput `json:"verdicts" jsonschema:"the viable viability_verdict rows this Strategy is built from"`
	CreatedByPersonID    string                  `json:"created_by_person_id" jsonschema:"the Person who created this Strategy, as a UUID string"`
	CreatedByDisplayName string                  `json:"created_by_display_name" jsonschema:"that Person's display name"`
	CreatedAt            string                  `json:"created_at" jsonschema:"when this Strategy was created, RFC3339"`
	UpdatedAt            string                  `json:"updated_at" jsonschema:"when this Strategy was last saved, RFC3339"`
}

// renderStrategy resolves d's creator's display name and renders the
// result as StrategyOutput -- the single call site every tool that
// renders a Strategy shares.
func renderStrategy(ctx context.Context, persons store.PersonStore, d store.StrategyDetail) (StrategyOutput, error) {
	creator, err := persons.GetByID(ctx, d.CreatedByPersonID)
	if err != nil {
		return StrategyOutput{}, fmt.Errorf("load strategy creator: %w", err)
	}

	verdicts := make([]StrategyVerdictOutput, 0, len(d.Verdicts))
	for _, link := range d.Verdicts {
		verdicts = append(verdicts, StrategyVerdictOutput{
			VerdictID:      link.VerdictID.String(),
			VerdictVersion: link.VerdictVersion,
			IdeaID:         link.IdeaID.String(),
			IdeaTitle:      link.IdeaTitle,
		})
	}

	return StrategyOutput{
		StrategyID:           d.ID.String(),
		ChannelID:            d.ChannelID.String(),
		Title:                d.Title,
		Cadence:              string(d.Cadence),
		PreferredWeekday:     d.PreferredWeekday,
		Active:               d.Active,
		Verdicts:             verdicts,
		CreatedByPersonID:    d.CreatedByPersonID.String(),
		CreatedByDisplayName: creator.DisplayName,
		CreatedAt:            d.CreatedAt.Format(time.RFC3339),
		UpdatedAt:            d.UpdatedAt.Format(time.RFC3339),
	}, nil
}

// parseCadence validates raw against the three values the
// strategy.cadence CHECK constraint (migration 008) allows, rejecting
// anything else before a write tool ever opens a transaction.
func parseCadence(raw string) (store.Cadence, error) {
	switch c := store.Cadence(strings.ToLower(strings.TrimSpace(raw))); c {
	case store.CadenceWeekly, store.CadenceBiweekly, store.CadenceMonthly:
		return c, nil
	default:
		return "", fmt.Errorf("cadence must be one of %q, %q, %q", store.CadenceWeekly, store.CadenceBiweekly, store.CadenceMonthly)
	}
}

// -- save_strategy --------------------------------------------------------

// SaveStrategyInput is save_strategy's argument schema (issue #1637).
// ChannelID/StrategyID/VerdictIDs are JSON-wire strings, not uuid.UUID
// fields directly -- see ../server/fakes_test.go's scopedInput doc for
// why.
type SaveStrategyInput struct {
	ChannelID string `json:"channel_id" jsonschema:"Channel this Strategy belongs to, as a UUID string"`
	// StrategyID updates an existing Strategy (replacing its linked
	// verdicts wholesale) when supplied; omit to create a new Strategy.
	StrategyID       string   `json:"strategy_id,omitempty" jsonschema:"update this existing Strategy (as a UUID string) instead of creating a new one; omit to create"`
	Title            string   `json:"title" jsonschema:"short description of this Strategy, e.g. \"short themed clips\"; must not be empty"`
	Cadence          string   `json:"cadence" jsonschema:"weekly, biweekly, or monthly"`
	PreferredWeekday string   `json:"preferred_weekday,omitempty" jsonschema:"full English weekday name (e.g. Monday) this Strategy's slots should roll onto; omit for no day preference"`
	Active           *bool    `json:"active,omitempty" jsonschema:"whether this Strategy is active; defaults to true when omitted"`
	VerdictIDs       []string `json:"verdict_ids" jsonschema:"the viability_verdict rows (as UUID strings) this Strategy is built from -- not idea_ids; must be non-empty, and each must currently be viable (issue #1637). The same verdict_id may be passed to more than one Strategy."`
	// IdempotencyKeyArg backs IdempotencyKey() below -- named ...Arg
	// because a Go type cannot declare both a field and a method named
	// IdempotencyKey.
	IdempotencyKeyArg string `json:"idempotency_key,omitempty" jsonschema:"caller-supplied idempotency key. If omitted, a retry creates a second Strategy rather than converging (a Strategy has no natural key) -- always supply one."`
}

// ChannelScopeID implements server.ChannelScoped.
func (i SaveStrategyInput) ChannelScopeID() uuid.UUID {
	id, _ := uuid.Parse(i.ChannelID)
	return id
}

// IdempotencyKey implements server.IdempotencyKeyed.
func (i SaveStrategyInput) IdempotencyKey() string { return i.IdempotencyKeyArg }

// registerSaveStrategy registers save_strategy via server.RegisterWrite --
// Channel-scoped via store.CanWrite (both Creator and Analyst may call
// it, mirroring set_pacing_policy).
func registerSaveStrategy(reg *server.Registry, strategies store.StrategyStore, persons store.PersonStore) {
	server.RegisterWrite(reg, &mcp.Tool{
		Name: "save_strategy",
		Description: "Create or update a Strategy: a cadence (weekly/biweekly/monthly, optionally pinned to a " +
			"preferred weekday) built directly from one or more viable viability_verdict rows (issue #1637) -- " +
			"finer-grained than the Channel-wide pacing policy (set_pacing_policy). Pass verdict_ids, not idea_ids: " +
			"grounding on the exact verdict records what analysis justified this Strategy, and the same verdict_id may " +
			"be reused across multiple Strategies. Omit strategy_id to create a new Strategy; supply it to update an " +
			"existing one, replacing its linked verdict_ids wholesale. Rejects a verdict_id that doesn't exist or isn't " +
			"currently viable -- nothing is written in that case. Always supply idempotency_key: a Strategy has no " +
			"natural key, so a keyless retry creates a duplicate rather than converging.",
	}, saveStrategyMutate(strategies), saveStrategyRender(strategies, persons))
}

func saveStrategyMutate(strategies store.StrategyStore) server.WriteMutate[SaveStrategyInput] {
	return func(ctx context.Context, in SaveStrategyInput) (uuid.UUID, error) {
		title := strings.TrimSpace(in.Title)
		if title == "" {
			return uuid.Nil, fmt.Errorf("title must not be empty")
		}

		cadence, err := parseCadence(in.Cadence)
		if err != nil {
			return uuid.Nil, err
		}

		var preferredWeekday string
		if strings.TrimSpace(in.PreferredWeekday) != "" {
			preferredWeekday, err = parseWeekdayName(in.PreferredWeekday)
			if err != nil {
				return uuid.Nil, err
			}
		}

		channelID, err := uuid.Parse(in.ChannelID)
		if err != nil {
			return uuid.Nil, fmt.Errorf("channel_id is not a valid UUID: %w", err)
		}

		var strategyID *uuid.UUID
		if strings.TrimSpace(in.StrategyID) != "" {
			id, err := uuid.Parse(in.StrategyID)
			if err != nil {
				return uuid.Nil, fmt.Errorf("strategy_id is not a valid UUID: %w", err)
			}
			strategyID = &id
		}

		if len(in.VerdictIDs) == 0 {
			return uuid.Nil, fmt.Errorf("verdict_ids must not be empty -- a Strategy must be built from at least one viable verdict")
		}
		verdictIDs := make([]uuid.UUID, 0, len(in.VerdictIDs))
		for _, raw := range in.VerdictIDs {
			id, err := uuid.Parse(raw)
			if err != nil {
				return uuid.Nil, fmt.Errorf("verdict_ids contains an invalid UUID %q: %w", raw, err)
			}
			verdictIDs = append(verdictIDs, id)
		}

		active := true
		if in.Active != nil {
			active = *in.Active
		}

		person := server.PersonFromContext(ctx)
		if person == nil {
			return uuid.Nil, fmt.Errorf("unauthenticated: no caller credential resolved")
		}

		detail, err := strategies.Save(ctx, store.SaveStrategyInput{
			StrategyID:        strategyID,
			ChannelID:         channelID,
			Title:             title,
			Cadence:           cadence,
			PreferredWeekday:  preferredWeekday,
			Active:            active,
			VerdictIDs:        verdictIDs,
			CreatedByPersonID: person.ID,
			IdempotencyKey:    in.IdempotencyKeyArg,
		})
		if err != nil {
			if errors.Is(err, store.ErrStrategyVerdictNotViable) {
				return uuid.Nil, fmt.Errorf("save_strategy only accepts verdict_ids that exist and are currently viable (issue #1637): %w", err)
			}
			if errors.Is(err, store.ErrStrategyNotFound) {
				return uuid.Nil, fmt.Errorf("strategy_id does not exist on channel_id: %w", err)
			}
			return uuid.Nil, err
		}
		return detail.ID, nil
	}
}

// saveStrategyRender always re-reads the Strategy (by ref, the
// strategy.ID mutate returned) from Postgres rather than trusting
// anything cached from mutate -- see server.RegisterWrite's doc on why
// render runs on every call, replay included.
func saveStrategyRender(strategies store.StrategyStore, persons store.PersonStore) server.WriteRender[StrategyOutput] {
	return func(ctx context.Context, ref uuid.UUID) (*mcp.CallToolResult, StrategyOutput, error) {
		detail, err := strategies.GetByID(ctx, ref)
		if err != nil {
			return nil, StrategyOutput{}, fmt.Errorf("load saved strategy: %w", err)
		}
		out, err := renderStrategy(ctx, persons, detail)
		if err != nil {
			return nil, StrategyOutput{}, err
		}
		return nil, out, nil
	}
}

// -- get_strategy ---------------------------------------------------------

// GetStrategyInput is get_strategy's argument schema.
type GetStrategyInput struct {
	ChannelID  string `json:"channel_id" jsonschema:"Channel the Strategy belongs to, as a UUID string"`
	StrategyID string `json:"strategy_id" jsonschema:"the Strategy to read, as a UUID string"`
}

// ChannelScopeID implements server.ChannelScoped.
func (i GetStrategyInput) ChannelScopeID() uuid.UUID {
	id, _ := uuid.Parse(i.ChannelID)
	return id
}

func registerGetStrategy(reg *server.Registry, strategies store.StrategyStore, persons store.PersonStore) {
	server.RegisterRead(reg, &mcp.Tool{
		Name:        "get_strategy",
		Description: "Read one Strategy (issue #1637) by ID, including the viable viability_verdict rows it's built from.",
	}, getStrategyHandler(strategies, persons))
}

func getStrategyHandler(strategies store.StrategyStore, persons store.PersonStore) mcp.ToolHandlerFor[GetStrategyInput, StrategyOutput] {
	return func(ctx context.Context, _ *mcp.CallToolRequest, in GetStrategyInput) (*mcp.CallToolResult, StrategyOutput, error) {
		channelID, err := uuid.Parse(in.ChannelID)
		if err != nil {
			return nil, StrategyOutput{}, fmt.Errorf("channel_id is not a valid UUID: %w", err)
		}
		strategyID, err := uuid.Parse(in.StrategyID)
		if err != nil {
			return nil, StrategyOutput{}, fmt.Errorf("strategy_id is not a valid UUID: %w", err)
		}

		detail, err := strategies.GetByID(ctx, strategyID)
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return nil, StrategyOutput{}, fmt.Errorf("strategy_id does not exist")
			}
			return nil, StrategyOutput{}, fmt.Errorf("load strategy: %w", err)
		}
		if detail.ChannelID != channelID {
			return nil, StrategyOutput{}, fmt.Errorf("strategy_id does not belong to channel_id")
		}

		out, err := renderStrategy(ctx, persons, detail)
		if err != nil {
			return nil, StrategyOutput{}, err
		}
		return nil, out, nil
	}
}

// -- list_strategies ---------------------------------------------------------

// ListStrategiesInput is list_strategies's argument schema.
type ListStrategiesInput struct {
	ChannelID  string `json:"channel_id" jsonschema:"Channel to list Strategies for, as a UUID string"`
	ActiveOnly bool   `json:"active_only,omitempty" jsonschema:"restrict to active Strategies only; default false lists every Strategy regardless of active"`
}

// ChannelScopeID implements server.ChannelScoped.
func (i ListStrategiesInput) ChannelScopeID() uuid.UUID {
	id, _ := uuid.Parse(i.ChannelID)
	return id
}

// ListStrategiesOutput is list_strategies's structured result.
type ListStrategiesOutput struct {
	Strategies []StrategyOutput `json:"strategies" jsonschema:"every Strategy for this Channel matching active_only, ordered by creation time"`
}

func registerListStrategies(reg *server.Registry, strategies store.StrategyStore, persons store.PersonStore) {
	server.RegisterRead(reg, &mcp.Tool{
		Name:        "list_strategies",
		Description: "List a Channel's Strategies (issue #1637), each with its cadence and the viable viability_verdict rows it's built from.",
	}, listStrategiesHandler(strategies, persons))
}

func listStrategiesHandler(strategies store.StrategyStore, persons store.PersonStore) mcp.ToolHandlerFor[ListStrategiesInput, ListStrategiesOutput] {
	return func(ctx context.Context, _ *mcp.CallToolRequest, in ListStrategiesInput) (*mcp.CallToolResult, ListStrategiesOutput, error) {
		channelID, err := uuid.Parse(in.ChannelID)
		if err != nil {
			return nil, ListStrategiesOutput{}, fmt.Errorf("channel_id is not a valid UUID: %w", err)
		}

		details, err := strategies.ListByChannel(ctx, channelID, in.ActiveOnly)
		if err != nil {
			return nil, ListStrategiesOutput{}, err
		}

		out := ListStrategiesOutput{Strategies: make([]StrategyOutput, 0, len(details))}
		for _, d := range details {
			rendered, err := renderStrategy(ctx, persons, d)
			if err != nil {
				return nil, ListStrategiesOutput{}, err
			}
			out.Strategies = append(out.Strategies, rendered)
		}
		return nil, out, nil
	}
}

// -- generate_schedule_plan -------------------------------------------------

// defaultCountPerIdea/maxCountPerIdea bound generate_schedule_plan's
// optional count_per_idea argument -- enough to preview several upcoming
// slots without a runaway response for a Strategy with many linked Ideas.
const (
	defaultCountPerIdea = 1
	maxCountPerIdea     = 6
)

// GenerateSchedulePlanInput is generate_schedule_plan's argument schema
// (issue #1637's Plan half).
type GenerateSchedulePlanInput struct {
	ChannelID    string `json:"channel_id" jsonschema:"Channel to generate a schedule plan for, as a UUID string"`
	CountPerIdea int    `json:"count_per_idea,omitempty" jsonschema:"how many upcoming cadence slots to propose per linked Idea, 1-6; defaults to 1"`
}

// ChannelScopeID implements server.ChannelScoped.
func (i GenerateSchedulePlanInput) ChannelScopeID() uuid.UUID {
	id, _ := uuid.Parse(i.ChannelID)
	return id
}

// ScheduledProposal is one computed-but-not-yet-saved next slot for a
// Strategy-linked Idea -- generate_schedule_plan's per-proposal shape.
// Pass IdeaID/VerdictID/ProposedPublishAt straight to save_schedule_draft
// to actually commit it.
type ScheduledProposal struct {
	StrategyID        string `json:"strategy_id" jsonschema:"the Strategy this proposal was generated from, as a UUID string"`
	StrategyTitle     string `json:"strategy_title" jsonschema:"that Strategy's title"`
	Cadence           string `json:"cadence" jsonschema:"that Strategy's cadence"`
	IdeaID            string `json:"idea_id" jsonschema:"the Idea to schedule, as a UUID string -- pass to save_schedule_draft"`
	IdeaTitle         string `json:"idea_title" jsonschema:"that Idea's title"`
	VerdictID         string `json:"verdict_id" jsonschema:"the Idea's current viable verdict version, as a UUID string -- pass to save_schedule_draft's verdict_id to pin this exact version"`
	VerdictVersion    int    `json:"verdict_version" jsonschema:"that verdict version's number"`
	ProposedPublishAt string `json:"proposed_publish_at" jsonschema:"the cadence-computed slot, RFC3339 -- pass to save_schedule_draft's proposed_publish_at to commit it"`
	SequenceIndex     int    `json:"sequence_index" jsonschema:"1-based position of this proposal within its Idea's count_per_idea run"`
}

// SkippedStrategyVerdict is one Strategy-linked verdict
// generate_schedule_plan excluded from Proposals, and why.
type SkippedStrategyVerdict struct {
	StrategyID string `json:"strategy_id" jsonschema:"the Strategy this linked verdict belongs to, as a UUID string"`
	VerdictID  string `json:"verdict_id" jsonschema:"the skipped verdict, as a UUID string"`
	IdeaID     string `json:"idea_id" jsonschema:"the Idea that verdict judged, as a UUID string"`
	IdeaTitle  string `json:"idea_title" jsonschema:"that Idea's title"`
	Reason     string `json:"reason" jsonschema:"why this linked verdict produced no proposals"`
}

// GenerateSchedulePlanOutput is generate_schedule_plan's structured
// result.
type GenerateSchedulePlanOutput struct {
	Proposals []ScheduledProposal      `json:"proposals" jsonschema:"cadence-computed next slots, not yet saved -- nothing is written by this tool"`
	Skipped   []SkippedStrategyVerdict `json:"skipped" jsonschema:"linked verdicts excluded from proposals (e.g. the idea is no longer viable) and why"`
}

func registerGenerateSchedulePlan(reg *server.Registry, strategies store.StrategyStore, schedules store.ScheduleStore, verdicts store.VerdictStore) {
	server.RegisterRead(reg, &mcp.Tool{
		Name: "generate_schedule_plan",
		Description: "Compute the next cadence-driven schedule slot(s) for a Channel's active Strategies (issue #1637): " +
			"for each linked Idea whose verdict is still viable, the next slot(s) after its most recent schedule_entry, " +
			"spaced by that Strategy's cadence and rolled onto its preferred_weekday if set. Read-only -- nothing is " +
			"saved by this tool. Pass a proposal's idea_id/verdict_id/proposed_publish_at to save_schedule_draft to " +
			"actually commit it (which re-applies FR16's viable-verdict gate and reports FR18's flags).",
	}, generateSchedulePlanHandler(strategies, schedules, verdicts))
}

// advanceCadence returns from advanced by one occurrence of cadence.
func advanceCadence(from time.Time, cadence store.Cadence) time.Time {
	switch cadence {
	case store.CadenceBiweekly:
		return from.AddDate(0, 0, 14)
	case store.CadenceMonthly:
		return from.AddDate(0, 1, 0)
	default: // store.CadenceWeekly, and any unrecognized value.
		return from.AddDate(0, 0, 7)
	}
}

// rollToWeekday advances t (never backward) to the next date, inclusive
// of t itself, whose UTC weekday name equals weekday.
func rollToWeekday(t time.Time, weekday string) time.Time {
	for i := 0; i < 7; i++ {
		if t.UTC().Weekday().String() == weekday {
			return t
		}
		t = t.AddDate(0, 0, 1)
	}
	return t
}

func generateSchedulePlanHandler(strategies store.StrategyStore, schedules store.ScheduleStore, verdicts store.VerdictStore) mcp.ToolHandlerFor[GenerateSchedulePlanInput, GenerateSchedulePlanOutput] {
	return func(ctx context.Context, _ *mcp.CallToolRequest, in GenerateSchedulePlanInput) (*mcp.CallToolResult, GenerateSchedulePlanOutput, error) {
		channelID := in.ChannelScopeID()

		count := in.CountPerIdea
		if count <= 0 {
			count = defaultCountPerIdea
		}
		if count > maxCountPerIdea {
			count = maxCountPerIdea
		}

		active, err := strategies.ListByChannel(ctx, channelID, true)
		if err != nil {
			return nil, GenerateSchedulePlanOutput{}, fmt.Errorf("generate_schedule_plan: list active strategies: %w", err)
		}

		entries, err := schedules.ListByChannel(ctx, channelID)
		if err != nil {
			return nil, GenerateSchedulePlanOutput{}, fmt.Errorf("generate_schedule_plan: list schedule entries: %w", err)
		}
		// latestByIdea is each Idea's most recent proposed_publish_at
		// across its draft/committed schedule_entry rows -- the base a
		// proposal's cadence is advanced from, so a plan never proposes a
		// slot earlier than what's already on the calendar for that Idea.
		latestByIdea := make(map[uuid.UUID]time.Time, len(entries))
		for _, e := range entries {
			if cur, ok := latestByIdea[e.IdeaID]; !ok || e.ProposedPublishAt.After(cur) {
				latestByIdea[e.IdeaID] = e.ProposedPublishAt
			}
		}

		out := GenerateSchedulePlanOutput{}
		now := time.Now().UTC()
		for _, strat := range active {
			for _, link := range strat.Verdicts {
				current, err := verdicts.Current(ctx, link.IdeaID)
				if err != nil || current.Verdict != store.VerdictViable {
					out.Skipped = append(out.Skipped, SkippedStrategyVerdict{
						StrategyID: strat.ID.String(),
						VerdictID:  link.VerdictID.String(),
						IdeaID:     link.IdeaID.String(),
						IdeaTitle:  link.IdeaTitle,
						Reason:     "idea's current verdict is no longer viable",
					})
					continue
				}

				base, ok := latestByIdea[link.IdeaID]
				if !ok || base.Before(now) {
					base = now
				}
				for i := 1; i <= count; i++ {
					base = advanceCadence(base, strat.Cadence)
					if strat.PreferredWeekday != "" {
						base = rollToWeekday(base, strat.PreferredWeekday)
					}
					out.Proposals = append(out.Proposals, ScheduledProposal{
						StrategyID:        strat.ID.String(),
						StrategyTitle:     strat.Title,
						Cadence:           string(strat.Cadence),
						IdeaID:            link.IdeaID.String(),
						IdeaTitle:         link.IdeaTitle,
						VerdictID:         current.ID.String(),
						VerdictVersion:    current.Version,
						ProposedPublishAt: base.Format(time.RFC3339),
						SequenceIndex:     i,
					})
				}
			}
		}
		return nil, out, nil
	}
}

// -- registration ------------------------------------------------------------

// RegisterStrategy registers save_strategy, get_strategy, list_strategies,
// and generate_schedule_plan against reg (see ../server/registry.go),
// backed by st's StrategyStore/ScheduleStore/VerdictStore/PersonStore.
func RegisterStrategy(reg *server.Registry, st *store.Store) {
	registerSaveStrategy(reg, st.Strategies(), st.Persons())
	registerGetStrategy(reg, st.Strategies(), st.Persons())
	registerListStrategies(reg, st.Strategies(), st.Persons())
	registerGenerateSchedulePlan(reg, st.Strategies(), st.Schedules(), st.Verdicts())
}
