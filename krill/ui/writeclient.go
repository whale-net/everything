// writeClient is how this binary's own app pages reach krill's write API:
// it mints one krill session per (operator, scope) through
// POST /sessions/init, then presents that session id on every mutating
// request. api's write gate (api/handlers/gate.go) resolves the session
// back into the acting / on-behalf-of Subject and scope the handler
// attributes its mutation to, so the identity a UI write carries is
// whatever InitSession recorded -- here, always the signed-in operator's
// real (iss, sub) pair (identity.go), never a value taken from the request
// body or a default.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"

	"github.com/google/uuid"

	"github.com/whale-net/everything/krill/store"
)

// sessionHeader is the header api's RequireSession gate reads a krill
// session id from (api/handlers/gate.go's unexported sessionHeader).
const sessionHeader = "X-Krill-Session-Id"

// writeClientConfig configures a writeClient. BaseURL is this deployment's
// krill `api` base URL (KRILL_API_URL).
type writeClientConfig struct {
	BaseURL string

	// HTTPClient is optional; a client with a sane timeout is built when
	// unset.
	HTTPClient *http.Client
}

// writeClient issues krill writes on behalf of the signed-in operator.
type writeClient struct {
	baseURL *url.URL
	http    *http.Client
}

// newWriteClient rejects an unset BaseURL rather than defaulting one: a UI
// with no configured api to write to cannot attribute anything, and
// silently pointing somewhere else would be worse than not booting.
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
	return &writeClient{baseURL: parsed, http: httpClient}, nil
}

// subjectRequest is the wire shape of one Subject in an init request --
// mirrors krill/api/handlers' SubjectRequest field for field, redeclared
// because this client speaks to `api` over HTTP rather than importing its
// handler package.
type subjectRequest struct {
	Iss  string `json:"iss"`
	Sub  string `json:"sub"`
	Kind string `json:"kind"`
}

// initSessionRequest is POST /sessions/init's body (api/handlers/session.go).
type initSessionRequest struct {
	ScopeID    string         `json:"scope_id"`
	Acting     subjectRequest `json:"acting"`
	OnBehalfOf subjectRequest `json:"on_behalf_of"`
}

// initSessionResponse is POST /sessions/init's response body
// (api/handlers/session.go's InitSessionResponse).
type initSessionResponse struct {
	SessionID string `json:"session_id"`
	ScopeID   string `json:"scope_id"`
}

func subjectRequestOf(subject store.Subject) subjectRequest {
	return subjectRequest{Iss: subject.Iss, Sub: subject.Sub, Kind: string(subject.Kind)}
}

// InitSession mints a krill session scoped to scopeID whose acting and
// on-behalf-of subjects are both operator -- a signed-in operator acts for
// themselves, so the two are the same triple rather than one inferred from
// the other (api/handlers/session.go's rule).
func (c *writeClient) InitSession(ctx context.Context, scopeID uuid.UUID, operator store.Subject) (store.SessionID, error) {
	var zero store.SessionID
	body, err := json.Marshal(initSessionRequest{
		ScopeID:    scopeID.String(),
		Acting:     subjectRequestOf(operator),
		OnBehalfOf: subjectRequestOf(operator),
	})
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
