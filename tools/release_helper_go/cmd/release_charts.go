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
	"time"

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
	)

	cmd := &cobra.Command{
		Use:          "release-charts",
		Short:        "Execute Helm chart composition, hermeticity check, packaging, ChartMuseum upload, and artifact recording",
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
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
		appVersions, err := resolveChartAppVersions(chart, allApps, git)
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
				return nil, fmt.Errorf("chart pins %d unpublished app(s), domain %q is at adoption stage 'allocate': %s",
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

		// 3. Build chart target with Bazel
		chartTarget, chartDir := chartOutputPaths(workspaceRoot, chart)
		fmt.Printf("Building bazel target: %s\n", chartTarget)
		if _, err := bazel.Run("build", chartTarget); err != nil {
			return nil, fmt.Errorf("bazel build %s: %w", chartTarget, err)
		}

		// 4. Package chart into temp directory
		outDir, err := os.MkdirTemp("", "helm-release-*")
		if err != nil {
			return nil, fmt.Errorf("create temp dir: %w", err)
		}
		defer os.RemoveAll(outDir)

		chartPath, err := packager.Package(chartDir, chart.Name, ver, outDir, appVersions)
		if err != nil {
			return nil, fmt.Errorf("package chart %s: %w", chart.Name, err)
		}

		// 5. Compute chart archive digest
		chartBytes, err := os.ReadFile(chartPath)
		if err != nil {
			return nil, fmt.Errorf("read packaged chart %s: %w", chartPath, err)
		}
		hasher := sha256.New()
		hasher.Write(chartBytes)
		chartDigest := fmt.Sprintf("sha256:%x", hasher.Sum(nil))

		// 6. Check for no-op rebuild against previous tag
		prevTag := getPreviousChartTag(git, chart.Name, tagName)
		digestUnchanged := false
		effectiveVersion := ver
		effectiveTag := tagName

		if uploader != nil {
			// First check if this exact version already exists on ChartMuseum (e.g. from a previous failed run)
			collisionRes, colErr := ResolveCandidateCollision(
				ver,
				chart.Name+".",
				p.Version == "",
				func(v string) (bool, bool, error) {
					data, fetchErr := uploader.FetchChart(ctx, repoURL, repoUser, repoPass, publishedName, v)
					if fetchErr != nil || len(data) == 0 {
						return false, false, fetchErr
					}
					return true, chartArchivesContentEqual(chartBytes, data), nil
				},
				func(newVer string) error {
					newChartPath, pkgErr := packager.Package(chartDir, chart.Name, newVer, outDir, appVersions)
					if pkgErr != nil {
						return pkgErr
					}
					chartPath = newChartPath
					newBytes, readErr := os.ReadFile(chartPath)
					if readErr != nil {
						return readErr
					}
					chartBytes = newBytes
					hasher := sha256.New()
					hasher.Write(newBytes)
					chartDigest = fmt.Sprintf("sha256:%x", hasher.Sum(nil))
					return nil
				},
			)
			if colErr == nil && collisionRes != nil {
				if collisionRes.DigestUnchanged {
					digestUnchanged = true
					fmt.Printf("::notice title=Chart package already published (%s)::%s is already present on ChartMuseum with identical content (%s). Skipping re-upload.\n",
						chart.Name, ver, chartDigest)
				} else if collisionRes.Advanced {
					ver = collisionRes.Version
					tagName = collisionRes.Tag
					effectiveVersion = ver
					effectiveTag = tagName
					fmt.Printf("::notice title=Chart version collision resolved (%s)::Advanced to %s to avoid collision with orphaned ChartMuseum package.\n",
						chart.Name, ver)
				}
			}

			// If not matching current version, check previous tag for no-op rebuild
			if !digestUnchanged && prevTag != "" {
				prevVersion := extractChartVersionFromTag(chart.Name, prevTag)
				var prevMatches bool
				prevData, err := uploader.FetchChart(ctx, repoURL, repoUser, repoPass, publishedName, prevVersion)
				if err == nil && len(prevData) > 0 {
					prevMatches = chartArchivesContentEqual(chartBytes, prevData)
				}

				decision := EvaluateNoOpDecision(ver, tagName, prevVersion, prevTag, prevMatches)
				digestUnchanged = decision.DigestUnchanged
				effectiveVersion = decision.EffectiveVersion
				effectiveTag = decision.EffectiveTag

				switch decision.Outcome {
				case OutcomeNoOpRebuild:
					fmt.Printf("::notice title=No-op chart rebuild (%s)::%s packages byte-identical to existing tag %s. Skipping new git tag, ChartMuseum upload, and App Registry record -- reusing %s.\n",
						chart.Name, ver, prevTag, prevTag)
				case OutcomeNewBaseline:
					fmt.Printf("::notice title=Major/Minor bump with shared digest (%s)::%s has the same packaged content as %s. Registering new version baseline %s with shared digest.\n",
						chart.Name, ver, prevTag, ver)
				}
			}
		}

		if digestUnchanged {
			result.Charts = append(result.Charts, &SingleChartResult{
				ChartName:        chart.Name,
				PublishedName:    publishedName,
				Domain:           chart.Domain,
				Version:          ver,
				EffectiveVersion: effectiveVersion,
				EffectiveTag:     effectiveTag,
				PreviousTag:      prevTag,
				Digest:           chartDigest,
				DigestUnchanged:  true,
				Published:        false,
				ChartPath:        chartPath,
			})
			continue
		}

		isAllocate := false
		if artifactClient != nil && !p.SkipRegistry {
			var aerr error
			isAllocate, aerr = isDomainAtAllocateStage(ctx, artifactClient, chart.Domain)
			if aerr != nil {
				return nil, aerr
			}
		}

		// 7. BeginPublish (Heartbeat) in App Registry
		beginPublishSucceeded := false
		if artifactClient != nil && p.BuildID != "" && !p.SkipRegistry {
			beginReq := &pb.BeginPublishRequest{
				Kind:           pb.ArtifactKind_ARTIFACT_KIND_CHART,
				OwnerFullName:  publishedName,
				Version:        ver,
				BuildId:        p.BuildID,
				IdempotencyKey: fmt.Sprintf("%s-%s-chart-begin", idempotencyPrefix, publishedName),
				Repository:     fmt.Sprintf("%s/%s", strings.TrimRight(repoURL, "/"), publishedName),
			}
			_, err := artifactClient.BeginPublish(ctx, beginReq)
			if err != nil {
				if isAllocate {
					return nil, fmt.Errorf("App Registry BeginPublish failed for chart %s (domain %q at stage 'allocate'): %w", publishedName, chart.Domain, err)
				}
				fmt.Printf("::warning title=App Registry BeginPublish failed::%v\n", err)
			} else {
				beginPublishSucceeded = true
			}
		}

		// 8. Upload chart package to ChartMuseum
		fmt.Printf("Uploading %s version %s to %s...\n", publishedName, ver, repoURL)
		if uploadErr := uploader.UploadChart(ctx, repoURL, repoUser, repoPass, chartPath); uploadErr != nil {
			if beginPublishSucceeded && artifactClient != nil {
				failReq := &pb.FailPublishRequest{
					Kind:           pb.ArtifactKind_ARTIFACT_KIND_CHART,
					OwnerFullName:  publishedName,
					Version:        ver,
					Reason:         fmt.Sprintf("chart upload failed (workflow run %s, attempt %s): %v", runID, attempt, uploadErr),
					IdempotencyKey: fmt.Sprintf("%s-%s-chart-fail", idempotencyPrefix, publishedName),
				}
				_, _ = artifactClient.FailPublish(ctx, failReq)
			}
			return nil, fmt.Errorf("upload chart %s to ChartMuseum: %w", publishedName, uploadErr)
		}

		// 9. Create git tag on success
		if p.CreateGitTag && git != nil && sha != "" {
			if _, err := git.Run("rev-parse", tagName); err != nil {
				fmt.Printf("Creating git tag %s...\n", tagName)
				_, _ = git.Run("tag", "-a", tagName, "-m", fmt.Sprintf("Release helm chart %s version %s", chart.Name, ver), sha)
			}
		}

		// 10. Resolve Contained Image Digests & Record Artifact
		containedImages := resolveContainedImages(chartDir, appVersions, chart, allApps, docker, fs)

		if artifactClient != nil && p.BuildID != "" && !p.SkipRegistry {
			recReq := &pb.RecordArtifactRequest{
				BuildId:        p.BuildID,
				Kind:           pb.ArtifactKind_ARTIFACT_KIND_CHART,
				OwnerFullName:  publishedName,
				Repository:     fmt.Sprintf("%s/%s", strings.TrimRight(repoURL, "/"), publishedName),
				Version:        ver,
				Digest:         chartDigest,
				Contains:       containedImages,
				PublishedAt:    time.Now().Unix(),
				IdempotencyKey: fmt.Sprintf("%s-%s-chart-record", idempotencyPrefix, publishedName),
			}
			if _, err := artifactClient.RecordArtifact(ctx, recReq); err != nil {
				if beginPublishSucceeded {
					failReq := &pb.FailPublishRequest{
						Kind:           pb.ArtifactKind_ARTIFACT_KIND_CHART,
						OwnerFullName:  publishedName,
						Version:        ver,
						Reason:         fmt.Sprintf("chart record artifact failed (workflow run %s, attempt %s): %v", runID, attempt, err),
						IdempotencyKey: fmt.Sprintf("%s-%s-chart-fail-record", idempotencyPrefix, publishedName),
					}
					_, _ = artifactClient.FailPublish(ctx, failReq)
				}
				if isAllocate {
					return nil, fmt.Errorf("App Registry RecordArtifact failed for chart %s (domain %q at stage 'allocate'): %w", publishedName, chart.Domain, err)
				}
				fmt.Printf("::warning title=App Registry RecordArtifact failed::%v\n", err)
			} else {
				fmt.Printf("✅ Recorded %s %s (%s) in App Registry\n", publishedName, ver, chartDigest)
			}
		}

		result.Charts = append(result.Charts, &SingleChartResult{
			ChartName:        chart.Name,
			PublishedName:    publishedName,
			Domain:           chart.Domain,
			Version:          ver,
			EffectiveVersion: effectiveVersion,
			EffectiveTag:     effectiveTag,
			PreviousTag:      prevTag,
			Digest:           chartDigest,
			DigestUnchanged:  false,
			Published:        true,
			ChartPath:        chartPath,
			ContainedImages:  containedImages,
		})
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

func getPreviousChartTag(git GitRunner, chartName, currentTag string) string {
	patterns := []string{fmt.Sprintf("%s.*", chartName)}
	prefixes := []string{chartName + "."}
	if !strings.HasPrefix(chartName, "helm-") {
		patterns = append(patterns, fmt.Sprintf("helm-%s.*", chartName))
		prefixes = append(prefixes, fmt.Sprintf("helm-%s.", chartName))
	}
	return GetPreviousGitTag(git, currentTag, patterns, prefixes)
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
