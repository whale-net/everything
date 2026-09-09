package session

import (
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
)

// TestSessionCursor_RoundTrip proves encodeSessionCursor/decodeSessionCursor
// round-trip both directions with created_at truncated to nanosecond
// precision (the wire precision the cursor itself carries, via UnixNano).
func TestSessionCursor_RoundTrip(t *testing.T) {
	ts := time.Unix(0, 1700000000123456789)
	id := uuid.New()

	for _, dir := range []sessionCursorDirection{sessionCursorNext, sessionCursorPrev} {
		token := encodeSessionCursor(dir, ts, id)
		gotDir, gotTS, gotID, err := decodeSessionCursor(token)
		if err != nil {
			t.Fatalf("decodeSessionCursor(%q) error: %v", token, err)
		}
		if gotDir != dir {
			t.Errorf("direction = %q, want %q", gotDir, dir)
		}
		if !gotTS.Equal(ts) {
			t.Errorf("createdAt = %v, want %v", gotTS, ts)
		}
		if gotID != id {
			t.Errorf("id = %v, want %v", gotID, id)
		}
	}
}

// TestSessionCursor_DecodeRejectsMalformedOrTamperedTokens proves a bad
// page_token is reported as ErrInvalidPageToken, never a panic -- the
// Testing-section requirement that a tampered token is InvalidArgument, not
// a crash or a silent full-list fallback (the handler maps this sentinel to
// codes.InvalidArgument).
func TestSessionCursor_DecodeRejectsMalformedOrTamperedTokens(t *testing.T) {
	valid := encodeSessionCursor(sessionCursorNext, time.Now(), uuid.New())

	cases := map[string]string{
		"not base64 at all":                     "!!!not-base64!!!",
		"empty string":                          "",
		"truncated valid token":                 valid[:len(valid)-4],
		"garbage base64 with wrong field count": "aGVsbG8", // base64("hello"), one field
	}

	for name, token := range cases {
		t.Run(name, func(t *testing.T) {
			_, _, _, err := decodeSessionCursor(token)
			if !errors.Is(err, ErrInvalidPageToken) {
				t.Errorf("decodeSessionCursor(%q) error = %v, want ErrInvalidPageToken", token, err)
			}
		})
	}
}

// TestSessionCursor_DecodeRejectsUnrecognizedDirection proves a direction
// byte outside {n, p} is also ErrInvalidPageToken, not silently treated as
// one direction or the other.
func TestSessionCursor_DecodeRejectsUnrecognizedDirection(t *testing.T) {
	token := encodeSessionCursor(sessionCursorDirection('x'), time.Now(), uuid.New())
	_, _, _, err := decodeSessionCursor(token)
	if !errors.Is(err, ErrInvalidPageToken) {
		t.Errorf("decodeSessionCursor with direction 'x' error = %v, want ErrInvalidPageToken", err)
	}
}
