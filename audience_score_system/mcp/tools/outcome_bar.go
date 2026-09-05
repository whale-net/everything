// outcome_bar.go covers the MCP surface over `outcome_bar` (migration
// 014, issue #1882): set_outcome_bar (FR1, write) and get_outcome_bar
// (FR2, read) -- the per-Channel outcome bar naming which metric to
// classify against and the threshold that separates "calibrated" from
// "miss" -- plus get_calibration_trend (FR5/FR6/FR7, read, issue #1885),
// the bucketed calibration-trend read over store.CalibrationStore
// (../../store/calibration.go, already real from #1884), classified
// against the Channel's CURRENT outcome bar (FR4). All three sit over
// store.OutcomeBarStore (../../store/outcome_bar.go, already real from
// #1882).
//
// No tool performs its own role check: server.RegisterWrite/RegisterRead
// apply store.CanWrite/store.CanRead automatically to any input
// implementing server.ChannelScoped (NFR2), and that tier -- not
// Creator-only -- is deliberate. The FR17-authority precedent for
// Channel-level planning inputs (CanWrite gating; see ARCHITECTURE.md
// "FR17 authority"), from tooling retired outright by M2.1, issue #1832,
// governs this rule: a Channel's calibration configuration is available
// to any Creator or Analyst with an open role, same tier as every other
// write in this package. This task reproduces that RULE, not the retired
// code, and does not re-register either retired tool.
package tools

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/whale-net/everything/audience_score_system/mcp/server"
	"github.com/whale-net/everything/audience_score_system/store"
)

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
// so store.CanWrite (Creator, Co-Creator, or Analyst -- reproducing the
// retired FR17-authority rule for Channel-level planning inputs, see
// ARCHITECTURE.md "FR17 authority", not its code) applies automatically;
// no explicit role check belongs in mutate.
func registerSetOutcomeBar(reg *server.Registry, bars store.OutcomeBarStore) {
	server.RegisterWrite(reg, &mcp.Tool{
		Name: "set_outcome_bar",
		Description: "Define or update the Channel's outcome bar: which metric to classify against and the threshold " +
			"separating \"calibrated\" from \"miss\" (FR1). metric_name only accepts \"views\" in this milestone. " +
			"Repeated identical calls converge on a single row (NFR1) -- no idempotency_key needed or accepted. " +
			"Available to any Creator or Analyst with an open role on the Channel.",
	}, setOutcomeBarMutate(bars), setOutcomeBarRender(bars))
}

// setOutcomeBarMutate calls bars.Upsert with the caller's Person as
// UpdatedByPersonID (person is always non-nil here: RegisterWrite applies
// store.CanWrite via ChannelScopeID before mutate ever runs, and that
// resolution requires an authenticated caller). Maps
// store.ErrUnsupportedOutcomeBarMetric and store.ErrInvalidOutcomeBarThreshold
// to caller-facing messages -- neither should surface as a raw pgx error --
// and returns the resulting row's ChannelID as ref: see setOutcomeBarRender's
// doc comment on why ref is the channel_id, not the row's own surrogate id.
func setOutcomeBarMutate(bars store.OutcomeBarStore) server.WriteMutate[SetOutcomeBarInput] {
	return func(ctx context.Context, in SetOutcomeBarInput) (uuid.UUID, error) {
		channelID, err := uuid.Parse(in.ChannelID)
		if err != nil {
			return uuid.Nil, fmt.Errorf("channel_id is not a valid UUID: %w", err)
		}

		person := server.PersonFromContext(ctx)
		if person == nil {
			return uuid.Nil, fmt.Errorf("unauthenticated: no caller credential resolved")
		}

		b, err := bars.Upsert(ctx, store.SetOutcomeBarInput{
			ChannelID:         channelID,
			MetricName:        in.MetricName,
			ThresholdValue:    in.ThresholdValue,
			UpdatedByPersonID: person.ID,
		})
		if err != nil {
			if errors.Is(err, store.ErrUnsupportedOutcomeBarMetric) {
				return uuid.Nil, fmt.Errorf("metric_name must be %q in this milestone: %w", store.OutcomeBarMetricViews, err)
			}
			if errors.Is(err, store.ErrInvalidOutcomeBarThreshold) {
				return uuid.Nil, fmt.Errorf("threshold_value must not be negative: %w", err)
			}
			return uuid.Nil, err
		}
		return b.ChannelID, nil
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

// -- get_calibration_trend -------------------------------------------------

// defaultCalibrationTrendLimit bounds get_calibration_trend's bucket count
// when the caller doesn't specify one -- twelve months of buckets, most
// recent first-selected (see store.CalibrationStore.MonthlyTrend's DESC
// selection before the chronological reversal).
const defaultCalibrationTrendLimit = 12

// GetCalibrationTrendInput is get_calibration_trend's argument schema
// (FR5/FR7). Since/Before/Limit mirror browse.go's
// GetPredictionVsOutcomeInput / defaultPredictionVsOutcomeLimit paging
// idiom -- see that type's doc comment for the page-backward-past-
// truncation pattern this reproduces.
type GetCalibrationTrendInput struct {
	ChannelID string     `json:"channel_id" jsonschema:"Channel to read the calibration trend for, as a UUID string"`
	Since     *time.Time `json:"since,omitempty" jsonschema:"Only include calendar-month buckets whose candidate videos published at or after this time"`
	Before    *time.Time `json:"before,omitempty" jsonschema:"Only include calendar-month buckets whose candidate videos published strictly before this time -- pair with since to page backward past a truncated response"`
	Limit     int        `json:"limit,omitempty" jsonschema:"Maximum month buckets to return, most recent first-selected (default 12). The response's truncated flag is set when older buckets exist beyond it."`
}

// ChannelScopeID implements server.ChannelScoped.
func (i GetCalibrationTrendInput) ChannelScopeID() uuid.UUID {
	id, _ := uuid.Parse(i.ChannelID)
	return id
}

// CalibrationBucketOutput is one calendar-month row of the calibration
// trend, rendered from store.CalibrationBucket verbatim (FR5) -- no
// bucketing, filtering, or rate arithmetic happens in this package.
type CalibrationBucketOutput struct {
	BucketStart     string  `json:"bucket_start" jsonschema:"Start of this calendar-month bucket, RFC3339"`
	Candidates      int     `json:"candidates" jsonschema:"Calibration candidates (viable verdict, bound published video, synced metrics) whose video published in this month"`
	Calibrated      int     `json:"calibrated" jsonschema:"Candidates whose recorded metric met or exceeded the outcome bar's threshold"`
	Miscalibrated   int     `json:"miscalibrated" jsonschema:"Candidates whose recorded metric fell short of the outcome bar's threshold (candidates - calibrated)"`
	CalibrationRate float64 `json:"calibration_rate" jsonschema:"calibrated / candidates, in [0,1]"`
}

// GetCalibrationTrendOutput is get_calibration_trend's structured result
// (FR5/FR6/FR7).
type GetCalibrationTrendOutput struct {
	OutcomeBar OutcomeBarOutput          `json:"outcome_bar" jsonschema:"The bar these rows were classified against (FR4: always the CURRENT setting). configured=false means no trend was computed (FR6)."`
	Buckets    []CalibrationBucketOutput `json:"buckets" jsonschema:"One row per calendar month with at least one candidate, oldest first; empty when no bar is configured"`
	Truncated  bool                      `json:"truncated" jsonschema:"True if older month buckets exist beyond limit -- re-call with before set to the oldest returned bucket_start to page backward"`
}

// registerGetCalibrationTrend registers get_calibration_trend via
// server.RegisterRead, so store.CanRead applies automatically and
// Creator/Analyst see byte-identical output (NFR2).
func registerGetCalibrationTrend(reg *server.Registry, bars store.OutcomeBarStore, calibration store.CalibrationStore) {
	server.RegisterRead(reg, &mcp.Tool{
		Name: "get_calibration_trend",
		Description: "Read the Channel's calibration trend (FR5): one row per calendar month, bucketed by the " +
			"published video's published_at, of how many candidates met or missed the Channel's outcome bar. A " +
			"candidate is an idea with a viable verdict, a video_script that was bound to a published video, and " +
			"synced metrics for that video -- ideas not yet resolved one way or the other are excluded rather than " +
			"counted against the Creator. Classification always uses the Channel's CURRENT outcome bar (FR4), so " +
			"changing the bar via set_outcome_bar reclassifies history the next time this is called; there is no " +
			"historical snapshot. configured: false on outcome_bar means no outcome bar has ever been set for this " +
			"Channel (FR6) -- a successful response, never an error, with no buckets computed; call set_outcome_bar " +
			"first. Complements, and does not replace, get_prediction_vs_outcome's per-idea comparison. Single " +
			"Channel per call.",
	}, getCalibrationTrendHandler(bars, calibration))
}

// getCalibrationTrendHandler (1) calls bars.GetByChannel, returning
// notConfiguredOutcomeBar() with an empty, non-nil Buckets slice and
// Truncated=false on pgx.ErrNoRows (FR6, nil error --
// calibration.MonthlyTrend is never called in that branch); (2) otherwise
// defaults in.Limit to defaultCalibrationTrendLimit when <= 0 and calls
// calibration.MonthlyTrend(ctx, channelID, bar, in.Since, in.Before,
// limit); and (3) renders rows in the store's returned order (chronological
// -- never re-sorted) into a non-nil []CalibrationBucketOutput, echoing the
// bar classified against and passing truncated through unchanged (FR7).
func getCalibrationTrendHandler(bars store.OutcomeBarStore, calibration store.CalibrationStore) mcp.ToolHandlerFor[GetCalibrationTrendInput, GetCalibrationTrendOutput] {
	return func(ctx context.Context, _ *mcp.CallToolRequest, in GetCalibrationTrendInput) (*mcp.CallToolResult, GetCalibrationTrendOutput, error) {
		channelID, err := uuid.Parse(in.ChannelID)
		if err != nil {
			return nil, GetCalibrationTrendOutput{}, err
		}

		bar, err := bars.GetByChannel(ctx, channelID)
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return nil, GetCalibrationTrendOutput{
					OutcomeBar: notConfiguredOutcomeBar(),
					Buckets:    make([]CalibrationBucketOutput, 0),
					Truncated:  false,
				}, nil
			}
			return nil, GetCalibrationTrendOutput{}, err
		}

		limit := in.Limit
		if limit <= 0 {
			limit = defaultCalibrationTrendLimit
		}

		rows, truncated, err := calibration.MonthlyTrend(ctx, channelID, bar, in.Since, in.Before, limit)
		if err != nil {
			return nil, GetCalibrationTrendOutput{}, err
		}

		buckets := make([]CalibrationBucketOutput, 0, len(rows))
		for _, r := range rows {
			buckets = append(buckets, CalibrationBucketOutput{
				BucketStart:     r.BucketStart.Format(time.RFC3339),
				Candidates:      r.Candidates,
				Calibrated:      r.Calibrated,
				Miscalibrated:   r.Miscalibrated,
				CalibrationRate: r.Rate,
			})
		}

		return nil, GetCalibrationTrendOutput{
			OutcomeBar: toOutcomeBarOutput(bar),
			Buckets:    buckets,
			Truncated:  truncated,
		}, nil
	}
}

// -- registration ------------------------------------------------------------

// RegisterOutcomeBar registers set_outcome_bar, get_outcome_bar, and
// get_calibration_trend against reg (see ../server/registry.go), backed
// by bars and calibration.
func RegisterOutcomeBar(reg *server.Registry, bars store.OutcomeBarStore, calibration store.CalibrationStore) {
	registerSetOutcomeBar(reg, bars)
	registerGetOutcomeBar(reg, bars)
	registerGetCalibrationTrend(reg, bars, calibration)
}
