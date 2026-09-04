package youtube

// Coverage for ChannelInfo and the New/WithHTTPClient test seam itself --
// see client.go's package doc comment "HTTP transport" section and this
// task's Testing section ("No test performs a live network call (enforce
// with an httptest server or injected *http.Client)").

import (
	"context"
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/oauth2"
)

func TestChannelInfo_Success_ReturnsChannel(t *testing.T) {
	c := newTestServer(t, map[string]http.HandlerFunc{
		"/youtube/v3/channels": func(w http.ResponseWriter, r *http.Request) {
			assert.Equal(t, "true", r.URL.Query().Get("mine"))
			writeJSONFixture(t, w, "channel_list.json")
		},
	})

	got, err := c.ChannelInfo(context.Background())
	require.NoError(t, err)
	assert.Equal(t, Channel{YouTubeChannelID: "chan-123", Title: "My Channel"}, got)
}

func TestChannelInfo_NoItems_ReturnsErrPermanent(t *testing.T) {
	c := newTestServer(t, map[string]http.HandlerFunc{
		"/youtube/v3/channels": func(w http.ResponseWriter, r *http.Request) {
			writeJSONFixture(t, w, "channel_list_empty.json")
		},
	})

	_, err := c.ChannelInfo(context.Background())
	assert.ErrorIs(t, err, ErrPermanent)
}

// TestChannelInfo_QuotaError_ClassifiesAsErrQuotaExceeded proves classify's
// decision actually reaches the caller through a real (httptest) HTTP round
// trip, not just in classify's own unit tests (errors_test.go) -- a 403
// quotaExceeded from the server must come back as ErrQuotaExceeded, and
// specifically NOT ErrRevoked, since FR4 correctness depends on that
// distinction never blurring in the real request path.
func TestChannelInfo_QuotaError_ClassifiesAsErrQuotaExceeded(t *testing.T) {
	c := newTestServer(t, map[string]http.HandlerFunc{
		"/youtube/v3/channels": func(w http.ResponseWriter, r *http.Request) {
			writeAPIError(w, http.StatusForbidden, "quotaExceeded", "Quota exceeded")
		},
	})

	_, err := c.ChannelInfo(context.Background())
	assert.ErrorIs(t, err, ErrQuotaExceeded)
	assert.NotErrorIs(t, err, ErrRevoked)
}

// TestChannelInfo_RevokedCredential_ClassifiesAsErrRevoked mirrors the
// quota test above for the 401 case (FR4's primary signal).
func TestChannelInfo_RevokedCredential_ClassifiesAsErrRevoked(t *testing.T) {
	c := newTestServer(t, map[string]http.HandlerFunc{
		"/youtube/v3/channels": func(w http.ResponseWriter, r *http.Request) {
			writeAPIError(w, http.StatusUnauthorized, "", "Invalid Credentials")
		},
	})

	_, err := c.ChannelInfo(context.Background())
	assert.ErrorIs(t, err, ErrRevoked)
}

// TestChannelInfo_ServerError_ClassifiesAsErrTransient covers the 5xx leg
// through the real request path.
func TestChannelInfo_ServerError_ClassifiesAsErrTransient(t *testing.T) {
	c := newTestServer(t, map[string]http.HandlerFunc{
		"/youtube/v3/channels": func(w http.ResponseWriter, r *http.Request) {
			writeAPIError(w, http.StatusInternalServerError, "", "backend error")
		},
	})

	_, err := c.ChannelInfo(context.Background())
	assert.ErrorIs(t, err, ErrTransient)
}

// TestNew_InvalidGrantTokenSource_ChannelInfoReturnsErrRevoked exercises the
// classify oauth2.RetrieveError branch through New's actual production
// wiring (no WithHTTPClient override): golang.org/x/oauth2's Transport
// calls TokenSource.Token() and returns its error verbatim BEFORE dialing
// any network connection (see client.go's "HTTP transport" doc comment),
// so this is still a zero-live-network-call test despite not using an
// httptest server.
func TestNew_InvalidGrantTokenSource_ChannelInfoReturnsErrRevoked(t *testing.T) {
	ts := erroringTokenSource{err: &oauth2.RetrieveError{ErrorCode: "invalid_grant"}}
	c := New(ts, WithRequestTimeout(2_000_000_000)) // 2s -- must never actually be reached

	_, err := c.ChannelInfo(context.Background())
	assert.ErrorIs(t, err, ErrRevoked)
}

// TestWithHTTPClient_NilIsNoOp guards WithHTTPClient's documented "a nil hc
// is a no-op" contract.
func TestWithHTTPClient_NilIsNoOp(t *testing.T) {
	cfg := &clientConfig{}
	WithHTTPClient(nil)(cfg)
	assert.Nil(t, cfg.httpClient)
}

// TestWithRequestTimeout_NonPositiveIsNoOp guards WithRequestTimeout's
// analogous contract, so a caller can't accidentally disable the "never
// blocks indefinitely" guarantee client.go documents.
func TestWithRequestTimeout_NonPositiveIsNoOp(t *testing.T) {
	cfg := &clientConfig{requestTimeout: defaultRequestTimeout}
	WithRequestTimeout(0)(cfg)
	assert.Equal(t, defaultRequestTimeout, cfg.requestTimeout)
	WithRequestTimeout(-1)(cfg)
	assert.Equal(t, defaultRequestTimeout, cfg.requestTimeout)
}
