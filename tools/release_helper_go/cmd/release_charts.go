package cmd

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/spf13/cobra"
	pb "github.com/whale-net/everything/tools/app_registry/protos"
	"github.com/whale-net/everything/tools/helm"
)

// chartArchiveContentDigest computes a digest over the canonical, decoded
// contents of a packaged chart .tgz archive (sorted file path + bytes),
// rather than the raw compressed tarball bytes. `helm package` output is not
// byte-reproducible across separate invocations of identical chart source --
// gzip header timestamps and tar entry ordering/mtimes are non-deterministic
// even with SOURCE_DATE_EPOCH set -- so comparing raw tarball digests
// misclassifies every retry of truly-unchanged content as a real content
// change, minting an orphaned ChartMuseum version each time. See issue #814.
// Returns an error if data is not a valid gzip+tar archive.
func chartArchiveContentDigest(data []byte) (string, error) {
	gz, err := gzip.NewReader(bytes.NewReader(data))
	if err != nil {
		return "", fmt.Errorf("gzip: %w", err)
	}
	defer gz.Close()

	type fileEntry struct {
		name string
		data []byte
	}
	var entries []fileEntry
	tr := tar.NewReader(gz)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return "", fmt.Errorf("tar: %w", err)
		}
		if hdr.Typeflag != tar.TypeReg {
			continue
		}
		content, err := io.ReadAll(tr)
		if err != nil {
			return "", fmt.Errorf("tar read %s: %w", hdr.Name, err)
		}
		entries = append(entries, fileEntry{name: hdr.Name, data: content})
	}

	sort.Slice(entries, func(i, j int) bool { return entries[i].name < entries[j].name })

	h := sha256.New()
	for _, e := range entries {
		fmt.Fprintf(h, "%s\x00%d\x00", e.name, len(e.data))
		h.Write(e.data)
	}
	return fmt.Sprintf("sha256:%x", h.Sum(nil)), nil
}

// chartArchivesContentEqual reports whether two packaged chart archives are
// semantically identical -- same file paths and file contents once decoded
// -- rather than comparing raw tarball bytes, which differ across
// `helm package` invocations even for unchanged chart source (see
// chartArchiveContentDigest). Falls back to raw-byte equality if either
// input can't be parsed as gzip+tar, preserving prior behavior for
// non-chart-archive inputs (e.g. test fixtures).
func chartArchivesContentEqual(a, b []byte) bool {
	digestA, errA := chartArchiveContentDigest(a)
	digestB, errB := chartArchiveContentDigest(b)
	if errA == nil && errB == nil {
		return digestA == digestB
	}
	return bytes.Equal(a, b)
}

// HelmPackager abstracts packaging a Helm chart for testing.
type HelmPackager interface {
	Package(chartDir, chartName, version, outDir string, appVersions map[string]string) (string, error)
}

type defaultHelmPackager struct{}

func (defaultHelmPackager) Package(chartDir, chartName, version, outDir string, appVersions map[string]string) (string, error) {
	return packageChartWithVersion(chartDir, chartName, version, outDir, appVersions)
}

var defaultPackager HelmPackager = defaultHelmPackager{}

// ChartUploader abstracts ChartMuseum interactions for testing.
type ChartUploader interface {
	UploadChart(ctx context.Context, repoURL, username, password, chartPath string) error
	FetchChart(ctx context.Context, repoURL, username, password, publishedName, version string) ([]byte, error)
}

type defaultChartUploader struct {
	client *http.Client
}

func (u *defaultChartUploader) getClient() *http.Client {
	if u.client != nil {
		return u.client
	}
	return http.DefaultClient
}

func (u *defaultChartUploader) UploadChart(ctx context.Context, repoURL, username, password, chartPath string) error {
	data, err := os.ReadFile(chartPath)
	if err != nil {
		return fmt.Errorf("read chart file %s: %w", chartPath, err)
	}

	url := strings.TrimRight(repoURL, "/") + "/api/charts"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(data))
	if err != nil {
		return fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/gzip")
	if username != "" || password != "" {
		req.SetBasicAuth(username, password)
	}

	resp, err := u.getClient().Do(req)
	if err != nil {
		return fmt.Errorf("upload to %s: %w", url, err)
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("ChartMuseum upload failed with status %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	return nil
}

func (u *defaultChartUploader) FetchChart(ctx context.Context, repoURL, username, password, publishedName, version string) ([]byte, error) {
	// Try downloading the chart tarball directly (e.g. /charts/app-registry-app-registry-v0.0.31.tgz)
	tarURL := fmt.Sprintf("%s/charts/%s-%s.tgz", strings.TrimRight(repoURL, "/"), publishedName, version)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, tarURL, nil)
	if err == nil {
		if username != "" || password != "" {
			req.SetBasicAuth(username, password)
		}
		resp, err := u.getClient().Do(req)
		if err == nil {
			defer resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return io.ReadAll(resp.Body)
			}
		}
	}

	// Fallback to /api/charts/:name/:version endpoint
	apiURL := fmt.Sprintf("%s/api/charts/%s/%s", strings.TrimRight(repoURL, "/"), publishedName, version)
	req, err = http.NewRequestWithContext(ctx, http.MethodGet, apiURL, nil)
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}
	if username != "" || password != "" {
		req.SetBasicAuth(username, password)
	}
	resp, err := u.getClient().Do(req)
	if err != nil {
		return nil, fmt.Errorf("fetch chart from %s: %w", apiURL, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("HTTP status %d", resp.StatusCode)
	}
	return io.ReadAll(resp.Body)
}

var defaultUploader ChartUploader = &defaultChartUploader{}

// ReleaseChartsParams encapsulates all dependencies and configuration for release-charts.
type ReleaseChartsParams struct {
	Ctx                  context.Context
	Charts               string
	Version              string
	IncrementMajor       bool
	IncrementMinor       bool
	IncrementPatch       bool
	BuildID              string
	ChartRepoURL         string
	IdempotencyKeyPrefix string
	DryRun               bool
	IncludeDemo          bool
	GitSHA               string
	SkipRegistry         bool
	CreateGitTag         bool
	ChartRepoUser        string
	ChartRepoPass        string
	// AppVersions carries the release batch's already-resolved plan
	// versions (keyed by AppMetadata.FullName(), "<domain>-<name>") for
	// composed member apps -- see resolveChartAppVersions's doc comment
	// (issue #901). Absent/empty is a no-op: every composed app resolves
	// via the existing independent-query path unchanged.
	AppVersions map[string]string

	Bazel          BazelRunner
	Git            GitRunner
	Docker         DockerRunner
	FS             FileSystem
	Packager       HelmPackager
	Uploader       ChartUploader
	Hermeticity    ChartHermeticityChecker
	WorkspaceRoot  string
	ArtifactClient pb.ArtifactRegistryClient
}

// SingleChartResult contains the outcome of a single chart release.
type SingleChartResult struct {
	ChartName        string               `json:"chart_name"`
	PublishedName    string               `json:"published_name"`
	Domain           string               `json:"domain"`
	Version          string               `json:"version"`
	EffectiveVersion string               `json:"effective_version"`
	EffectiveTag     string               `json:"effective_tag"`
	PreviousTag      string               `json:"previous_tag,omitempty"`
	Digest           string               `json:"digest,omitempty"`
	DigestUnchanged  bool                 `json:"digest_unchanged"`
	Published        bool                 `json:"published"`
	ChartPath        string               `json:"chart_path,omitempty"`
	ContainedImages  []*pb.ContainedImage `json:"contained_images,omitempty"`
}

// ReleaseChartsResult contains the overall outcome of release-charts execution.
type ReleaseChartsResult struct {
	Charts []*SingleChartResult `json:"charts"`
}

func newReleaseChartsCmd() *cobra.Command {
	var (
		charts               string
		version              string
		incrementMajor       bool
		incrementMinor       bool
		incrementPatch       bool
		buildID              string
		chartRepoURL         string
		idempotencyKeyPrefix string
		dryRun               bool
		includeDemo          bool
		gitSHA               string
		skipRegistry         bool
		createGitTag         bool
		chartRepoUser        string
		chartRepoPass        string
		appVersionsJSON      string
	)

	cmd := &cobra.Command{
		Use:          "release-charts",
		Short:        "Execute Helm chart composition, hermeticity check, packaging, ChartMuseum upload, and artifact recording",
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			var appVersions map[string]string
			if strings.TrimSpace(appVersionsJSON) != "" {
				if err := json.Unmarshal([]byte(appVersionsJSON), &appVersions); err != nil {
					return fmt.Errorf("parse --app-versions: %w", err)
				}
			}

			workspaceRoot, err := defaultWorkspaceRoot()
			if err != nil {
				return fmt.Errorf("workspace root: %w", err)
			}

			repoURL := chartRepoURL
			if repoURL == "" {
				repoURL = defaultEnv("CHART_REPO_URL")
			}
			if repoURL == "" {
				repoURL = "https://charts.whalenet.dev"
			}

			user := chartRepoUser
			if user == "" {
				user = defaultEnv("CHART_REPO_USER")
			}
			pass := chartRepoPass
			if pass == "" {
				pass = defaultEnv("CHART_REPO_PASS")
			}

			res, err := ExecuteReleaseCharts(ReleaseChartsParams{
				Ctx:                  cmd.Context(),
				Charts:               charts,
				Version:              version,
				IncrementMajor:       incrementMajor,
				IncrementMinor:       incrementMinor,
				IncrementPatch:       incrementPatch,
				BuildID:              buildID,
				ChartRepoURL:         repoURL,
				IdempotencyKeyPrefix: idempotencyKeyPrefix,
				DryRun:               dryRun,
				IncludeDemo:          includeDemo,
				GitSHA:               gitSHA,
				SkipRegistry:         skipRegistry,
				CreateGitTag:         createGitTag,
				ChartRepoUser:        user,
				ChartRepoPass:        pass,
				AppVersions:          appVersions,
				Bazel:                defaultBazel,
				Git:                  defaultGit,
				Docker:               defaultDocker,
				FS:                   defaultFS,
				Packager:             defaultPackager,
				Uploader:             defaultUploader,
				Hermeticity:          defaultHermeticityChecker,
				WorkspaceRoot:        workspaceRoot,
			})
			if err != nil {
				return err
			}

			if dryRun {
				return nil
			}

			for _, c := range res.Charts {
				fmt.Fprintf(cmd.OutOrStdout(), "Successfully processed release-charts for %s (version: %s, digest: %s, published: %t)\n",
					c.ChartName, c.EffectiveVersion, c.Digest, c.Published)
			}
			return nil
		},
	}

	cmd.Flags().StringVar(&charts, "charts", "", "Comma- or space-separated list of charts, domain names, or 'all'")
	cmd.Flags().StringVar(&version, "version", "", "Explicit release version for charts")
	cmd.Flags().BoolVar(&incrementMajor, "increment-major", false, "Auto-increment major version")
	cmd.Flags().BoolVar(&incrementMinor, "increment-minor", false, "Auto-increment minor version")
	cmd.Flags().BoolVar(&incrementPatch, "increment-patch", false, "Auto-increment patch version")
	cmd.Flags().StringVar(&buildID, "build-id", "", "App Registry Build ID")
	cmd.Flags().StringVar(&chartRepoURL, "chart-repo-url", "", "ChartMuseum repository URL (default: https://charts.whalenet.dev)")
	cmd.Flags().StringVar(&idempotencyKeyPrefix, "idempotency-key-prefix", "", "Prefix for idempotency keys")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "Print release plan without executing upload or mutating registry")
	cmd.Flags().BoolVar(&includeDemo, "include-demo", false, "Include demo domain charts when using 'all'")
	cmd.Flags().StringVar(&gitSHA, "git-sha", "", "Git commit SHA")
	cmd.Flags().BoolVar(&skipRegistry, "skip-registry", false, "Skip App Registry API interactions")
	cmd.Flags().BoolVar(&createGitTag, "create-git-tag", false, "Create git tag upon successful publication")
	cmd.Flags().StringVar(&chartRepoUser, "chart-repo-user", "", "ChartMuseum username")
	cmd.Flags().StringVar(&chartRepoPass, "chart-repo-pass", "", "ChartMuseum password")
	cmd.Flags().StringVar(&appVersionsJSON, "app-versions", "", `JSON object of the release batch's resolved plan versions, keyed by app FullName ("<domain>-<name>"), e.g. {"manmanv2-control-api":"v1.2.3"}. A composed member app present here uses this pinned version instead of an independent latest_published query (issue #901). Absent/empty is a no-op.`)

	_ = cmd.MarkFlagRequired("charts")
	_ = cmd.MarkFlagRequired("chart-repo-url")

	return cmd
}

// ExecuteReleaseCharts executes the full Helm chart release workflow.
func ExecuteReleaseCharts(p ReleaseChartsParams) (*ReleaseChartsResult, error) {
	ctx := p.Ctx
	if ctx == nil {
		ctx = context.Background()
	}

	workspaceRoot := p.WorkspaceRoot
	if workspaceRoot == "" {
		var err error
		workspaceRoot, err = defaultWorkspaceRoot()
		if err != nil {
			return nil, fmt.Errorf("workspace root: %w", err)
		}
	}

	bazel := p.Bazel
	if bazel == nil {
		bazel = defaultBazel
	}
	git := p.Git
	if git == nil {
		git = defaultGit
	}
	docker := p.Docker
	if docker == nil {
		docker = defaultDocker
	}
	fs := p.FS
	if fs == nil {
		fs = defaultFS
	}
	packager := p.Packager
	if packager == nil {
		packager = defaultPackager
	}
	uploader := p.Uploader
	if uploader == nil {
		uploader = defaultUploader
	}
	hermeticity := p.Hermeticity
	if hermeticity == nil {
		hermeticity = defaultHermeticityChecker
	}

	repoURL := p.ChartRepoURL
	if repoURL == "" {
		repoURL = envOrDefault("CHART_REPO_URL", "https://charts.whalenet.dev")
	}
	repoUser := p.ChartRepoUser
	if repoUser == "" {
		repoUser = defaultEnv("CHART_REPO_USER")
	}
	repoPass := p.ChartRepoPass
	if repoPass == "" {
		repoPass = defaultEnv("CHART_REPO_PASS")
	}

	allCharts, err := ListAllHelmCharts(bazel, fs, workspaceRoot)
	if err != nil {
		return nil, fmt.Errorf("list all helm charts: %w", err)
	}

	allApps, err := ListAllApps(bazel, fs, workspaceRoot)
	if err != nil {
		return nil, fmt.Errorf("list all apps: %w", err)
	}

	selectedCharts := filterHelmCharts(p.Charts, allCharts, p.IncludeDemo)
	if len(selectedCharts) == 0 {
		if p.Charts != "" && strings.ToLower(strings.TrimSpace(p.Charts)) != "all" {
			return nil, fmt.Errorf("no helm charts matched %q", p.Charts)
		}
		return &ReleaseChartsResult{Charts: []*SingleChartResult{}}, nil
	}

	// Determine idempotency prefix
	runID := defaultEnv("GITHUB_RUN_ID")
	if runID == "" {
		runID = "local"
	}
	attempt := defaultEnv("GITHUB_RUN_ATTEMPT")
	if attempt == "" {
		attempt = "1"
	}
	idempotencyPrefix := p.IdempotencyKeyPrefix
	if idempotencyPrefix == "" {
		idempotencyPrefix = fmt.Sprintf("%s-%s", runID, attempt)
	}

	// App Registry client setup
	var artifactClient pb.ArtifactRegistryClient
	if p.ArtifactClient != nil {
		artifactClient = p.ArtifactClient
	} else if !p.SkipRegistry {
		c, closeFn, err := NewArtifactRegistryClient(ctx)
		if err == nil {
			artifactClient = c
			if closeFn != nil {
				defer closeFn() //nolint:errcheck
			}
		}
	}

	sha := p.GitSHA
	if sha == "" {
		sha = defaultEnv("GITHUB_SHA")
	}
	if sha == "" && git != nil {
		if out, err := git.Run("rev-parse", "HEAD"); err == nil {
			sha = strings.TrimSpace(out)
		}
	}

	result := &ReleaseChartsResult{
		Charts: make([]*SingleChartResult, 0, len(selectedCharts)),
	}

	for _, chart := range selectedCharts {
		publishedName := strings.TrimPrefix(chart.Name, "helm-")

		ver := p.Version
		if ver == "" {
			bumpType := "patch"
			switch {
			case p.IncrementMajor:
				bumpType = "major"
			case p.IncrementMinor:
				bumpType = "minor"
			}

			// versionClient is resolved independently of the best-effort
			// artifactClient above: that dial silently leaves artifactClient
			// nil on failure (registry integration elsewhere in this
			// function is soft/continue-on-error), but version resolution
			// for a domain opted into App Registry must not silently fall
			// back to tag-scanning just because the dial failed -- see
			// resolveVersion's doc comment and issue #829. DryRun is excluded
			// here (unlike the read-only hermeticity check below): unlike
			// CheckChartHermeticity, AllocateVersion has a real side effect
			// -- it reserves the version by inserting an "allocated" artifact
			// row -- so a dry run must never call it.
			var versionClient pb.ArtifactRegistryClient
			if !p.DryRun && defaultEnv("APP_REGISTRY_CICD_OPT_IN") == "true" && !p.SkipRegistry {
				if artifactClient != nil {
					versionClient = artifactClient
				} else {
					c, closeFn, derr := dialVersioningClient(ctx, nil)
					if derr != nil {
						return nil, derr
					}
					defer closeFn() //nolint:errcheck
					versionClient = c
				}
			}
			var err error
			ver, _, err = resolveVersion(ctx, versionClient, pb.ArtifactKind_ARTIFACT_KIND_CHART, publishedName, bumpType,
				fmt.Sprintf("%s-%s-allocate", idempotencyPrefix, publishedName),
				func() (string, error) { return autoIncrementHelmVersion(chart.Name, bumpType, git) },
			)
			if err != nil {
				return nil, fmt.Errorf("auto-version for chart %s: %w", chart.Name, err)
			}
		}
		tagName := fmt.Sprintf("%s.%s", chart.Name, ver)

		// 1. Resolve member app versions
		appVersions, err := resolveChartAppVersions(ctx, chart, allApps, git, artifactClient, p.AppVersions)
		if err != nil {
			return nil, fmt.Errorf("resolve app versions for chart %s: %w", chart.Name, err)
		}

		// 2. Validate contained image pins using CheckChartHermeticity (AR-7f)
		if defaultEnv("APP_REGISTRY_CICD_OPT_IN") == "true" && !p.SkipRegistry {
			pins := make([]ChartPin, 0, len(appVersions))
			for appFullName, v := range appVersions {
				pins = append(pins, ChartPin{AppFullName: appFullName, Version: v})
			}
			sort.Slice(pins, func(i, j int) bool { return pins[i].AppFullName < pins[j].AppFullName })

			enforced, violations, err := hermeticity.Check(ctx, chart.Domain, pins)
			if err != nil {
				fmt.Printf("::warning::App Registry chart-hermeticity check skipped (registry error): %v\n", err)
			} else if enforced && len(violations) > 0 {
				names := make([]string, 0, len(violations))
				for _, v := range violations {
					names = append(names, fmt.Sprintf("%s@%s (%s)", v.AppFullName, v.Version, v.Reason))
				}
				return nil, fmt.Errorf("chart pins %d unpublished app(s) in domain %q: %s",
					len(violations), chart.Domain, strings.Join(names, ", "))
			}
		}

		if p.DryRun {
			fmt.Println(strings.Repeat("=", 80))
			fmt.Printf("DRY RUN: Helm chart release plan for %s\n", chart.Name)
			fmt.Println(strings.Repeat("=", 80))
			fmt.Printf("Domain:      %s\n", chart.Domain)
			fmt.Printf("Chart:       %s\n", publishedName)
			fmt.Printf("Version:     %s\n", ver)
			fmt.Printf("Tag:         %s\n", tagName)
			fmt.Printf("Repository:  %s/%s\n", strings.TrimRight(repoURL, "/"), publishedName)
			fmt.Println("Apps:")
			for appK, appV := range appVersions {
				fmt.Printf("  - %s: %s\n", appK, appV)
			}
			fmt.Println("DRY RUN: No charts were packaged, uploaded, or App Registry mutated.")

			result.Charts = append(result.Charts, &SingleChartResult{
				ChartName:        chart.Name,
				PublishedName:    publishedName,
				Domain:           chart.Domain,
				Version:          ver,
				EffectiveVersion: ver,
				EffectiveTag:     tagName,
				Published:        false,
			})
			continue
		}

		chartReleaser := &ChartReleaser{
			Chart:         chart,
			AllApps:       allApps,
			RepoURL:       repoURL,
			RepoUser:      repoUser,
			RepoPass:      repoPass,
			AppVersions:   appVersions,
			WorkspaceRoot: workspaceRoot,
			Bazel:         bazel,
			Docker:        docker,
			FS:            fs,
			Packager:      packager,
			Uploader:      uploader,
		}

		execRes, err := ExecuteRelease(ReleaseParams{
			Ctx:                  ctx,
			Domain:               chart.Domain,
			OwnerFullName:        publishedName,
			Version:              ver,
			AllowAutoAdvance:     (p.Version == ""),
			BuildID:              p.BuildID,
			IdempotencyKeyPrefix: idempotencyPrefix,
			GitSHA:               sha,
			Repository:           fmt.Sprintf("%s/%s", strings.TrimRight(repoURL, "/"), publishedName),
			SkipRegistry:         p.SkipRegistry,
			CreateGitTag:         p.CreateGitTag,
			TagName:              tagName,
			TagPrefix:            chart.Name + ".",
			PreviousTagPatterns:  []string{chart.Name + ".*"},
			PreviousTagPrefixes:  []string{chart.Name + ".", publishedName + "."},
			Releaser:             chartReleaser,
			Git:                  git,
			ArtifactClient:       artifactClient,
		})
		if err != nil {
			return nil, err
		}

		result.Charts = append(result.Charts, &SingleChartResult{
			ChartName:        chart.Name,
			PublishedName:    publishedName,
			Domain:           chart.Domain,
			Version:          ver,
			EffectiveVersion: execRes.EffectiveVersion,
			EffectiveTag:     execRes.EffectiveTag,
			PreviousTag:      execRes.PreviousTag,
			Digest:           execRes.Digest,
			DigestUnchanged:  execRes.DigestUnchanged,
			Published:        execRes.Published,
			ChartPath:        chartReleaser.ChartPath(),
			ContainedImages:  execRes.ContainedImages,
		})

		if !p.DryRun {
			notesDir := "/tmp/release-notes"
			if err := os.MkdirAll(notesDir, 0755); err == nil {
				chartNotes, nErr := generateReleaseNotesForChart(
					ctx,
					chart,
					execRes.EffectiveVersion,
					execRes.EffectiveTag,
					"",
					execRes.PreviousTag,
					"markdown",
					appVersions,
					allApps,
					git,
					docker,
					fs,
					artifactClient,
					repoUser,
					"",
				)
				if nErr == nil && chartNotes != "" {
					_ = os.WriteFile(filepath.Join(notesDir, chart.Name+".md"), []byte(chartNotes), 0644)
					_ = os.WriteFile(filepath.Join(notesDir, publishedName+".md"), []byte(chartNotes), 0644)
				}
			}

			if chartPath := chartReleaser.ChartPath(); chartPath != "" {
				helmChartsDir := "/tmp/helm-charts"
				if err := os.MkdirAll(helmChartsDir, 0755); err == nil {
					destFile := filepath.Join(helmChartsDir, filepath.Base(chartPath))
					if data, rErr := os.ReadFile(chartPath); rErr == nil {
						_ = os.WriteFile(destFile, data, 0644)
					}
				}
			}
		}
	}

	return result, nil
}

func filterHelmCharts(input string, allCharts []HelmChartMetadata, includeDemo bool) []HelmChartMetadata {
	clean := strings.ToLower(strings.TrimSpace(input))
	if clean == "" || clean == "all" {
		if includeDemo {
			return allCharts
		}
		var result []HelmChartMetadata
		for _, c := range allCharts {
			if c.Domain != "demo" {
				result = append(result, c)
			}
		}
		return result
	}

	tokens := strings.FieldsFunc(input, func(r rune) bool {
		return r == ',' || r == ' ' || r == '\t' || r == '\n'
	})

	seen := map[string]bool{}
	var result []HelmChartMetadata
	for _, tok := range tokens {
		tok = strings.TrimSpace(tok)
		if tok == "" {
			continue
		}
		for _, c := range allCharts {
			published := strings.TrimPrefix(c.Name, "helm-")
			if (c.Name == tok || c.Domain == tok || published == tok || c.FullName() == tok) && !seen[c.Name] {
				seen[c.Name] = true
				result = append(result, c)
			}
		}
	}
	return result
}

func extractChartVersionFromTag(chartName, tag string) string {
	prefixes := []string{chartName + "."}
	if !strings.HasPrefix(chartName, "helm-") {
		prefixes = append(prefixes, "helm-"+chartName+".")
	}
	return ExtractVersionFromTag(tag, prefixes...)
}

// resolveContainedImagesFromDigests builds a chart's ContainedImages list
// from the chart's compose-time image lockfile plus a caller-supplied
// digest map, without any `docker` call. Used by finalize-chart (issue
// #928): by the time a chart is finalized, every composed app's image
// digest is already known from the merged build job's per-app manifest (a
// digest never changes across a GHCR retag from the build-scoped tag to the
// resolved version tag -- see finalize_app.go), so there is no need for the
// finalize environment to hold registry read credentials just to re-derive
// what is already known. appDigests is keyed by AppMetadata.FullName(),
// matching appVersions and the lockfile's AppFullName field. An app present
// in the lockfile but missing from appDigests is skipped with a warning
// (mirrors resolveContainedImages' unresolved-digest warning) rather than
// failing the whole chart.
func resolveContainedImagesFromDigests(chartDir string, appVersions map[string]string, appDigests map[string]string, fs FileSystem) []*pb.ContainedImage {
	lockfilePath := filepath.Join(chartDir, helm.LockfileFileName)

	var lockfile helm.ChartLockfile
	var loadedLockfile bool
	if fs != nil {
		if data, err := fs.ReadFile(lockfilePath); err == nil {
			if err := json.Unmarshal(data, &lockfile); err == nil {
				loadedLockfile = true
			}
		}
	}
	if !loadedLockfile {
		if data, err := os.ReadFile(lockfilePath); err == nil {
			if err := json.Unmarshal(data, &lockfile); err == nil {
				loadedLockfile = true
			}
		}
	}
	if !loadedLockfile {
		return nil
	}

	var contained []*pb.ContainedImage
	for _, img := range lockfile.Images {
		ver := img.Version
		if v, ok := appVersions[img.AppFullName]; ok && v != "" {
			ver = v
		}
		digest, ok := appDigests[img.AppFullName]
		if !ok || digest == "" {
			fmt.Printf("::warning::No resolved digest for %s (%s), omitting from contains list\n", img.AppFullName, img.Repository)
			continue
		}
		contained = append(contained, &pb.ContainedImage{
			AppFullName: img.AppFullName,
			Repository:  img.Repository,
			Version:     ver,
			Digest:      digest,
		})
	}
	return contained
}

func resolveContainedImages(chartDir string, appVersions map[string]string, chart HelmChartMetadata, allApps []AppMetadata, docker DockerRunner, fs FileSystem) []*pb.ContainedImage {
	var contained []*pb.ContainedImage
	lockfilePath := filepath.Join(chartDir, helm.LockfileFileName)

	var lockfile helm.ChartLockfile
	var loadedLockfile bool

	if fs != nil {
		if data, err := fs.ReadFile(lockfilePath); err == nil {
			if err := json.Unmarshal(data, &lockfile); err == nil {
				loadedLockfile = true
			}
		}
	}
	if !loadedLockfile {
		if data, err := os.ReadFile(lockfilePath); err == nil {
			if err := json.Unmarshal(data, &lockfile); err == nil {
				loadedLockfile = true
			}
		}
	}

	if loadedLockfile && len(lockfile.Images) > 0 {
		for _, img := range lockfile.Images {
			ver := img.Version
			if v, ok := appVersions[img.AppFullName]; ok && v != "" {
				ver = v
			}
			digest := extractImageDigest(docker, img.Repository, ver)
			if digest != "" {
				contained = append(contained, &pb.ContainedImage{
					AppFullName: img.AppFullName,
					Repository:  img.Repository,
					Version:     ver,
					Digest:      digest,
				})
			} else {
				fmt.Printf("::warning::Could not resolve digest for %s:%s, omitting from %s's contains list\n", img.Repository, ver, chart.Name)
			}
		}
	} else {
		// Fallback derivation from chart.Apps and allApps
		for _, appName := range chart.Apps {
			matched, err := findChartApp(appName, chart.Domain, allApps)
			if err != nil {
				continue
			}
			ver := appVersions[matched.FullName()]
			if ver == "" {
				ver = "latest"
			}
			repo := fmt.Sprintf("ghcr.io/whale-net/%s", matched.FullName())
			digest := extractImageDigest(docker, repo, ver)
			if digest != "" {
				contained = append(contained, &pb.ContainedImage{
					AppFullName: matched.FullName(),
					Repository:  repo,
					Version:     ver,
					Digest:      digest,
				})
			}
		}
	}

	return contained
}
