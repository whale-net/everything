package session

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestEffectiveMaxCostUSD(t *testing.T) {
	two, five := 2.0, 5.0
	cases := []struct {
		name     string
		override *float64
		defCost  float64
		want     float64
	}{
		{"override wins over definition", &two, 5, 2},
		{"definition used when no override", nil, 5, 5},
		{"unset definition falls back to platform default", nil, 0, DefaultMaxCostUSD},
		{"unset definition with override uses override", &five, 0, 5},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			def := AgentDefinition{MaxCostUSD: tc.defCost}
			assert.Equal(t, tc.want, EffectiveMaxCostUSD(tc.override, def))
			assert.Equal(t, tc.defCost, def.MaxCostUSD, "definition must not be mutated")
		})
	}
}

func TestValidateMaxCostUSD(t *testing.T) {
	assert.NoError(t, ValidateMaxCostUSD("max_cost_usd_override", 0.01))
	assert.NoError(t, ValidateMaxCostUSD("max_cost_usd_override", MaxCostUSDCeiling))
	for _, v := range []float64{0, -1, 10.01, 50} {
		err := ValidateMaxCostUSD("max_cost_usd_override", v)
		assert.Error(t, err, "value %g", v)
		if err != nil {
			assert.Contains(t, err.Error(), "max_cost_usd_override")
			assert.Contains(t, err.Error(), "ceiling 10")
		}
	}
}
