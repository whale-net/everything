package workshop

import (
	"fmt"
	"strconv"
	"strings"
)

// numericVersionWidth is wide enough for any int64 Steam time_updated or
// SteamCMD numeric manifest id (max 19 digits) plus headroom, so two
// differently zero-padded representations of the same numeric version
// always collide on the same fixed-width rendering.
const numericVersionWidth = 20

// CacheKey derives the content-addressed cache identity for a workshop item.
// It intentionally takes no host, SGC, deployment, or library parameter
// (NFR1): the same (workshopID, contentVersion) pair always derives the
// identical key no matter which host, SGC, deployment, or library is
// asking -- that identity is what lets two racing hosts converge on one
// cache object instead of fighting (NFR4).
//
// contentVersion is normalised before being embedded in the key: a purely
// numeric version (Steam's time_updated, or a numeric SteamCMD manifest id)
// is rendered as a fixed-width decimal so two differently-padded or
// differently-typed representations of the same version still derive
// byte-identical keys; a non-numeric SteamCMD manifest/version string is
// used as-is, trimmed of surrounding whitespace.
func CacheKey(workshopID, contentVersion string) string {
	return fmt.Sprintf("ws/%s/%s", strings.TrimSpace(workshopID), normalizeContentVersion(contentVersion))
}

// S3Key derives the object key for a cache entry from its cache key.
func S3Key(cacheKey string) string {
	return fmt.Sprintf("workshop-cache/%s.tar", strings.TrimPrefix(cacheKey, "ws/"))
}

// normalizeContentVersion canonicalises a content-version string so the
// same underlying version always renders identically regardless of the
// caller's numeric formatting.
func normalizeContentVersion(contentVersion string) string {
	trimmed := strings.TrimSpace(contentVersion)
	if v, err := strconv.ParseInt(trimmed, 10, 64); err == nil {
		return fmt.Sprintf("%0*d", numericVersionWidth, v)
	}
	return trimmed
}
