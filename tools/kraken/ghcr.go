package kraken

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"
)

const defaultTimeout = 30 * time.Second

// GHCRPackageVersion represents a GHCR package version.
type GHCRPackageVersion struct {
	VersionID int      `json:"id"`
	Tags      []string `json:"tags"`
	CreatedAt string   `json:"created_at,omitempty"`
	UpdatedAt string   `json:"updated_at,omitempty"`
}

// HasTag checks if this version has a specific tag.
func (v *GHCRPackageVersion) HasTag(tag string) bool {
	for _, t := range v.Tags {
		if t == tag {
			return true
		}
	}
	return false
}

// IsUntagged checks if this version has no tags.
func (v *GHCRPackageVersion) IsUntagged() bool {
	return len(v.Tags) == 0
}

// String returns a string representation of the version.
func (v *GHCRPackageVersion) String() string {
	tagsStr := "untagged"
	if len(v.Tags) > 0 {
		tagsStr = strings.Join(v.Tags, ", ")
	}
	return fmt.Sprintf("GHCRPackageVersion(id=%d, tags=[%s])", v.VersionID, tagsStr)
}

// GHCRClient is a client for interacting with GitHub Container Registry API.
type GHCRClient struct {
	Owner          string
	Token          string
	BaseURL        string
	ownerTypeCache string
	httpClient     *http.Client
}

// NewGHCRClient creates a new GHCR client.
func NewGHCRClient(owner, token string) (*GHCRClient, error) {
	if token == "" {
		token = os.Getenv("GITHUB_TOKEN")
	}
	if token == "" {
		return nil, fmt.Errorf("GitHub token is required. Set GITHUB_TOKEN environment variable")
	}

	return &GHCRClient{
		Owner:   owner,
		Token:   token,
		BaseURL: "https://api.github.com",
		httpClient: &http.Client{
			Timeout: defaultTimeout,
		},
	}, nil
}

func (c *GHCRClient) doRequest(method, url string) (*http.Response, error) {
	req, err := http.NewRequest(method, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+c.Token)
	req.Header.Set("Accept", "application/vnd.github.v3+json")
	req.Header.Set("Content-Type", "application/json")
	return c.httpClient.Do(req)
}

func (c *GHCRClient) detectOwnerType() string {
	if c.ownerTypeCache != "" {
		return c.ownerTypeCache
	}

	url := fmt.Sprintf("%s/users/%s", c.BaseURL, c.Owner)
	resp, err := c.doRequest("GET", url)
	if err != nil {
		fmt.Fprintf(os.Stderr, "⚠️  Error detecting owner type: %v, defaulting to 'orgs'\n", err)
		c.ownerTypeCache = "orgs"
		return "orgs"
	}
	defer resp.Body.Close()

	if resp.StatusCode == 200 {
		var data map[string]interface{}
		if err := json.NewDecoder(resp.Body).Decode(&data); err == nil {
			if data["type"] == "Organization" {
				c.ownerTypeCache = "orgs"
			} else {
				c.ownerTypeCache = "users"
			}
			return c.ownerTypeCache
		}
	}

	fmt.Fprintf(os.Stderr, "⚠️  Could not determine owner type, defaulting to 'orgs'\n")
	c.ownerTypeCache = "orgs"
	return "orgs"
}

// ListPackageVersions lists all versions of a package.
func (c *GHCRClient) ListPackageVersions(packageName string) ([]GHCRPackageVersion, error) {
	ownerType := c.detectOwnerType()
	url := fmt.Sprintf("%s/%s/%s/packages/container/%s/versions?per_page=100", c.BaseURL, ownerType, c.Owner, packageName)

	var allVersions []GHCRPackageVersion

	for url != "" {
		resp, err := c.doRequest("GET", url)
		if err != nil {
			return nil, fmt.Errorf("error listing package versions: %w", err)
		}
		defer resp.Body.Close()

		if resp.StatusCode == 404 {
			fmt.Fprintf(os.Stderr, "ℹ️  Package %s not found or has no versions\n", packageName)
			return nil, nil
		}

		if resp.StatusCode != 200 {
			body, _ := io.ReadAll(resp.Body)
			return nil, fmt.Errorf("unexpected status %d: %s", resp.StatusCode, string(body))
		}

		var versionsData []struct {
			ID        int    `json:"id"`
			CreatedAt string `json:"created_at"`
			UpdatedAt string `json:"updated_at"`
			Metadata  *struct {
				Container *struct {
					Tags []string `json:"tags"`
				} `json:"container"`
			} `json:"metadata"`
		}

		if err := json.NewDecoder(resp.Body).Decode(&versionsData); err != nil {
			return nil, fmt.Errorf("decoding response: %w", err)
		}

		for _, vd := range versionsData {
			var tags []string
			if vd.Metadata != nil && vd.Metadata.Container != nil {
				tags = vd.Metadata.Container.Tags
			}
			allVersions = append(allVersions, GHCRPackageVersion{
				VersionID: vd.ID,
				Tags:      tags,
				CreatedAt: vd.CreatedAt,
				UpdatedAt: vd.UpdatedAt,
			})
		}

		// Check for pagination
		linkHeader := resp.Header.Get("Link")
		url = ""
		if strings.Contains(linkHeader, `rel="next"`) {
			for _, link := range strings.Split(linkHeader, ",") {
				if strings.Contains(link, `rel="next"`) {
					start := strings.Index(link, "<")
					end := strings.Index(link, ">")
					if start >= 0 && end > start {
						url = link[start+1 : end]
					}
					break
				}
			}
		}
	}

	return allVersions, nil
}

// DeletePackageVersion deletes a specific package version.
func (c *GHCRClient) DeletePackageVersion(packageName string, versionID int) (bool, error) {
	ownerType := c.detectOwnerType()
	url := fmt.Sprintf("%s/%s/%s/packages/container/%s/versions/%d", c.BaseURL, ownerType, c.Owner, packageName, versionID)

	resp, err := c.doRequest("DELETE", url)
	if err != nil {
		return false, fmt.Errorf("error deleting package version %d: %w", versionID, err)
	}
	defer resp.Body.Close()

	switch resp.StatusCode {
	case 204:
		return true, nil
	case 404:
		fmt.Fprintf(os.Stderr, "⚠️  Package version %d not found\n", versionID)
		return false, nil
	default:
		body, _ := io.ReadAll(resp.Body)
		return false, fmt.Errorf("unexpected status %d: %s", resp.StatusCode, string(body))
	}
}

// FindVersionsByTags finds package versions matching specific tags.
func (c *GHCRClient) FindVersionsByTags(packageName string, tags []string) ([]GHCRPackageVersion, error) {
	allVersions, err := c.ListPackageVersions(packageName)
	if err != nil {
		return nil, err
	}

	var matching []GHCRPackageVersion
	for _, version := range allVersions {
		for _, tag := range tags {
			if version.HasTag(tag) {
				matching = append(matching, version)
				break
			}
		}
	}

	return matching, nil
}

// ValidatePermissions validates that the GitHub token has the necessary permissions.
func (c *GHCRClient) ValidatePermissions() bool {
	ownerType := c.detectOwnerType()
	url := fmt.Sprintf("%s/%s/%s", c.BaseURL, ownerType, c.Owner)

	resp, err := c.doRequest("GET", url)
	if err != nil {
		fmt.Fprintf(os.Stderr, "❌ Error validating permissions: %v\n", err)
		return false
	}
	defer resp.Body.Close()

	if resp.StatusCode == 200 {
		scopes := resp.Header.Get("X-OAuth-Scopes")
		hasWrite := strings.Contains(scopes, "write:packages")
		hasRead := strings.Contains(scopes, "read:packages") || hasWrite
		if hasWrite && hasRead {
			return true
		}
		fmt.Fprintf(os.Stderr, "⚠️  Missing required scopes. Current: %s\n", scopes)
		fmt.Fprintln(os.Stderr, "   Required: write:packages, read:packages")
		return false
	}

	if resp.StatusCode == 403 {
		fmt.Fprintln(os.Stderr, "❌ Access forbidden. Check token permissions.")
		return false
	}

	fmt.Fprintf(os.Stderr, "⚠️  Could not validate permissions: %d\n", resp.StatusCode)
	return false
}

// GetPackageInfo gets package metadata from GHCR.
func (c *GHCRClient) GetPackageInfo(packageName string) (map[string]interface{}, error) {
	ownerType := c.detectOwnerType()
	url := fmt.Sprintf("%s/%s/%s/packages/container/%s", c.BaseURL, ownerType, c.Owner, packageName)

	resp, err := c.doRequest("GET", url)
	if err != nil {
		fmt.Fprintf(os.Stderr, "⚠️  Error getting package info: %v\n", err)
		return nil, nil
	}
	defer resp.Body.Close()

	if resp.StatusCode == 200 {
		var data map[string]interface{}
		if err := json.NewDecoder(resp.Body).Decode(&data); err != nil {
			return nil, err
		}
		return data, nil
	}

	if resp.StatusCode == 404 {
		return nil, nil
	}

	body, _ := io.ReadAll(resp.Body)
	return nil, fmt.Errorf("unexpected status %d: %s", resp.StatusCode, string(body))
}
