package youtube

// Table-driven coverage of classify's error-classification boundary --
// this task's Testing section: invalid_grant -> ErrRevoked, 403 quota
// reasons -> ErrQuotaExceeded, 5xx/network/context-deadline -> ErrTransient,
// other 4xx -> ErrPermanent. This is the table FR4 correctness depends on
// (errors.go's package doc comment): a misclassified quota error would
// wrongly disconnect a healthy Channel, so every quota case also asserts
// NOT ErrRevoked, and the 403-forbidden (non-quota) case asserts NOT
// ErrQuotaExceeded and NOT ErrRevoked.

import (
	"context"
	"errors"
	"fmt"
	"net"
	"testing"

	"github.com/stretchr/testify/assert"
	"golang.org/x/oauth2"
	"google.golang.org/api/googleapi"
)

func TestClassify_Nil_ReturnsNil(t *testing.T) {
	assert.NoError(t, classify(nil))
}

func TestClassify_Table(t *testing.T) {
	cases := []struct {
		name    string
		err     error
		want    error
		mustNot []error // sentinels the result must NOT satisfy errors.Is against
	}{
		{
			name:    "oauth invalid_grant is revoked",
			err:     &oauth2.RetrieveError{ErrorCode: "invalid_grant"},
			want:    ErrRevoked,
			mustNot: []error{ErrQuotaExceeded, ErrTransient, ErrPermanent},
		},
		{
			name: "oauth invalid_grant wrapped in another error is still detected via errors.As",
			err:  fmt.Errorf("token refresh failed: %w", &oauth2.RetrieveError{ErrorCode: "invalid_grant"}),
			want: ErrRevoked,
		},
		{
			name:    "oauth error with a different code is transient, NOT revoked",
			err:     &oauth2.RetrieveError{ErrorCode: "temporarily_unavailable"},
			want:    ErrTransient,
			mustNot: []error{ErrRevoked},
		},
		{
			name: "googleapi 401 is revoked",
			err:  &googleapi.Error{Code: 401, Message: "Invalid Credentials"},
			want: ErrRevoked,
		},
		{
			name: "googleapi 403 quotaExceeded reason is quota, NOT revoked or permanent",
			err: &googleapi.Error{Code: 403, Message: "Quota exceeded", Errors: []googleapi.ErrorItem{
				{Reason: "quotaExceeded", Message: "Quota exceeded"},
			}},
			want:    ErrQuotaExceeded,
			mustNot: []error{ErrRevoked, ErrPermanent},
		},
		{
			name: "googleapi 403 dailyLimitExceeded reason is quota",
			err: &googleapi.Error{Code: 403, Errors: []googleapi.ErrorItem{
				{Reason: "dailyLimitExceeded"},
			}},
			want: ErrQuotaExceeded,
		},
		{
			name: "googleapi 403 rateLimitExceeded reason is quota",
			err: &googleapi.Error{Code: 403, Errors: []googleapi.ErrorItem{
				{Reason: "rateLimitExceeded"},
			}},
			want: ErrQuotaExceeded,
		},
		{
			name: "googleapi 403 userRateLimitExceeded reason is quota",
			err: &googleapi.Error{Code: 403, Errors: []googleapi.ErrorItem{
				{Reason: "userRateLimitExceeded"},
			}},
			want: ErrQuotaExceeded,
		},
		{
			name: "googleapi 403 with no structured reason falls back to a quota mention in the message",
			err:  &googleapi.Error{Code: 403, Message: "User rate limit exceeded, please retry"},
			want: ErrQuotaExceeded,
		},
		{
			name: "googleapi 403 forbidden reason is permanent -- the FR4 misclassification guard",
			err: &googleapi.Error{Code: 403, Message: "Access forbidden", Errors: []googleapi.ErrorItem{
				{Reason: "forbidden", Message: "Access forbidden"},
			}},
			want:    ErrPermanent,
			mustNot: []error{ErrQuotaExceeded, ErrRevoked},
		},
		{
			name: "googleapi 403 accessNotConfigured reason is permanent, not quota",
			err: &googleapi.Error{Code: 403, Errors: []googleapi.ErrorItem{
				{Reason: "accessNotConfigured"},
			}},
			want:    ErrPermanent,
			mustNot: []error{ErrQuotaExceeded},
		},
		{
			name: "googleapi 500 is transient",
			err:  &googleapi.Error{Code: 500, Message: "Internal error"},
			want: ErrTransient,
		},
		{
			name: "googleapi 503 is transient",
			err:  &googleapi.Error{Code: 503, Message: "Service unavailable"},
			want: ErrTransient,
		},
		{
			name: "googleapi 404 is permanent",
			err:  &googleapi.Error{Code: 404, Message: "Video not found"},
			want: ErrPermanent,
		},
		{
			name: "googleapi 400 is permanent",
			err:  &googleapi.Error{Code: 400, Message: "Bad request"},
			want: ErrPermanent,
		},
		{
			name: "context deadline exceeded is transient",
			err:  context.DeadlineExceeded,
			want: ErrTransient,
		},
		{
			name: "context canceled is transient",
			err:  context.Canceled,
			want: ErrTransient,
		},
		{
			name: "a net.Error (e.g. connection reset) is transient",
			err:  &net.OpError{Op: "dial", Net: "tcp", Err: errors.New("connection refused")},
			want: ErrTransient,
		},
		{
			name:    "an unrecognized error falls through to permanent, never silently treated as retryable",
			err:     errors.New("boom"),
			want:    ErrPermanent,
			mustNot: []error{ErrTransient, ErrQuotaExceeded, ErrRevoked},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := classify(tc.err)
			assert.ErrorIs(t, got, tc.want)
			for _, not := range tc.mustNot {
				assert.NotErrorIs(t, got, not, "classify(%v) must not also satisfy errors.Is(_, %v)", tc.err, not)
			}
		})
	}
}

// TestClassify_PreservesUnderlyingMessage guards the "original error's
// message is preserved for logging" contract in classify's doc comment --
// a caller (or this package's own logging) can still find Google's own
// error text in the classified result.
func TestClassify_PreservesUnderlyingMessage(t *testing.T) {
	err := &googleapi.Error{Code: 404, Message: "video 42 does not exist"}
	got := classify(err)
	assert.ErrorContains(t, got, "video 42 does not exist")
}
