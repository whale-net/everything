package main

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/whale-net/everything/whagent_net/session"
)

// TestApplyPinnedCaps covers the max_turns_override resolution the worker
// applies per turn: override wins, then the definition, then the platform
// default when the definition is 0.
func TestApplyPinnedCaps(t *testing.T) {
	i32 := func(v int32) *int32 { return &v }
	f64 := func(v float64) *float64 { return &v }

	multiTurn := session.AgentDefinition{AgentID: "a", MaxTurns: 10, MaxCostUSD: 3}
	unlimited := session.AgentDefinition{AgentID: "b", MaxTurns: 0, MaxCostUSD: 0}

	tests := []struct {
		name      string
		def       session.AgentDefinition
		turnsOvr  *int32
		costOvr   *float64
		wantTurns int
		wantCost  float64
	}{
		{"turns override=1 on multi-turn def is single-turn", multiTurn, i32(1), nil, 1, 3},
		{"turns override=N replaces definition", multiTurn, i32(4), nil, 4, 3},
		{"no override keeps definition max_turns", multiTurn, nil, nil, 10, 3},
		{"no override and def max_turns=0 uses default 100", unlimited, nil, nil, session.DefaultMaxTurns, session.DefaultMaxCostUSD},
		{"cost override replaces definition cap", multiTurn, nil, f64(7.5), 10, 7.5},
		{"both overrides applied together", multiTurn, i32(2), f64(0.5), 2, 0.5},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			before := tc.def
			got := applyPinnedCaps(tc.def, tc.turnsOvr, tc.costOvr)
			assert.Equal(t, tc.wantTurns, got.MaxTurns)
			assert.Equal(t, tc.wantCost, got.MaxCostUSD)
			assert.Equal(t, before, tc.def, "input definition must not be mutated")
		})
	}
}
