// Strategy MCP tool group (issue #1637): save_strategy/get_strategy/
// list_strategies manage a grouping of one or more viable
// viability_verdict rows under a title and an active flag -- context for
// the Ideas/scripts built from them, with no recurrence/pacing mechanics
// of its own (FR47, issue #1833 removed `cadence` and the
// generate_schedule_plan tool that read it). See ../../store/strategy.go
// (StrategyStore) and migration 008's comment for the schema and design
// rationale.
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
	PreferredWeekday     string                  `json:"preferred_weekday,omitempty" jsonschema:"full English weekday name this Strategy's slots roll onto; empty means no day preference"`
	Active               bool                    `json:"active" jsonschema:"whether this Strategy is currently active"`
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
		PreferredWeekday:     d.PreferredWeekday,
		Active:               d.Active,
		Verdicts:             verdicts,
		CreatedByPersonID:    d.CreatedByPersonID.String(),
		CreatedByDisplayName: creator.DisplayName,
		CreatedAt:            d.CreatedAt.Format(time.RFC3339),
		UpdatedAt:            d.UpdatedAt.Format(time.RFC3339),
	}, nil
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
// it).
func registerSaveStrategy(reg *server.Registry, strategies store.StrategyStore, persons store.PersonStore) {
	server.RegisterWrite(reg, &mcp.Tool{
		Name: "save_strategy",
		Description: "Create or update a Strategy: a grouping (optionally pinned to a preferred weekday) built " +
			"directly from one or more viable viability_verdict rows (issue #1637). Pass verdict_ids, not idea_ids: " +
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

		var preferredWeekday string
		if strings.TrimSpace(in.PreferredWeekday) != "" {
			pw, err := parseWeekdayName(in.PreferredWeekday)
			if err != nil {
				return uuid.Nil, err
			}
			preferredWeekday = pw
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

// defaultListStrategiesLimit bounds list_strategies' response when a
// caller supplies limit <= 0. Strategies are a deliberately curated
// per-Channel construct (likely single digits in practice per issue
// #1813), so this default is generous headroom rather than a tight cap --
// it exists as a query-level guard against ever hitting the calling MCP
// client's response-size cap, not because that's expected in normal use.
// The limit is pushed into ListByChannel's SQL LIMIT (issue #1808/#1813's
// follow-up), so a Strategy trimmed by truncation never pays its N+1
// linked-verdicts query either.
const defaultListStrategiesLimit = 50

// ListStrategiesInput is list_strategies's argument schema.
type ListStrategiesInput struct {
	ChannelID  string `json:"channel_id" jsonschema:"Channel to list Strategies for, as a UUID string"`
	ActiveOnly bool   `json:"active_only,omitempty" jsonschema:"restrict to active Strategies only; default false lists every Strategy regardless of active"`
	Limit      int    `json:"limit,omitempty" jsonschema:"Maximum Strategies to return, oldest first (default 50). The response's truncated flag is set when more matching Strategies exist."`
}

// ChannelScopeID implements server.ChannelScoped.
func (i ListStrategiesInput) ChannelScopeID() uuid.UUID {
	id, _ := uuid.Parse(i.ChannelID)
	return id
}

// ListStrategiesOutput is list_strategies's structured result.
type ListStrategiesOutput struct {
	Strategies []StrategyOutput `json:"strategies" jsonschema:"Strategies for this Channel matching active_only, oldest first"`
	Truncated  bool             `json:"truncated" jsonschema:"True if more matching Strategies exist beyond limit"`
}

func registerListStrategies(reg *server.Registry, strategies store.StrategyStore, persons store.PersonStore) {
	server.RegisterRead(reg, &mcp.Tool{
		Name: "list_strategies",
		Description: "List a Channel's Strategies (issue #1637), each with the viable viability_verdict rows it's " +
			"built from, oldest first. Response is capped at limit (default 50); see truncated -- Strategies are a " +
			"deliberately curated construct so this is a guard rail rather than an expected limit in normal use.",
	}, listStrategiesHandler(strategies, persons))
}

func listStrategiesHandler(strategies store.StrategyStore, persons store.PersonStore) mcp.ToolHandlerFor[ListStrategiesInput, ListStrategiesOutput] {
	return func(ctx context.Context, _ *mcp.CallToolRequest, in ListStrategiesInput) (*mcp.CallToolResult, ListStrategiesOutput, error) {
		channelID, err := uuid.Parse(in.ChannelID)
		if err != nil {
			return nil, ListStrategiesOutput{}, fmt.Errorf("channel_id is not a valid UUID: %w", err)
		}

		limit := in.Limit
		if limit <= 0 {
			limit = defaultListStrategiesLimit
		}
		details, truncated, err := strategies.ListByChannel(ctx, channelID, in.ActiveOnly, limit)
		if err != nil {
			return nil, ListStrategiesOutput{}, err
		}

		out := ListStrategiesOutput{Strategies: make([]StrategyOutput, 0, len(details)), Truncated: truncated}
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

// -- registration ------------------------------------------------------------

// RegisterStrategy registers save_strategy, get_strategy, and
// list_strategies against reg (see ../server/registry.go), backed by
// st's StrategyStore/PersonStore.
func RegisterStrategy(reg *server.Registry, st *store.Store) {
	registerSaveStrategy(reg, st.Strategies(), st.Persons())
	registerGetStrategy(reg, st.Strategies(), st.Persons())
	registerListStrategies(reg, st.Strategies(), st.Persons())
}
