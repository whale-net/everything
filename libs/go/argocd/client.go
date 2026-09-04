// Package argocd is a minimal REST client for ArgoCD's Application API
// (FR14/FR15). It talks plain HTTP + bearer token against ArgoCD's own REST
// API rather than depending on the official argo-cd Go SDK, to keep this
// package's dependency footprint small (FR14) — see issue #1027.
//
// This package is standalone: it has no dependency on the writeback
// workflow or any App Registry-specific type, and must never import
// anything under tools/app_registry. It is consumed by later tasks in the
// argo-promotion-sync plan (TriggerArgoRefresh/PollArgoSyncStatus/retry
// activities), not directly here.
//
// project scoping (NFR7): every method takes a project argument and passes
// it through as ArgoCD's own scoping query/body parameter, even though
// ArgoCD-side enforcement of per-domain AppProjects isn't tightened yet
// (whale-net/argok8s#86 is still pending) — this is a known, accepted
// residual risk and not this package's job to close.
package argocd

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
)

// maxErrBodyLen caps how much of a non-2xx response body is included in a
// returned error, so a large/unexpected HTML error page (e.g. from a proxy
// in front of ArgoCD) doesn't blow up log lines.
const maxErrBodyLen = 2048

// Config is Client's configuration, sourced from ARGOCD_SERVER /
// ARGOCD_AUTH_TOKEN by callers (worker/main.go in a later task). Both
// fields are REQUIRED with no baked-in fallback — see NewClient, which
// fails fast at construction if either is empty, matching the "no silent
// default" convention used by worker/writeback/gitops.go's
// NewGitOpsActivities.
type Config struct {
	// ServerURL is the base URL of the ArgoCD API server, e.g.
	// "https://argocd.example.com" (ARGOCD_SERVER). No trailing slash
	// required — Client normalizes it.
	ServerURL string
	// AuthToken is sent as "Authorization: Bearer <token>" on every
	// request (ARGOCD_AUTH_TOKEN). Should be scoped via ArgoCD-side RBAC
	// to least privilege (NFR1) rather than an admin credential — see
	// tools/app_registry/ENV.md.
	AuthToken string
}

// Client is a minimal ArgoCD Application API client. Safe for concurrent
// use — it holds no mutable state beyond its configuration and an
// *http.Client.
type Client struct {
	serverURL  string
	authToken  string
	httpClient *http.Client
}

// NewClient constructs a Client. It fails fast (returns a non-nil error)
// if cfg.ServerURL or cfg.AuthToken is empty, and never falls back to a
// default. httpClient may be nil, in which case http.DefaultClient is
// used.
func NewClient(cfg Config, httpClient *http.Client) (*Client, error) {
	if cfg.ServerURL == "" {
		return nil, errors.New("argocd: Config.ServerURL is required")
	}
	if cfg.AuthToken == "" {
		return nil, errors.New("argocd: Config.AuthToken is required")
	}
	if httpClient == nil {
		httpClient = http.DefaultClient
	}
	serverURL := cfg.ServerURL
	for len(serverURL) > 0 && serverURL[len(serverURL)-1] == '/' {
		serverURL = serverURL[:len(serverURL)-1]
	}
	return &Client{
		serverURL:  serverURL,
		authToken:  cfg.AuthToken,
		httpClient: httpClient,
	}, nil
}

// Refresh triggers a normal-mode refresh of the named Application, scoped
// to project. It corresponds to
// GET {ServerURL}/api/v1/applications/{name}?refresh=normal&project={project}.
func (c *Client) Refresh(ctx context.Context, project, name string) error {
	q := url.Values{}
	q.Set("refresh", "normal")
	q.Set("project", project)
	reqURL := fmt.Sprintf("%s/api/v1/applications/%s?%s", c.serverURL, url.PathEscape(name), q.Encode())

	_, err := c.do(ctx, http.MethodGet, reqURL, nil)
	if err != nil {
		return fmt.Errorf("argocd: Refresh(%s/%s): %w", project, name, err)
	}
	return nil
}

// syncRequest mirrors the subset of ArgoCD's ApplicationSyncRequest shape
// this client needs to identify the target application.
type syncRequest struct {
	Name    string `json:"name"`
	Project string `json:"project,omitempty"`
}

// Sync triggers a sync of the named Application, scoped to project. It
// corresponds to POST {ServerURL}/api/v1/applications/{name}/sync.
//
// Sync is implemented and unit-tested here but, as of this package's
// introduction, has zero callers anywhere in the repo (FR14) — it exists
// for future drift-detection/rollback automation, not for any activity in
// the current plan.
func (c *Client) Sync(ctx context.Context, project, name string) error {
	body, err := json.Marshal(syncRequest{Name: name, Project: project})
	if err != nil {
		return fmt.Errorf("argocd: Sync(%s/%s): marshal request: %w", project, name, err)
	}
	reqURL := fmt.Sprintf("%s/api/v1/applications/%s/sync", c.serverURL, url.PathEscape(name))

	_, err = c.do(ctx, http.MethodPost, reqURL, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("argocd: Sync(%s/%s): %w", project, name, err)
	}
	return nil
}

// applicationResponse is the subset of ArgoCD's Application JSON response
// this client parses.
type applicationResponse struct {
	Status struct {
		Sync struct {
			Status string `json:"status"`
		} `json:"sync"`
		Health struct {
			Status string `json:"status"`
		} `json:"health"`
		// OperationState.Phase is the ONLY signal that reflects hook
		// resources (PreSync/Sync/PostSync, e.g. a migration Job run via
		// the argocd.argoproj.io/hook annotation) -- ArgoCD excludes hook
		// resources from the Health rollup above entirely, so an
		// Application can report Sync.Status=Synced, Health.Status=Healthy
		// while a PostSync hook is still Running. Phase is one of ArgoCD's
		// operation phases (e.g. "Running"/"Succeeded"/"Failed"/"Error"/
		// "Terminating"), "" if no operation has ever run against this
		// Application.
		OperationState struct {
			Phase string `json:"phase"`
		} `json:"operationState"`
	} `json:"status"`
}

// GetStatus fetches the named Application's current sync, health, and
// operation-phase status, scoped to project. It corresponds to
// GET {ServerURL}/api/v1/applications/{name}?project={project}.
//
// syncStatus is one of ArgoCD's sync states (e.g. "Synced"/"OutOfSync"/
// "Unknown"); health is one of ArgoCD's health states (e.g.
// "Healthy"/"Progressing"/"Degraded"/"Suspended"/"Missing"/"Unknown");
// operationPhase is the most recent sync operation's phase (e.g.
// "Running"/"Succeeded"/"Failed"/"Error"/"Terminating"), "" if none has run.
// Callers that want to know whether EVERY resource ArgoCD manages for this
// Application -- including hooks -- has finished rolling out must check
// operationPhase == "Succeeded" in addition to syncStatus/health, since
// health alone does not cover hook resources.
func (c *Client) GetStatus(ctx context.Context, project, name string) (syncStatus, health, operationPhase string, err error) {
	q := url.Values{}
	q.Set("project", project)
	reqURL := fmt.Sprintf("%s/api/v1/applications/%s?%s", c.serverURL, url.PathEscape(name), q.Encode())

	respBody, err := c.do(ctx, http.MethodGet, reqURL, nil)
	if err != nil {
		return "", "", "", fmt.Errorf("argocd: GetStatus(%s/%s): %w", project, name, err)
	}

	var app applicationResponse
	if err := json.Unmarshal(respBody, &app); err != nil {
		return "", "", "", fmt.Errorf("argocd: GetStatus(%s/%s): parse response: %w", project, name, err)
	}
	return app.Status.Sync.Status, app.Status.Health.Status, app.Status.OperationState.Phase, nil
}

// do issues an HTTP request with the bearer token attached and returns the
// response body on a 2xx status. Non-2xx responses are turned into an
// error that includes the HTTP status code and a truncated response body,
// so callers can distinguish e.g. 404 (Application not found) from 401/403
// (RBAC denial) from 5xx.
func (c *Client) do(ctx context.Context, method, reqURL string, body io.Reader) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, method, reqURL, body)
	if err != nil {
		return nil, fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+c.authToken)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("do request: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read response body: %w", err)
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		truncated := respBody
		if len(truncated) > maxErrBodyLen {
			truncated = truncated[:maxErrBodyLen]
		}
		return nil, fmt.Errorf("unexpected status %d: %s", resp.StatusCode, truncated)
	}

	return respBody, nil
}
