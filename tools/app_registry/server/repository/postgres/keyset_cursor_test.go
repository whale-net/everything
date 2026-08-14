package postgres

import (
	"errors"
	"testing"
	"time"

	"github.com/whale-net/everything/tools/app_registry/server/repository"
)

// TestKeysetCursor_RoundTrip proves decodeKeysetCursor(encodeKeysetCursor(ts,
// id)) recovers the same (ts, id). ts is truncated to microsecond precision
// first -- Postgres TIMESTAMPTZ is microsecond-precision, so the value that
// actually round-trips through Postgres (read back out of a row, then
// re-encoded into the next page's cursor) will already have lost any
// sub-microsecond component before it ever reaches encodeKeysetCursor.
func TestKeysetCursor_RoundTrip(t *testing.T) {
	ts := time.Date(2026, 8, 13, 12, 34, 56, 123456000, time.UTC).Truncate(time.Microsecond)
	id := "reconcile-run-abc123"

	token := encodeKeysetCursor(ts, id)
	gotTs, gotID, err := decodeKeysetCursor(token)
	if err != nil {
		t.Fatalf("decodeKeysetCursor: unexpected error: %v", err)
	}
	if !gotTs.Equal(ts) {
		t.Fatalf("timestamp mismatch: want %v, got %v", ts, gotTs)
	}
	if gotID != id {
		t.Fatalf("id mismatch: want %q, got %q", id, gotID)
	}
}

// TestKeysetCursor_MalformedToken proves a garbage token -- not valid
// base64, or valid base64 with no "|" separator -- returns an error wrapping
// repository.ErrInvalidArgument rather than panicking, so handlers'
// mapRepoErr maps it to codes.InvalidArgument without any new mapping code.
func TestKeysetCursor_MalformedToken(t *testing.T) {
	cases := []struct {
		name  string
		token string
	}{
		{"not_base64", "not-valid-base64!!!"},
		{"base64_no_separator", "bm8tc2VwYXJhdG9yaGVyZQ"}, // base64("no-separatorhere")
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, _, err := decodeKeysetCursor(tc.token)
			if err == nil {
				t.Fatal("expected an error, got nil")
			}
			if !errors.Is(err, repository.ErrInvalidArgument) {
				t.Fatalf("expected error to wrap repository.ErrInvalidArgument, got %v", err)
			}
		})
	}
}

// TestKeysetCursor_EmptyToken proves decodeKeysetCursor does not panic on an
// empty string, even though callers should special-case "" ("no cursor,
// first page") before ever calling this function.
func TestKeysetCursor_EmptyToken(t *testing.T) {
	_, _, err := decodeKeysetCursor("")
	if err == nil {
		t.Fatal("expected an error, got nil")
	}
	if !errors.Is(err, repository.ErrInvalidArgument) {
		t.Fatalf("expected error to wrap repository.ErrInvalidArgument, got %v", err)
	}
}
