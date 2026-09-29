package apiclient

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func echoAuth(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(r.Header.Get("Authorization")))
	}))
	t.Cleanup(srv.Close)
	return srv
}

func get(t *testing.T, ctx context.Context, c *http.Client, url string) string {
	t.Helper()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	require.NoError(t, err)
	resp, err := c.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close() //nolint:errcheck
	buf := make([]byte, 256)
	n, _ := resp.Body.Read(buf)
	return string(buf[:n])
}

func TestUserTokenForwardedAsBearer(t *testing.T) {
	srv := echoAuth(t)
	c := &http.Client{Transport: &Transport{}}
	got := get(t, WithUserToken(context.Background(), "op-token"), c, srv.URL)
	assert.Equal(t, "Bearer op-token", got)
}

func TestNoCredentialSendsNoAuthorization(t *testing.T) {
	srv := echoAuth(t)
	c := &http.Client{Transport: &Transport{}}
	assert.Equal(t, "", get(t, context.Background(), c, srv.URL))
}

// The machine token source refreshes: each expiry re-hits the token endpoint.
func TestClientCredentialsBearerAndRefresh(t *testing.T) {
	var issued atomic.Int32
	idp := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.NoError(t, r.ParseForm())
		assert.Equal(t, "client_credentials", r.Form.Get("grant_type"))
		n := issued.Add(1)
		w.Header().Set("Content-Type", "application/json")
		// expires_in of 1s is inside oauth2's expiry delta, so every call refreshes.
		fmt.Fprintf(w, `{"access_token":"m-%d","token_type":"Bearer","expires_in":1}`, n)
	}))
	defer idp.Close()

	ts, err := NewClientCredentialsSource(context.Background(), ClientCredentialsConfig{TokenURL: idp.URL, ClientID: "id", ClientSecret: "s"})
	require.NoError(t, err)
	srv := echoAuth(t)
	c := &http.Client{Transport: &Transport{Machine: ts}}

	assert.Equal(t, "Bearer m-1", get(t, context.Background(), c, srv.URL))
	assert.Equal(t, "Bearer m-2", get(t, context.Background(), c, srv.URL), "expired token is refreshed")
}

func TestUserTokenWinsOverMachine(t *testing.T) {
	idp := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("machine token must not be fetched when a user token is present")
	}))
	defer idp.Close()
	ts, err := NewClientCredentialsSource(context.Background(), ClientCredentialsConfig{TokenURL: idp.URL, ClientID: "id", ClientSecret: "s"})
	require.NoError(t, err)
	srv := echoAuth(t)
	c := &http.Client{Transport: &Transport{Machine: ts}}
	assert.Equal(t, "Bearer u", get(t, WithUserToken(context.Background(), "u"), c, srv.URL))
}

func TestClientCredentialsRequiresConfig(t *testing.T) {
	_, err := NewClientCredentialsSource(context.Background(), ClientCredentialsConfig{})
	assert.Error(t, err)
}
