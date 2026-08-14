package fake

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/whale-net/everything/tools/app_registry/server/repository"
)

// encodeFakeCursor/decodeFakeCursor are this package's own opaque page_token
// codec for ListReconcileRuns -- a plain-text analog of
// postgres/keyset_cursor.go's encodeKeysetCursor/decodeKeysetCursor. The
// fake does not import postgres (no such dependency exists between these
// sibling packages, and adding one just for this would be backwards -- fake
// exists precisely so handler-level tests don't need postgres at all), so
// this is a same-shape but independent implementation. Only this fake's own
// round-trip needs to hold; a token produced by one is never fed to the
// other.
func encodeFakeCursor(ts time.Time, id string) string {
	return fmt.Sprintf("%d|%s", ts.UnixNano(), id)
}

func decodeFakeCursor(token string) (ts time.Time, id string, err error) {
	parts := strings.SplitN(token, "|", 2)
	if len(parts) != 2 {
		return time.Time{}, "", fmt.Errorf("invalid page_token: malformed cursor: %w", repository.ErrInvalidArgument)
	}
	nanos, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil {
		return time.Time{}, "", fmt.Errorf("invalid page_token: %w", repository.ErrInvalidArgument)
	}
	return time.Unix(0, nanos), parts[1], nil
}
