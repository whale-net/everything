package tools

// Pure-Go coverage for strategy.go's SaveStrategyInput/GetStrategyInput/
// ListStrategiesInput.ChannelScopeID() and SaveStrategyInput.
// IdempotencyKey(). FR47 (issue #1833) removed store.Cadence/
// parseCadence/advanceCadence and deleted generate_schedule_plan along
// with the pacingTracker/rollToWeekday helpers that existed only to
// serve it -- there is no longer any pure Go logic of that shape left in
// strategy.go to unit-test here. What remains -- save_strategy's
// verdict-viability gate, create-vs-update semantics, idempotent replay,
// and get_strategy/list_strategies -- all need a real Postgres and a
// real MCP transport, so that coverage lives in
// strategy_integration_test.go (build tag "integration") instead,
// mirroring schedule_draft_test.go's split.

import (
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
)

// ── ChannelScopeID / IdempotencyKey sanity for every input type ─────────

func TestStrategyInputs_ChannelScopeID(t *testing.T) {
	id := uuid.New()

	assert.Equal(t, id, SaveStrategyInput{ChannelID: id.String()}.ChannelScopeID())
	assert.Equal(t, uuid.Nil, SaveStrategyInput{ChannelID: "not-a-uuid"}.ChannelScopeID())

	assert.Equal(t, id, GetStrategyInput{ChannelID: id.String()}.ChannelScopeID())
	assert.Equal(t, id, ListStrategiesInput{ChannelID: id.String()}.ChannelScopeID())
}

func TestSaveStrategyInput_IdempotencyKey(t *testing.T) {
	in := SaveStrategyInput{IdempotencyKeyArg: "abc"}
	assert.Equal(t, "abc", in.IdempotencyKey())
}
