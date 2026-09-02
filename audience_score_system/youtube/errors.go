package youtube

import "errors"

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

// classify maps err (as returned by the vendored youtube/v3 or
// youtubeanalytics/v2 service clients) to one of the sentinel errors above,
// wrapped so errors.Is(result, ErrX) succeeds while %w still preserves the
// original error for logging (no token/credential material is ever part
// of that message -- see this task's "Validation" section). Every Client
// method funnels its API-call error through classify before returning it.
//
// Scaffold only (issue #1573): returns errNotImplemented for every
// non-nil input. This task's Testing section requires the classification
// table (invalid_grant -> ErrRevoked, 403 quotaExceeded/rateLimitExceeded
// -> ErrQuotaExceeded, 5xx/timeout -> ErrTransient, other 4xx ->
// ErrPermanent) to be written before the mapping code that satisfies it
// (red/green). Lands in the Implementation phase.
func classify(err error) error {
	if err == nil {
		return nil
	}
	return errNotImplemented
}
