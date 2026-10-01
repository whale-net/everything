// Pure-Go coverage of paging.go's ResolvePageSize, the scope-bound
// Encode/DecodeContinuationToken pair, the filter-bound Encode/
// DecodeFilteredContinuationToken pair, and FilterSet's canonical encoding
// and builder semantics -- no database needed, since none of them depends on
// Postgres. A real query exercising a real Page[T] lives in
// task_console_integration_test.go instead, where a real cursor's SortKey/ID
// values exist to round-trip.
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

// The three refusals are three distinct sentinels, so a caller (and the
// error mapping above it) can tell "wrong scope" from "wrong filters" from
// "not a token" without string-matching a message.
func TestContinuationTokenRefusals_AreDistinct(t *testing.T) {
	assert.NotErrorIs(t, store.ErrTokenScopeMismatch, store.ErrTokenFilterMismatch)
	assert.NotErrorIs(t, store.ErrTokenFilterMismatch, store.ErrTokenScopeMismatch)
	assert.NotErrorIs(t, store.ErrTokenFilterMismatch, store.ErrInvalidContinuationToken)
	assert.NotErrorIs(t, store.ErrInvalidContinuationToken, store.ErrTokenFilterMismatch)
	assert.NotErrorIs(t, store.ErrTokenScopeMismatch, store.ErrInvalidContinuationToken)
}

// A scope-bound token names its scope and nothing about the filters: the
// wire carries the canonical encoding verbatim, and the cursor alongside it.
func TestFilteredContinuationToken_WireCarriesScopeAndCanonicalFilters(t *testing.T) {
	scopeID := uuid.New()
	cursor := store.Cursor{SortKey: "2026-09-21T00:00:00Z", ID: uuid.New()}
	filters := store.FilterString("milestone", "m1").WithUUID("product", uuid.New())

	fields := decodeTokenFields(t, store.EncodeFilteredContinuationToken(scopeID, filters, cursor))

	assert.Equal(t, scopeID.String(), fields["scope_id"])
	assert.Equal(t, cursor.SortKey, fields["sort_key"])
	assert.Equal(t, cursor.ID.String(), fields["id"])
	assert.Equal(t, store.CanonicalFilterSet(filters), fields["filters"])
}

// A cross-scope token's refusal must leak neither the scope it was issued
// under nor the cursor a resume would have paged from.
func TestDecodeContinuationToken_CrossScopeRefusalExposesNoIssuingScopeOrCursor(t *testing.T) {
	issuingScope := uuid.New()
	cursor := store.Cursor{SortKey: "sensitive-sort-key", ID: uuid.New()}
	tok := store.EncodeContinuationToken(issuingScope, cursor)

	_, err := store.DecodeContinuationToken(uuid.New(), tok)
	require.Error(t, err)
	assert.NotContains(t, err.Error(), issuingScope.String())
	assert.NotContains(t, err.Error(), "sensitive-sort-key")
}

// With and its typed counterparts are builders, not in-place mutations: a
// read that starts from a shared base filter set and derives per-request
// variants from it must not have that base rewritten under it, or the second
// variant silently inherits the first's keys and the token it is about to
// issue binds a filter set nobody applied.
func TestFilterSet_Builders_DoNotMutateReceiver(t *testing.T) {
	product := uuid.New()

	t.Run("string", func(t *testing.T) {
		base := store.FilterString("product", "p1")
		before := store.CanonicalFilterSet(base)

		_ = base.With("milestone", "m1")

		assert.Equal(t, before, store.CanonicalFilterSet(base))
	})

	// The typed builders share one copy-on-write path, so each is exercised
	// in turn: an in-place mutation in any of them corrupts the base.
	for name, derive := range map[string]func(store.FilterSet) store.FilterSet{
		"uuid": func(f store.FilterSet) store.FilterSet { return f.WithUUID("other_product", uuid.New()) },
		"bool": func(f store.FilterSet) store.FilterSet { return f.WithBool("only_stuck", true) },
		"int":  func(f store.FilterSet) store.FilterSet { return f.WithInt("lane_index", 2) },
	} {
		t.Run(name, func(t *testing.T) {
			base := store.FilterUUID("product", product)
			before := store.CanonicalFilterSet(base)

			_ = derive(base)

			assert.Equal(t, before, store.CanonicalFilterSet(base))
		})
	}
}

// Two variants derived from one base stay independent -- the reason the
// builders copy.
func TestFilterSet_DerivedVariants_AreIndependent(t *testing.T) {
	base := store.FilterString("product", "p1")

	escalated := base.With("state", "escalated")
	claimed := base.With("state", "claimed")

	assert.Equal(t, store.CanonicalFilterSet(store.FilterString("product", "p1")), store.CanonicalFilterSet(base))
	assert.NotEqual(t, store.CanonicalFilterSet(escalated), store.CanonicalFilterSet(claimed))
}

// A nil receiver is the empty filter set, so a read can build one up from
// nothing without a nil check of its own.
func TestFilterSet_Builders_OnNilReceiver(t *testing.T) {
	var base store.FilterSet

	assert.Equal(t, store.CanonicalFilterSet(store.FilterString("k", "v")), store.CanonicalFilterSet(base.With("k", "v")))
	assert.Equal(t, store.CanonicalFilterSet(store.FilterBool("k", true)), store.CanonicalFilterSet(base.WithBool("k", true)))
	assert.Equal(t, store.CanonicalFilterSet(store.FilterInt("k", 7)), store.CanonicalFilterSet(base.WithInt("k", 7)))

	id := uuid.New()
	assert.Equal(t, store.CanonicalFilterSet(store.FilterUUID("k", id)), store.CanonicalFilterSet(base.WithUUID("k", id)))
}

// The typed constructors and their With counterparts are one API: a request
// that assembles its first filter with FilterUUID accepts a token issued by
// one that started from an empty set and grew the same key.
func TestFilterSet_TopLevelConstructors_MatchWithCounterparts(t *testing.T) {
	product := uuid.New()

	assert.Equal(t, store.CanonicalFilterSet(store.FilterString("k", "v")), store.CanonicalFilterSet(store.FilterSet{}.With("k", "v")))
	assert.Equal(t, store.CanonicalFilterSet(store.FilterUUID("k", product)), store.CanonicalFilterSet(store.FilterSet{}.WithUUID("k", product)))
	assert.Equal(t, store.CanonicalFilterSet(store.FilterBool("k", true)), store.CanonicalFilterSet(store.FilterSet{}.WithBool("k", true)))
	assert.Equal(t, store.CanonicalFilterSet(store.FilterInt("k", 7)), store.CanonicalFilterSet(store.FilterSet{}.WithInt("k", 7)))
}

// The canonical order is lexicographic, pinned rather than merely
// self-consistent: a canonicalizer that shuffled keys consistently would make
// two differently-assembled requests agree while still emitting a different
// encoding from one release to the next, silently invalidating every token
// already handed out.
func TestCanonicalFilterSet_OrderIsLexicographic(t *testing.T) {
	// Assembled in deliberately non-lexicographic order.
	alpha := uuid.MustParse("00000000-0000-0000-0000-00000000000a")
	f := store.FilterSet{}.With("zebra", "z").With("middle", "m").WithUUID("alpha", alpha)

	assert.Equal(t,
		`[{"key":"alpha","value":"u:00000000-0000-0000-0000-00000000000a"},`+
			`{"key":"middle","value":"s:m"},`+
			`{"key":"zebra","value":"s:z"}]`,
		store.CanonicalFilterSet(f))
}

// Two requests that apply the same keys in different orders encode
// identically, so assembly order is never a reason to refuse a resume.
func TestCanonicalFilterSet_KeyOrder_IsCanonicalAcrossManyKeys(t *testing.T) {
	product := uuid.New()
	shuffled := store.FilterSet{}.WithBool("only_stuck", true).WithInt("lane_index", 2).
		With("milestone", "m1").WithUUID("product", product).With("state", "escalated")
	sorted := store.FilterSet{}.WithUUID("product", product).With("milestone", "m1").
		WithInt("lane_index", 2).With("state", "escalated").WithBool("only_stuck", true)

	assert.Equal(t, store.CanonicalFilterSet(sorted), store.CanonicalFilterSet(shuffled))
}

// An empty filter value is a filter, not an absent one -- "no state" and
// "state is the empty string" must not share a token.
func TestCanonicalFilterSet_EmptyStringValueDiffersFromAbsent(t *testing.T) {
	assert.NotEqual(t, store.CanonicalFilterSet(store.FilterString("state", "")), store.CanonicalFilterSet(nil))
}

// Filter values are caller's own strings, so the canonical encoding has to
// survive quotes, backslashes and non-ASCII text without two different
// values collapsing onto one encoding.
func TestCanonicalFilterSet_ValuesAreEscapedNotCollapsed(t *testing.T) {
	quoted := store.FilterString("title", `he said "hi"`)
	backslash := store.FilterString("title", `he said "hi"\`)
	nonASCII := store.FilterString("title", "naïve — title")

	encoded := map[string]string{
		"quoted":     store.CanonicalFilterSet(quoted),
		"backslash":  store.CanonicalFilterSet(backslash),
		"nonASCII":   store.CanonicalFilterSet(nonASCII),
		"otherPlain": store.CanonicalFilterSet(store.FilterString("title", "naive - title")),
	}
	seen := map[string]string{}
	for name, v := range encoded {
		require.NotContains(t, seen, v, "%s collides with another value's encoding", name)
		seen[v] = name
	}
}

// The filter set a read binds is exactly the one it applied: every key
// survives the token, so a resume under the same filters pages from the same
// cursor rather than starting over.
func TestFilteredContinuationToken_WholeFilterSetSurvivesTheToken(t *testing.T) {
	scopeID := uuid.New()
	cursor := store.Cursor{SortKey: "k", ID: uuid.New()}
	product := uuid.New()
	filters := store.FilterUUID("product", product).
		With("milestone", "m1").
		WithInt("lane_index", 3).
		WithBool("only_stuck", false).
		With("state", "")

	tok := store.EncodeFilteredContinuationToken(scopeID, filters, cursor)

	// Same logical filters, rebuilt from scratch key by key, in another order.
	resumed := store.FilterSet{}.With("state", "").WithBool("only_stuck", false).
		WithInt("lane_index", 3).With("milestone", "m1").WithUUID("product", product)

	got, err := store.DecodeFilteredContinuationToken(scopeID, resumed, tok)
	require.NoError(t, err)
	assert.Equal(t, cursor, got)
}
