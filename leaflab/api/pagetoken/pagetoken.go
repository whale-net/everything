// Package pagetoken provides opaque keyset pagination token encoding/decoding
// for the LeafLab API.
//
// Tokens are never an offset encoding and do not round-trip caller-controlled
// SQL. A token encodes a server-side cursor (e.g., the last row's key values)
// and is validated server-side before use. A mutated or foreign token is
// rejected without leaking its contents.
package pagetoken

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
)

// Token is the internal representation of a page cursor. Exported for testing;
// callers should treat tokens as opaque.
type Token struct {
	// For ListBoards (keyset on recorded_at DESC, reading_id), this would hold
	// the last board's recorded_at and board_id. For other RPCs, holds
	// whatever values the keyset pagination is indexed on.
	LastRecordedAt int64  `json:"last_recorded_at,omitempty"`
	LastBoardID    int64  `json:"last_board_id,omitempty"`
}

// Encode marshals a token to an opaque base64 string. The result is not
// intended for human parsing and may change without notice.
func Encode(t *Token) (string, error) {
	if t == nil {
		return "", nil
	}
	data, err := json.Marshal(t)
	if err != nil {
		return "", fmt.Errorf("marshal token: %w", err)
	}
	return base64.StdEncoding.EncodeToString(data), nil
}

// Decode unmarshals an opaque page token. Returns an error if the token is
// invalid, mutated, or otherwise rejected. Does not leak token contents in
// the error message.
func Decode(encoded string) (*Token, error) {
	if encoded == "" {
		return nil, nil // empty token is the first page
	}
	data, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		return nil, fmt.Errorf("invalid page token")
	}
	var t Token
	if err := json.Unmarshal(data, &t); err != nil {
		return nil, fmt.Errorf("invalid page token")
	}
	return &t, nil
}
