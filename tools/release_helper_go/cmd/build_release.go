package cmd

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"
)

// cliBinaryAppNames is the fixed set of apps FR7 of #979 packages
// multi-platform CLI binaries for -- not every CLI app, just these two.
var cliBinaryAppNames = []string{"release_helper_go", "app-registry"}

// cliPlatformNames is the fixed multi-platform set FR7 of #979 packages CLI
// binaries for -- deliberately explicit rather than PackageAppAssets'
// DefaultCLIPlatforms (which omits darwin_amd64), matching what
// release-v2.yml's former build-cli-binaries job always passed.
var cliPlatformNames = []string{"linux_amd64", "linux_arm64", "darwin_arm64", "darwin_amd64"}

// BuildOpenAPISpecResult is one OpenAPI spec's build-openapi outcome.
type BuildOpenAPISpecResult struct {
	App      string `json:"app"`
	Domain   string `json:"domain"`
	SpecPath string `json:"spec_path"`
}

type openAPIMatrixWrapper struct {
	Include []openAPISpecEntry `json:"include"`
}

// BuildOpenAPISpecs builds every entry's Bazel OpenAPI-spec target and copies
// the resulting JSON into outputDir, named "<domain>-<app>_openapi_spec.json".
// This is a faithful Go port of the bash/jq/sed loop release-v2.yml's former
// build-openapi-specs job ran (issue #1024/#1055) -- including its exact
// two-path-form fallback for locating the built spec file, since the primary
// form (bazel-bin/<target's package dir>/<domain>-<app>_openapi_spec.json)
// does not match every fastapi_app's actual output layout.
func BuildOpenAPISpecs(bazel BazelRunner, entries []openAPISpecEntry, outputDir string) ([]BuildOpenAPISpecResult, error) {
	if len(entries) == 0 {
		return nil, nil
	}
	if err := os.MkdirAll(outputDir, 0755); err != nil {
		return nil, fmt.Errorf("create output dir %s: %w", outputDir, err)
	}

	bazelBinOut, err := bazel.Run("info", "bazel-bin", "--config=ci-images")
	if err != nil {
		return nil, fmt.Errorf("bazel info bazel-bin: %w", err)
	}
	bazelBin := strings.TrimSpace(bazelBinOut)

	results := make([]BuildOpenAPISpecResult, 0, len(entries))
	for _, entry := range entries {
		fmt.Printf("Building OpenAPI spec for %s-%s (target: %s)...\n", entry.Domain, entry.App, entry.OpenAPITarget)
		if _, err := bazel.Run("build", "--config=ci-images", entry.OpenAPITarget); err != nil {
			return nil, fmt.Errorf("bazel build %s: %w", entry.OpenAPITarget, err)
		}

		// Mirrors the bash: strip a "@@//" or "//" label prefix, then derive
		// the target's package directory by turning ":" into "/" and taking
		// the dirname.
		targetPath := strings.TrimPrefix(entry.OpenAPITarget, "@@//")
		targetPath = strings.TrimPrefix(targetPath, "//")
		targetAsPath := strings.Replace(targetPath, ":", "/", 1)

		specFilename := fmt.Sprintf("%s-%s_openapi_spec.json", entry.Domain, entry.App)
		targetDir := targetAsPath
		if idx := strings.LastIndex(targetAsPath, "/"); idx >= 0 {
			targetDir = targetAsPath[:idx]
		}
		specFile := filepath.Join(bazelBin, targetDir, specFilename)
		if _, err := os.Stat(specFile); err != nil {
			// Fallback: some fastapi_app targets emit the spec at
			// bazel-bin/<target path with ':' -> '/'>.json instead of
			// alongside a domain-app-named file in the package directory.
			specFile = filepath.Join(bazelBin, targetAsPath+".json")
		}

		destFile := filepath.Join(outputDir, specFilename)
		if err := copyFile(specFile, destFile); err != nil {
			return nil, fmt.Errorf("copy openapi spec %s -> %s: %w", specFile, destFile, err)
		}

		results = append(results, BuildOpenAPISpecResult{App: entry.App, Domain: entry.Domain, SpecPath: destFile})
	}
	return results, nil
}

// parseReleaseMatrixItems re-unmarshals a PlanResult.Matrix/ChartMatrix-shaped
// generic map back into typed items -- matrixItem (App, Domain, Version) is
// shared with summary.go's identical need to read this same "include" shape.
func parseReleaseMatrixItems(matrix map[string]interface{}) ([]matrixItem, error) {
	if matrix == nil {
		return nil, nil
	}
	raw, err := json.Marshal(matrix)
	if err != nil {
		return nil, fmt.Errorf("marshal release matrix: %w", err)
	}
	var wrapper releaseMatrix
	if err := json.Unmarshal(raw, &wrapper); err != nil {
		return nil, fmt.Errorf("unmarshal release matrix: %w", err)
	}
	return wrapper.Include, nil
}

// parseOpenAPIMatrixItems re-unmarshals a PlanResult.OpenAPIMatrix-shaped
// generic map back into typed openAPISpecEntry items (plan.go's type).
func parseOpenAPIMatrixItems(matrix map[string]interface{}) ([]openAPISpecEntry, error) {
	if matrix == nil {
		return nil, nil
	}
	raw, err := json.Marshal(matrix)
	if err != nil {
		return nil, fmt.Errorf("marshal openapi matrix: %w", err)
	}
	var wrapper openAPIMatrixWrapper
	if err := json.Unmarshal(raw, &wrapper); err != nil {
		return nil, fmt.Errorf("unmarshal openapi matrix: %w", err)
	}
	return wrapper.Include, nil
}

func matrixHasApp(items []matrixItem, app string) bool {
	for _, item := range items {
		if item.App == app {
			return true
		}
	}
	return false
}

// BuildReleaseArtifactsParams configures ExecuteBuildReleaseArtifacts.
type BuildReleaseArtifactsParams struct {
	Ctx    context.Context
	Plan   PlanResult
	GitSHA string
	// Registry defaults to "ghcr.io" (see ExecuteBuildApp) when empty.
	Registry string
	// DryRun mirrors release-v2.yml's dry_run input: app images, OpenAPI
	// specs, and CLI binaries are all skipped when true. Chart source trees
	// are NOT gated on DryRun -- that is pre-existing release-v2.yml
	// behavior (its "Build chart source trees" step was never conditioned
	// on dry_run, only on whether charts were requested), preserved as-is
	// here rather than "fixed" as a silent behavior change.
	DryRun bool

	AppsOutputDir        string // default /tmp/build-manifest/apps
	ChartsOutputDir      string // default /tmp/build-manifest/charts
	OpenAPIOutputDir     string // default /tmp/openapi-specs
	CLIBinariesOutputDir string // default /tmp/cli-binaries

	Bazel         BazelRunner
	Docker        DockerRunner
	FS            FileSystem
	WorkspaceRoot string
}

// BuildReleaseArtifactsResult summarizes everything ExecuteBuildReleaseArtifacts built.
type BuildReleaseArtifactsResult struct {
	Apps         []*BuildAppManifest
	Charts       []BuildChartResult
	OpenAPISpecs []BuildOpenAPISpecResult
	CLIBinaries  map[string][]PackagedAsset
}

// ExecuteBuildReleaseArtifacts builds every release artifact a resolved plan
// calls for -- app images, chart source trees, OpenAPI specs, and
// multi-platform CLI binaries -- in one Bazel session. It consolidates what
// release-v2.yml used to spread across four jobs/steps (build-app per app,
// build-chart, a bash/jq OpenAPI-build loop, and package-assets per CLI app)
// behind one call, so a caller (CI or a human) gets the same per-artifact-type
// gating (dry-run, has-specs, app-presence in the matrix) that used to live
// as YAML `if:` conditions -- now as ordinary, unit-testable Go branches
// (issue #1024/#1055).
func ExecuteBuildReleaseArtifacts(p BuildReleaseArtifactsParams) (*BuildReleaseArtifactsResult, error) {
	bazel := p.Bazel
	if bazel == nil {
		bazel = defaultBazel
	}
	docker := p.Docker
	if docker == nil {
		docker = defaultDocker
	}
	fs := p.FS
	if fs == nil {
		fs = defaultFS
	}
	workspaceRoot := p.WorkspaceRoot
	if workspaceRoot == "" {
		var err error
		workspaceRoot, err = defaultWorkspaceRoot()
		if err != nil {
			return nil, fmt.Errorf("workspace root: %w", err)
		}
	}

	appsOutputDir := p.AppsOutputDir
	if appsOutputDir == "" {
		appsOutputDir = "/tmp/build-manifest/apps"
	}
	chartsOutputDir := p.ChartsOutputDir
	if chartsOutputDir == "" {
		chartsOutputDir = "/tmp/build-manifest/charts"
	}
	openapiOutputDir := p.OpenAPIOutputDir
	if openapiOutputDir == "" {
		openapiOutputDir = "/tmp/openapi-specs"
	}
	cliOutputDir := p.CLIBinariesOutputDir
	if cliOutputDir == "" {
		cliOutputDir = "/tmp/cli-binaries"
	}

	result := &BuildReleaseArtifactsResult{}

	matrixItems, err := parseReleaseMatrixItems(p.Plan.Matrix)
	if err != nil {
		return nil, fmt.Errorf("parse release matrix: %w", err)
	}

	// App images (push by digest) -- mirrors the former "Build app images"
	// step's `if: dry_run == 'false' && release-matrix != ''`.
	if !p.DryRun && len(matrixItems) > 0 {
		for _, item := range matrixItems {
			manifest, err := ExecuteBuildApp(BuildAppParams{
				Ctx:           p.Ctx,
				Domain:        item.Domain,
				App:           item.App,
				GitSHA:        p.GitSHA,
				Registry:      p.Registry,
				OutputDir:     appsOutputDir,
				Bazel:         bazel,
				Docker:        docker,
				WorkspaceRoot: workspaceRoot,
			})
			if err != nil {
				return nil, fmt.Errorf("build app %s-%s: %w", item.Domain, item.App, err)
			}
			if manifest != nil {
				result.Apps = append(result.Apps, manifest)
			}
		}
	}

	// Chart source trees -- mirrors the former step's
	// `if: helm_charts != ''`; an empty Plan.Charts is the resolved-plan
	// equivalent of that raw input being empty. See DryRun's doc comment
	// above: deliberately not gated on DryRun.
	if len(p.Plan.Charts) > 0 {
		chartResults, err := ExecuteBuildCharts(BuildChartParams{
			Ctx:           p.Ctx,
			Charts:        strings.Join(p.Plan.Charts, ","),
			OutputDir:     chartsOutputDir,
			Bazel:         bazel,
			FS:            fs,
			WorkspaceRoot: workspaceRoot,
		})
		if err != nil {
			return nil, fmt.Errorf("build charts: %w", err)
		}
		result.Charts = chartResults
	}

	// OpenAPI specs -- mirrors the former build-openapi-specs job's
	// `if: has-specs == 'true' && dry_run == 'false'`.
	if p.Plan.HasSpecs && !p.DryRun {
		specEntries, err := parseOpenAPIMatrixItems(p.Plan.OpenAPIMatrix)
		if err != nil {
			return nil, fmt.Errorf("parse openapi matrix: %w", err)
		}
		specResults, err := BuildOpenAPISpecs(bazel, specEntries, openapiOutputDir)
		if err != nil {
			return nil, fmt.Errorf("build openapi specs: %w", err)
		}
		result.OpenAPISpecs = specResults
	}

	// CLI binaries -- mirrors the former build-cli-binaries job's
	// `if: dry_run == 'false' && release-matrix contains release_helper_go
	// or app-registry`. Only package whichever of the two is actually
	// present, same as the original bash loop.
	if !p.DryRun {
		var cliApps []string
		for _, name := range cliBinaryAppNames {
			if matrixHasApp(matrixItems, name) {
				cliApps = append(cliApps, name)
			}
		}
		if len(cliApps) > 0 {
			allApps, err := ListAllApps(bazel, fs, workspaceRoot)
			if err != nil {
				return nil, fmt.Errorf("list all apps: %w", err)
			}
			result.CLIBinaries = make(map[string][]PackagedAsset, len(cliApps))
			for _, name := range cliApps {
				// resolveApps only matches full names ("tools-<app>") and
				// domain sweeps, not bare app names -- a bare name can
				// collide with an unrelated same-named domain (e.g.
				// "app-registry" is both this CLI's bare name and the
				// app-registry domain). Matches release-v2.yml's former
				// bash loop's identical fix for this same collision risk.
				resolved, err := resolveApps([]string{"tools-" + name}, allApps)
				if err != nil {
					return nil, fmt.Errorf("resolve app %s: %w", name, err)
				}
				assets, err := PackageAppAssets(bazel, fs, workspaceRoot, resolved[0], filepath.Join(cliOutputDir, name), cliPlatformNames)
				if err != nil {
					return nil, fmt.Errorf("package cli binaries for %s: %w", name, err)
				}
				result.CLIBinaries[name] = assets
			}
		}
	}

	return result, nil
}

func newBuildReleaseCmd() *cobra.Command {
	var (
		fromResolvedPlan     string
		gitSHA               string
		registry             string
		dryRun               bool
		appsOutputDir        string
		chartsOutputDir      string
		openapiOutputDir     string
		cliBinariesOutputDir string
	)

	cmd := &cobra.Command{
		Use:   "build-release",
		Short: "Build every release artifact (app images, chart source trees, OpenAPI specs, CLI binaries) for a resolved plan in one Bazel session",
		Long: "build-release consolidates what used to be four separate GitHub Actions jobs/steps " +
			"(build-app per app, build-chart, a bash/jq OpenAPI-build loop, and package-assets per " +
			"CLI app -- see issue #1024) into one CLI invocation driven by a resolved plan (the same " +
			"JSON shape `release-helper plan --format=json` emits). Each artifact type builds only " +
			"if the plan actually calls for it, mirroring the per-type `if:` guards release-v2.yml " +
			"used to declare in YAML -- now ordinary, unit-testable Go branches (issue #1055).",
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			if fromResolvedPlan == "" {
				return fmt.Errorf("missing required flag: --from-resolved-plan")
			}
			var plan PlanResult
			if err := json.Unmarshal([]byte(fromResolvedPlan), &plan); err != nil {
				return fmt.Errorf("--from-resolved-plan is not valid JSON: %w", err)
			}
			if gitSHA == "" {
				gitSHA = defaultEnv("GITHUB_SHA")
			}
			if gitSHA == "" {
				return fmt.Errorf("missing required flag: --git-sha (and GITHUB_SHA is unset)")
			}

			workspaceRoot, err := defaultWorkspaceRoot()
			if err != nil {
				return fmt.Errorf("workspace root: %w", err)
			}

			result, err := ExecuteBuildReleaseArtifacts(BuildReleaseArtifactsParams{
				Ctx:                  cmd.Context(),
				Plan:                 plan,
				GitSHA:               gitSHA,
				Registry:             registry,
				DryRun:               dryRun,
				AppsOutputDir:        appsOutputDir,
				ChartsOutputDir:      chartsOutputDir,
				OpenAPIOutputDir:     openapiOutputDir,
				CLIBinariesOutputDir: cliBinariesOutputDir,
				Bazel:                defaultBazel,
				Docker:               defaultDocker,
				FS:                   defaultFS,
				WorkspaceRoot:        workspaceRoot,
			})
			if err != nil {
				return err
			}

			fmt.Fprintf(cmd.OutOrStdout(), "Built %d app image(s), %d chart(s), %d openapi spec(s), %d cli-binary app(s)\n",
				len(result.Apps), len(result.Charts), len(result.OpenAPISpecs), len(result.CLIBinaries))
			return nil
		},
	}

	cmd.Flags().StringVar(&fromResolvedPlan, "from-resolved-plan", "", "Resolved plan JSON (release-helper plan --format=json's own output shape)")
	cmd.Flags().StringVar(&gitSHA, "git-sha", "", "Git commit SHA to tag app image builds with (default: $GITHUB_SHA)")
	cmd.Flags().StringVar(&registry, "registry", "ghcr.io", "Container registry for app images")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "Dry run -- skip app images, OpenAPI specs, and CLI binaries (chart source trees still build; see release-v2.yml's pre-existing per-type gating)")
	cmd.Flags().StringVar(&appsOutputDir, "apps-output-dir", "/tmp/build-manifest/apps", "Directory to write per-app build manifests to")
	cmd.Flags().StringVar(&chartsOutputDir, "charts-output-dir", "/tmp/build-manifest/charts", "Directory to copy chart source trees into")
	cmd.Flags().StringVar(&openapiOutputDir, "openapi-output-dir", "/tmp/openapi-specs", "Directory to write built OpenAPI spec JSON files to")
	cmd.Flags().StringVar(&cliBinariesOutputDir, "cli-binaries-output-dir", "/tmp/cli-binaries", "Directory to write packaged multi-platform CLI binaries to")

	_ = cmd.MarkFlagRequired("from-resolved-plan")

	return cmd
}
