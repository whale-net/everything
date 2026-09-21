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
package store

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"

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
// JSON), never hand-constructed elsewhere.
type continuationToken struct {
	ScopeID uuid.UUID `json:"scope_id"`
	SortKey string    `json:"sort_key"`
	ID      uuid.UUID `json:"id"`
}

// EncodeContinuationToken builds the opaque token a Page.NextToken
// carries: scopeID (the scope boundary DecodeContinuationToken enforces
// on resume) plus cursor, this page's last row's keyset position.
func EncodeContinuationToken(scopeID uuid.UUID, cursor Cursor) string {
	raw, err := json.Marshal(continuationToken{ScopeID: scopeID, SortKey: cursor.SortKey, ID: cursor.ID})
	if err != nil {
		// continuationToken's fields are a UUID and two strings -- all
		// directly JSON-marshalable, so Marshal cannot actually fail
		// here. Panic rather than silently hand back a token that would
		// fail to decode.
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
// can never use a cross-scope token's cursor value even by accident.
func DecodeContinuationToken(scopeID uuid.UUID, raw string) (Cursor, error) {
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
	return Cursor{SortKey: tok.SortKey, ID: tok.ID}, nil
}
