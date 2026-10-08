// writeClient mints one krill session per (operator, scope) and presents it on every
// mutating request, so api attributes UI writes to the signed-in operator's real
// (iss, sub) as recorded at session init, never to request-body values.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/whale-net/everything/krill/apiclient"
	"github.com/whale-net/everything/krill/store"
)

// sessionHeader is the header api's RequireSession gate reads the session id from.
const sessionHeader = "X-Krill-Session-Id"

// writeClientConfig configures a writeClient. BaseURL is krill's api base URL (KRILL_API_URL).
type writeClientConfig struct {
	BaseURL string

	// HTTPClient is optional; a client with a timeout is built when unset.
	HTTPClient *http.Client
}

// writeClient issues krill writes on behalf of the signed-in operator.
type writeClient struct {
	baseURL *url.URL
	http    *http.Client
}

// newWriteClient rejects an unset BaseURL: a UI with no api to write to cannot
// attribute anything, so it refuses to boot rather than default.
func newWriteClient(cfg writeClientConfig) (*writeClient, error) {
	if cfg.BaseURL == "" {
		return nil, fmt.Errorf("KRILL_API_URL is required: krill-ui issues its writes against krill's api binary")
	}
	parsed, err := url.Parse(cfg.BaseURL)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return nil, fmt.Errorf("invalid KRILL_API_URL %q: expected an absolute http(s) base URL", cfg.BaseURL)
	}
	httpClient := cfg.HTTPClient
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 15 * time.Second}
	}
	if httpClient.Transport == nil || !isBearerTransport(httpClient.Transport) {
		copied := *httpClient
		copied.Transport = &apiclient.Transport{Base: httpClient.Transport}
		httpClient = &copied
	}
	return &writeClient{baseURL: parsed, http: httpClient}, nil
}

// initSessionRequest is POST /sessions/init's body. Identity and scope are
// never sent: api derives both from the verified bearer token.
type initSessionRequest struct{}

// initSessionResponse is POST /sessions/init's response body.
type initSessionResponse struct {
	SessionID string `json:"session_id"`
	ScopeID   string `json:"scope_id"`
}

// InitSession mints a krill session for the caller ctx's bearer token
// (apiclient.WithUserToken) verifies as; api records identity and scope
// from that token, not from this request.
func (c *writeClient) InitSession(ctx context.Context) (store.SessionID, error) {
	var zero store.SessionID
	body, err := json.Marshal(initSessionRequest{})
	if err != nil {
		return zero, fmt.Errorf("encode init request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL.JoinPath("sessions/init").String(), bytes.NewReader(body))
	if err != nil {
		return zero, fmt.Errorf("build init request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.http.Do(req)
	if err != nil {
		return zero, fmt.Errorf("init session: %w", err)
	}
	defer resp.Body.Close() //nolint:errcheck

	if resp.StatusCode != http.StatusCreated {
		// A 403 (reader role) or 401 is api's own answer; carry it so the
		// browser sees the rejection rather than a generic 502.
		var parsed struct {
			Error string `json:"error"`
		}
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<10))
		if json.Unmarshal(msg, &parsed) != nil || parsed.Error == "" {
			parsed.Error = strings.TrimSpace(string(msg))
		}
		if parsed.Error == "" {
			parsed.Error = http.StatusText(resp.StatusCode)
		}
		if resp.StatusCode == http.StatusForbidden || resp.StatusCode == http.StatusUnauthorized {
			return zero, &writeRejection{status: resp.StatusCode, message: parsed.Error}
		}
		return zero, fmt.Errorf("init session: api returned %s", resp.Status)
	}

	var parsed initSessionResponse
	if err := json.NewDecoder(resp.Body).Decode(&parsed); err != nil {
		return zero, fmt.Errorf("decode init response: %w", err)
	}
	id, err := uuid.Parse(parsed.SessionID)
	if err != nil {
		return zero, fmt.Errorf("init session: api returned a malformed session id %q", parsed.SessionID)
	}
	return store.SessionID(id), nil
}

// Write issues one mutating request against api, carrying sessionID in the
// gate's header. body may be nil; when non-nil it is JSON-encoded. The
// caller owns the returned response body.
func (c *writeClient) Write(ctx context.Context, sessionID store.SessionID, method, path string, body any) (*http.Response, error) {
	var payload io.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			return nil, fmt.Errorf("encode request body: %w", err)
		}
		payload = bytes.NewReader(encoded)
	}

	req, err := http.NewRequestWithContext(ctx, method, c.baseURL.JoinPath(path).String(), payload)
	if err != nil {
		return nil, fmt.Errorf("build %s %s: %w", method, path, err)
	}
	req.Header.Set(sessionHeader, sessionID.String())
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("%s %s: %w", method, path, err)
	}
	return resp, nil
}

func isBearerTransport(rt http.RoundTripper) bool {
	_, ok := rt.(*apiclient.Transport)
	return ok
}
