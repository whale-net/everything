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
//
// Scaffold status (issue #1027): this file currently declares the public
// interface only. Refresh/Sync/GetStatus bodies are placeholders — the
// Implementation phase fills in the actual HTTP calls, JSON
// marshal/unmarshal, and non-2xx error handling described in the issue.
package argocd

import (
	"context"
	"errors"
	"net/http"
)

// Config is Client's configuration, sourced from ARGOCD_SERVER /
// ARGOCD_AUTH_TOKEN by callers (worker/main.go in a later task). Both
// fields are REQUIRED with no baked-in fallback — see NewClient, which
// fails fast at construction if either is empty, matching the "no silent
// default" convention used by worker/writeback/gitops.go's
// NewGitOpsActivities.
type Config struct {
	// ServerURL is the base URL of the ArgoCD API server, e.g.
	// "https://argocd.example.com" (ARGOCD_SERVER).
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
	return &Client{
		serverURL:  cfg.ServerURL,
		authToken:  cfg.AuthToken,
		httpClient: httpClient,
	}, nil
}

// Refresh triggers a normal-mode refresh of the named Application, scoped
// to project. It corresponds to
// GET {ServerURL}/api/v1/applications/{name}?refresh=normal&project={project}.
//
// TODO(Implementation phase, issue #1027): issue the GET request, attach
// the bearer token, and turn non-2xx responses into an error including
// status code + truncated body.
func (c *Client) Refresh(ctx context.Context, project, name string) error {
	return errNotImplemented
}

// Sync triggers a sync of the named Application, scoped to project. It
// corresponds to POST {ServerURL}/api/v1/applications/{name}/sync with a
// JSON body identifying name/project per ArgoCD's ApplicationSyncRequest
// shape.
//
// Sync must be implemented and unit-tested but is not called by any
// activity in this plan (FR14) — it exists for future
// drift-detection/rollback automation.
//
// TODO(Implementation phase, issue #1027): issue the POST request with a
// JSON body, attach the bearer token, and turn non-2xx responses into an
// error including status code + truncated body.
func (c *Client) Sync(ctx context.Context, project, name string) error {
	return errNotImplemented
}

// GetStatus fetches the named Application's current sync and health
// status, scoped to project. It corresponds to
// GET {ServerURL}/api/v1/applications/{name}?project={project}.
//
// syncStatus is one of ArgoCD's sync states (e.g. "Synced"/"OutOfSync"/
// "Unknown"); health is one of ArgoCD's health states (e.g.
// "Healthy"/"Progressing"/"Degraded"/"Suspended"/"Missing"/"Unknown").
//
// TODO(Implementation phase, issue #1027): issue the GET request, attach
// the bearer token, parse .status.sync.status/.status.health.status out of
// the response JSON, and turn non-2xx responses into an error including
// status code + truncated body.
func (c *Client) GetStatus(ctx context.Context, project, name string) (syncStatus, health string, err error) {
	return "", "", errNotImplemented
}

var errNotImplemented = errors.New("argocd: not implemented (scaffold only, see issue #1027)")
