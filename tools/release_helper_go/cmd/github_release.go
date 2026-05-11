package cmd

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"
)

type ghReleasePayload struct {
	TagName         string `json:"tag_name"`
	Name            string `json:"name"`
	Body            string `json:"body"`
	Draft           bool   `json:"draft"`
	Prerelease      bool   `json:"prerelease"`
	TargetCommitish string `json:"target_commitish,omitempty"`
}

type ghReleaseResponse struct {
	ID      int    `json:"id"`
	HTMLURL string `json:"html_url"`
	TagName string `json:"tag_name"`
	Message string `json:"message"` // populated on error responses
}

type ghReleaseClient struct {
	owner, repo, token string
	http               *http.Client
}

func newGHReleaseClient(owner, repo, token string) *ghReleaseClient {
	return &ghReleaseClient{
		owner: owner,
		repo:  repo,
		token: token,
		http:  &http.Client{Timeout: 30 * time.Second},
	}
}

func (c *ghReleaseClient) do(method, url string, body io.Reader, contentType string) (*http.Response, error) {
	req, err := http.NewRequest(method, url, body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("Accept", "application/vnd.github.v3+json")
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	return c.http.Do(req)
}

// getByTag fetches a release by tag. Returns nil if not found (404).
func (c *ghReleaseClient) getByTag(tagName string) (*ghReleaseResponse, error) {
	url := fmt.Sprintf("https://api.github.com/repos/%s/%s/releases/tags/%s", c.owner, c.repo, tagName)
	resp, err := c.do("GET", url, nil, "")
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode == 404 {
		return nil, nil
	}
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("GET release %s: HTTP %d", tagName, resp.StatusCode)
	}
	var r ghReleaseResponse
	if err := json.NewDecoder(resp.Body).Decode(&r); err != nil {
		return nil, err
	}
	return &r, nil
}

// create creates a new GitHub release.
func (c *ghReleaseClient) create(p ghReleasePayload) (*ghReleaseResponse, error) {
	url := fmt.Sprintf("https://api.github.com/repos/%s/%s/releases", c.owner, c.repo)
	body, _ := json.Marshal(p)
	resp, err := c.do("POST", url, bytes.NewReader(body), "application/json")
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	var r ghReleaseResponse
	json.NewDecoder(resp.Body).Decode(&r) //nolint:errcheck

	if resp.StatusCode == 201 {
		fmt.Printf("✓ Created release: %s\n", r.HTMLURL)
		return &r, nil
	}
	if resp.StatusCode == 422 && strings.Contains(strings.ToLower(r.Message), "already_exists") {
		fmt.Printf("ℹ Release %s already exists, skipping\n", p.TagName)
		return &r, nil
	}
	return nil, fmt.Errorf("create release %s: HTTP %d: %s", p.TagName, resp.StatusCode, r.Message)
}

// uploadAsset uploads a file to an existing GitHub release.
func (c *ghReleaseClient) uploadAsset(releaseID int, filePath, assetName string) error {
	data, err := os.ReadFile(filePath)
	if err != nil {
		return fmt.Errorf("read asset %s: %w", filePath, err)
	}

	url := fmt.Sprintf(
		"https://uploads.github.com/repos/%s/%s/releases/%d/assets?name=%s",
		c.owner, c.repo, releaseID, assetName,
	)
	req, err := http.NewRequest("POST", url, bytes.NewReader(data))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("Accept", "application/vnd.github.v3+json")
	req.Header.Set("Content-Type", "application/json")

	uploadClient := &http.Client{Timeout: 60 * time.Second}
	resp, err := uploadClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != 201 {
		var errBody map[string]interface{}
		json.NewDecoder(resp.Body).Decode(&errBody) //nolint:errcheck
		return fmt.Errorf("upload asset %s: HTTP %d", assetName, resp.StatusCode)
	}
	fmt.Printf("✓ Uploaded asset: %s\n", assetName)
	return nil
}

func newCreateCombinedGithubReleaseCmd() *cobra.Command {
	var (
		owner           string
		repo            string
		commitSHA       string
		prerelease      bool
		apps            string
		releaseNotesDir string
		openapiSpecsDir string
	)

	cmd := &cobra.Command{
		Use:          "create-combined-github-release-with-notes <version>",
		Short:        "Create GitHub releases for multiple apps using pre-generated release notes",
		Args:         cobra.ExactArgs(1),
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			version := args[0]

			token := defaultEnv("GITHUB_TOKEN")
			if token == "" {
				fmt.Fprintf(cmd.ErrOrStderr(), "Error: GITHUB_TOKEN environment variable not set\n")
				return fmt.Errorf("missing GITHUB_TOKEN")
			}

			// Parse per-app versions and domains from MATRIX env var.
			appVersions := map[string]string{}
			appDomains := map[string]string{}
			if matrixEnv := defaultEnv("MATRIX"); matrixEnv != "" {
				var matrix struct {
					Include []map[string]string `json:"include"`
				}
				if err := json.Unmarshal([]byte(matrixEnv), &matrix); err == nil {
					for _, item := range matrix.Include {
						appName := item["app"]
						appDomain := item["domain"]
						appVer := item["version"]
						if appName != "" && appDomain != "" {
							full := appDomain + "-" + appName
							if appVer != "" {
								appVersions[full] = appVer
							}
							appDomains[full] = appDomain
						}
					}
				}
			}

			// Resolve app list.
			appList := parseAppList(apps)
			if len(appList) == 0 {
				// Collect from matrix if available.
				for k := range appVersions {
					appList = append(appList, k)
				}
			}
			if len(appList) == 0 {
				fmt.Fprintf(cmd.ErrOrStderr(), "Error: no apps specified\n")
				return fmt.Errorf("no apps")
			}

			// Load all apps for metadata resolution (domain lookup).
			workspaceRoot, err := defaultWorkspaceRoot()
			if err != nil {
				return fmt.Errorf("workspace root: %w", err)
			}
			allApps, err := ListAllApps(defaultBazel, defaultFS, workspaceRoot)
			if err != nil {
				return err
			}
			appsByFull := map[string]AppMetadata{}
			for _, a := range allApps {
				appsByFull[a.FullName()] = a
			}

			gh := newGHReleaseClient(owner, repo, token)

			var failed []string
			for _, appName := range appList {
				appName = strings.TrimSpace(appName)
				if appName == "" {
					continue
				}

				appVer := version
				if v, ok := appVersions[appName]; ok {
					appVer = v
				}
				if appVer == "" {
					fmt.Fprintf(cmd.ErrOrStderr(), "✗ No version for %s\n", appName)
					failed = append(failed, appName)
					continue
				}

				// Resolve canonical metadata.
				resolved, err := resolveApps([]string{appName}, allApps)
				if err != nil || len(resolved) == 0 {
					fmt.Fprintf(cmd.ErrOrStderr(), "✗ Could not resolve %s: %v\n", appName, err)
					failed = append(failed, appName)
					continue
				}
				meta := resolved[0]
				fullName := meta.FullName()
				tagName := fmt.Sprintf("%s.%s", fullName, appVer)

				fmt.Printf("Processing %s (tag: %s)...\n", fullName, tagName)

				// Load pre-generated release notes.
				releaseNotes := ""
				if releaseNotesDir != "" {
					notesFile := filepath.Join(releaseNotesDir, fullName+".md")
					if data, err := os.ReadFile(notesFile); err == nil {
						releaseNotes = string(data)
						fmt.Printf("✓ Loaded pre-generated release notes for %s\n", fullName)
					}
				}
				if releaseNotes == "" {
					// Fall back to generating notes inline.
					releaseNotes, err = generateReleaseNotes(meta, tagName, "", "markdown", defaultGit)
					if err != nil {
						fmt.Fprintf(cmd.ErrOrStderr(), "✗ Failed release notes for %s: %v\n", fullName, err)
						failed = append(failed, appName)
						continue
					}
				}

				// Warn if expected OpenAPI spec is missing.
				if openapiSpecsDir != "" && meta.OpenAPISpecTarget != "" {
					specFile := filepath.Join(openapiSpecsDir, fullName+"-openapi.json")
					if _, statErr := os.Stat(specFile); os.IsNotExist(statErr) {
						warning := fmt.Sprintf(
							"\n\n---\n\n⚠️ **Warning: OpenAPI Specification Missing**\n\n"+
								"This app is configured to generate an OpenAPI specification (target: `%s`), "+
								"but the spec file was not found in the build artifacts.\n",
							meta.OpenAPISpecTarget,
						)
						releaseNotes += warning
					}
				}

				// Check for existing release.
				existing, err := gh.getByTag(tagName)
				if err != nil {
					fmt.Fprintf(cmd.ErrOrStderr(), "⚠ Could not check existing release for %s: %v\n", tagName, err)
				}
				var releaseResp *ghReleaseResponse
				if existing != nil {
					fmt.Printf("ℹ Release %s already exists: %s\n", tagName, existing.HTMLURL)
					releaseResp = existing
				} else {
					payload := ghReleasePayload{
						TagName:    tagName,
						Name:       tagName,
						Body:       releaseNotes,
						Prerelease: prerelease,
					}
					if commitSHA != "" {
						payload.TargetCommitish = commitSHA
					}
					releaseResp, err = gh.create(payload)
					if err != nil {
						fmt.Fprintf(cmd.ErrOrStderr(), "✗ Failed to create release for %s: %v\n", fullName, err)
						failed = append(failed, appName)
						continue
					}
				}

				// Upload OpenAPI spec if present.
				if openapiSpecsDir != "" && releaseResp != nil && releaseResp.ID != 0 {
					specFile := filepath.Join(openapiSpecsDir, fullName+"-openapi.json")
					if _, statErr := os.Stat(specFile); statErr == nil {
						assetName := fullName + "-openapi.json"
						if uploadErr := gh.uploadAsset(releaseResp.ID, specFile, assetName); uploadErr != nil {
							fmt.Fprintf(cmd.ErrOrStderr(), "⚠ Failed to upload OpenAPI spec for %s: %v\n", fullName, uploadErr)
						}
					}
				}
			}

			if len(failed) > 0 {
				fmt.Fprintf(cmd.ErrOrStderr(), "✗ Failed releases: %s\n", strings.Join(failed, ", "))
				return fmt.Errorf("some releases failed")
			}
			fmt.Printf("✓ All releases created successfully\n")
			return nil
		},
	}

	cmd.Flags().StringVar(&owner, "owner", "", "Repository owner")
	cmd.Flags().StringVar(&repo, "repo", "", "Repository name")
	cmd.Flags().StringVar(&commitSHA, "commit", "", "Commit SHA to target")
	cmd.Flags().BoolVar(&prerelease, "prerelease", false, "Mark as prerelease")
	cmd.Flags().StringVar(&apps, "apps", "", "Comma-separated list of apps")
	cmd.Flags().StringVar(&releaseNotesDir, "release-notes-dir", "", "Directory containing pre-generated release notes")
	cmd.Flags().StringVar(&openapiSpecsDir, "openapi-specs-dir", "", "Directory containing OpenAPI spec files")
	return cmd
}
