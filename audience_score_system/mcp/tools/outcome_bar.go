// outcome_bar.go covers the MCP surface over `outcome_bar` (migration
// 014, issue #1882): set_outcome_bar (FR1, write) and get_outcome_bar
// (FR2, read) -- the per-Channel outcome bar naming which metric to
// classify against and the threshold that separates "calibrated" from
// "miss" (FR4, #1885's own task, reads it). Both tools sit over
// store.OutcomeBarStore (../../store/outcome_bar.go, already real from
// #1882).
//
// Neither tool performs its own role check: server.RegisterWrite/
// RegisterRead apply store.CanWrite/store.CanRead automatically to any
// input implementing server.ChannelScoped (NFR2), and that tier -- not
// Creator-only -- is deliberate. set_pacing_policy/get_pacing_policy
// (retired outright by M2.1, issue #1832; see ARCHITECTURE.md "FR17
// authority") are the root plan's cited precedent for this rule: a
// Channel's calibration configuration is available to any Creator or
// Analyst with an open role, same tier as every other write in this
// package. This task reproduces that RULE, not the retired code, and
// does not re-register either retired tool name.
//
// Scaffold only: setOutcomeBarMutate below returns
// errOutcomeBarToolNotImplemented until Implementation wires in the real
// store.OutcomeBarStore.Upsert call and its error-message mapping, same
// scaffold/feat split video_script.go (issue #1823) followed for its own
// mutate functions.
package tools

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/whale-net/everything/audience_score_system/mcp/server"
	"github.com/whale-net/everything/audience_score_system/store"
)

// errOutcomeBarToolNotImplemented is returned by setOutcomeBarMutate until
// Implementation lands. Never wrapped into a store error -- this is
// scaffold-only scaffolding, not a runtime condition a caller can hit
// through the real store.
var errOutcomeBarToolNotImplemented = errors.New("outcome_bar tool not implemented yet")

// -- shared rendering ---------------------------------------------------------

// OutcomeBarOutput is the Channel's configured outcome bar, as both
// get_outcome_bar and get_calibration_trend (#1885, reused verbatim --
// that task must not duplicate this type) render it. Configured=false is
// FR2's explicit "not configured" result -- never a defaulted threshold
// and never an error.
type OutcomeBarOutput struct {
	Configured        bool     `json:"configured" jsonschema:"whether this Channel has ever had an outcome bar set; when false every field below is empty/absent, never a defaulted threshold (FR2)"`
	MetricName        string   `json:"metric_name,omitempty" jsonschema:"the metric this outcome bar classifies against; only \"views\" is accepted in this milestone. Empty when configured is false."`
	ThresholdValue    *float64 `json:"threshold_value,omitempty" jsonschema:"the threshold separating \"calibrated\" from \"miss\"; absent when configured is false"`
	UpdatedAt         string   `json:"updated_at,omitempty" jsonschema:"when this outcome bar was last set, RFC3339; empty when configured is false"`
	UpdatedByPersonID string   `json:"updated_by_person_id,omitempty" jsonschema:"the Person who last set this outcome bar, as a UUID string; empty when configured is false"`
}

// toOutcomeBarOutput renders b -- an outcome bar known to exist -- as
// OutcomeBarOutput. Shared by set_outcome_bar's render step and
// get_outcome_bar's handler (and, per #1885, get_calibration_trend) so
// none of them can ever disagree on shape.
func toOutcomeBarOutput(b store.OutcomeBar) OutcomeBarOutput {
	threshold := b.ThresholdValue
	return OutcomeBarOutput{
		Configured:        true,
		MetricName:        b.MetricName,
		ThresholdValue:    &threshold,
		UpdatedAt:         b.UpdatedAt.Format(time.RFC3339),
		UpdatedByPersonID: b.UpdatedByPersonID.String(),
	}
}

// notConfiguredOutcomeBar is FR2's explicit "not configured" result: a
// successful response, never an error, and never a defaulted threshold.
func notConfiguredOutcomeBar() OutcomeBarOutput {
	return OutcomeBarOutput{Configured: false}
}

// -- set_outcome_bar ------------------------------------------------------

// SetOutcomeBarInput is set_outcome_bar's argument schema (FR1). It
// deliberately carries NO idempotency_key field: store.OutcomeBarStore.
// Upsert (../../store/outcome_bar.go) converges on the single row per
// channel_id via a natural-key upsert (NFR1), so mutate is safe to run
// directly on every call -- see server.RegisterWrite's doc comment on
// inputs that don't implement IdempotencyKeyed. Do not "fix" this by
// adding one back.
type SetOutcomeBarInput struct {
	ChannelID      string  `json:"channel_id" jsonschema:"Channel this outcome bar belongs to, as a UUID string"`
	MetricName     string  `json:"metric_name" jsonschema:"the metric to classify against; only \"views\" is accepted in this milestone"`
	ThresholdValue float64 `json:"threshold_value" jsonschema:"the threshold separating \"calibrated\" from \"miss\"; must not be negative"`
}

// ChannelScopeID implements server.ChannelScoped.
func (i SetOutcomeBarInput) ChannelScopeID() uuid.UUID {
	id, _ := uuid.Parse(i.ChannelID)
	return id
}

// registerSetOutcomeBar registers set_outcome_bar via server.RegisterWrite,
// so store.CanWrite (Creator, Co-Creator, or Analyst -- reproducing
// set_pacing_policy's retired FR17-authority rule, not its code) applies
// automatically; no explicit role check belongs in mutate.
func registerSetOutcomeBar(reg *server.Registry, bars store.OutcomeBarStore) {
	server.RegisterWrite(reg, &mcp.Tool{
		Name: "set_outcome_bar",
		Description: "Define or update the Channel's outcome bar: which metric to classify against and the threshold " +
			"separating \"calibrated\" from \"miss\" (FR1). metric_name only accepts \"views\" in this milestone. " +
			"Repeated identical calls converge on a single row (NFR1) -- no idempotency_key needed or accepted. " +
			"Available to any Creator or Analyst with an open role on the Channel.",
	}, setOutcomeBarMutate(bars), setOutcomeBarRender(bars))
}

// setOutcomeBarMutate is scaffold-only: Implementation must call
// bars.Upsert with server.PersonFromContext(ctx).ID as
// UpdatedByPersonID, map store.ErrUnsupportedOutcomeBarMetric and
// store.ErrInvalidOutcomeBarThreshold to caller-facing messages (neither
// should surface as a raw pgx error), and return the resulting row's
// ChannelID as ref -- see setOutcomeBarRender's doc comment on why ref is
// the channel_id, not the row's own surrogate id.
func setOutcomeBarMutate(bars store.OutcomeBarStore) server.WriteMutate[SetOutcomeBarInput] {
	return func(ctx context.Context, in SetOutcomeBarInput) (uuid.UUID, error) {
		return uuid.Nil, errOutcomeBarToolNotImplemented
	}
}

// setOutcomeBarRender re-reads the outcome bar via bars.GetByChannel,
// never trusting anything cached from mutate, per server.RegisterWrite's
// contract that render runs on every call so the response is never
// stale. ref is the Channel's id: store.OutcomeBarStore exposes no
// GetByID, only GetByChannel, so the channel_id -- itself a column of
// "the row" mutate upserted -- is the only key that can round-trip
// through ref.
func setOutcomeBarRender(bars store.OutcomeBarStore) server.WriteRender[OutcomeBarOutput] {
	return func(ctx context.Context, ref uuid.UUID) (*mcp.CallToolResult, OutcomeBarOutput, error) {
		b, err := bars.GetByChannel(ctx, ref)
		if err != nil {
			return nil, OutcomeBarOutput{}, err
		}
		return nil, toOutcomeBarOutput(b), nil
	}
}

// -- get_outcome_bar ------------------------------------------------------

// GetOutcomeBarInput is get_outcome_bar's argument schema (FR2).
type GetOutcomeBarInput struct {
	ChannelID string `json:"channel_id" jsonschema:"Channel to read the outcome bar for, as a UUID string"`
}

// ChannelScopeID implements server.ChannelScoped.
func (i GetOutcomeBarInput) ChannelScopeID() uuid.UUID {
	id, _ := uuid.Parse(i.ChannelID)
	return id
}

// registerGetOutcomeBar registers get_outcome_bar via server.RegisterRead,
// so store.CanRead applies automatically and Creator/Analyst see
// byte-identical output (NFR2).
func registerGetOutcomeBar(reg *server.Registry, bars store.OutcomeBarStore) {
	server.RegisterRead(reg, &mcp.Tool{
		Name: "get_outcome_bar",
		Description: "Read the Channel's configured outcome bar (FR2). configured: false is a successful response, " +
			"never an error and never a defaulted threshold, meaning no outcome bar has ever been set for this " +
			"Channel.",
	}, getOutcomeBarHandler(bars))
}

func getOutcomeBarHandler(bars store.OutcomeBarStore) mcp.ToolHandlerFor[GetOutcomeBarInput, OutcomeBarOutput] {
	return func(ctx context.Context, _ *mcp.CallToolRequest, in GetOutcomeBarInput) (*mcp.CallToolResult, OutcomeBarOutput, error) {
		channelID, err := uuid.Parse(in.ChannelID)
		if err != nil {
			return nil, OutcomeBarOutput{}, err
		}

		b, err := bars.GetByChannel(ctx, channelID)
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return nil, notConfiguredOutcomeBar(), nil
			}
			return nil, OutcomeBarOutput{}, err
		}
		return nil, toOutcomeBarOutput(b), nil
	}
}

// -- registration ------------------------------------------------------------

// RegisterOutcomeBar registers set_outcome_bar and get_outcome_bar against
// reg (see ../server/registry.go), backed by bars.
func RegisterOutcomeBar(reg *server.Registry, bars store.OutcomeBarStore) {
	registerSetOutcomeBar(reg, bars)
	registerGetOutcomeBar(reg, bars)
}
