package postgres

import (
	"encoding/base64"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/whale-net/everything/tools/app_registry/server/repository"
)

// encodeKeysetCursor produces an opaque page_token string encoding a
// timestamp + tie-break id, for a keyset-paginated ORDER BY <ts> DESC, <id>
// [DESC] query. Callers pass the (ts, id) of the LAST row on the page just
// returned; the next call's WHERE clause resumes strictly after that pair.
func encodeKeysetCursor(ts time.Time, id string) string {
	raw := fmt.Sprintf("%d|%s", ts.UnixNano(), id)
	return base64.RawURLEncoding.EncodeToString([]byte(raw))
}

// decodeKeysetCursor is encodeKeysetCursor's inverse. Returns a descriptive
// error wrapping repository.ErrInvalidArgument on a malformed or tampered
// token, never a panic; callers map that to codes.InvalidArgument the same
// way other bad-input errors are mapped (see handlers/errors.go's
// mapRepoErr).
func decodeKeysetCursor(token string) (ts time.Time, id string, err error) {
	raw, err := base64.RawURLEncoding.DecodeString(token)
	if err != nil {
		return time.Time{}, "", fmt.Errorf("invalid page_token: %w", repository.ErrInvalidArgument)
	}
	parts := strings.SplitN(string(raw), "|", 2)
	if len(parts) != 2 {
		return time.Time{}, "", fmt.Errorf("invalid page_token: malformed cursor: %w", repository.ErrInvalidArgument)
	}
	nanos, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil {
		return time.Time{}, "", fmt.Errorf("invalid page_token: %w", repository.ErrInvalidArgument)
	}
	return time.Unix(0, nanos), parts[1], nil
}
