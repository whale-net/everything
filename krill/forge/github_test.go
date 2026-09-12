// Unit tests for GitHubClient.CreateIssue and IssueContent (issue #2496's
// Testing section): the HTTP round trip is faked via an httptest.Server so
// no test here ever makes a real network call, and IssueContent's
// title/body assertions prove the created issue points back at the krill
// Product it stands in for (C9).
package forge_test

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/whale-net/everything/krill/forge"
)

// TestGitHubClient_CreateIssue_PostsToRepoIssuesEndpoint proves CreateIssue
// hits POST /repos/{repoFullName}/issues with the given title/body and a
// bearer Authorization header, and returns the faked issue's number and
// html_url -- with no real network call (BaseURL points at the
// httptest.Server).
func TestGitHubClient_CreateIssue_PostsToRepoIssuesEndpoint(t *testing.T) {
	var gotPath, gotAuth string
	var gotBody map[string]string

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotAuth = r.Header.Get("Authorization")
		body, err := io.ReadAll(r.Body)
		require.NoError(t, err)
		require.NoError(t, json.Unmarshal(body, &gotBody))

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"number": 99, "html_url": "https://github.com/whale-net/everything/issues/99"}`))
	}))
	defer server.Close()

	client := &forge.GitHubClient{Token: "test-token", BaseURL: server.URL}
	issueNumber, issueURL, err := client.CreateIssue(t.Context(), "whale-net/everything", "Product: Krill", "the body")
	require.NoError(t, err)

	assert.Equal(t, "/repos/whale-net/everything/issues", gotPath)
	assert.Equal(t, "Bearer test-token", gotAuth)
	assert.Equal(t, "Product: Krill", gotBody["title"])
	assert.Equal(t, "the body", gotBody["body"])

	assert.Equal(t, 99, issueNumber)
	assert.Equal(t, "https://github.com/whale-net/everything/issues/99", issueURL)
}

// TestGitHubClient_CreateIssue_NonCreatedStatus_ReturnsError proves an
// unexpected status code (e.g. a 4xx/5xx from GitHub) surfaces as an error
// rather than a zero-value success.
func TestGitHubClient_CreateIssue_NonCreatedStatus_ReturnsError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnprocessableEntity)
		_, _ = w.Write([]byte(`{"message": "Validation Failed"}`))
	}))
	defer server.Close()

	client := &forge.GitHubClient{Token: "test-token", BaseURL: server.URL}
	_, _, err := client.CreateIssue(t.Context(), "whale-net/everything", "title", "body")
	assert.Error(t, err)
}

// TestIssueContent_PointsBackAtKrillProduct is C9: the generated title
// names the Product and the body links back to it by its krill surrogate
// id, so existing PR/commit/conversation cross-linking conventions keep
// working unchanged against the issue this content is minted for.
func TestIssueContent_PointsBackAtKrillProduct(t *testing.T) {
	title, body := forge.IssueContent("Krill", "0d1f7e2a-0000-0000-0000-000000000001")

	assert.Equal(t, "Product: Krill", title)
	assert.True(t, strings.Contains(body, "Krill"), "body must name the Product")
	assert.True(t, strings.Contains(body, "0d1f7e2a-0000-0000-0000-000000000001"), "body must carry the Product's krill surrogate id")
}
