// This file (issue #2869, NFR6) is the paging machinery every M5 console
// query shares: FR4's ListClaimedTasks (task_console.go, this task), and
// the FR5/FR10/FR12 queries that reuse it rather than each rolling its
// own (this task's own issue body, "Why this task"). Every console query
// returns a bounded page -- a caller-supplied page size up to
// MaxConsolePageSize, DefaultConsolePageSize when none is given, a
// deterministic order, and a continuation token whenever more rows
// remain -- via keyset (cursor) paging, never OFFSET, so a page boundary
// stays stable under concurrent writes.
//
// Continuation tokens are scope-qualified (LB1, NFR1): EncodeContinuationToken
// embeds the issuing scope id, and DecodeContinuationToken rejects a token
// whose embedded scope id differs from the scope the resumed query is
// running against with ErrTokenScopeMismatch, before ever handing back
// its cursor value -- a token issued under one scope can never become a
// path across a scope boundary into another.
//
// A filtered read binds its filter set too: a token carries the canonical
// encoding of the filters the issuing request applied
// (CanonicalFilterSet), and resuming it against a request whose filters
// differ is refused with ErrTokenFilterMismatch rather than answered as a
// wrong-filter page or an empty one. A read with no filters keeps today's
// scope-only token, byte for byte.
package store

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strconv"

	"github.com/google/uuid"
)

// DefaultConsolePageSize is the page size every M5 console query applies
// when a caller's PageParams.PageSize is absent or zero (NFR6).
const DefaultConsolePageSize = 25

// MaxConsolePageSize is the largest page size a caller may request
// (NFR6). ResolvePageSize CLAMPS a larger request down to this value
// rather than rejecting it -- this task's documented choice: a Swarm
// Operator asking for "everything" gets the largest page this API will
// hand back in one round trip, not an error to retry with a smaller
// number.
const MaxConsolePageSize = 100

// PageParams is every M5 console query's paging input (NFR6):
// caller-supplied page size (ResolvePageSize applies the default/clamp)
// and an opaque ContinuationToken from a prior page's Page.NextToken, or
// "" for the first page.
type PageParams struct {
	PageSize          int
	ContinuationToken string
}

// Page is every M5 console query's paging output (NFR6): the page's rows
// plus NextToken, populated exactly when more rows remain beyond this
// page -- the final page's NextToken is "".
type Page[T any] struct {
	Items     []T
	NextToken string
}

// ResolvePageSize applies NFR6's page-size rule to requested (a caller's
// raw PageParams.PageSize): absent or zero -> DefaultConsolePageSize;
// above MaxConsolePageSize -> clamped down to it (see MaxConsolePageSize's
// own doc comment for why clamp, not reject).
func ResolvePageSize(requested int) int {
	if requested <= 0 {
		return DefaultConsolePageSize
	}
	if requested > MaxConsolePageSize {
		return MaxConsolePageSize
	}
	return requested
}

// FilterSet is one read's complete filter set: the named filters a
// request applied, absent meaning "not filtered on" (an unfiltered read
// has an empty or nil FilterSet). Values are stored in a canonical,
// self-describing string form so comparison is structural and type-safe --
// FilterInt("count", 1) can never equal FilterString("count", "1"), and an
// absent key never equals a key explicitly set to its zero value.
type FilterSet map[string]string

// The typed FilterSet constructors. A filter key never appears with two
// different value kinds, so two requests that agree logically encode
// identically whichever constructor they used.
func FilterString(key, value string) FilterSet {
	return FilterSet{key: stringFilterValue(value)}
}

func FilterUUID(key string, value uuid.UUID) FilterSet {
	return FilterSet{key: uuidFilterValue(value)}
}

func FilterBool(key string, value bool) FilterSet {
	return FilterSet{key: boolFilterValue(value)}
}

func FilterInt(key string, value int) FilterSet {
	return FilterSet{key: intFilterValue(value)}
}

// With returns a copy of f with key set to value -- FilterSet's own
// builder, so a read assembling several filters never has to copy the
// map by hand.
func (f FilterSet) With(key, value string) FilterSet {
	out := make(FilterSet, len(f)+1)
	maps.Copy(out, f)
	out[key] = stringFilterValue(value)
	return out
}

// WithUUID, WithBool and WithInt are With's typed counterparts.
func (f FilterSet) WithUUID(key string, value uuid.UUID) FilterSet {
	return f.withEncoded(key, uuidFilterValue(value))
}

func (f FilterSet) WithBool(key string, value bool) FilterSet {
	return f.withEncoded(key, boolFilterValue(value))
}

func (f FilterSet) WithInt(key string, value int) FilterSet {
	return f.withEncoded(key, intFilterValue(value))
}

func (f FilterSet) withEncoded(key, encoded string) FilterSet {
	out := make(FilterSet, len(f)+1)
	maps.Copy(out, f)
	out[key] = encoded
	return out
}

func stringFilterValue(v string) string  { return "s:" + v }
func uuidFilterValue(v uuid.UUID) string { return "u:" + v.String() }
func boolFilterValue(v bool) string {
	if v {
		return "b:1"
	}
	return "b:0"
}
func intFilterValue(v int) string { return "i:" + strconv.Itoa(v) }

// filterSetEntry is CanonicalFilterSet's per-key wire shape: the key, and
// its already-encoded self-describing value (so no type tag is needed
// here -- the value carries its own kind prefix).
type filterSetEntry struct {
	Key   string `json:"key"`
	Value string `json:"value"`
}

// CanonicalFilterSet renders f in a canonical form: filter keys in
// lexicographic order, each with its encoded value, JSON-marshalled. Two
// requests with the same logical filter set produce byte-identical
// output regardless of the order the caller assembled the map in, which is
// what makes token filter comparison stable. An empty or nil FilterSet
// encodes to "", the unfiltered case a scope-only token already carries.
func CanonicalFilterSet(f FilterSet) string {
	if len(f) == 0 {
		return ""
	}
	keys := slices.Sorted(maps.Keys(f))
	entries := make([]filterSetEntry, len(keys))
	for i, k := range keys {
		entries[i] = filterSetEntry{Key: k, Value: f[k]}
	}
	raw, err := json.Marshal(entries)
	if err != nil {
		// filterSetEntry is two strings -- always marshalable.
		panic(fmt.Sprintf("krill/store: canonicalize filter set: %v", err))
	}
	return string(raw)
}

// ErrTokenScopeMismatch is DecodeContinuationToken's named rejection
// (LB1, NFR1) when a token decodes cleanly but names a different scope
// than the one the resumed query is running against -- see this file's
// own package doc comment.
var ErrTokenScopeMismatch = errors.New("krill/store: continuation token was issued for a different scope")

// ErrInvalidContinuationToken is DecodeContinuationToken's named
// rejection for a token that is not valid base64, or does not decode to
// this file's own token shape -- a malformed or forged token, distinct
// from ErrTokenScopeMismatch (a well-formed token naming the wrong
// scope).
var ErrInvalidContinuationToken = errors.New("krill/store: malformed continuation token")

// ErrTokenFilterMismatch is the filtered-token sibling of
// ErrTokenScopeMismatch: the token decodes cleanly and names the right
// scope, but binds a different filter set than the request presenting it
// applied. Distinct from both refusals above so a caller can tell "you
// resumed with different filters" from "that token is malformed" or
// "that token belongs to another scope" -- and distinct from a page
// result, so a wrong-filter resume fails loudly instead of returning rows
// filtered differently or an empty page.
var ErrTokenFilterMismatch = errors.New("krill/store: continuation token was issued for a different filter set")

// Cursor is one console query's keyset paging position: the deterministic
// sort key's value at the last row of the previous page, plus that row's
// own id as the tiebreaker every console query's total order ends on
// (e.g. ListClaimedTasks' `lease_expires_at ASC, task.id ASC`). SortKey
// is opaque to this file -- each query encodes its own sort column into a
// string (e.g. a timestamp as time.RFC3339Nano) when it calls
// EncodeContinuationToken, and parses it back out of a resumed Cursor
// itself; this file only carries it verbatim.
type Cursor struct {
	SortKey string
	ID      uuid.UUID
}

// continuationToken is EncodeContinuationToken/DecodeContinuationToken's
// wire shape -- opaque to every caller outside this file (base64-encoded
// JSON), never hand-constructed elsewhere. Filters is omitempty so a
// read with no filters emits exactly the pre-filter shape, byte for byte.
type continuationToken struct {
	ScopeID uuid.UUID `json:"scope_id"`
	SortKey string    `json:"sort_key"`
	ID      uuid.UUID `json:"id"`
	Filters string    `json:"filters,omitempty"`
}

// EncodeContinuationToken builds the opaque token a Page.NextToken
// carries: scopeID (the scope boundary DecodeContinuationToken enforces
// on resume) plus cursor, this page's last row's keyset position. A
// filtered read uses EncodeFilteredContinuationToken instead, which binds
// its filter set alongside the scope.
func EncodeContinuationToken(scopeID uuid.UUID, cursor Cursor) string {
	return EncodeFilteredContinuationToken(scopeID, nil, cursor)
}

// EncodeFilteredContinuationToken is EncodeContinuationToken for a read
// that applies filters: the token additionally binds the canonical
// encoding of filters, so a resume under a different filter set is
// refused rather than answered with rows filtered differently.
func EncodeFilteredContinuationToken(scopeID uuid.UUID, filters FilterSet, cursor Cursor) string {
	tok := continuationToken{
		ScopeID: scopeID,
		SortKey: cursor.SortKey,
		ID:      cursor.ID,
	}
	if len(filters) > 0 {
		tok.Filters = CanonicalFilterSet(filters)
	}
	raw, err := json.Marshal(tok)
	if err != nil {
		// continuationToken's fields are UUIDs and strings -- all directly
		// JSON-marshalable, so Marshal cannot actually fail here. Panic
		// rather than silently hand back a token that would fail to decode.
		panic(fmt.Sprintf("krill/store: encode continuation token: %v", err))
	}
	return base64.RawURLEncoding.EncodeToString(raw)
}

// DecodeContinuationToken parses raw (a caller-supplied PageParams.
// ContinuationToken) against scopeID, the scope the resumed query is
// running against. Returns ErrInvalidContinuationToken for anything that
// does not decode to continuationToken's shape, and ErrTokenScopeMismatch
// when the token decodes cleanly but names a different scope than
// scopeID -- checked before the cursor is ever handed back, so a caller
// can never use a cross-scope token's cursor value even by accident. A
// token issued by a filtered read is refused here with
// ErrTokenFilterMismatch, since this decode is by definition an unfiltered
// resume.
func DecodeContinuationToken(scopeID uuid.UUID, raw string) (Cursor, error) {
	return DecodeFilteredContinuationToken(scopeID, nil, raw)
}

// DecodeFilteredContinuationToken is DecodeContinuationToken for a read
// that applies filters: the token's bound filter set must equal filters'
// canonical encoding, or the resume is refused with
// ErrTokenFilterMismatch -- before the cursor is handed back, so a
// wrong-filter resume yields no rows at all, never a wrong-filter page and
// never an empty one.
func DecodeFilteredContinuationToken(scopeID uuid.UUID, filters FilterSet, raw string) (Cursor, error) {
	data, err := base64.RawURLEncoding.DecodeString(raw)
	if err != nil {
		return Cursor{}, fmt.Errorf("%w: %v", ErrInvalidContinuationToken, err)
	}
	var tok continuationToken
	if err := json.Unmarshal(data, &tok); err != nil {
		return Cursor{}, fmt.Errorf("%w: %v", ErrInvalidContinuationToken, err)
	}
	if tok.ScopeID != scopeID {
		return Cursor{}, ErrTokenScopeMismatch
	}
	if tok.Filters != CanonicalFilterSet(filters) {
		return Cursor{}, ErrTokenFilterMismatch
	}
	return Cursor{SortKey: tok.SortKey, ID: tok.ID}, nil
}
