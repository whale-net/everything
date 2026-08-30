package cmd

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/spf13/cobra"
	pb "github.com/whale-net/everything/tools/app_registry/protos"
	appmetapb "github.com/whale-net/everything/tools/appmeta/proto"
	"github.com/whale-net/everything/tools/helm"
)

// FinalizeChartParams configures ExecuteFinalizeChart.
type FinalizeChartParams struct {
	Ctx                  context.Context
	ChartName            string
	Domain               string
	ChartDir             string
	Version              string
	AppVersions          map[string]string
	AppDigests           map[string]string
	BuildID              string
	ChartRepoURL         string
	ChartRepoUser        string
	ChartRepoPass        string
	IdempotencyKeyPrefix string
	SkipRegistry         bool

	Uploader       ChartUploader
	Packager       HelmPackager
	Hermeticity    ChartHermeticityChecker
	ArtifactClient pb.ArtifactRegistryClient
}

func newFinalizeChartCmd() *cobra.Command {
	var (
		chartName            string
		domain               string
		chartDir             string
		version              string
		appVersionsJSON      string
		appDigestsJSON       string
		buildID              string
		chartRepoURL         string
		chartRepoUser        string
		chartRepoPass        string
		idempotencyKeyPrefix string
		skipRegistry         bool
		outputDir            string
	)

	cmd := &cobra.Command{
		Use:   "finalize-chart",
		Short: "Package an already-built chart source tree with resolved versions and publish to ChartMuseum (issue #928)",
		Long: "finalize-chart is the \"resolve final version and publish\" half of what " +
			"release-charts used to do in one step: given a chart source tree already built by " +
			"build-chart, it substitutes the resolved final chart version and each composed " +
			"app's resolved image version/digest, packages the chart (`helm package`), and runs " +
			"it through the same BeginPublish -> collision/no-op detection -> ChartMuseum " +
			"upload -> RecordArtifact/FailPublish sequence release-charts already used (see " +
			"ExecuteRelease) -- reused unchanged, not reimplemented. Invoked by Temporal's " +
			"FinalizePublish activity (worker/release/finalize.go), not by GHA directly.",
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			if chartName == "" {
				return fmt.Errorf("missing required flag: --chart")
			}
			if chartDir == "" {
				return fmt.Errorf("missing required flag: --chart-dir")
			}
			if version == "" {
				return fmt.Errorf("missing required flag: --version")
			}

			var appVersions map[string]string
			if strings.TrimSpace(appVersionsJSON) != "" {
				if err := json.Unmarshal([]byte(appVersionsJSON), &appVersions); err != nil {
					return fmt.Errorf("parse --app-versions: %w", err)
				}
			}
			var appDigests map[string]string
			if strings.TrimSpace(appDigestsJSON) != "" {
				if err := json.Unmarshal([]byte(appDigestsJSON), &appDigests); err != nil {
					return fmt.Errorf("parse --app-digests: %w", err)
				}
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

			res, err := ExecuteFinalizeChart(FinalizeChartParams{
				Ctx:                  cmd.Context(),
				ChartName:            chartName,
				Domain:               domain,
				ChartDir:             chartDir,
				Version:              version,
				AppVersions:          appVersions,
				AppDigests:           appDigests,
				BuildID:              buildID,
				ChartRepoURL:         repoURL,
				ChartRepoUser:        user,
				ChartRepoPass:        pass,
				IdempotencyKeyPrefix: idempotencyKeyPrefix,
				SkipRegistry:         skipRegistry,
				Uploader:             defaultUploader,
				Packager:             defaultPackager,
				Hermeticity:          defaultHermeticityChecker,
			})
			if err != nil {
				return err
			}

			fmt.Fprintf(cmd.OutOrStdout(), "Finalized chart %s (version: %s, effective: %s, digest: %s, published: %t)\n",
				chartName, version, res.EffectiveVersion, res.Digest, res.Published)

			// --output-dir mirrors finalize-app's identical convention
			// (finalize_app.go): lets a caller (Temporal's FinalizePublish,
			// worker/release/finalize.go) recover res.EffectiveVersion
			// after this runs as a subprocess. Charts previously had no
			// way to report this at all -- FinalizePublish's per-target
			// outcome tracking needs it for charts exactly as it already
			// does for apps (issue #973's proper fix, superseding the
			// resolved-plan-JSON comparison PR #976 first attempted).
			//
			// ExecuteFinalizeChart above has ALREADY succeeded (chart
			// packaged and uploaded to ChartMuseum, App Registry recorded)
			// by the time we get here -- this write is a secondary
			// bookkeeping sidecar, not part of that. A failure writing it
			// must not make this command exit non-zero for the same
			// reason finalize-app's identical write doesn't (see
			// writeFinalizeResultFile's doc comment / this PR's Finding
			// 1). Warn and still exit 0.
			if outputDir != "" {
				if werr := writeFinalizeResultFile(outputDir, domain, chartName, res); werr != nil {
					fmt.Fprintf(cmd.ErrOrStderr(), "::warning::finalize-chart: publish succeeded but failed to write --output-dir result file: %v\n", werr)
				}
			}
			return nil
		},
	}

	cmd.Flags().StringVar(&chartName, "chart", "", "Chart name (e.g. helm-demo)")
	cmd.Flags().StringVar(&domain, "domain", "", "Chart domain")
	cmd.Flags().StringVar(&chartDir, "chart-dir", "", "Path to the chart source tree build-chart produced")
	cmd.Flags().StringVar(&version, "version", "", "Resolved final chart version")
	cmd.Flags().StringVar(&appVersionsJSON, "app-versions", "", `JSON object of resolved app versions, keyed by app FullName ("<domain>-<name>")`)
	cmd.Flags().StringVar(&appDigestsJSON, "app-digests", "", `JSON object of resolved app image digests, keyed by app FullName ("<domain>-<name>")`)
	cmd.Flags().StringVar(&buildID, "build-id", "", "App Registry Build ID")
	cmd.Flags().StringVar(&chartRepoURL, "chart-repo-url", "", "ChartMuseum repository URL (default: $CHART_REPO_URL or https://charts.whalenet.dev)")
	cmd.Flags().StringVar(&chartRepoUser, "chart-repo-user", "", "ChartMuseum username (default: $CHART_REPO_USER)")
	cmd.Flags().StringVar(&chartRepoPass, "chart-repo-pass", "", "ChartMuseum password (default: $CHART_REPO_PASS)")
	cmd.Flags().StringVar(&idempotencyKeyPrefix, "idempotency-key-prefix", "", "Prefix for idempotency keys")
	cmd.Flags().BoolVar(&skipRegistry, "skip-registry", false, "Skip App Registry API interactions")
	cmd.Flags().StringVar(&outputDir, "output-dir", "", "Write the finalize result (including effective_version) as <domain>-<chart>.json here; skipped if unset")

	_ = cmd.MarkFlagRequired("chart")
	_ = cmd.MarkFlagRequired("chart-dir")
	_ = cmd.MarkFlagRequired("version")

	return cmd
}

// ExecuteFinalizeChart packages p.ChartDir with p.Version/p.AppVersions and
// runs it through ExecuteRelease exactly as release-charts does, via a
// ChartReleaser configured with ChartDir/AppDigests (see releaser_chart.go)
// instead of a fresh Bazel build.
func ExecuteFinalizeChart(p FinalizeChartParams) (*ReleaseResult, error) {
	if p.ChartName == "" || p.ChartDir == "" || p.Version == "" {
		return nil, fmt.Errorf("chart, chart-dir, and version are required")
	}

	ctx := p.Ctx
	if ctx == nil {
		ctx = context.Background()
	}
	uploader := p.Uploader
	if uploader == nil {
		uploader = defaultUploader
	}
	packager := p.Packager
	if packager == nil {
		packager = defaultPackager
	}
	hermeticity := p.Hermeticity
	if hermeticity == nil {
		hermeticity = defaultHermeticityChecker
	}

	chart := HelmChartMetadata{ChartManifest: &appmetapb.ChartManifest{Name: p.ChartName, Domain: p.Domain}}
	publishedName := strings.TrimPrefix(p.ChartName, "helm-")

	var artifactClient pb.ArtifactRegistryClient
	var closeFn func() error
	if p.ArtifactClient != nil {
		artifactClient = p.ArtifactClient
	} else if !p.SkipRegistry {
		var err error
		artifactClient, closeFn, err = NewArtifactRegistryClient(ctx)
		if err != nil {
			artifactClient = nil
		}
	}
	if closeFn != nil {
		defer closeFn() //nolint:errcheck
	}

	// Chart-only release safety net (issue #1538): p.AppVersions only
	// covers apps that were part of this release batch (built by
	// worker/release/finalize.go's apps loop) -- an app the chart composes
	// but that wasn't rebuilt this run has no entry, and a batch that
	// rebuilds *no* apps at all (the common chart-only case: bumping just
	// the chart version) leaves it completely empty. Without this,
	// packageChartWithVersion has nothing to validate against and silently
	// ships whatever imageTag composer.go baked into values.yaml at Bazel
	// build time, which is always the literal string "latest" (see
	// tools/helm/composer.go's GetImageTag) -- never a real, deterministic
	// version. Resolve every missing entry from the App Registry's last
	// published artifact for that app instead, using the chart's
	// compose-time lockfile to know which apps it actually composes.
	appVersions, err := resolveMissingChartAppVersions(ctx, p.ChartDir, p.AppVersions, artifactClient)
	if err != nil {
		return nil, err
	}
	p.AppVersions = appVersions

	// Same chart-pin hermeticity gate release-charts applies before
	// packaging (AR-7f) -- reused unchanged so an allocate-mode domain
	// cannot finalize a chart pinning an unpublished app any more than it
	// could before this split. See release_charts.go's identical check.
	if defaultEnv("APP_REGISTRY_CICD_OPT_IN") == "true" && !p.SkipRegistry {
		pins := make([]ChartPin, 0, len(p.AppVersions))
		for appFullName, v := range p.AppVersions {
			pins = append(pins, ChartPin{AppFullName: appFullName, Version: v})
		}
		sort.Slice(pins, func(i, j int) bool { return pins[i].AppFullName < pins[j].AppFullName })
		enforced, violations, err := hermeticity.Check(ctx, p.Domain, pins)
		if err != nil {
			fmt.Printf("::warning::App Registry chart-hermeticity check skipped (registry error): %v\n", err)
		} else if enforced && len(violations) > 0 {
			names := make([]string, 0, len(violations))
			for _, v := range violations {
				names = append(names, fmt.Sprintf("%s@%s (%s)", v.AppFullName, v.Version, v.Reason))
			}
			return nil, fmt.Errorf("chart pins %d unpublished app(s) in domain %q: %s",
				len(violations), p.Domain, strings.Join(names, ", "))
		}
	}

	releaser := &ChartReleaser{
		Chart:       chart,
		RepoURL:     p.ChartRepoURL,
		RepoUser:    p.ChartRepoUser,
		RepoPass:    p.ChartRepoPass,
		AppVersions: p.AppVersions,
		AppDigests:  p.AppDigests,
		ChartDir:    p.ChartDir,
		Packager:    packager,
		Uploader:    uploader,
	}

	return ExecuteRelease(ReleaseParams{
		Ctx:           ctx,
		Domain:        p.Domain,
		OwnerFullName: publishedName,
		Version:       p.Version,
		// AllowAutoAdvance is always false here, unlike release-charts'
		// (p.Version == "") -- ExecuteRelease's collision-resolution block
		// still runs regardless (Kind() == CHART forces entry), but with
		// this false it only detects a no-op/collision, never silently
		// advances to a different version. p.Version is already the
		// batch's resolved plan version (minted upstream by Temporal's
		// ResolvePlan, possibly via AllocateVersion's reservation) --
		// finalize must never pick a version other than the one already
		// resolved and (in allocate-mode domains) reserved.
		AllowAutoAdvance:     false,
		BuildID:              p.BuildID,
		IdempotencyKeyPrefix: p.IdempotencyKeyPrefix,
		Repository:           fmt.Sprintf("%s/%s", strings.TrimRight(p.ChartRepoURL, "/"), publishedName),
		SkipRegistry:         p.SkipRegistry,
		TagName:              fmt.Sprintf("%s.%s", p.ChartName, p.Version),
		TagPrefix:            p.ChartName + ".",
		PreviousTagPatterns:  []string{p.ChartName + ".*"},
		PreviousTagPrefixes:  []string{p.ChartName + ".", publishedName + "."},
		Releaser:             releaser,
		ArtifactClient:       artifactClient,
	})
}

// resolveMissingChartAppVersions fills in any app the chart composes that
// is missing from appVersions -- an app that wasn't part of this release
// batch (issue #1538's chart-only case). It reads the chart's compose-time
// lockfile (image-lockfile.json, written by tools/helm/composer.go at
// Bazel build time -- see chartOutputPaths/read-chart-lockfile) to discover
// every app the chart actually pins, and resolves each missing one from
// the App Registry's last published artifact, mirroring
// resolveChartAppVersions' identical independent-resolution behavior for
// release-charts (build_helm.go).
//
// client == nil (App Registry not opted in, dial failed, or
// --skip-registry) means there is nowhere to resolve a missing entry from
// -- this hard-errors rather than falling back to the chart's baked-in
// placeholder tag, which is always the literal string "latest" (see
// GetImageTag). A missing entry with no way to resolve it is exactly the
// bug this function exists to close off, not something to paper over.
func resolveMissingChartAppVersions(ctx context.Context, chartDir string, appVersions map[string]string, client pb.ArtifactRegistryClient) (map[string]string, error) {
	lockfilePath := filepath.Join(chartDir, helm.LockfileFileName)
	data, err := os.ReadFile(lockfilePath)
	if err != nil {
		// No lockfile to cross-check against (e.g. chart predates AR-2b) --
		// leave appVersions exactly as passed in.
		return appVersions, nil
	}
	var lockfile helm.ChartLockfile
	if err := json.Unmarshal(data, &lockfile); err != nil {
		return nil, fmt.Errorf("parse %s: %w", lockfilePath, err)
	}

	resolved := make(map[string]string, len(appVersions)+len(lockfile.Images))
	for k, v := range appVersions {
		resolved[k] = v
	}
	for _, img := range lockfile.Images {
		if v, ok := resolved[img.AppFullName]; ok && v != "" {
			continue
		}
		if client == nil {
			return nil, fmt.Errorf("resolve version for app %q composed by chart: not part of this release batch and no App Registry client available", img.AppFullName)
		}
		resp, err := client.GetArtifact(ctx, &pb.GetArtifactRequest{
			OwnerFullName:   img.AppFullName,
			Kind:            pb.ArtifactKind_ARTIFACT_KIND_IMAGE,
			LatestPublished: true,
		})
		if err != nil {
			return nil, fmt.Errorf("resolve version for app %q composed by chart: App Registry GetArtifact failed: %w", img.AppFullName, err)
		}
		if resp.Artifact == nil || resp.Artifact.Version == "" {
			return nil, fmt.Errorf("resolve version for app %q composed by chart: App Registry returned no published artifact", img.AppFullName)
		}
		resolved[img.AppFullName] = resp.Artifact.Version
	}
	return resolved, nil
}
