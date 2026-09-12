// Package forge is krill's minimal GitHub forge client (issue #2496, FR20,
// C9): today it does exactly one thing -- create a thin GitHub issue,
// using the `scope` row's own coordinates (LB1). Krill does not own
// branch or PR lifecycle (C20, deferred): this client only ever creates
// an issue and hands back its number/URL. It never reads, updates, or
// closes anything, and never derives state from a branch name -- there is
// no method here that even accepts one.
package forge

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
)

// Client creates krill's one forge artifact kind (a thin GitHub issue,
// mirroring store.PointerArtifact.Kind's "github_issue" discriminator).
// api/handlers/pointer.go depends on this interface, not *GitHubClient
// directly, so pointer_test.go can fake it with no network call --
// GitHubClient itself is tested by faking the HTTP round trip instead (see
// this package's own test, which points BaseURL at an httptest.Server).
type Client interface {
	// CreateIssue opens a new issue in repoFullName ("owner/repo") with
	// the given title/body, and returns its number and HTML URL.
	CreateIssue(ctx context.Context, repoFullName, title, body string) (issueNumber int, issueURL string, err error)
}

// GitHubClient is the real Client implementation, authenticating with a
// plain bearer token (KRILL_GITHUB_TOKEN -- see ../ENV.md). A full GitHub
// App installation-token flow (tools/app_registry/worker/release's
// GitHubDispatcher precedent) is not needed for the single write this
// client ever makes.
type GitHubClient struct {
	// Token is the bearer credential sent on every request -- a
	// fine-grained PAT or GitHub App installation token with "Issues:
	// write" on the target repository is enough; this client asks for
	// nothing else.
	Token string

	// HTTPClient issues every request. Defaults to http.DefaultClient.
	HTTPClient *http.Client

	// BaseURL defaults to "https://api.github.com"; overridable in tests
	// to point at an httptest.Server so no test ever makes a real network
	// call.
	BaseURL string
}

var _ Client = (*GitHubClient)(nil)

func (c *GitHubClient) httpClient() *http.Client {
	if c.HTTPClient != nil {
		return c.HTTPClient
	}
	return http.DefaultClient
}

func (c *GitHubClient) baseURL() string {
	if c.BaseURL != "" {
		return c.BaseURL
	}
	return "https://api.github.com"
}

// CreateIssue implements Client via POST /repos/{repoFullName}/issues.
func (c *GitHubClient) CreateIssue(ctx context.Context, repoFullName, title, body string) (int, string, error) {
	reqBody, err := json.Marshal(map[string]string{"title": title, "body": body})
	if err != nil {
		return 0, "", fmt.Errorf("marshal create-issue request: %w", err)
	}

	url := fmt.Sprintf("%s/repos/%s/issues", c.baseURL(), repoFullName)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(reqBody))
	if err != nil {
		return 0, "", fmt.Errorf("build create-issue request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+c.Token)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.httpClient().Do(req)
	if err != nil {
		return 0, "", fmt.Errorf("create issue in %s: %w", repoFullName, err)
	}
	defer resp.Body.Close() //nolint:errcheck

	if resp.StatusCode != http.StatusCreated {
		respBody, _ := io.ReadAll(resp.Body)
		return 0, "", fmt.Errorf("create issue in %s: unexpected status %d: %s", repoFullName, resp.StatusCode, string(respBody))
	}

	var out struct {
		Number  int    `json:"number"`
		HTMLURL string `json:"html_url"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return 0, "", fmt.Errorf("decode create-issue response: %w", err)
	}
	return out.Number, out.HTMLURL, nil
}

// IssueContent builds the pointer issue's title/body for a krill Product
// (FR20): the title names the Product and the body links back to it by
// its krill surrogate id (LB2), so a human opening the issue on GitHub can
// tell at a glance which krill Product it stands in for, and existing
// PR/commit/conversation cross-linking conventions (e.g. this repo's own
// "Part of #<n>") keep working unchanged against the issue this returns
// the content for (C9).
func IssueContent(productName, productID string) (title, body string) {
	title = fmt.Sprintf("Product: %s", productName)
	body = fmt.Sprintf(
		"This is krill's thin GitHub pointer issue for Product %q (krill id `%s`).\n\n"+
			"The actual spec lives in krill's own entity model, not in this repository -- "+
			"see krill/ARCHITECTURE.md. Existing PR/commit/conversation cross-linking "+
			"conventions (e.g. \"Part of #<this issue>\") keep working unchanged by "+
			"referencing this issue.\n",
		productName, productID,
	)
	return title, body
}
