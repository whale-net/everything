package contract

import (
	"encoding/base64"
	"errors"
	"testing"
	"time"
)

// TestDecodeCursor_EmptyToken_MeansFirstPage covers the documented "no
// token" contract: an empty page_token is not malformed, it means "start
// from the beginning".
func TestDecodeCursor_EmptyToken_MeansFirstPage(t *testing.T) {
	values, err := DecodeCursor("")
	if err != nil {
		t.Fatalf("DecodeCursor(\"\") error = %v, want nil", err)
	}
	if values != nil {
		t.Errorf("DecodeCursor(\"\") = %v, want nil", values)
	}
}

// TestDecodeCursor_RejectsHandwrittenOffsetLookingToken proves the cursor is
// sealed, not just base64: a plausible hand-written offset-style token
// ("offset=20", base64'd, the shape a caller might guess/forge if the
// encoding were transparent) is rejected rather than silently reinterpreted
// as a keyset position (FR61 -- opaque token, never an offset encoding).
func TestDecodeCursor_RejectsHandwrittenOffsetLookingToken(t *testing.T) {
	handwritten := base64.RawURLEncoding.EncodeToString([]byte("offset=20"))

	_, err := DecodeCursor(handwritten)
	if err == nil {
		t.Fatal("DecodeCursor accepted a hand-written offset-looking token, want ErrInvalidPageToken")
	}
	if !errors.Is(err, ErrInvalidPageToken) {
		t.Errorf("error = %v, want wrapping ErrInvalidPageToken", err)
	}
}

// TestDecodeCursor_RejectsNonBase64Token covers a token that isn't even
// valid base64 -- e.g. a caller-typed offset like "20" directly.
func TestDecodeCursor_RejectsNonBase64Token(t *testing.T) {
	_, err := DecodeCursor("20")
	if !errors.Is(err, ErrInvalidPageToken) {
		t.Errorf("DecodeCursor(\"20\") error = %v, want ErrInvalidPageToken", err)
	}
}

// TestDecodeCursor_RejectsTamperedChecksum proves a caller cannot forge a
// valid-looking token by editing the sealed payload (e.g. changing which
// row to resume from) without also recomputing the checksum -- and without
// the checksum, DecodeCursor rejects it rather than trusting the edited
// value.
func TestDecodeCursor_RejectsTamperedChecksum(t *testing.T) {
	token := EncodeCursor("42")

	raw, err := base64.RawURLEncoding.DecodeString(token)
	if err != nil {
		t.Fatalf("test setup: decode base64: %v", err)
	}
	tampered := []byte(string(raw))
	tampered[len(tampered)-1] ^= 0xFF // flip a bit in the checksum tail
	forged := base64.RawURLEncoding.EncodeToString(tampered)

	if _, err := DecodeCursor(forged); !errors.Is(err, ErrInvalidPageToken) {
		t.Errorf("DecodeCursor(tampered) error = %v, want ErrInvalidPageToken", err)
	}
}

// TestEncodeDecodeCursor_RoundTrip is the baseline sanity check that a
// well-formed cursor from this package decodes back to the same values.
func TestEncodeDecodeCursor_RoundTrip(t *testing.T) {
	token := EncodeCursor("7", "abc")
	values, err := DecodeCursor(token)
	if err != nil {
		t.Fatalf("DecodeCursor(EncodeCursor(...)) error = %v", err)
	}
	if len(values) != 2 || values[0] != "7" || values[1] != "abc" {
		t.Errorf("values = %v, want [7 abc]", values)
	}
}

// TestEncodeDecodeBoardCursor_RoundTrip covers the ListBoards-specific
// (board_id) keyset helper.
func TestEncodeDecodeBoardCursor_RoundTrip(t *testing.T) {
	token := EncodeBoardCursor(99)
	id, ok, err := DecodeBoardCursor(token)
	if err != nil {
		t.Fatalf("DecodeBoardCursor error = %v", err)
	}
	if !ok {
		t.Fatal("DecodeBoardCursor ok = false, want true")
	}
	if id != 99 {
		t.Errorf("id = %d, want 99", id)
	}

	id, ok, err = DecodeBoardCursor("")
	if err != nil || ok || id != 0 {
		t.Errorf("DecodeBoardCursor(\"\") = (%d, %v, %v), want (0, false, nil)", id, ok, err)
	}
}

// TestEncodeDecodeReadingCursor_RoundTrip covers the (recorded_at DESC,
// reading_id) keyset helper defined in this task for a later phase's
// readings-listing RPC.
func TestEncodeDecodeReadingCursor_RoundTrip(t *testing.T) {
	want := time.Unix(1700000000, 123000000).UTC()
	token := EncodeReadingCursor(want, 55)

	got, id, ok, err := DecodeReadingCursor(token)
	if err != nil {
		t.Fatalf("DecodeReadingCursor error = %v", err)
	}
	if !ok {
		t.Fatal("DecodeReadingCursor ok = false, want true")
	}
	if !got.Equal(want) {
		t.Errorf("recordedAt = %v, want %v", got, want)
	}
	if id != 55 {
		t.Errorf("readingID = %d, want 55", id)
	}
}

// TestEncodeDecodeConfigHistoryCursor_RoundTrip covers ListConfigHistory's
// (version) keyset helper (FR35.1/FR61) -- a well-formed token round-trips
// to the same version, and an empty token decodes to "first (newest) page"
// rather than an error.
func TestEncodeDecodeConfigHistoryCursor_RoundTrip(t *testing.T) {
	token := EncodeConfigHistoryCursor(42)
	version, ok, err := DecodeConfigHistoryCursor(token)
	if err != nil {
		t.Fatalf("DecodeConfigHistoryCursor error = %v", err)
	}
	if !ok {
		t.Fatal("DecodeConfigHistoryCursor ok = false, want true")
	}
	if version != 42 {
		t.Errorf("version = %d, want 42", version)
	}

	version, ok, err = DecodeConfigHistoryCursor("")
	if err != nil || ok || version != 0 {
		t.Errorf("DecodeConfigHistoryCursor(\"\") = (%d, %v, %v), want (0, false, nil)", version, ok, err)
	}
}

// TestClampPageSize_AboveCap_ClampsRatherThanErrors covers FR61: a
// page_size above PageCap is silently clamped to the cap. ClampPageSize
// returns a plain int32 (no error return at all), so "not rejected" holds
// by construction -- this test pins the clamped value itself.
func TestClampPageSize_AboveCap_ClampsRatherThanErrors(t *testing.T) {
	got := ClampPageSize(PageCap * 1000)
	if got != PageCap {
		t.Errorf("ClampPageSize(way above cap) = %d, want %d", got, PageCap)
	}
}

func TestClampPageSize_NonPositive_UsesDefault(t *testing.T) {
	for _, requested := range []int32{0, -1, -100} {
		if got := ClampPageSize(requested); got != DefaultPageSize {
			t.Errorf("ClampPageSize(%d) = %d, want DefaultPageSize %d", requested, got, DefaultPageSize)
		}
	}
}

func TestClampPageSize_WithinRange_Unchanged(t *testing.T) {
	if got := ClampPageSize(10); got != 10 {
		t.Errorf("ClampPageSize(10) = %d, want 10", got)
	}
	if got := ClampPageSize(PageCap); got != PageCap {
		t.Errorf("ClampPageSize(PageCap) = %d, want PageCap", got)
	}
}
