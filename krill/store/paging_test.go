// Pure-Go coverage of paging.go's ResolvePageSize and Encode/
// DecodeContinuationToken (issue #2869, NFR6) -- no database needed, since
// neither depends on Postgres. This is the first consumer of either
// function to add its own coverage (issue #2873, FR10's "paging default/
// max/clamp" and "cross-scope token rejected" Testing items); a real query
// exercising a real Page[T] lives in task_console_integration_test.go
// instead, where a real cursor's SortKey/ID values exist to round-trip.
package store_test

import (
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/whale-net/everything/krill/store"
)

func TestResolvePageSize_AbsentOrZero_AppliesDefault(t *testing.T) {
	assert.Equal(t, store.DefaultConsolePageSize, store.ResolvePageSize(0))
	assert.Equal(t, store.DefaultConsolePageSize, store.ResolvePageSize(-1))
}

func TestResolvePageSize_WithinRange_Passthrough(t *testing.T) {
	assert.Equal(t, 10, store.ResolvePageSize(10))
}

func TestResolvePageSize_AboveMax_Clamped(t *testing.T) {
	assert.Equal(t, store.MaxConsolePageSize, store.ResolvePageSize(store.MaxConsolePageSize+1))
	assert.Equal(t, store.MaxConsolePageSize, store.ResolvePageSize(1_000_000))
}

func TestContinuationToken_RoundTrip(t *testing.T) {
	scopeID := uuid.New()
	cursor := store.Cursor{SortKey: "2026-09-21T00:00:00Z", ID: uuid.New()}

	tok := store.EncodeContinuationToken(scopeID, cursor)
	require.NotEmpty(t, tok)

	got, err := store.DecodeContinuationToken(scopeID, tok)
	require.NoError(t, err)
	assert.Equal(t, cursor, got)
}

func TestDecodeContinuationToken_CrossScope_Rejected(t *testing.T) {
	issuingScope := uuid.New()
	otherScope := uuid.New()
	tok := store.EncodeContinuationToken(issuingScope, store.Cursor{SortKey: "x", ID: uuid.New()})

	_, err := store.DecodeContinuationToken(otherScope, tok)
	require.Error(t, err)
	assert.ErrorIs(t, err, store.ErrTokenScopeMismatch)
}

func TestDecodeContinuationToken_Malformed_Rejected(t *testing.T) {
	_, err := store.DecodeContinuationToken(uuid.New(), "not-a-valid-token")
	require.Error(t, err)
	assert.ErrorIs(t, err, store.ErrInvalidContinuationToken)
}
