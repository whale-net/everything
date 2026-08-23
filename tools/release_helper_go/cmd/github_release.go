package cmd

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"
	pb "github.com/whale-net/everything/tools/app_registry/protos"
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

	contentType := "application/octet-stream"
	if strings.HasSuffix(assetName, ".json") {
		contentType = "application/json"
	} else if strings.HasSuffix(assetName, ".txt") || assetName == "SHA256SUMS" {
		contentType = "text/plain"
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
	req.Header.Set("Content-Type", contentType)

	uploadClient := &http.Client{Timeout: 120 * time.Second}
	resp, err := uploadClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode == 201 {
		fmt.Printf("✓ Uploaded asset: %s\n", assetName)
		return nil
	}
	if resp.StatusCode == 422 {
		fmt.Printf("ℹ Asset %s already exists on release %d, skipping\n", assetName, releaseID)
		return nil
	}
	var errBody map[string]interface{}
	json.NewDecoder(resp.Body).Decode(&errBody) //nolint:errcheck
	return fmt.Errorf("upload asset %s: HTTP %d", assetName, resp.StatusCode)
}

// recordPublishedArtifact records a published binary or firmware artifact in
// App Registry, via the same BeginPublish -> RecordArtifact/FailPublish
// sequence releaser.go uses for apps/charts -- required since the AR-5
// cutover, when RecordArtifact stopped accepting a direct create with no
// prior BeginPublish. It intentionally stops there and never calls
// PromotionRegistry.Promote: this CLI runs in release.yml under
// app-registry-builder credentials, and Promote requires the distinct
// app-registry-promoter-<env> role (server/auth/auth.go RequirePromoter).
// Promotion for every artifact kind -- image, chart, and now binary (#780,
// see promote.yml's `kind` input) -- is exclusively human-triggered via the
// (currently disabled, pending live Keycloak promoter clients) promote.yml
// workflow. Recording an artifact here just makes it a promotable candidate.
//
// Every registry error here is best-effort (warn and skip, never fails the
// release) -- the same posture this function always had, just now spanning
// two RPCs instead of one.
func recordPublishedArtifact(ctx context.Context, warn func(string), meta AppMetadata, version, digest, repoOwner, repoName, buildID string) error {
	if defaultEnv("APP_REGISTRY_CICD_OPT_IN") != "true" {
		return nil
	}
	var kind pb.ArtifactKind
	switch meta.AppType {
	case "firmware":
		kind = pb.ArtifactKind_ARTIFACT_KIND_FIRMWARE
	case "cli", "binary":
		kind = pb.ArtifactKind_ARTIFACT_KIND_BINARY
	default:
		return nil
	}

	if repoOwner == "" {
		repoOwner = "whale-net"
	}
	if repoName == "" {
		repoName = "everything"
	}
	repository := fmt.Sprintf("github.com/%s/%s", repoOwner, repoName)
	if digest != "" && !strings.HasPrefix(digest, "sha256:") {
		digest = "sha256:" + digest
	}

	idempotencyKey := fmt.Sprintf("%s-%s-%s", buildID, meta.FullName(), strings.ToLower(kind.String()))
	if buildID == "" {
		idempotencyKey = fmt.Sprintf("%s-%s-%s", meta.FullName(), version, strings.ToLower(kind.String()))
	}

	beginReq := &pb.BeginPublishRequest{
		Kind:           kind,
		OwnerFullName:  meta.FullName(),
		Version:        version,
		BuildId:        buildID,
		Repository:     repository,
		IdempotencyKey: idempotencyKey + "-begin",
	}
	if _, err := defaultArtifactRecorder.BeginPublish(ctx, beginReq); err != nil {
		warn(fmt.Sprintf("App Registry record artifact skipped (BeginPublish failed): %v", err))
		return nil
	}

	req := &pb.RecordArtifactRequest{
		BuildId:        buildID,
		Kind:           kind,
		OwnerFullName:  meta.FullName(),
		Repository:     repository,
		Version:        version,
		Digest:         digest,
		PublishedAt:    time.Now().Unix(),
		IdempotencyKey: idempotencyKey,
	}

	resp, err := defaultArtifactRecorder.RecordArtifact(ctx, req)
	if err != nil {
		failReq := &pb.FailPublishRequest{
			Kind:           kind,
			OwnerFullName:  meta.FullName(),
			Version:        version,
			Reason:         fmt.Sprintf("RecordArtifact failed: %v", err),
			IdempotencyKey: idempotencyKey + "-fail",
		}
		_, _ = defaultArtifactRecorder.FailPublish(ctx, failReq)
		warn(fmt.Sprintf("App Registry record artifact skipped (registry error): %v", err))
		return nil
	}
	if resp != nil && resp.Artifact != nil {
		fmt.Printf("✓ Recorded %s artifact in App Registry: %s (id: %s)\n", meta.AppType, meta.FullName(), resp.Artifact.ArtifactId)
	}
	return nil
}

// resolveTargetNames returns the caller-specified comma/space-separated
// list of names from flagValue, or -- when flagValue is empty -- every key
// in fallback (e.g. every app/chart present in the resolved release
// matrix, keyed exactly as create-combined-github-release-with-notes's own
// chart/app resolvers expect: "domain-app" and the chart's bare Name).
// This is what lets callers that only have a domain shorthand or "all"
// (like a workflow's raw github.event.inputs.helm_charts) skip
// --apps/--charts entirely and let the already-resolved $MATRIX/
// $CHART_MATRIX drive selection, instead of re-resolving that shorthand
// against exact names here -- which is a resolution this command doesn't
// perform and previously failed on ("Could not resolve chart <name>").
func resolveTargetNames(flagValue string, fallback map[string]string) []string {
	list := parseAppList(flagValue)
	if len(list) == 0 && flagValue == "" {
		for k := range fallback {
			list = append(list, k)
		}
	}
	return list
}

func newCreateCombinedGithubReleaseCmd() *cobra.Command {
	var (
		owner           string
		repo            string
		commitSHA       string
		prerelease      bool
		apps            string
		charts          string
		releaseNotesDir string
		openapiSpecsDir string
		assetsDir       string
		helmChartsDir   string
	)

	cmd := &cobra.Command{
		Use:          "create-combined-github-release-with-notes <version>",
		Short:        "Create GitHub releases for top-level entities (charts and standalone apps) using pre-generated release notes",
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

			// Parse per-chart versions from CHART_MATRIX env var. Keyed by
			// domain+"-"+chart (mirroring the app block above), since
			// HelmChartMetadata.Name/FullName below are both domain-
			// prefixed ("helm-<domain>-<chart>") -- keying by the bare
			// chart name here (as before) never matched, causing every
			// chart to fail with "Could not resolve chart <name>".
			chartVersions := map[string]string{}
			if chartMatrixEnv := defaultEnv("CHART_MATRIX"); chartMatrixEnv != "" {
				var cmatrix struct {
					Include []map[string]string `json:"include"`
				}
				if err := json.Unmarshal([]byte(chartMatrixEnv), &cmatrix); err == nil {
					for _, item := range cmatrix.Include {
						chartName := item["chart"]
						chartDomain := item["domain"]
						chartVer := item["version"]
						if chartName != "" && chartDomain != "" {
							full := chartDomain + "-" + chartName
							if chartVer != "" {
								chartVersions[full] = chartVer
							}
						}
					}
				}
			}

			// Resolve app list and chart list.
			appList := resolveTargetNames(apps, appVersions)
			chartList := resolveTargetNames(charts, chartVersions)

			if len(appList) == 0 && len(chartList) == 0 {
				fmt.Fprintf(cmd.ErrOrStderr(), "Error: no apps or charts specified\n")
				return fmt.Errorf("no apps or charts")
			}

			workspaceRoot, err := defaultWorkspaceRoot()
			if err != nil {
				return fmt.Errorf("workspace root: %w", err)
			}
			allApps, err := ListAllApps(defaultBazel, defaultFS, workspaceRoot)
			if err != nil {
				return err
			}
			allCharts, err := ListAllHelmCharts(defaultBazel, defaultFS, workspaceRoot)
			if err != nil {
				return err
			}

			var artifactClient pb.ArtifactRegistryClient
			if defaultEnv("APP_REGISTRY_CICD_OPT_IN") == "true" {
				c, closeConn, err := NewArtifactRegistryClient(cmd.Context())
				if err == nil && c != nil {
					artifactClient = c
					defer closeConn()
				}
			}

			gh := newGHReleaseClient(owner, repo, token)
			var failed []string

			// Track member apps of released charts so we don't mint duplicate releases for them.
			releasedChartMemberApps := make(map[string]bool)
			for _, chartName := range chartList {
				chartName = strings.TrimSpace(chartName)
				if chartName == "" {
					continue
				}
				var matchedChart *HelmChartMetadata
				for i := range allCharts {
					c := &allCharts[i]
					published := strings.TrimPrefix(c.Name, "helm-")
					if c.Name == chartName || published == chartName || c.FullName() == chartName {
						matchedChart = c
						break
					}
				}
				if matchedChart != nil {
					for _, ref := range matchedChart.AppRefs {
						full := strings.ReplaceAll(ref, "/", "-")
						releasedChartMemberApps[full] = true
					}
					for _, bareName := range matchedChart.Apps {
						releasedChartMemberApps[matchedChart.Domain+"-"+bareName] = true
						releasedChartMemberApps[bareName] = true
					}
				}
			}

			// 1. Process Helm charts (top-level releases)
			for _, chartName := range chartList {
				chartName = strings.TrimSpace(chartName)
				if chartName == "" {
					continue
				}

				var matchedChart *HelmChartMetadata
				for i := range allCharts {
					c := &allCharts[i]
					published := strings.TrimPrefix(c.Name, "helm-")
					if c.Name == chartName || published == chartName || c.FullName() == chartName {
						matchedChart = c
						break
					}
				}
				if matchedChart == nil {
					fmt.Fprintf(cmd.ErrOrStderr(), "✗ Could not resolve chart %s\n", chartName)
					failed = append(failed, chartName)
					continue
				}

				chartVer := version
				if v, ok := chartVersions[chartName]; ok && v != "" {
					chartVer = v
				} else if v, ok := chartVersions[matchedChart.Name]; ok && v != "" {
					chartVer = v
				} else if v, ok := chartVersions[matchedChart.FullName()]; ok && v != "" {
					chartVer = v
				}
				if chartVer == "" {
					fmt.Fprintf(cmd.ErrOrStderr(), "✗ No version for chart %s\n", matchedChart.Name)
					failed = append(failed, matchedChart.Name)
					continue
				}

				tagName := fmt.Sprintf("%s.%s", matchedChart.Name, chartVer)
				publishedName := strings.TrimPrefix(matchedChart.Name, "helm-")
				fmt.Printf("Processing chart %s (tag: %s)...\n", matchedChart.Name, tagName)

				// Load pre-generated release notes.
				releaseNotes := ""
				if releaseNotesDir != "" {
					for _, candidate := range []string{matchedChart.Name + ".md", publishedName + ".md", matchedChart.FullName() + ".md"} {
						notesFile := filepath.Join(releaseNotesDir, candidate)
						if data, err := os.ReadFile(notesFile); err == nil {
							releaseNotes = string(data)
							fmt.Printf("✓ Loaded pre-generated release notes for chart %s\n", matchedChart.Name)
							break
						}
					}
				}
				if releaseNotes == "" {
					releaseNotes, err = generateReleaseNotesForChart(
						cmd.Context(),
						*matchedChart,
						chartVer,
						tagName,
						"",
						"",
						"markdown",
						appVersions,
						allApps,
						defaultGit,
						defaultDocker,
						defaultFS,
						artifactClient,
						owner,
						repo,
					)
					if err != nil {
						fmt.Fprintf(cmd.ErrOrStderr(), "✗ Failed release notes for chart %s: %v\n", matchedChart.Name, err)
						failed = append(failed, matchedChart.Name)
						continue
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
						fmt.Fprintf(cmd.ErrOrStderr(), "✗ Failed to create release for chart %s: %v\n", matchedChart.Name, err)
						failed = append(failed, matchedChart.Name)
						continue
					}
				}

				// Upload Helm chart package (.tgz) if present
				if releaseResp != nil && releaseResp.ID != 0 {
					candidateChartDirs := []string{}
					if helmChartsDir != "" {
						candidateChartDirs = append(candidateChartDirs, helmChartsDir)
					}
					if assetsDir != "" {
						candidateChartDirs = append(candidateChartDirs, assetsDir)
					}
					candidateChartDirs = append(candidateChartDirs,
						"/tmp/helm-charts",
						filepath.Join(os.TempDir(), "helm-charts"),
					)

					var uploadedChart bool
					for _, cd := range candidateChartDirs {
						entries, rErr := os.ReadDir(cd)
						if rErr != nil {
							continue
						}
						for _, e := range entries {
							if e.IsDir() || !strings.HasSuffix(e.Name(), ".tgz") {
								continue
							}
							if strings.HasPrefix(e.Name(), matchedChart.Name) || strings.HasPrefix(e.Name(), publishedName) {
								chartFilePath := filepath.Join(cd, e.Name())
								if uploadErr := gh.uploadAsset(releaseResp.ID, chartFilePath, e.Name()); uploadErr != nil {
									fmt.Fprintf(cmd.ErrOrStderr(), "⚠ Failed to upload chart asset %s: %v\n", e.Name(), uploadErr)
								} else {
									uploadedChart = true
								}
								break
							}
						}
						if uploadedChart {
							break
						}
					}
				}

				// Upload OpenAPI specs for contained apps in this chart
				if openapiSpecsDir != "" && releaseResp != nil && releaseResp.ID != 0 {
					for _, appRef := range matchedChart.AppRefs {
						parts := strings.SplitN(appRef, "/", 2)
						if len(parts) != 2 {
							continue
						}
						matchedApp, findErr := findAppByDomainAndName(parts[0], parts[1], allApps)
						if findErr != nil || matchedApp.OpenapiSpecTarget == "" {
							continue
						}
						specFile := filepath.Join(openapiSpecsDir, matchedApp.FullName()+"-openapi.json")
						if _, statErr := os.Stat(specFile); statErr == nil {
							assetName := matchedApp.FullName() + "-openapi.json"
							if uploadErr := gh.uploadAsset(releaseResp.ID, specFile, assetName); uploadErr != nil {
								fmt.Fprintf(cmd.ErrOrStderr(), "⚠ Failed to upload OpenAPI spec for %s: %v\n", matchedApp.FullName(), uploadErr)
							}
						}
					}
				}
			}

			// 2. Process standalone applications (skipping member apps of released charts)
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

				resolved, err := resolveApps([]string{appName}, allApps)
				if err != nil || len(resolved) == 0 {
					fmt.Fprintf(cmd.ErrOrStderr(), "✗ Could not resolve %s: %v\n", appName, err)
					failed = append(failed, appName)
					continue
				}
				meta := resolved[0]
				fullName := meta.FullName()

				// If app is deployed via a chart and that chart was released, skip standalone release.
				if releasedChartMemberApps[fullName] || releasedChartMemberApps[meta.Name] {
					fmt.Printf("ℹ Skipping standalone release for %s (published as part of Helm chart)\n", fullName)
					continue
				}

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
					releaseNotes, err = generateReleaseNotesForApp(
						cmd.Context(),
						meta,
						appVer,
						tagName,
						"",
						"",
						"markdown",
						defaultGit,
						artifactClient,
						owner,
						repo,
					)
					if err != nil {
						fmt.Fprintf(cmd.ErrOrStderr(), "✗ Failed release notes for %s: %v\n", fullName, err)
						failed = append(failed, appName)
						continue
					}
				}

				// Warn if expected OpenAPI spec is missing.
				if openapiSpecsDir != "" && meta.OpenapiSpecTarget != "" {
					specFile := filepath.Join(openapiSpecsDir, fullName+"-openapi.json")
					if _, statErr := os.Stat(specFile); os.IsNotExist(statErr) {
						warning := fmt.Sprintf(
							"\n\n---\n\n⚠️ **Warning: OpenAPI Specification Missing**\n\n"+
								"This app is configured to generate an OpenAPI specification (target: `%s`), "+
								"but the spec file was not found in the build artifacts.\n",
							meta.OpenapiSpecTarget,
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

				// Upload release assets (binaries, checksums) if present.
				var primaryDigest string
				candidateAssetDirs := []string{}
				if assetsDir != "" {
					candidateAssetDirs = append(candidateAssetDirs,
						filepath.Join(assetsDir, fullName),
						filepath.Join(assetsDir, meta.Name),
						assetsDir,
					)
				}
				candidateAssetDirs = append(candidateAssetDirs,
					filepath.Join(os.TempDir(), "release-assets", fullName),
					filepath.Join(os.TempDir(), "release-assets", meta.Name),
				)

				for _, ad := range candidateAssetDirs {
					entries, readErr := os.ReadDir(ad)
					if readErr == nil && len(entries) > 0 {
						for _, e := range entries {
							if e.IsDir() {
								continue
							}
							filePath := filepath.Join(ad, e.Name())
							if releaseResp != nil && releaseResp.ID != 0 {
								if uploadErr := gh.uploadAsset(releaseResp.ID, filePath, e.Name()); uploadErr != nil {
									fmt.Fprintf(cmd.ErrOrStderr(), "⚠ Failed to upload asset %s for %s: %v\n", e.Name(), fullName, uploadErr)
								}
							}
							if primaryDigest == "" && !strings.Contains(e.Name(), "checksum") && !strings.Contains(e.Name(), "SHA256") {
								if hash, _, err := computeFileSHA256(filePath); err == nil {
									primaryDigest = hash
								}
							}
						}
						break
					}
				}

				// Record in App Registry if opt-in is enabled
				if defaultEnv("APP_REGISTRY_CICD_OPT_IN") == "true" {
					warn := func(msg string) { fmt.Fprintf(cmd.ErrOrStderr(), "::warning::%s\n", msg) }
					buildID := defaultEnv("APP_REGISTRY_BUILD_ID")
					if recordErr := recordPublishedArtifact(cmd.Context(), warn, meta, appVer, primaryDigest, owner, repo, buildID); recordErr != nil {
						fmt.Fprintf(cmd.ErrOrStderr(), "⚠ App Registry record failed for %s: %v\n", fullName, recordErr)
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
	cmd.Flags().StringVar(&charts, "charts", "", "Comma-separated list of charts")
	cmd.Flags().StringVar(&releaseNotesDir, "release-notes-dir", "", "Directory containing pre-generated release notes")
	cmd.Flags().StringVar(&openapiSpecsDir, "openapi-specs-dir", "", "Directory containing OpenAPI spec files")
	cmd.Flags().StringVar(&assetsDir, "assets-dir", "", "Directory containing release assets to upload")
	cmd.Flags().StringVar(&helmChartsDir, "helm-charts-dir", "", "Directory containing packaged helm charts (.tgz)")
	return cmd
}
