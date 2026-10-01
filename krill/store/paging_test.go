// Pure-Go coverage of paging.go's ResolvePageSize, the scope-bound
// Encode/DecodeContinuationToken pair, and the filter-bound
// Encode/DecodeFilteredContinuationToken pair -- no database needed, since
// none of them depends on Postgres. A real query exercising a real Page[T]
// lives in task_console_integration_test.go instead, where a real cursor's
// SortKey/ID values exist to round-trip.
package store_test

import (
	"encoding/base64"
	"encoding/json"
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

// decodeTokenFields base64/JSON-decodes a token back into a generic map so a
// test can assert on the wire shape itself -- used to prove the unfiltered
// token's bytes are unchanged by the filter field's arrival.
func decodeTokenFields(t *testing.T, tok string) map[string]any {
	t.Helper()
	raw, err := base64.RawURLEncoding.DecodeString(tok)
	require.NoError(t, err)
	var fields map[string]any
	require.NoError(t, json.Unmarshal(raw, &fields))
	return fields
}

func TestCanonicalFilterSet_NilAndEmpty_EncodeToEmpty(t *testing.T) {
	assert.Empty(t, store.CanonicalFilterSet(nil))
	assert.Empty(t, store.CanonicalFilterSet(store.FilterSet{}))
}

func TestCanonicalFilterSet_KeyOrder_IsCanonical(t *testing.T) {
	product := uuid.New()
	forward := store.FilterString("milestone", "m1").WithUUID("product", product)
	reverse := store.FilterSet{}.WithUUID("product", product).With("milestone", "m1")

	assert.Equal(t, store.CanonicalFilterSet(forward), store.CanonicalFilterSet(reverse))
}

func TestCanonicalFilterSet_TypesAreNotInterchangeable(t *testing.T) {
	// Same key, same rendered digits, different kind: distinct filter sets.
	assert.NotEqual(t, store.CanonicalFilterSet(store.FilterInt("n", 1)), store.CanonicalFilterSet(store.FilterString("n", "1")))
	assert.NotEqual(t, store.CanonicalFilterSet(store.FilterBool("f", true)), store.CanonicalFilterSet(store.FilterString("f", "1")))
}

func TestCanonicalFilterSet_AbsentDiffersFromZeroValue(t *testing.T) {
	assert.NotEqual(t, store.CanonicalFilterSet(store.FilterInt("n", 0)), store.CanonicalFilterSet(nil))
	assert.NotEqual(t, store.CanonicalFilterSet(store.FilterBool("only", false)), store.CanonicalFilterSet(nil))
}

func TestFilteredContinuationToken_RoundTrip(t *testing.T) {
	scopeID := uuid.New()
	cursor := store.Cursor{SortKey: "2026-09-21T00:00:00Z", ID: uuid.New()}
	filters := store.FilterString("milestone", "m1").
		WithUUID("product", uuid.New()).
		WithBool("only_stuck", true).
		WithInt("lane_index", 2)

	tok := store.EncodeFilteredContinuationToken(scopeID, filters, cursor)
	require.NotEmpty(t, tok)

	got, err := store.DecodeFilteredContinuationToken(scopeID, filters, tok)
	require.NoError(t, err)
	assert.Equal(t, cursor, got)
}

func TestFilteredContinuationToken_LogicallyEqualFilterSetsAcceptEachOther(t *testing.T) {
	scopeID := uuid.New()
	cursor := store.Cursor{SortKey: "k", ID: uuid.New()}
	product := uuid.New()

	issued := store.FilterString("milestone", "m1").WithUUID("product", product)
	resumed := store.FilterSet{}.WithUUID("product", product).With("milestone", "m1")

	tok := store.EncodeFilteredContinuationToken(scopeID, issued, cursor)
	got, err := store.DecodeFilteredContinuationToken(scopeID, resumed, tok)
	require.NoError(t, err)
	assert.Equal(t, cursor, got)
}

func TestDecodeFilteredContinuationToken_DifferentFilterSet_Rejected(t *testing.T) {
	scopeID := uuid.New()
	cursor := store.Cursor{SortKey: "k", ID: uuid.New()}
	issued := store.FilterString("milestone", "m1")

	tok := store.EncodeFilteredContinuationToken(scopeID, issued, cursor)

	cases := map[string]store.FilterSet{
		"different value":   store.FilterString("milestone", "m2"),
		"different key":     store.FilterString("product", "p1"),
		"extra filter":      store.FilterString("milestone", "m1").WithBool("only_stuck", true),
		"missing filter":    nil,
		"zero value for it": store.FilterString("milestone", "m1").WithInt("lane_index", 0),
	}
	for name, presented := range cases {
		t.Run(name, func(t *testing.T) {
			got, err := store.DecodeFilteredContinuationToken(scopeID, presented, tok)
			require.Error(t, err)
			assert.ErrorIs(t, err, store.ErrTokenFilterMismatch)
			// Refused before the cursor is handed back -- a wrong-filter
			// resume must not even be able to page.
			assert.Equal(t, store.Cursor{}, got)
		})
	}
}

func TestDecodeFilteredContinuationToken_WrongScope_IsScopeRefusalNotFilterRefusal(t *testing.T) {
	issuingScope := uuid.New()
	cursor := store.Cursor{SortKey: "k", ID: uuid.New()}
	filters := store.FilterString("milestone", "m1")
	tok := store.EncodeFilteredContinuationToken(issuingScope, filters, cursor)

	// Wrong scope and wrong filters at once: the scope refusal wins, since
	// it is the outer boundary.
	_, err := store.DecodeFilteredContinuationToken(uuid.New(), store.FilterString("milestone", "m2"), tok)
	require.Error(t, err)
	assert.ErrorIs(t, err, store.ErrTokenScopeMismatch)
	assert.NotErrorIs(t, err, store.ErrTokenFilterMismatch)
}

func TestFilteredContinuationToken_RefusalExposesNoFilterValueOrCursor(t *testing.T) {
	scopeID := uuid.New()
	cursor := store.Cursor{SortKey: "sensitive-sort-key", ID: uuid.New()}
	issued := store.FilterString("milestone", "secret-milestone-name")
	tok := store.EncodeFilteredContinuationToken(scopeID, issued, cursor)

	_, err := store.DecodeFilteredContinuationToken(scopeID, store.FilterString("milestone", "other"), tok)
	require.Error(t, err)
	assert.NotContains(t, err.Error(), "secret-milestone-name")
	assert.NotContains(t, err.Error(), "sensitive-sort-key")
}

func TestFilteredContinuationToken_MalformedStillMalformed(t *testing.T) {
	_, err := store.DecodeFilteredContinuationToken(uuid.New(), store.FilterString("milestone", "m1"), "not-a-valid-token")
	require.Error(t, err)
	assert.ErrorIs(t, err, store.ErrInvalidContinuationToken)
	assert.NotErrorIs(t, err, store.ErrTokenFilterMismatch)
}

// The scope-only entry points must keep the exact pre-filter behaviour: the
// same bytes for the same inputs, and no filter requirement on their callers.
func TestContinuationToken_NoFilter_BackCompatBytes(t *testing.T) {
	scopeID := uuid.New()
	cursor := store.Cursor{SortKey: "k", ID: uuid.New()}

	legacy := store.EncodeContinuationToken(scopeID, cursor)
	assert.Equal(t, legacy, store.EncodeFilteredContinuationToken(scopeID, nil, cursor))
	assert.Equal(t, legacy, store.EncodeFilteredContinuationToken(scopeID, store.FilterSet{}, cursor))

	// No "filters" key is emitted at all, so a token issued today is still
	// readable by any decoder that predates the field.
	assert.NotContains(t, decodeTokenFields(t, legacy), "filters")
}

func TestDecodeContinuationToken_FilteredTokenRefusedByUnfilteredDecode(t *testing.T) {
	scopeID := uuid.New()
	cursor := store.Cursor{SortKey: "k", ID: uuid.New()}
	tok := store.EncodeFilteredContinuationToken(scopeID, store.FilterString("milestone", "m1"), cursor)

	// An unfiltered decode is by definition a request applying no filters,
	// so a filter-bound token can never be resumed through it.
	_, err := store.DecodeContinuationToken(scopeID, tok)
	require.Error(t, err)
	assert.ErrorIs(t, err, store.ErrTokenFilterMismatch)
}

func TestDecodeFilteredContinuationToken_ScopeOnlyTokenAcceptedWithNilFilters(t *testing.T) {
	scopeID := uuid.New()
	cursor := store.Cursor{SortKey: "k", ID: uuid.New()}

	// The existing queue reads decode their scope-only tokens through the
	// filtered entry point with no filters, unchanged.
	tok := store.EncodeContinuationToken(scopeID, cursor)
	got, err := store.DecodeFilteredContinuationToken(scopeID, nil, tok)
	require.NoError(t, err)
	assert.Equal(t, cursor, got)
}
