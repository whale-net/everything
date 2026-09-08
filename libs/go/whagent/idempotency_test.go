package whagent

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestDeriveIdempotencyKey_DeterministicForIdenticalInputs(t *testing.T) {
	a := DeriveIdempotencyKey("session-1", 3, 2)
	b := DeriveIdempotencyKey("session-1", 3, 2)
	assert.Equal(t, a, b)
	assert.NotEmpty(t, a)
}

func TestDeriveIdempotencyKey_DiffersAcrossCallIndex(t *testing.T) {
	assert.NotEqual(t,
		DeriveIdempotencyKey("session-1", 1, 1),
		DeriveIdempotencyKey("session-1", 1, 2),
	)
}

func TestDeriveIdempotencyKey_DiffersAcrossTurn(t *testing.T) {
	assert.NotEqual(t,
		DeriveIdempotencyKey("session-1", 1, 1),
		DeriveIdempotencyKey("session-1", 2, 1),
	)
}

func TestDeriveIdempotencyKey_DiffersAcrossSessionID(t *testing.T) {
	assert.NotEqual(t,
		DeriveIdempotencyKey("session-1", 1, 1),
		DeriveIdempotencyKey("session-2", 1, 1),
	)
}
