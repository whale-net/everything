package contract

import (
	"encoding/base64"
	"errors"
	"fmt"
	"hash/crc32"
	"strconv"
	"strings"
	"time"
)

// PageCap is the server-enforced maximum page_size for any listing RPC
// (FR61). A request above this is clamped, not rejected -- see
// ClampPageSize.
const PageCap int32 = 100

// DefaultPageSize is used when a caller requests a non-positive page_size.
const DefaultPageSize int32 = 25

// ClampPageSize enforces PageCap server-side: a requested size above the
// cap is silently clamped rather than rejected (FR61). A non-positive
// requested size falls back to DefaultPageSize.
func ClampPageSize(requested int32) int32 {
	if requested <= 0 {
		return DefaultPageSize
	}
	if requested > PageCap {
		return PageCap
	}
	return requested
}

// ErrInvalidPageToken classifies a page_token that failed to decode --
// malformed, hand-written, or tampered with. Callers map this to
// contract.InvalidArgument (or contract.New(FailureInvalidArgument, ...))
// the same way other bad-input errors are mapped.
var ErrInvalidPageToken = errors.New("invalid page_token")

// cursorMagic tags every token this package produces. Its presence (and
// the trailing checksum below) is what lets DecodeCursor reject a
// hand-written, offset-looking token instead of silently reinterpreting it
// as a keyset position.
const cursorMagic = "llk1"

// EncodeCursor seals an ordered sequence of keyset column values -- the
// values of the last row on the page just returned -- into an opaque
// page_token. This is not an offset encoding: the token carries the actual
// column values needed to resume a keyset scan (e.g. `WHERE (col1, col2) >
// (v1, v2)`), so pagination stays correct while rows are appended mid-scan
// (FR61). The token is sealed (magic prefix + checksum), not just
// base64'd, so a caller-forged or hand-written token is rejected by
// DecodeCursor rather than silently scanned as if it were valid.
func EncodeCursor(values ...string) string {
	payload := cursorMagic + "|" + strings.Join(values, "|")
	sum := crc32.ChecksumIEEE([]byte(payload))
	raw := fmt.Sprintf("%s|%08x", payload, sum)
	return base64.RawURLEncoding.EncodeToString([]byte(raw))
}

// DecodeCursor is EncodeCursor's inverse. An empty token decodes to a nil
// slice with no error, meaning "first page". Any other malformed,
// hand-written, or tampered token returns ErrInvalidPageToken -- never a
// panic, and never a value silently reinterpreted as an offset.
func DecodeCursor(token string) ([]string, error) {
	if token == "" {
		return nil, nil
	}
	raw, err := base64.RawURLEncoding.DecodeString(token)
	if err != nil {
		return nil, fmt.Errorf("%w: not valid base64", ErrInvalidPageToken)
	}
	parts := strings.Split(string(raw), "|")
	if len(parts) < 3 || parts[0] != cursorMagic {
		return nil, fmt.Errorf("%w: unrecognized cursor shape", ErrInvalidPageToken)
	}
	values, sumHex := parts[1:len(parts)-1], parts[len(parts)-1]
	payload := cursorMagic + "|" + strings.Join(values, "|")
	wantSum := fmt.Sprintf("%08x", crc32.ChecksumIEEE([]byte(payload)))
	if sumHex != wantSum {
		return nil, fmt.Errorf("%w: checksum mismatch", ErrInvalidPageToken)
	}
	return values, nil
}

// EncodeBoardCursor seals the (board_id) keyset used by ListBoards's
// page_token, ordered to match ORDER BY board_id.
func EncodeBoardCursor(boardID int64) string {
	return EncodeCursor(strconv.FormatInt(boardID, 10))
}

// DecodeBoardCursor is EncodeBoardCursor's inverse. An empty token decodes
// to (0, false, nil), meaning "first page".
func DecodeBoardCursor(token string) (boardID int64, ok bool, err error) {
	values, err := DecodeCursor(token)
	if err != nil {
		return 0, false, err
	}
	if values == nil {
		return 0, false, nil
	}
	if len(values) != 1 {
		return 0, false, fmt.Errorf("%w: board cursor expects 1 field, got %d", ErrInvalidPageToken, len(values))
	}
	id, err := strconv.ParseInt(values[0], 10, 64)
	if err != nil {
		return 0, false, fmt.Errorf("%w: board cursor id is not an integer", ErrInvalidPageToken)
	}
	return id, true, nil
}

// EncodeGrantCursor seals the (grant_id) keyset used by
// ListHouseholdGrants's page_token (FR61, FR7), ordered to match ORDER BY
// grant_id -- same shape as EncodeBoardCursor, over household_grant's own
// BIGSERIAL id instead of board_id.
func EncodeGrantCursor(grantID int64) string {
	return EncodeCursor(strconv.FormatInt(grantID, 10))
}

// DecodeGrantCursor is EncodeGrantCursor's inverse. An empty token decodes
// to (0, false, nil), meaning "first page".
func DecodeGrantCursor(token string) (grantID int64, ok bool, err error) {
	values, err := DecodeCursor(token)
	if err != nil {
		return 0, false, err
	}
	if values == nil {
		return 0, false, nil
	}
	if len(values) != 1 {
		return 0, false, fmt.Errorf("%w: grant cursor expects 1 field, got %d", ErrInvalidPageToken, len(values))
	}
	id, err := strconv.ParseInt(values[0], 10, 64)
	if err != nil {
		return 0, false, fmt.Errorf("%w: grant cursor id is not an integer", ErrInvalidPageToken)
	}
	return id, true, nil
}

// EncodeReadingCursor seals the (recorded_at DESC, reading_id) keyset that
// matches idx_sensor_reading_sensor_id, for the sensor-reading listing RPC
// added in a later phase. Defined here, alongside the boards keyset, so
// every listing RPC in this plan reuses one cursor shape instead of
// inventing its own -- even though no readings endpoint exists yet.
func EncodeReadingCursor(recordedAt time.Time, readingID int64) string {
	return EncodeCursor(strconv.FormatInt(recordedAt.UnixNano(), 10), strconv.FormatInt(readingID, 10))
}

// DecodeReadingCursor is EncodeReadingCursor's inverse. An empty token
// decodes to (zero time, 0, false, nil), meaning "first page".
func DecodeReadingCursor(token string) (recordedAt time.Time, readingID int64, ok bool, err error) {
	values, err := DecodeCursor(token)
	if err != nil {
		return time.Time{}, 0, false, err
	}
	if values == nil {
		return time.Time{}, 0, false, nil
	}
	if len(values) != 2 {
		return time.Time{}, 0, false, fmt.Errorf("%w: reading cursor expects 2 fields, got %d", ErrInvalidPageToken, len(values))
	}
	nanos, err := strconv.ParseInt(values[0], 10, 64)
	if err != nil {
		return time.Time{}, 0, false, fmt.Errorf("%w: reading cursor timestamp is not an integer", ErrInvalidPageToken)
	}
	id, err := strconv.ParseInt(values[1], 10, 64)
	if err != nil {
		return time.Time{}, 0, false, fmt.Errorf("%w: reading cursor id is not an integer", ErrInvalidPageToken)
	}
	return time.Unix(0, nanos), id, true, nil
}
