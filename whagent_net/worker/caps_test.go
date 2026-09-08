package main

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/whale-net/everything/whagent_net/session"
)

// TestCheckCaps_TurnCapTrips proves FR6: turn >= the definition's MaxTurns
// trips capCheck.Capped with CapKind turns, on the exact turn that reaches
// the cap (not one turn later).
func TestCheckCaps_TurnCapTrips(t *testing.T) {
	def := session.AgentDefinition{MaxTurns: 2, MaxCostUSD: 100}

	check, err := checkCaps(2, def, 0)
	require.NoError(t, err)
	assert.True(t, check.Capped)
	assert.Equal(t, session.CapKindTurns, check.CapKind)
}

// TestCheckCaps_TurnBelowCap_NotCapped proves the turn just short of the
// cap is not reported capped.
func TestCheckCaps_TurnBelowCap_NotCapped(t *testing.T) {
	def := session.AgentDefinition{MaxTurns: 2, MaxCostUSD: 100}

	check, err := checkCaps(1, def, 0)
	require.NoError(t, err)
	assert.False(t, check.Capped)
}

// TestCheckCaps_CostCapTrips proves FR7: costSoFar >= the definition's
// MaxCostUSD trips capCheck.Capped with CapKind cost, when the turn count
// itself is nowhere near the turn cap.
func TestCheckCaps_CostCapTrips(t *testing.T) {
	def := session.AgentDefinition{MaxTurns: 100, MaxCostUSD: 0.50}

	check, err := checkCaps(3, def, 0.50)
	require.NoError(t, err)
	assert.True(t, check.Capped)
	assert.Equal(t, session.CapKindCost, check.CapKind)
}

// TestCheckCaps_CostBelowCap_NotCapped proves a running cost just short of
// the cap is not reported capped.
func TestCheckCaps_CostBelowCap_NotCapped(t *testing.T) {
	def := session.AgentDefinition{MaxTurns: 100, MaxCostUSD: 0.50}

	check, err := checkCaps(3, def, 0.4999)
	require.NoError(t, err)
	assert.False(t, check.Capped)
}

// TestCheckCaps_BothExceeded_TurnsReportedFirst proves checkCaps' documented
// check order (caps.go's doc comment: "turns before cost"): when a call
// happens to exceed both caps simultaneously, exactly CapKindTurns is
// reported, never CapKindCost.
func TestCheckCaps_BothExceeded_TurnsReportedFirst(t *testing.T) {
	def := session.AgentDefinition{MaxTurns: 2, MaxCostUSD: 0.50}

	check, err := checkCaps(5, def, 5.00)
	require.NoError(t, err)
	assert.True(t, check.Capped)
	assert.Equal(t, session.CapKindTurns, check.CapKind, "turns must be checked before cost, so a call exceeding both reports turns")
}

// TestCheckCaps_Defaults_TurnCap proves FR6/FR7's documented defaults (100
// turns / $1) apply when an agent definition's MaxTurns is the Go zero
// value -- an unpopulated field must never be silently treated as
// "uncapped".
func TestCheckCaps_Defaults_TurnCap(t *testing.T) {
	def := session.AgentDefinition{} // MaxTurns, MaxCostUSD both zero-valued

	notCapped, err := checkCaps(defaultMaxTurns-1, def, 0)
	require.NoError(t, err)
	assert.False(t, notCapped.Capped, "turn %d must be under the default 100-turn cap", defaultMaxTurns-1)

	capped, err := checkCaps(defaultMaxTurns, def, 0)
	require.NoError(t, err)
	assert.True(t, capped.Capped)
	assert.Equal(t, session.CapKindTurns, capped.CapKind)
}

// TestCheckCaps_Defaults_CostCap proves the same for the $1 cost default.
func TestCheckCaps_Defaults_CostCap(t *testing.T) {
	def := session.AgentDefinition{} // MaxTurns, MaxCostUSD both zero-valued

	notCapped, err := checkCaps(1, def, defaultMaxCostUSD-0.01)
	require.NoError(t, err)
	assert.False(t, notCapped.Capped)

	capped, err := checkCaps(1, def, defaultMaxCostUSD)
	require.NoError(t, err)
	assert.True(t, capped.Capped)
	assert.Equal(t, session.CapKindCost, capped.CapKind)
}

// TestCheckCaps_EstimatedCostStillCounts proves checkCaps treats costSoFar
// purely as a number -- it has no notion of "estimated" vs.
// "provider-reported" (that distinction lives in usage.go's
// TurnUsage.CostEstimated and is folded into UsageStore.SumCost's running
// total before it ever reaches checkCaps, per
// TestUsageStore_SumCost_SumsAcrossTurnsIncludingEstimated in
// whagent_net/session). A session whose every turn's cost was
// estimated-only still caps once the sum reaches the threshold.
func TestCheckCaps_EstimatedCostStillCounts(t *testing.T) {
	def := session.AgentDefinition{MaxTurns: 100, MaxCostUSD: 1.0}

	// costSoFar as if summed entirely from CostEstimated=true turn_usage
	// rows -- checkCaps has no way to know that, and must not care.
	estimatedOnlySum := 1.0

	check, err := checkCaps(5, def, estimatedOnlySum)
	require.NoError(t, err)
	assert.True(t, check.Capped)
	assert.Equal(t, session.CapKindCost, check.CapKind)
}
