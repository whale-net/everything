package main

import (
	"errors"
	"fmt"
	"net"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/whale-net/everything/whagent_net/session"
)

// fakeNetTimeoutError is a minimal net.Error whose Timeout() is true --
// used to exercise classify.go's isNetTimeout branch (a lower-level
// transport timeout distinct from a message-text "timeout" match).
type fakeNetTimeoutError struct{}

func (fakeNetTimeoutError) Error() string   { return "dial tcp: i/o timeout" }
func (fakeNetTimeoutError) Timeout() bool   { return true }
func (fakeNetTimeoutError) Temporary() bool { return true }

var _ net.Error = fakeNetTimeoutError{}

// TestClassifyError_RateLimit_Retryable proves a simulated provider rate
// limit (HTTP 429) classifies retryable (issue body's Testing phase:
// "a simulated provider rate limit -> retryable").
func TestClassifyError_RateLimit_Retryable(t *testing.T) {
	err := fmt.Errorf("call model: POST https://openrouter.ai/api/v1/chat/completions: 429 Too Many Requests rate limit exceeded")

	category, detail := classifyError(err)
	assert.Equal(t, session.ErrorCategoryRetryable, category)
	assert.Contains(t, strings.ToLower(detail), "rate limit")
}

// TestClassifyError_RateLimitByStatusCodeOnly_Retryable proves the 429
// status code alone (no "rate limit" text) is still classified retryable.
func TestClassifyError_RateLimitByStatusCodeOnly_Retryable(t *testing.T) {
	err := fmt.Errorf("call model: POST https://openrouter.ai/api/v1/chat/completions: 429 Too Many Requests")

	category, _ := classifyError(err)
	assert.Equal(t, session.ErrorCategoryRetryable, category)
}

// TestClassifyError_ServerError_Retryable proves a provider 5xx is a
// transient transport failure (retryable).
func TestClassifyError_ServerError_Retryable(t *testing.T) {
	err := fmt.Errorf("call model: POST https://openrouter.ai/api/v1/chat/completions: 503 Service Unavailable")

	category, detail := classifyError(err)
	assert.Equal(t, session.ErrorCategoryRetryable, category)
	assert.Contains(t, strings.ToLower(detail), "transient transport failure")
}

// TestClassifyError_ConnectionReset_Retryable proves a connection-level
// failure is retryable even with no HTTP status in the message.
func TestClassifyError_ConnectionReset_Retryable(t *testing.T) {
	err := fmt.Errorf("call model: dial tcp 1.2.3.4:443: connection reset by peer")

	category, _ := classifyError(err)
	assert.Equal(t, session.ErrorCategoryRetryable, category)
}

// TestClassifyError_MessageTimeout_Retryable proves a message-text timeout
// (context deadline exceeded / timed out) is retryable.
func TestClassifyError_MessageTimeout_Retryable(t *testing.T) {
	for _, msg := range []string{
		"call model: context deadline exceeded",
		"call model: request timed out",
		"call model: read tcp: i/o timeout",
	} {
		t.Run(msg, func(t *testing.T) {
			category, _ := classifyError(fmt.Errorf(msg))
			assert.Equal(t, session.ErrorCategoryRetryable, category)
		})
	}
}

// TestClassifyError_NetTimeout_Retryable proves a wrapped net.Error whose
// Timeout() is true classifies retryable via isNetTimeout, independent of
// its message text matching any of the string patterns above.
func TestClassifyError_NetTimeout_Retryable(t *testing.T) {
	err := fmt.Errorf("call model: %w", fakeNetTimeoutError{})

	category, detail := classifyError(err)
	assert.Equal(t, session.ErrorCategoryRetryable, category)
	assert.Contains(t, strings.ToLower(detail), "timeout")
}

// TestClassifyError_AuthFailure_NonRetryable proves an auth/permission
// failure (issue body: "an unserved-model or auth failure -> non_retryable").
func TestClassifyError_AuthFailure_NonRetryable(t *testing.T) {
	for _, msg := range []string{
		"call model: POST https://openrouter.ai/api/v1/chat/completions: 401 Unauthorized invalid api key",
		"call model: POST https://openrouter.ai/api/v1/chat/completions: 403 Forbidden",
		"resolve agent definition: permission denied for this model",
	} {
		t.Run(msg, func(t *testing.T) {
			category, detail := classifyError(fmt.Errorf(msg))
			assert.Equal(t, session.ErrorCategoryNonRetryable, category)
			assert.Contains(t, strings.ToLower(detail), "auth")
		})
	}
}

// TestClassifyError_ModelNotServed_NonRetryable proves a model the provider
// does not serve classifies non_retryable.
func TestClassifyError_ModelNotServed_NonRetryable(t *testing.T) {
	for _, msg := range []string{
		"call model: POST https://openrouter.ai/api/v1/chat/completions: 404 Not Found",
		"call model: model_not_found: the requested model is not served",
	} {
		t.Run(msg, func(t *testing.T) {
			category, _ := classifyError(fmt.Errorf(msg))
			assert.Equal(t, session.ErrorCategoryNonRetryable, category)
		})
	}
}

// TestClassifyError_MalformedAgentDefinition_NonRetryable proves a
// malformed agent definition classifies non_retryable.
func TestClassifyError_MalformedAgentDefinition_NonRetryable(t *testing.T) {
	err := fmt.Errorf("resolve agent definition: session %s has no agent definition assignment", "22222222-2222-2222-2222-222222222222")

	category, detail := classifyError(err)
	assert.Equal(t, session.ErrorCategoryNonRetryable, category)
	assert.Contains(t, strings.ToLower(detail), "malformed agent definition")
}

// TestClassifyError_UnrecognizedError_DefaultsNonRetryable proves the safe
// default (classify.go's doc comment: "an operator who cannot yet
// distinguish retryable from non_retryable should not be told a failure is
// safe to retry") -- an error this file's rules do not recognize is
// non_retryable, never guessed retryable.
func TestClassifyError_UnrecognizedError_DefaultsNonRetryable(t *testing.T) {
	err := errors.New("something entirely unexpected happened")

	category, detail := classifyError(err)
	assert.Equal(t, session.ErrorCategoryNonRetryable, category)
	assert.Equal(t, "something entirely unexpected happened", detail)
}

// TestClassifyError_NilError_NonRetryable proves the documented nil-input
// behavior: classifyError is never meant to be called with a nil error
// (failTurn only calls it when an activity actually failed), but if it
// ever were, it must not panic and must default non_retryable, not
// retryable.
func TestClassifyError_NilError_NonRetryable(t *testing.T) {
	category, _ := classifyError(nil)
	assert.Equal(t, session.ErrorCategoryNonRetryable, category)
}

// TestClassifyError_DetailIsTrimmed proves detail never exceeds
// maxErrorDetailLen (classify.go's doc comment: "short enough that
// GetSession's error_detail column and the failure transcript event
// payload both stay small").
func TestClassifyError_DetailIsTrimmed(t *testing.T) {
	long := strings.Repeat("x", maxErrorDetailLen*2)
	err := errors.New(long)

	_, detail := classifyError(err)
	// trimDetail's truncation marker ("…") is itself a multi-byte UTF-8
	// rune, so the trimmed byte length is maxErrorDetailLen-1 (the kept
	// prefix) plus the marker's own byte width, not exactly
	// maxErrorDetailLen -- bound generously rather than asserting an exact
	// byte count classify.go's doc comment does not promise.
	assert.LessOrEqual(t, len(detail), maxErrorDetailLen+4)
	assert.Less(t, len(detail), len(long), "a long detail must actually be shortened")
	assert.True(t, strings.HasSuffix(detail, "…"), "a trimmed detail must end with the truncation marker")
}

// TestClassifyError_ShortDetail_NotTrimmed proves a detail already within
// bounds is passed through unmodified.
func TestClassifyError_ShortDetail_NotTrimmed(t *testing.T) {
	err := errors.New("short message")

	_, detail := classifyError(err)
	assert.Equal(t, "short message", detail)
}
