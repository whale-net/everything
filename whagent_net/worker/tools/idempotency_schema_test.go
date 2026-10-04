package tools

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/whale-net/everything/libs/go/whagent"
)

func TestWithoutIdempotencyKey_StripsPropertyAndRequired(t *testing.T) {
	in := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"deployment_id":                map[string]any{"type": "integer"},
			whagent.IdempotencyKeyArgument: map[string]any{"type": "string"},
		},
		"required": []any{"deployment_id", whagent.IdempotencyKeyArgument},
	}

	out := withoutIdempotencyKey(in)

	assert.Equal(t, map[string]any{"deployment_id": map[string]any{"type": "integer"}}, out["properties"])
	assert.Equal(t, []any{"deployment_id"}, out["required"])
	// The input map is left untouched.
	assert.Contains(t, in["properties"], whagent.IdempotencyKeyArgument)
	assert.Equal(t, []any{"deployment_id", whagent.IdempotencyKeyArgument}, in["required"])
}

func TestWithoutIdempotencyKey_NoKeyOrNoSchema_ReturnedAsIs(t *testing.T) {
	read := map[string]any{"type": "object", "properties": map[string]any{"id": map[string]any{"type": "integer"}}}
	assert.Equal(t, read, withoutIdempotencyKey(read))
	assert.Nil(t, withoutIdempotencyKey(nil))
}
