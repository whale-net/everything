package kraken

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"
)

// GitHubReleaseData represents data for creating a GitHub release.
type GitHubReleaseData struct {
	TagName         string `json:"tag_name"`
	Name            string `json:"name"`
	Body            string `json:"body"`
	Draft           bool   `json:"draft"`
	Prerelease      bool   `json:"prerelease"`
	TargetCommitish string `json:"target_commitish,omitempty"`
}

// GitHubReleaseClient is a client for interacting with GitHub Releases API.
type GitHubReleaseClient struct {
	Owner      string
	Repo       string
	Token      string
	BaseURL    string
	httpClient *http.Client
}

// NewGitHubReleaseClient creates a new GitHub release client.
func NewGitHubReleaseClient(owner, repo, token string) (*GitHubReleaseClient, error) {
	if token == "" {
		token = os.Getenv("GITHUB_TOKEN")
	}
	if token == "" {
		return nil, fmt.Errorf("GitHub token is required. Set GITHUB_TOKEN environment variable")
	}

	return &GitHubReleaseClient{
		Owner:   owner,
		Repo:    repo,
		Token:   token,
		BaseURL: "https://api.github.com",
		httpClient: &http.Client{
			Timeout: 30 * time.Second,
		},
	}, nil
}

func (c *GitHubReleaseClient) doRequest(method, url string, body interface{}) (*http.Response, error) {
	var bodyReader io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		bodyReader = bytes.NewReader(data)
	}

	req, err := http.NewRequest(method, url, bodyReader)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+c.Token)
	req.Header.Set("Accept", "application/vnd.github.v3+json")
	req.Header.Set("Content-Type", "application/json")
	return c.httpClient.Do(req)
}

// ValidatePermissions validates that the GitHub token has the necessary permissions.
func (c *GitHubReleaseClient) ValidatePermissions() bool {
	url := fmt.Sprintf("%s/repos/%s/%s", c.BaseURL, c.Owner, c.Repo)
	resp, err := c.doRequest("GET", url, nil)
	if err != nil {
		fmt.Fprintf(os.Stderr, "❌ Error validating permissions: %v\n", err)
		return false
	}
	defer resp.Body.Close()

	if resp.StatusCode == 200 {
		if os.Getenv("GITHUB_ACTIONS") != "" {
			scopes := resp.Header.Get("X-OAuth-Scopes")
			fmt.Fprintf(os.Stderr, "✅ Token is valid. Available OAuth scopes: %s\n", scopes)
			return true
		}

		var repoData map[string]interface{}
		if err := json.NewDecoder(resp.Body).Decode(&repoData); err == nil {
			if perms, ok := repoData["permissions"].(map[string]interface{}); ok {
				if perms["push"] == true || perms["admin"] == true || perms["maintain"] == true {
					return true
				}
			}
		}

		// Try releases endpoint as fallback
		releasesURL := fmt.Sprintf("%s/repos/%s/%s/releases", c.BaseURL, c.Owner, c.Repo)
		relResp, err := c.doRequest("GET", releasesURL, nil)
		if err == nil {
			defer relResp.Body.Close()
			if relResp.StatusCode == 200 {
				fmt.Fprintln(os.Stderr, "⚠️  Permission validation unclear, but releases endpoint accessible. Proceeding with caution.")
				return true
			}
		}

		fmt.Fprintln(os.Stderr, "❌ Insufficient permissions. Need write access to repository.")
		return false
	}

	fmt.Fprintf(os.Stderr, "❌ Token validation failed with status: %d\n", resp.StatusCode)
	return false
}

// CreateRelease creates a GitHub release.
func (c *GitHubReleaseClient) CreateRelease(data *GitHubReleaseData) (map[string]interface{}, error) {
	url := fmt.Sprintf("%s/repos/%s/%s/releases", c.BaseURL, c.Owner, c.Repo)

	resp, err := c.doRequest("POST", url, data)
	if err != nil {
		return nil, fmt.Errorf("creating release: %w", err)
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)

	if resp.StatusCode == 201 {
		var result map[string]interface{}
		if err := json.Unmarshal(body, &result); err != nil {
			return nil, err
		}
		return result, nil
	}

	if resp.StatusCode == 422 {
		return nil, fmt.Errorf("release already exists for tag %s", data.TagName)
	}

	return nil, fmt.Errorf("unexpected status %d: %s", resp.StatusCode, string(body))
}

// GetReleaseByTag gets a release by its tag name.
func (c *GitHubReleaseClient) GetReleaseByTag(tagName string) (map[string]interface{}, error) {
	url := fmt.Sprintf("%s/repos/%s/%s/releases/tags/%s", c.BaseURL, c.Owner, c.Repo, tagName)

	resp, err := c.doRequest("GET", url, nil)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode == 200 {
		var result map[string]interface{}
		if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
			return nil, err
		}
		return result, nil
	}

	if resp.StatusCode == 404 {
		return nil, nil
	}

	body, _ := io.ReadAll(resp.Body)
	return nil, fmt.Errorf("unexpected status %d: %s", resp.StatusCode, string(body))
}

// DeleteRelease deletes a GitHub release by ID.
func (c *GitHubReleaseClient) DeleteRelease(releaseID int) bool {
	url := fmt.Sprintf("%s/repos/%s/%s/releases/%d", c.BaseURL, c.Owner, c.Repo, releaseID)

	resp, err := c.doRequest("DELETE", url, nil)
	if err != nil {
		fmt.Fprintf(os.Stderr, "❌ Error deleting release %d: %v\n", releaseID, err)
		return false
	}
	defer resp.Body.Close()

	return resp.StatusCode == 204
}

// FindReleasesByTags finds GitHub releases matching specific tags.
func (c *GitHubReleaseClient) FindReleasesByTags(tags []string) (map[string]map[string]interface{}, error) {
	result := make(map[string]map[string]interface{})

	for _, tag := range tags {
		release, err := c.GetReleaseByTag(tag)
		if err != nil {
			fmt.Fprintf(os.Stderr, "⚠️  Error finding release for tag %s: %v\n", tag, err)
			continue
		}
		if release != nil {
			result[tag] = release
		}
	}

	return result, nil
}

// UploadReleaseAsset uploads an asset to a GitHub release.
func (c *GitHubReleaseClient) UploadReleaseAsset(releaseID int, filePath, fileName string) error {
	data, err := os.ReadFile(filePath)
	if err != nil {
		return fmt.Errorf("reading file: %w", err)
	}

	url := fmt.Sprintf("https://uploads.github.com/repos/%s/%s/releases/%d/assets?name=%s",
		c.Owner, c.Repo, releaseID, fileName)

	req, err := http.NewRequest("POST", url, bytes.NewReader(data))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+c.Token)
	req.Header.Set("Content-Type", "application/octet-stream")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("uploading asset: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == 201 {
		return nil
	}

	body, _ := io.ReadAll(resp.Body)
	return fmt.Errorf("unexpected status %d: %s", resp.StatusCode, string(body))
}

// CreateReleaseForApp creates a GitHub release for a specific app.
func CreateReleaseForApp(owner, repo, appName, version, commitSHA string, artifactsDir string, dryRun bool) (map[string]interface{}, error) {
	bazelTarget, err := FindAppBazelTarget(appName)
	if err != nil {
		return nil, err
	}

	metadata, err := GetAppMetadata(bazelTarget)
	if err != nil {
		return nil, err
	}

	tagName := FormatGitTag(metadata.Domain, metadata.Name, version)
	releaseName := fmt.Sprintf("%s %s %s", metadata.Domain, metadata.Name, version)

	// Generate release notes
	releaseNotes, err := GenerateReleaseNotes(
		fmt.Sprintf("%s-%s", metadata.Domain, metadata.Name),
		tagName, "", "markdown",
	)
	if err != nil {
		releaseNotes = fmt.Sprintf("Release %s %s", metadata.Name, version)
	}

	// Check for OpenAPI spec
	if metadata.OpenAPISpecTarget != "" && artifactsDir != "" {
		specFile := fmt.Sprintf("%s/%s_openapi_spec.json", artifactsDir, metadata.Name)
		if _, err := os.Stat(specFile); os.IsNotExist(err) {
			releaseNotes += fmt.Sprintf("\n\n⚠️ **Warning:** OpenAPI spec expected but not found for %s", metadata.Name)
		}
	}

	releaseData := &GitHubReleaseData{
		TagName:    tagName,
		Name:       releaseName,
		Body:       releaseNotes,
		Draft:      false,
		Prerelease: strings.Contains(version, "-"),
	}
	if commitSHA != "" {
		releaseData.TargetCommitish = commitSHA
	}

	if dryRun {
		fmt.Printf("DRY RUN: Would create release:\n")
		fmt.Printf("  Tag: %s\n", tagName)
		fmt.Printf("  Name: %s\n", releaseName)
		fmt.Printf("  Prerelease: %v\n", releaseData.Prerelease)
		return nil, nil
	}

	client, err := NewGitHubReleaseClient(owner, repo, "")
	if err != nil {
		return nil, err
	}

	result, err := client.CreateRelease(releaseData)
	if err != nil {
		return nil, err
	}

	fmt.Printf("✅ Created release: %s\n", releaseName)

	// Upload OpenAPI spec if available
	if metadata.OpenAPISpecTarget != "" && artifactsDir != "" {
		specFile := fmt.Sprintf("%s/%s_openapi_spec.json", artifactsDir, metadata.Name)
		if _, err := os.Stat(specFile); err == nil {
			if releaseIDFloat, ok := result["id"].(float64); ok {
				releaseID := int(releaseIDFloat)
				if err := client.UploadReleaseAsset(releaseID, specFile, fmt.Sprintf("%s-openapi-spec.json", metadata.Name)); err != nil {
					fmt.Fprintf(os.Stderr, "⚠️  Failed to upload OpenAPI spec: %v\n", err)
				} else {
					fmt.Printf("📄 Uploaded OpenAPI spec for %s\n", metadata.Name)
				}
			}
		}
	}

	return result, nil
}
