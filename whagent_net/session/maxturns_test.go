package session

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestEffectiveMaxTurns(t *testing.T) {
	one, ten := int32(1), int32(10)
	cases := []struct {
		name     string
		override *int32
		defTurns int
		want     int
	}{
		{"override wins over definition", &ten, 5, 10},
		{"override of 1 is single-turn even when definition is higher", &one, 50, 1},
		{"definition used when no override", nil, 5, 5},
		{"definition of 1 is single-turn", nil, 1, 1},
		{"unset definition falls back to platform default", nil, 0, DefaultMaxTurns},
		{"unset definition with override uses override", &ten, 0, 10},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			def := AgentDefinition{MaxTurns: tc.defTurns}
			assert.Equal(t, tc.want, EffectiveMaxTurns(tc.override, def))
			assert.Equal(t, tc.defTurns, def.MaxTurns, "definition must not be mutated")
		})
	}
}
