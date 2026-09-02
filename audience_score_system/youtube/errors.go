package youtube

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"strings"

	"golang.org/x/oauth2"
	"google.golang.org/api/googleapi"
)

// Sentinel errors every Client method call classifies its failure into --
// callers branch on these via errors.Is, never on the underlying
// googleapi.Error status/reason directly, so a Google API detail change
// never leaks past this package. This is the enum FR4 correctness depends
// on: a misclassified quota error would wrongly disconnect a healthy
// Channel, so ErrRevoked must never fire for anything but a genuine
// revocation/invalid-grant.
var (
	// ErrRevoked means the Channel's credential is revoked or invalid
	// (invalid_grant, or a 401 reporting revoked/invalid credentials) --
	// the caller marks the Channel needs-reauth (FR4). Must be
	// distinguishable from every other case below; never returned for a
	// quota or transient failure.
	ErrRevoked = errors.New("youtube: credential revoked or invalid")

	// ErrQuotaExceeded means a 403 quotaExceeded/rateLimitExceeded --
	// retryable with backoff, NOT a reauth condition.
	ErrQuotaExceeded = errors.New("youtube: quota exceeded")

	// ErrTransient means a 5xx, timeout, or connection reset -- retryable.
	ErrTransient = errors.New("youtube: transient error")

	// ErrPermanent means a 4xx that will not fix itself on retry (e.g. a
	// malformed request or a video id that does not exist).
	ErrPermanent = errors.New("youtube: permanent error")
)

// quotaReasons is the set of googleapi.ErrorItem.Reason values a 403
// response uses to report quota/rate-limit exhaustion, per Google's YouTube
// Data/Analytics API error reference. Any other 403 reason (e.g.
// "forbidden", "accessNotConfigured") is a genuine permission problem, not
// a retryable quota condition, and classifies as ErrPermanent instead.
var quotaReasons = map[string]bool{
	"quotaExceeded":         true,
	"dailyLimitExceeded":    true,
	"rateLimitExceeded":     true,
	"userRateLimitExceeded": true,
}

// classify maps err (as returned by the vendored youtube/v3 or
// youtubeanalytics/v2 service clients, or by the oauth2.TokenSource ts
// wraps when a request's underlying token refresh fails) to one of the
// sentinel errors above, wrapped so errors.Is(result, ErrX) succeeds while
// the original error's message is preserved for logging (no token/
// credential material is ever part of that message -- googleapi.Error and
// oauth2.RetrieveError messages never include the token itself, only
// Google's server-side error text). Every Client method funnels its
// API-call error through classify before returning it.
//
// Classification order matters: an OAuth token-refresh failure is checked
// first (it can occur before any HTTP request to Google's Data/Analytics
// endpoints even forms), then a googleapi.Error's HTTP status/reason, then
// generic network/context failures. Anything unrecognized falls through to
// ErrPermanent rather than being silently treated as retryable.
func classify(err error) error {
	if err == nil {
		return nil
	}

	sentinel, result := classifySentinel(err)

	// Log the classification decision itself (never the request/response
	// body, which callers never hand classify -- only the already-surfaced
	// Go error, whose Message text from googleapi/oauth2 never contains a
	// token or credential value). This is this package's one
	// instrumentation point for every API failure, per every Client
	// method funneling through classify.
	level := slog.LevelWarn
	if sentinel == ErrPermanent {
		level = slog.LevelError
	}
	logger.Log(context.Background(), level, "youtube api call failed",
		"classification", sentinelName(sentinel),
		"error", err.Error(),
	)

	return result
}

// classifySentinel is classify's pure decision logic, split out so
// classify's single logging call always fires exactly once regardless of
// which branch below matched.
//
// Classification order matters: an OAuth token-refresh failure is checked
// first (it can occur before any HTTP request to Google's Data/Analytics
// endpoints even forms), then a googleapi.Error's HTTP status/reason, then
// generic network/context failures. Anything unrecognized falls through to
// ErrPermanent rather than being silently treated as retryable.
func classifySentinel(err error) (sentinel, wrapped error) {
	// A token refresh failure surfaces via golang.org/x/oauth2's Transport,
	// which returns ts.Token()'s error verbatim (see client.go's "HTTP
	// transport" doc comment) -- tokens.Store further wraps it with %w, so
	// errors.As still finds it through that chain. Only invalid_grant is a
	// genuine revocation signal (mirrors tokens.Store's own isInvalidGrant);
	// any other oauth2 failure (e.g. a network hiccup reaching Google's
	// token endpoint) is transient.
	var retrieveErr *oauth2.RetrieveError
	if errors.As(err, &retrieveErr) {
		if retrieveErr.ErrorCode == "invalid_grant" {
			return ErrRevoked, fmt.Errorf("%w: oauth invalid_grant: %v", ErrRevoked, err)
		}
		return ErrTransient, fmt.Errorf("%w: oauth token refresh failed: %v", ErrTransient, err)
	}

	var apiErr *googleapi.Error
	if errors.As(err, &apiErr) {
		switch {
		case apiErr.Code == 401:
			// Google reports a revoked or otherwise invalid credential on
			// an authenticated Data/Analytics call as 401, distinct from
			// the invalid_grant case above (which happens at the token
			// endpoint, before any Data/Analytics request forms).
			return ErrRevoked, fmt.Errorf("%w: %v", ErrRevoked, err)
		case apiErr.Code == 403 && hasQuotaReason(apiErr):
			return ErrQuotaExceeded, fmt.Errorf("%w: %v", ErrQuotaExceeded, err)
		case apiErr.Code >= 500:
			return ErrTransient, fmt.Errorf("%w: %v", ErrTransient, err)
		case apiErr.Code >= 400:
			return ErrPermanent, fmt.Errorf("%w: %v", ErrPermanent, err)
		}
	}

	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
		return ErrTransient, fmt.Errorf("%w: %v", ErrTransient, err)
	}

	var netErr net.Error
	if errors.As(err, &netErr) {
		return ErrTransient, fmt.Errorf("%w: %v", ErrTransient, err)
	}

	return ErrPermanent, fmt.Errorf("%w: %v", ErrPermanent, err)
}

// sentinelName returns the short, log-friendly name for one of the
// sentinel errors above.
func sentinelName(sentinel error) string {
	switch sentinel {
	case ErrRevoked:
		return "revoked"
	case ErrQuotaExceeded:
		return "quota_exceeded"
	case ErrTransient:
		return "transient"
	default:
		return "permanent"
	}
}

// hasQuotaReason reports whether apiErr's structured error reason(s) (or,
// failing that, its message text) indicate a quota/rate-limit condition
// rather than a genuine 403 permission failure.
func hasQuotaReason(apiErr *googleapi.Error) bool {
	for _, item := range apiErr.Errors {
		if quotaReasons[item.Reason] {
			return true
		}
	}
	msg := strings.ToLower(apiErr.Message)
	return strings.Contains(msg, "quota") || strings.Contains(msg, "rate limit")
}
