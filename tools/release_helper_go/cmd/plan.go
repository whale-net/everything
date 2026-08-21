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
				if eventType == "workflow_dispatch" && versionOpts == 0 {
					fmt.Fprintf(cmd.ErrOrStderr(), "Error: manual releases require --version, --increment-major, --increment-minor, or --increment-patch\n")
					return ErrMissingVersionOption
				}
				if version != "" && version != "latest" && !semverRE.MatchString(version) {
					fmt.Fprintf(cmd.ErrOrStderr(), "Error: version %q does not follow semantic versioning (vMAJOR.MINOR.PATCH)\n", version)
					return ErrInvalidVersion
				}

				workspaceRoot, err := defaultWorkspaceRoot()
				if err != nil {
					return fmt.Errorf("workspace root: %w", err)
				}

				effectiveCharts := charts
				if effectiveCharts == "" && helmCharts != "" {
					effectiveCharts = helmCharts
				}

				result, err = planRelease(planParams{
					ctx:                  cmd.Context(),
					eventType:            eventType,
					requestedApps:        apps,
					requestedCharts:      effectiveCharts,
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
	ctx                    context.Context
	eventType              string
	requestedApps          string
	requestedCharts        string
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
		if p.requestedApps == "" && p.requestedCharts == "" {
			return nil, fmt.Errorf("manual releases require --apps or --charts to be specified: %w", ErrMissingReleaseTargets)
		}

		if p.requestedApps != "" {
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
				releaseApps, err = resolveApps(strings.Split(p.requestedApps, ","), allApps)
				if err != nil {
					return nil, err
				}
			}
			if err := assignVersions(ctx, p, releaseApps, p.version, p.incrementMajor, p.incrementMinor, p.incrementPatch, perAppVersions); err != nil {
				return nil, err
			}
		}

		if p.requestedCharts != "" {
			allCharts, err := ListAllHelmCharts(p.bazel, p.fs, p.workspaceRoot)
			if err != nil {
				return nil, err
			}
			selectedCharts = selectHelmCharts(p.requestedCharts, allCharts, p.includeDemo, nil)
			if err := assignChartVersions(ctx, p, selectedCharts, p.version, p.incrementMajor, p.incrementMinor, p.incrementPatch, perChartVersions); err != nil {
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

func assignChartVersions(ctx context.Context, p planParams, charts []HelmChartMetadata, version string, major, minor, patch bool, out map[string]string) error {
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
		if version != "" {
			out[fullName] = version
			continue
		}
		if major || minor || patch {
			bumpType := "patch"
			switch {
			case major:
				bumpType = "major"
			case minor:
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
func assignVersions(ctx context.Context, p planParams, apps []AppMetadata, version string, major, minor, patch bool, out map[string]string) error {
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
		if version != "" {
			out[apps[i].FullName()] = version
			continue
		}
		if major || minor || patch {
			incrementType := "patch"
			switch {
			case major:
				incrementType = "major"
			case minor:
				incrementType = "minor"
			}
			fullName := apps[i].FullName()
			domain, name := apps[i].Domain, apps[i].Name
			newVer, _, err := resolveVersion(ctx, client, determineArtifactKind(apps[i]), fullName, incrementType,
				fmt.Sprintf("%s-%s-allocate", idemPrefix, fullName),
				func() (string, error) { return autoIncrementVersion(domain, name, incrementType, p.git) },
			)
			if err != nil {
				return fmt.Errorf("auto-increment for %s: %w", fullName, err)
			}
			out[fullName] = newVer
		}
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
	if err != nil || strings.TrimSpace(tagsOut) == "" {
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

// resolveApps matches requested app names (full, short, or domain) against allApps.
func resolveApps(requested []string, allApps []AppMetadata) ([]AppMetadata, error) {
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
		if domainApps, ok := byDomain[req]; ok {
			result = append(result, domainApps...)
			continue
		}
		if nameApps, ok := byName[req]; ok {
			if len(nameApps) == 1 {
				result = append(result, nameApps[0])
				continue
			}
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
