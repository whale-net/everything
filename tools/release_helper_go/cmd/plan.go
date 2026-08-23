package cmd

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"
	sharedsemver "github.com/whale-net/everything/libs/go/semver"
	pb "github.com/whale-net/everything/tools/app_registry/protos"
	appmetapb "github.com/whale-net/everything/tools/appmeta/proto"
)

var validEventTypes = []string{"workflow_dispatch", "tag_push", "pull_request", "push", "fallback"}

var semverRE = regexp.MustCompile(`^v(\d+)\.(\d+)\.(\d+)(?:-[a-zA-Z0-9\-\.]+)?$`)

// isBumpKeyword reports whether sel is one of the three per-target bump
// types --version-selections accepts (see newPlanCmd's flag doc).
func isBumpKeyword(sel string) bool {
	return sel == "major" || sel == "minor" || sel == "patch"
}

// isValidVersionSelection reports whether sel is a legal --version-selections
// entry value: a bump keyword, or a literal "vMAJOR.MINOR.PATCH" version.
func isValidVersionSelection(sel string) bool {
	return isBumpKeyword(sel) || semverRE.MatchString(sel)
}

// PlanValidationError represents a typed validation error for plan inputs.
type PlanValidationError struct {
	Field   string
	Message string
}

func (e *PlanValidationError) Error() string {
	return fmt.Sprintf("validation error on %s: %s", e.Field, e.Message)
}

var (
	ErrMutuallyExclusiveVersionOptions = &PlanValidationError{
		Field:   "version_options",
		Message: "options are mutually exclusive: --version, --increment-major, --increment-minor, and --increment-patch",
	}
	ErrInvalidEventType = &PlanValidationError{
		Field:   "event-type",
		Message: "invalid event-type",
	}
	ErrInvalidFormat = &PlanValidationError{
		Field:   "format",
		Message: "invalid format",
	}
	ErrMissingVersionOption = &PlanValidationError{
		Field:   "version_options",
		Message: "manual releases require --version, --increment-major, --increment-minor, or --increment-patch",
	}
	ErrInvalidVersion = &PlanValidationError{
		Field:   "version",
		Message: "version does not follow semantic versioning (vMAJOR.MINOR.PATCH)",
	}
	ErrMissingReleaseTargets = &PlanValidationError{
		Field:   "targets",
		Message: "manual releases require --apps or --charts to be specified",
	}
	ErrInvalidResolvedPlan = &PlanValidationError{
		Field:   "from-resolved-plan",
		Message: "must be valid JSON matching PlanResult's shape (release-helper plan --format=json's own output)",
	}
	ErrMutuallyExclusiveAppsInput = &PlanValidationError{
		Field:   "apps_input",
		Message: "--apps and --apps-metadata are mutually exclusive",
	}
	ErrMutuallyExclusiveChartsInput = &PlanValidationError{
		Field:   "charts_input",
		Message: "--charts/--helm-charts and --charts-metadata are mutually exclusive",
	}
	ErrInvalidAppsMetadata = &PlanValidationError{
		Field:   "apps-metadata",
		Message: "must be a JSON array of {domain, name, app_type}",
	}
	ErrInvalidChartsMetadata = &PlanValidationError{
		Field:   "charts-metadata",
		Message: "must be a JSON array of {domain, name}",
	}
	ErrInvalidVersionSelections = &PlanValidationError{
		Field:   "version-selections",
		Message: `must be a JSON object mapping full_name to "major", "minor", "patch", or "vMAJOR.MINOR.PATCH"`,
	}
)

type PlanResult struct {
	Matrix        map[string]interface{} `json:"matrix"`
	ChartMatrix   map[string]interface{} `json:"chart_matrix,omitempty"`
	OpenAPIMatrix map[string]interface{} `json:"openapi_matrix,omitempty"`
	HasSpecs      bool                   `json:"has_specs"`
	Apps          []string               `json:"apps"`
	Charts        []string               `json:"charts,omitempty"`
	Version       *string                `json:"version"`
	Versions      map[string]string      `json:"versions"`
	BuildID       string                 `json:"build_id,omitempty"`
	EventType     string                 `json:"event_type"`
}

type openAPISpecEntry struct {
	App           string `json:"app"`
	Domain        string `json:"domain"`
	OpenAPITarget string `json:"openapi_target"`
}

func newPlanCmd() *cobra.Command {
	var (
		eventType            string
		apps                 string
		charts               string
		helmCharts           string
		appsMetadata         string
		chartsMetadata       string
		versionSelections    string
		version              string
		incrementMajor       bool
		incrementMinor       bool
		incrementPatch       bool
		baseCommit           string
		format               string
		includeDemo          bool
		dryRun               bool
		gitSHA               string
		gitRef               string
		workflowRunID        string
		workflowAttempt      int
		actor                string
		idempotencyKeyPrefix string
		skipRegistry         bool
		fromResolvedPlan     string
	)

	cmd := &cobra.Command{
		Use:          "plan",
		Short:        "Plan a release and output CI matrix",
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			if format != "json" && format != "github" {
				fmt.Fprintf(cmd.ErrOrStderr(), "Error: format must be one of: json, github\n")
				return ErrInvalidFormat
			}

			var result *PlanResult

			if fromResolvedPlan != "" {
				// Issue #929/#927: skip planRelease() entirely -- no bazel
				// calls, no registry queries, no git operations, no
				// workspace-root discovery. fromResolvedPlan is expected to
				// be exactly `release-helper plan --format=json`'s own
				// stdout (this file's PlanResult JSON shape), forwarded
				// verbatim by App Registry's Temporal DispatchBuild as the
				// `resolved_plan` workflow_dispatch input (see
				// tools/app_registry/worker/release/activities.go's
				// DispatchBuild and plan.go's ResolvePlan). Unmarshal it
				// directly and fall through to the same --format json/github
				// emission below, so this function stays the single place
				// that knows PlanResult's field mapping -- eliminating
				// release-v2.yml's hand-written jq mirror of the same
				// mapping (which required checkout + bazel setup to run
				// `release-helper plan` fresh just to re-derive an
				// equivalent PlanResult).
				var parsed PlanResult
				if err := json.Unmarshal([]byte(fromResolvedPlan), &parsed); err != nil {
					fmt.Fprintf(cmd.ErrOrStderr(), "Error: --from-resolved-plan is not valid JSON: %v\n", err)
					return fmt.Errorf("%v: %w", err, ErrInvalidResolvedPlan)
				}
				result = &parsed
			} else {
				// Input validation (no Bazel calls needed)
				if !isValidEventType(eventType) {
					fmt.Fprintf(cmd.ErrOrStderr(), "Error: event-type must be one of: %s\n", joinStrings(validEventTypes))
					return ErrInvalidEventType
				}
				versionOpts := boolCount(version != "", incrementMajor, incrementMinor, incrementPatch)
				if versionOpts > 1 {
					fmt.Fprintf(cmd.ErrOrStderr(), "Error: --version, --increment-major, --increment-minor, and --increment-patch are mutually exclusive\n")
					return ErrMutuallyExclusiveVersionOptions
				}
				// versionSelections (--version-selections) satisfies this
				// requirement on its own when it covers every requested
				// target -- e.g. ResolvePlan omits every batch-wide version
				// flag when every ReleaseTarget carries its own
				// VersionSelection (see that activity's doc comment). A
				// versionSelections that only covers *some* targets must
				// still be paired with a batch-wide flag by the caller (as
				// ResolvePlan itself always does) for the remaining
				// targets -- not re-validated here per-target, since that
				// would require the app/chart list this early in
				// validation, before ListAllApps/AppMetadataFromInputs run.
				if eventType == "workflow_dispatch" && versionOpts == 0 && versionSelections == "" {
					fmt.Fprintf(cmd.ErrOrStderr(), "Error: manual releases require --version, --increment-major, --increment-minor, --increment-patch, or --version-selections\n")
					return ErrMissingVersionOption
				}
				if version != "" && version != "latest" && !semverRE.MatchString(version) {
					fmt.Fprintf(cmd.ErrOrStderr(), "Error: version %q does not follow semantic versioning (vMAJOR.MINOR.PATCH)\n", version)
					return ErrInvalidVersion
				}
				if apps != "" && appsMetadata != "" {
					fmt.Fprintln(cmd.ErrOrStderr(), "Error: --apps and --apps-metadata are mutually exclusive")
					return ErrMutuallyExclusiveAppsInput
				}
				effectiveCharts := charts
				if effectiveCharts == "" && helmCharts != "" {
					effectiveCharts = helmCharts
				}
				if effectiveCharts != "" && chartsMetadata != "" {
					fmt.Fprintln(cmd.ErrOrStderr(), "Error: --charts/--helm-charts and --charts-metadata are mutually exclusive")
					return ErrMutuallyExclusiveChartsInput
				}

				var parsedAppsMetadata []AppMetadataInput
				if appsMetadata != "" {
					if err := json.Unmarshal([]byte(appsMetadata), &parsedAppsMetadata); err != nil {
						fmt.Fprintf(cmd.ErrOrStderr(), "Error: --apps-metadata is not valid JSON: %v\n", err)
						return fmt.Errorf("%v: %w", err, ErrInvalidAppsMetadata)
					}
				}
				var parsedChartsMetadata []HelmChartMetadataInput
				if chartsMetadata != "" {
					if err := json.Unmarshal([]byte(chartsMetadata), &parsedChartsMetadata); err != nil {
						fmt.Fprintf(cmd.ErrOrStderr(), "Error: --charts-metadata is not valid JSON: %v\n", err)
						return fmt.Errorf("%v: %w", err, ErrInvalidChartsMetadata)
					}
				}

				var parsedVersionSelections map[string]string
				if versionSelections != "" {
					if err := json.Unmarshal([]byte(versionSelections), &parsedVersionSelections); err != nil {
						fmt.Fprintf(cmd.ErrOrStderr(), "Error: --version-selections is not valid JSON: %v\n", err)
						return fmt.Errorf("%v: %w", err, ErrInvalidVersionSelections)
					}
					for fullName, sel := range parsedVersionSelections {
						if !isValidVersionSelection(sel) {
							fmt.Fprintf(cmd.ErrOrStderr(), "Error: --version-selections entry %q has invalid value %q (must be \"major\", \"minor\", \"patch\", or \"vMAJOR.MINOR.PATCH\")\n", fullName, sel)
							return ErrInvalidVersionSelections
						}
					}
				}

				// Skip the eager bazel-workspace-root check when every
				// requested target is satisfied by an explicit metadata
				// input (issue #889 follow-up: ResolvePlan's real call path
				// no longer needs a full monorepo checkout -- see
				// tools/app_registry/worker/release/plan.go's package doc
				// comment). Any bazel-driven discovery path (a name list, an
				// "all" expansion, or any non-workflow_dispatch event type)
				// still needs it -- and executeAppRegistryUpfront's own
				// best-effort AssertApps re-sweep still calls
				// ListAllApps/ListAllHelmCharts independently of this
				// variable and degrades gracefully (see its doc comment) if
				// bazel isn't available there.
				needsBazelWorkspace := eventType != "workflow_dispatch" ||
					(apps != "" && parsedAppsMetadata == nil) ||
					(effectiveCharts != "" && parsedChartsMetadata == nil)

				var workspaceRoot string
				var err error
				if needsBazelWorkspace {
					workspaceRoot, err = defaultWorkspaceRoot()
					if err != nil {
						return fmt.Errorf("workspace root: %w", err)
					}
				}

				result, err = planRelease(planParams{
					ctx:                  cmd.Context(),
					eventType:            eventType,
					requestedApps:        apps,
					requestedCharts:      effectiveCharts,
					appsMetadata:         parsedAppsMetadata,
					chartsMetadata:       parsedChartsMetadata,
					versionSelections:    parsedVersionSelections,
					version:              version,
					incrementMajor:       incrementMajor,
					incrementMinor:       incrementMinor,
					incrementPatch:       incrementPatch,
					baseCommit:           baseCommit,
					includeDemo:          includeDemo,
					dryRun:               dryRun,
					gitSHA:               gitSHA,
					gitRef:               gitRef,
					workflowRunID:        workflowRunID,
					workflowAttempt:      workflowAttempt,
					actor:                actor,
					idempotencyKeyPrefix: idempotencyKeyPrefix,
					skipRegistry:         skipRegistry,
					bazel:                defaultBazel,
					git:                  defaultGit,
					fs:                   defaultFS,
					workspaceRoot:        workspaceRoot,
				})
				if err != nil {
					fmt.Fprintf(cmd.ErrOrStderr(), "Error: %v\n", err)
					return err
				}
			}

			if format == "github" {
				matrixJSON, _ := json.Marshal(result.Matrix)
				fmt.Fprintf(cmd.OutOrStdout(), "matrix=%s\n", matrixJSON)
				fmt.Fprintf(cmd.OutOrStdout(), "apps=%s\n", strings.Join(result.Apps, " "))
				if result.Version != nil {
					fmt.Fprintf(cmd.OutOrStdout(), "version=%s\n", *result.Version)
				}
				if result.ChartMatrix != nil {
					chartMatrixJSON, _ := json.Marshal(result.ChartMatrix)
					fmt.Fprintf(cmd.OutOrStdout(), "chart_matrix=%s\n", chartMatrixJSON)
					fmt.Fprintf(cmd.OutOrStdout(), "charts=%s\n", strings.Join(result.Charts, " "))
				}
				if result.OpenAPIMatrix != nil {
					openapiMatrixJSON, _ := json.Marshal(result.OpenAPIMatrix)
					fmt.Fprintf(cmd.OutOrStdout(), "openapi_matrix=%s\n", openapiMatrixJSON)
					if result.HasSpecs {
						fmt.Fprintln(cmd.OutOrStdout(), "has_specs=true")
					} else {
						fmt.Fprintln(cmd.OutOrStdout(), "has_specs=false")
					}
				}
				if result.BuildID != "" {
					fmt.Fprintf(cmd.OutOrStdout(), "build_id=%s\n", result.BuildID)
				}
				if len(result.Versions) > 0 {
					versionsJSON, _ := json.Marshal(result.Versions)
					fmt.Fprintf(cmd.OutOrStdout(), "versions=%s\n", versionsJSON)
				}
				return nil
			}
			enc := json.NewEncoder(cmd.OutOrStdout())
			enc.SetIndent("", "  ")
			return enc.Encode(result)
		},
	}

	cmd.Flags().StringVar(&eventType, "event-type", "", "Type of trigger event")
	cmd.Flags().StringVar(&apps, "apps", "", "Comma-separated list of apps, domain names, or 'all'")
	cmd.Flags().StringVar(&charts, "charts", "", "Comma-separated list of charts, domain names, or 'all'")
	cmd.Flags().StringVar(&helmCharts, "helm-charts", "", "Alias for --charts")
	cmd.Flags().StringVar(&appsMetadata, "apps-metadata", "", "JSON array of {domain, name, app_type} -- bypasses bazel discovery entirely, for a caller that already resolved this exact app list (e.g. App Registry's ResolvePlan); mutually exclusive with --apps")
	cmd.Flags().StringVar(&chartsMetadata, "charts-metadata", "", "JSON array of {domain, name} -- bypasses bazel discovery entirely, for a caller that already resolved this exact chart list; mutually exclusive with --charts/--helm-charts")
	cmd.Flags().StringVar(&versionSelections, "version-selections", "", `JSON object mapping full_name to "major"/"minor"/"patch" (that target's own bump type) or "vMAJOR.MINOR.PATCH" (hardcoded version for that target) -- per-target override of --version/--increment-*, which stays the default for any target with no entry here`)
	cmd.Flags().StringVar(&version, "version", "", "Release version")
	cmd.Flags().BoolVar(&incrementMajor, "increment-major", false, "Auto-increment major version")
	cmd.Flags().BoolVar(&incrementMinor, "increment-minor", false, "Auto-increment minor version")
	cmd.Flags().BoolVar(&incrementPatch, "increment-patch", false, "Auto-increment patch version")
	cmd.Flags().StringVar(&baseCommit, "base-commit", "", "Compare changes against this commit")
	cmd.Flags().StringVar(&format, "format", "json", "Output format (json or github)")
	cmd.Flags().BoolVar(&includeDemo, "include-demo", false, "Include demo domain apps/charts when using 'all'")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "Dry run - plan without mutating App Registry")
	cmd.Flags().StringVar(&gitSHA, "git-sha", "", "Git commit SHA")
	cmd.Flags().StringVar(&gitRef, "git-ref", "", "Git ref (branch or tag)")
	cmd.Flags().StringVar(&workflowRunID, "workflow-run-id", "", "GitHub Actions workflow run ID")
	cmd.Flags().IntVar(&workflowAttempt, "workflow-attempt", 1, "GitHub Actions workflow attempt")
	cmd.Flags().StringVar(&actor, "actor", "", "GitHub Actions actor")
	cmd.Flags().StringVar(&idempotencyKeyPrefix, "idempotency-key-prefix", "", "Idempotency key prefix")
	cmd.Flags().BoolVar(&skipRegistry, "skip-registry", false, "Skip App Registry upfront operations")
	cmd.Flags().StringVar(&fromResolvedPlan, "from-resolved-plan", "", "Skip planning and format a pre-resolved plan JSON (release-helper plan --format=json's own output shape, e.g. App Registry's resolved_plan) directly -- no bazel/registry/git calls")
	return cmd
}

type planParams struct {
	ctx             context.Context
	eventType       string
	requestedApps   string
	requestedCharts string
	// appsMetadata/chartsMetadata (issue #889 follow-up), when non-nil,
	// bypass ListAllApps/ListAllHelmCharts' bazel query entirely -- see
	// AppMetadataFromInputs/HelmChartMetadataFromInputs's doc comments. An
	// alternative to requestedApps/requestedCharts (a bazel-discovered name
	// list), not a companion to it -- newPlanCmd validates the two are
	// mutually exclusive per app/chart axis before planRelease is called.
	appsMetadata           []AppMetadataInput
	chartsMetadata         []HelmChartMetadataInput
	// versionSelections (issue #889 follow-up) is a per-target override of
	// version/incrementMajor/incrementMinor/incrementPatch below, keyed by
	// full_name. A target present here uses its own entry instead of the
	// batch-wide flags; a target absent from it falls back to those flags
	// unchanged -- see assignVersions/assignChartVersions for the precedence
	// order (explicit vX.Y.Z wins, then a bump keyword, then the batch
	// default).
	versionSelections map[string]string
	version                string
	incrementMajor         bool
	incrementMinor         bool
	incrementPatch         bool
	baseCommit             string
	includeDemo            bool
	dryRun                 bool
	gitSHA                 string
	gitRef                 string
	workflowRunID          string
	workflowAttempt        int
	actor                  string
	idempotencyKeyPrefix   string
	skipRegistry           bool
	bazel                  BazelRunner
	git                    GitRunner
	fs                     FileSystem
	workspaceRoot          string
	appRegistryClient      pb.AppRegistryClient
	artifactRegistryClient pb.ArtifactRegistryClient
}

// resolvedAttempt is workflowAttempt, falling back to GITHUB_RUN_ATTEMPT and
// then 1 -- shared by idempotencyPrefix and RecordBuild's WorkflowAttempt so
// both agree on the same value.
func (p planParams) resolvedAttempt() int {
	if p.workflowAttempt > 0 {
		return p.workflowAttempt
	}
	if a := envOrDefault("GITHUB_RUN_ATTEMPT", ""); a != "" {
		if v, err := strconv.Atoi(a); err == nil && v > 0 {
			return v
		}
	}
	return 1
}

// idempotencyPrefix is the base idempotency key every App Registry call in
// a plan run derives its own key from -- registry-backed version resolution
// (assignVersions/assignChartVersions) included, so a retried plan step
// replays the same allocation rather than reserving a second version.
func (p planParams) idempotencyPrefix() string {
	if p.idempotencyKeyPrefix != "" {
		return p.idempotencyKeyPrefix
	}
	runID := p.workflowRunID
	if runID == "" {
		runID = envOrDefault("GITHUB_RUN_ID", "local")
	}
	return fmt.Sprintf("%s-%d", runID, p.resolvedAttempt())
}

// registryOptedIn reports whether this plan run may call App Registry RPCs
// with side effects (AllocateVersion included): opted into CI/CD via
// APP_REGISTRY_CICD_OPT_IN, and neither a dry run (which must not allocate
// a real version, an AllocateVersion side effect) nor --skip-registry.
func (p planParams) registryOptedIn() bool {
	return !p.dryRun && !p.skipRegistry && defaultEnv("APP_REGISTRY_CICD_OPT_IN") == "true"
}

func planRelease(p planParams) (*PlanResult, error) {
	ctx := p.ctx
	if ctx == nil {
		ctx = context.Background()
	}

	var releaseApps []AppMetadata
	perAppVersions := map[string]string{}
	var selectedCharts []HelmChartMetadata
	perChartVersions := map[string]string{}

	switch p.eventType {
	case "workflow_dispatch":
		if p.requestedApps == "" && len(p.appsMetadata) == 0 && p.requestedCharts == "" && len(p.chartsMetadata) == 0 {
			return nil, fmt.Errorf("manual releases require --apps or --charts to be specified: %w", ErrMissingReleaseTargets)
		}

		if p.requestedApps != "" || len(p.appsMetadata) > 0 {
			if len(p.appsMetadata) > 0 {
				// Bazel-free path (issue #889 follow-up): the caller
				// (ResolvePlan) already resolved this exact target list
				// against App Registry -- see AppMetadataFromInputs.
				releaseApps = AppMetadataFromInputs(p.appsMetadata)
			} else {
				allApps, err := ListAllApps(p.bazel, p.fs, p.workspaceRoot)
				if err != nil {
					return nil, err
				}
				if strings.ToLower(p.requestedApps) == "all" {
					releaseApps = allApps
					if !p.includeDemo {
						releaseApps = filterOutDemo(releaseApps)
					}
				} else {
					releaseApps, err = resolveAppsPreferDomain(strings.Split(p.requestedApps, ","), allApps)
					if err != nil {
						return nil, err
					}
				}
			}
			if err := assignVersions(ctx, p, releaseApps, p.version, p.incrementMajor, p.incrementMinor, p.incrementPatch, p.versionSelections, perAppVersions); err != nil {
				return nil, err
			}
		}

		if p.requestedCharts != "" || len(p.chartsMetadata) > 0 {
			if len(p.chartsMetadata) > 0 {
				// Bazel-free path (issue #889 follow-up) -- see
				// HelmChartMetadataFromInputs.
				selectedCharts = HelmChartMetadataFromInputs(p.chartsMetadata)
			} else {
				allCharts, err := ListAllHelmCharts(p.bazel, p.fs, p.workspaceRoot)
				if err != nil {
					return nil, err
				}
				selectedCharts = selectHelmCharts(p.requestedCharts, allCharts, p.includeDemo, nil)
			}
			if err := assignChartVersions(ctx, p, selectedCharts, p.version, p.incrementMajor, p.incrementMinor, p.incrementPatch, p.versionSelections, perChartVersions); err != nil {
				return nil, err
			}
		}

	case "tag_push":
		if p.version == "" {
			return nil, fmt.Errorf("tag push releases require --version")
		}
		if p.baseCommit == "" {
			if prev, err := getPreviousTag(p.git); err == nil {
				p.baseCommit = prev
			}
		}
		var err error
		releaseApps, err = DetectChangedApps(p.baseCommit, p.bazel, p.git, p.fs, p.workspaceRoot)
		if err != nil {
			return nil, err
		}

	case "pull_request", "push":
		var err error
		if p.baseCommit == "" {
			releaseApps, err = ListAllApps(p.bazel, p.fs, p.workspaceRoot)
		} else {
			releaseApps, err = DetectChangedApps(p.baseCommit, p.bazel, p.git, p.fs, p.workspaceRoot)
		}
		if err != nil {
			return nil, err
		}

	case "fallback":
		var err error
		releaseApps, err = ListAllApps(p.bazel, p.fs, p.workspaceRoot)
		if err != nil {
			return nil, err
		}
	}

	// Identify apps with OpenAPI specs
	var appsWithSpecs []openAPISpecEntry
	for _, a := range releaseApps {
		if a.OpenapiSpecTarget != "" {
			appsWithSpecs = append(appsWithSpecs, openAPISpecEntry{
				App:           a.Name,
				Domain:        a.Domain,
				OpenAPITarget: a.OpenapiSpecTarget,
			})
		}
	}

	result := buildPlanResult(releaseApps, selectedCharts, appsWithSpecs, p.version, p.eventType, perAppVersions, perChartVersions)

	if err := executeAppRegistryUpfront(ctx, p, releaseApps, selectedCharts, perAppVersions, perChartVersions, result); err != nil {
		return nil, err
	}

	return result, nil
}

func assignChartVersions(ctx context.Context, p planParams, charts []HelmChartMetadata, version string, major, minor, patch bool, versionSelections map[string]string, out map[string]string) error {
	var client pb.ArtifactRegistryClient
	if p.registryOptedIn() {
		c, closeFn, err := dialVersioningClient(ctx, p.artifactRegistryClient)
		if err != nil {
			return err
		}
		defer closeFn() //nolint:errcheck
		client = c
	}
	idemPrefix := p.idempotencyPrefix()

	for i := range charts {
		fullName := charts[i].FullName()

		// Per-target override (issue #889 follow-up): an explicit
		// "vX.Y.Z" entry wins outright, no registry/git call for this
		// target. A bump-keyword entry ("major"/"minor"/"patch") picks
		// this target's own bump type, overriding the batch-wide
		// major/minor/patch flags below for this target only.
		targetVersion, targetMajor, targetMinor, targetPatch := version, major, minor, patch
		if sel, ok := versionSelections[fullName]; ok {
			if isBumpKeyword(sel) {
				targetVersion = ""
				targetMajor, targetMinor, targetPatch = sel == "major", sel == "minor", sel == "patch"
			} else {
				out[fullName] = sel
				continue
			}
		}

		if targetVersion != "" {
			out[fullName] = targetVersion
			continue
		}
		if targetMajor || targetMinor || targetPatch {
			bumpType := "patch"
			switch {
			case targetMajor:
				bumpType = "major"
			case targetMinor:
				bumpType = "minor"
			}
			publishedName := strings.TrimPrefix(charts[i].Name, "helm-")
			newVer, _, err := resolveVersion(ctx, client, pb.ArtifactKind_ARTIFACT_KIND_CHART, publishedName, bumpType,
				fmt.Sprintf("%s-%s-allocate", idemPrefix, publishedName),
				func() (string, error) { return autoIncrementHelmVersion(charts[i].Name, bumpType, p.git) },
			)
			if err != nil {
				return fmt.Errorf("auto-increment for chart %s: %w", charts[i].Name, err)
			}
			out[fullName] = newVer
		} else {
			out[fullName] = "v0.1.0"
		}
	}
	return nil
}

func executeAppRegistryUpfront(ctx context.Context, p planParams, releaseApps []AppMetadata, selectedCharts []HelmChartMetadata, perAppVersions, perChartVersions map[string]string, result *PlanResult) error {
	if p.dryRun || p.skipRegistry {
		return nil
	}

	sha := p.gitSHA
	if sha == "" {
		sha = envOrDefault("GITHUB_SHA", "")
	}
	if sha == "" && p.git != nil {
		if out, err := p.git.Run("rev-parse", "HEAD"); err == nil {
			sha = strings.TrimSpace(out)
		}
	}

	ref := p.gitRef
	if ref == "" {
		ref = envOrDefault("GITHUB_REF", "")
	}
	if ref == "" && p.git != nil {
		if out, err := p.git.Run("rev-parse", "--abbrev-ref", "HEAD"); err == nil {
			ref = strings.TrimSpace(out)
		}
	}

	runID := p.workflowRunID
	if runID == "" {
		runID = envOrDefault("GITHUB_RUN_ID", "local")
	}

	act := p.actor
	if act == "" {
		act = envOrDefault("GITHUB_ACTOR", "release-helper")
	}

	idemPrefix := p.idempotencyPrefix()

	// 1. AssertApps
	var appClient pb.AppRegistryClient
	if p.appRegistryClient != nil {
		appClient = p.appRegistryClient
	} else {
		c, closeApp, err := NewAppRegistryClient(ctx)
		if err == nil {
			appClient = c
			if closeApp != nil {
				defer closeApp() //nolint:errcheck
			}
		}
	}

	if appClient != nil {
		allApps, err := ListAllApps(p.bazel, p.fs, p.workspaceRoot)
		if err == nil {
			allCharts, err := ListAllHelmCharts(p.bazel, p.fs, p.workspaceRoot)
			if err == nil {
				set := &appmetapb.AppManifestSet{
					GitSha:            sha,
					DiscoveredAt:      time.Now().Unix(),
					SourceCommittedAt: resolveSourceCommittedAt(p.git, sha),
				}
				for i := range allApps {
					set.Apps = append(set.Apps, allApps[i].AppManifest)
				}
				for i := range allCharts {
					set.Charts = append(set.Charts, allCharts[i].ChartManifest)
				}
				_, _ = appClient.AssertApps(ctx, &pb.AssertAppsRequest{
					Manifests:      set,
					IdempotencyKey: fmt.Sprintf("%s-assert", idemPrefix),
				})
			}
		}
	}

	// 2. RecordBuild
	var artClient pb.ArtifactRegistryClient
	if p.artifactRegistryClient != nil {
		artClient = p.artifactRegistryClient
	} else {
		c, closeArt, err := NewArtifactRegistryClient(ctx)
		if err == nil {
			artClient = c
			if closeArt != nil {
				defer closeArt() //nolint:errcheck
			}
		}
	}

	if artClient != nil {
		buildResp, err := artClient.RecordBuild(ctx, &pb.RecordBuildRequest{
			GitSha:          sha,
			GitRef:          ref,
			WorkflowRunId:   runID,
			WorkflowAttempt: int32(p.resolvedAttempt()),
			Actor:           act,
			StartedAt:       time.Now().Unix(),
			IdempotencyKey:  idemPrefix,
		})
		if err == nil && buildResp != nil && buildResp.Build != nil {
			result.BuildID = buildResp.Build.BuildId
		}

		// 3. BeginPublishBatch
		if result.BuildID != "" && (len(releaseApps) > 0 || len(selectedCharts) > 0) {
			var batchTargets []*pb.BeginPublishBatchTarget
			for _, app := range releaseApps {
				v := perAppVersions[app.FullName()]
				if v == "" && p.version != "" {
					v = p.version
				}
				kind := determineArtifactKind(app)
				batchTargets = append(batchTargets, &pb.BeginPublishBatchTarget{
					Kind:          kind,
					OwnerFullName: app.FullName(),
					Version:       v,
				})
			}
			for _, chart := range selectedCharts {
				v := perChartVersions[chart.FullName()]
				if v == "" && p.version != "" {
					v = p.version
				}
				batchTargets = append(batchTargets, &pb.BeginPublishBatchTarget{
					Kind:          pb.ArtifactKind_ARTIFACT_KIND_CHART,
					OwnerFullName: chart.FullName(),
					Version:       v,
				})
			}

			if len(batchTargets) > 0 {
				_, _ = artClient.BeginPublishBatch(ctx, &pb.BeginPublishBatchRequest{
					BuildId:              result.BuildID,
					Targets:              batchTargets,
					IdempotencyKeyPrefix: idemPrefix,
				})
			}
		}
	}

	return nil
}

// buildPlanResult assembles the CI matrix. perAppVersions and perChartVersions
// override version on a per-app/chart basis.
func buildPlanResult(
	apps []AppMetadata,
	charts []HelmChartMetadata,
	specs []openAPISpecEntry,
	version, eventType string,
	perAppVersions, perChartVersions map[string]string,
) *PlanResult {
	include := make([]map[string]string, 0, len(apps))
	appNames := make([]string, 0, len(apps))
	versions := make(map[string]string, len(apps)+len(charts))

	for _, app := range apps {
		fullName := app.FullName()
		v := version
		if assigned, ok := perAppVersions[fullName]; ok {
			v = assigned
		}
		include = append(include, map[string]string{
			"app":          app.Name,
			"domain":       app.Domain,
			"bazel_target": app.BazelTarget,
			"version":      v,
		})
		appNames = append(appNames, fullName)
		versions[fullName] = v
	}

	chartInclude := make([]map[string]string, 0, len(charts))
	chartNames := make([]string, 0, len(charts))
	for _, chart := range charts {
		fullName := chart.FullName()
		v := version
		if assigned, ok := perChartVersions[fullName]; ok {
			v = assigned
		}
		chartInclude = append(chartInclude, map[string]string{
			"chart":        chart.Name,
			"domain":       chart.Domain,
			"bazel_target": chart.BazelTarget,
			"version":      v,
		})
		chartNames = append(chartNames, chart.Name)
		versions[fullName] = v
	}

	openapiInclude := make([]map[string]string, 0, len(specs))
	for _, spec := range specs {
		openapiInclude = append(openapiInclude, map[string]string{
			"app":            spec.App,
			"domain":         spec.Domain,
			"openapi_target": spec.OpenAPITarget,
		})
	}

	var versionPtr *string
	if version != "" {
		versionPtr = &version
	}

	res := &PlanResult{
		Matrix:    map[string]interface{}{"include": include},
		Apps:      appNames,
		Version:   versionPtr,
		Versions:  versions,
		EventType: eventType,
	}

	if len(chartInclude) > 0 {
		res.ChartMatrix = map[string]interface{}{"include": chartInclude}
		res.Charts = chartNames
	}
	if len(openapiInclude) > 0 {
		res.OpenAPIMatrix = map[string]interface{}{"include": openapiInclude}
		res.HasSpecs = true
	}

	return res
}

// assignVersions resolves per-app versions either from the explicit version
// flag or by auto-incrementing based on git tags, recording each into out
// (keyed by the app's FullName). AppManifest.Version — the manifest's own
// declared default (normally "latest") — is left untouched: it describes
// the app definition, not the version being planned for this release.
func assignVersions(ctx context.Context, p planParams, apps []AppMetadata, version string, major, minor, patch bool, versionSelections map[string]string, out map[string]string) error {
	var client pb.ArtifactRegistryClient
	if p.registryOptedIn() {
		c, closeFn, err := dialVersioningClient(ctx, p.artifactRegistryClient)
		if err != nil {
			return err
		}
		defer closeFn() //nolint:errcheck
		client = c
	}
	idemPrefix := p.idempotencyPrefix()

	for i := range apps {
		fullName := apps[i].FullName()

		// Per-target override (issue #889 follow-up) -- see
		// assignChartVersions' identical precedence-order comment.
		targetVersion, targetMajor, targetMinor, targetPatch := version, major, minor, patch
		if sel, ok := versionSelections[fullName]; ok {
			if isBumpKeyword(sel) {
				targetVersion = ""
				targetMajor, targetMinor, targetPatch = sel == "major", sel == "minor", sel == "patch"
			} else {
				out[fullName] = sel
				continue
			}
		}

		if targetVersion != "" {
			out[fullName] = targetVersion
			continue
		}
		if targetMajor || targetMinor || targetPatch {
			incrementType := "patch"
			switch {
			case targetMajor:
				incrementType = "major"
			case targetMinor:
				incrementType = "minor"
			}
			domain, name := apps[i].Domain, apps[i].Name
			newVer, _, err := resolveVersion(ctx, client, determineArtifactKind(apps[i]), fullName, incrementType,
				fmt.Sprintf("%s-%s-allocate", idemPrefix, fullName),
				func() (string, error) { return autoIncrementVersion(domain, name, incrementType, p.git) },
			)
			if err != nil {
				return fmt.Errorf("auto-increment for %s: %w", fullName, err)
			}
			out[fullName] = newVer
			continue
		}
		// Reachable now that --version-selections lets workflow_dispatch
		// pass CLI-level validation with versionOpts == 0 (see newPlanCmd):
		// a versionSelections covering only some requested apps, with no
		// batch-wide fallback flag, leaves this app with no version source
		// at all. Fail loudly rather than silently emitting an empty
		// version into the matrix.
		return fmt.Errorf("no version resolved for app %s: no --version-selections entry and no --version/--increment-* fallback", fullName)
	}
	return nil
}

// autoIncrementVersion computes the next version for an app based on git
// tags. This is the live, git-tag-based path AR-5's AllocateVersion is
// meant to eventually replace (per domain, once a domain is cut over to
// adoption stage "allocate") — see tools/app_registry/PLAN.md's AR-5. It is
// deliberately untouched by AR-5a beyond gaining "major" alongside the
// pre-existing "minor"/"patch": no call site here talks to the registry.
func autoIncrementVersion(domain, name, incrementType string, git GitRunner) (string, error) {
	prefix := fmt.Sprintf("%s-%s.", domain, name)
	tagsOut, err := git.Run("tag", "--sort=-version:refname", "--list", prefix+"v*")
	if err != nil {
		// A real git failure (e.g. this process has no working .git at
		// all -- see App Registry's worker/release/plan.go's ResolvePlan,
		// which shells out `release_helper_go plan` from a bare scratch
		// dir) must not be conflated with "ran fine, found zero matching
		// tags": that used to fall through to the v0.0.1/v0.1.0 default
		// below unconditionally, silently reissuing the first version for
		// every app on every call in a tag-less directory regardless of
		// what was actually already published -- observed for manmanv2
		// (six apps all "resolved" to v0.0.1 despite higher published
		// versions existing) whenever AllocateVersion's FailedPrecondition
		// fallback (resolveVersion, registry_version.go) is hit for a
		// domain not yet at App Registry's "allocate" adoption stage.
		return "", fmt.Errorf("list git tags for %s: %w", prefix, err)
	}
	if strings.TrimSpace(tagsOut) == "" {
		if incrementType == "minor" {
			return "v0.1.0", nil
		}
		return "v0.0.1", nil
	}
	latest := strings.SplitN(strings.TrimSpace(tagsOut), "\n", 2)[0]
	ver := strings.TrimPrefix(latest, prefix)
	if !semverRE.MatchString(ver) {
		if incrementType == "minor" {
			return "v0.1.0", nil
		}
		return "v0.0.1", nil
	}
	return incrementVersion(ver, incrementType)
}

// incrementVersion bumps ver (accepting an optional "v" prefix and
// prerelease suffix, which is stripped — matching this function's
// pre-existing behaviour) by incrementType ("major", "minor", or "patch"),
// via the shared libs/go/semver parser rather than a second hand-rolled one.
// See tools/app_registry/PLAN.md's AR-5 addendum item 1: chart bumping
// (build_helm.go) already accepted all three; "major" was the one this
// function was missing.
func incrementVersion(ver, incrementType string) (string, error) {
	v, err := sharedsemver.Parse(ver)
	if err != nil {
		return "", fmt.Errorf("invalid version: %s", strings.TrimPrefix(ver, "v"))
	}
	next, err := v.Increment(incrementType)
	if err != nil {
		return "", err
	}
	return next.String(), nil
}

// resolveApps matches requested app names (full, short, or domain) against
// allApps, preferring an unambiguous app-name match over a same-named
// domain sweep. Use this for callers that resolve one specific, named app
// (package-assets, build-app-adjacent single lookups): a domain and an app
// name can collide (e.g. domain "app-registry" for the server/worker/ui
// components vs. app name "app-registry" for the tools-domain CLI), and
// asking for one app by name means that app, not every app in a
// same-named domain. Callers that build a release matrix from a raw,
// possibly multi-domain --apps list should use resolveAppsPreferDomain
// instead, so a bare domain name that happens to collide with one of its
// own sibling domains' app names still reaches the whole domain.
func resolveApps(requested []string, allApps []AppMetadata) ([]AppMetadata, error) {
	return resolveAppsWithPolicy(requested, allApps, false)
}

// resolveAppsPreferDomain is resolveApps but tries a domain sweep before an
// unambiguous same-named app. See resolveApps' doc comment for when to use
// which: this is for callers assembling a release matrix from a raw --apps
// list (plan's own --apps flag, plan-openapi-builds), where a bare
// "app-registry" must still be able to reach the app-registry *domain*
// (server/migrate/worker/ui) even though "app-registry" is also the literal
// name of an unrelated CLI app that happens to live in the tools domain.
func resolveAppsPreferDomain(requested []string, allApps []AppMetadata) ([]AppMetadata, error) {
	return resolveAppsWithPolicy(requested, allApps, true)
}

func resolveAppsWithPolicy(requested []string, allApps []AppMetadata, preferDomain bool) ([]AppMetadata, error) {
	// Build lookup maps
	byFull := make(map[string]AppMetadata)
	byName := make(map[string][]AppMetadata)
	byDomain := make(map[string][]AppMetadata)

	for _, app := range allApps {
		byFull[app.FullName()] = app
		byFull[app.Domain+"/"+app.Name] = app
		byName[app.Name] = append(byName[app.Name], app)
		byDomain[app.Domain] = append(byDomain[app.Domain], app)
	}

	var result []AppMetadata
	var invalid []string

	for _, req := range requested {
		req = strings.TrimSpace(req)
		if req == "" {
			continue
		}
		if app, ok := byFull[req]; ok {
			result = append(result, app)
			continue
		}
		if preferDomain {
			if domainApps, ok := byDomain[req]; ok {
				result = append(result, domainApps...)
				continue
			}
			if nameApps, ok := byName[req]; ok && len(nameApps) == 1 {
				result = append(result, nameApps[0])
				continue
			}
		} else {
			if nameApps, ok := byName[req]; ok && len(nameApps) == 1 {
				result = append(result, nameApps[0])
				continue
			}
			if domainApps, ok := byDomain[req]; ok {
				result = append(result, domainApps...)
				continue
			}
		}
		if nameApps, ok := byName[req]; ok {
			names := make([]string, len(nameApps))
			for i, a := range nameApps {
				names[i] = a.FullName()
			}
			invalid = append(invalid, fmt.Sprintf("%s (ambiguous: %s)", req, strings.Join(names, ", ")))
			continue
		}
		invalid = append(invalid, req)
	}

	if len(invalid) > 0 {
		return nil, fmt.Errorf("invalid apps: %s", strings.Join(invalid, "; "))
	}

	// Detect duplicates (e.g. domain name + specific app from that domain).
	seen := make(map[string]bool, len(result))
	deduped := make([]AppMetadata, 0, len(result))
	var dups []string
	for _, app := range result {
		full := app.FullName()
		if seen[full] {
			dups = append(dups, full)
			continue
		}
		seen[full] = true
		deduped = append(deduped, app)
	}
	if len(dups) > 0 {
		return nil, fmt.Errorf("duplicate apps in request: %s", strings.Join(dups, ", "))
	}
	return deduped, nil
}

func filterOutDemo(apps []AppMetadata) []AppMetadata {
	var out []AppMetadata
	for _, app := range apps {
		if app.Domain != "demo" {
			out = append(out, app)
		}
	}
	return out
}

func boolCount(bs ...bool) int {
	n := 0
	for _, b := range bs {
		if b {
			n++
		}
	}
	return n
}

func isValidEventType(et string) bool {
	for _, v := range validEventTypes {
		if et == v {
			return true
		}
	}
	return false
}

func joinStrings(ss []string) string {
	return strings.Join(ss, ", ")
}
