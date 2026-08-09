package cmd

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"

	"github.com/spf13/cobra"

	// Aliased: this package already declares a local (unrelated) `semver`
	// type in cleanup.go for GHCR tag-retention sorting. That one stays as
	// is — it's a different concern (sorting existing published tags for
	// deletion, not allocating new ones) and out of scope for AR-5. This
	// import is the shared parser plan.go's incrementVersion now delegates
	// to instead of its own regex/strings.Split logic — see PLAN.md's AR-5
	// addendum and libs/go/semver's package doc for why it's shared with
	// tools/app_registry instead of copied a second time.
	sharedsemver "github.com/whale-net/everything/libs/go/semver"
)

var validEventTypes = []string{"workflow_dispatch", "tag_push", "pull_request", "push", "fallback"}

var semverRE = regexp.MustCompile(`^v(\d+)\.(\d+)\.(\d+)(?:-[a-zA-Z0-9\-\.]+)?$`)

type PlanResult struct {
	Matrix    map[string]interface{} `json:"matrix"`
	Apps      []string               `json:"apps"`
	Version   *string                `json:"version"`
	Versions  map[string]string      `json:"versions"`
	EventType string                 `json:"event_type"`
}

func newPlanCmd() *cobra.Command {
	var (
		eventType      string
		apps           string
		version        string
		incrementMajor bool
		incrementMinor bool
		incrementPatch bool
		baseCommit     string
		format         string
		includeDemo    bool
	)

	cmd := &cobra.Command{
		Use:          "plan",
		Short:        "Plan a release and output CI matrix",
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			// Input validation (no Bazel calls needed)
			if !isValidEventType(eventType) {
				fmt.Fprintf(cmd.ErrOrStderr(), "Error: event-type must be one of: %s\n", joinStrings(validEventTypes))
				return fmt.Errorf("invalid event-type")
			}
			if format != "json" && format != "github" {
				fmt.Fprintf(cmd.ErrOrStderr(), "Error: format must be one of: json, github\n")
				return fmt.Errorf("invalid format")
			}
			versionOpts := boolCount(version != "", incrementMajor, incrementMinor, incrementPatch)
			if versionOpts > 1 {
				fmt.Fprintf(cmd.ErrOrStderr(), "Error: --version, --increment-major, --increment-minor, and --increment-patch are mutually exclusive\n")
				return fmt.Errorf("mutually exclusive options")
			}
			if eventType == "workflow_dispatch" && versionOpts == 0 {
				fmt.Fprintf(cmd.ErrOrStderr(), "Error: manual releases require --version, --increment-major, --increment-minor, or --increment-patch\n")
				return fmt.Errorf("missing version option")
			}
			if version != "" && version != "latest" && !semverRE.MatchString(version) {
				fmt.Fprintf(cmd.ErrOrStderr(), "Error: version %q does not follow semantic versioning (vMAJOR.MINOR.PATCH)\n", version)
				return fmt.Errorf("invalid version")
			}

			workspaceRoot, err := defaultWorkspaceRoot()
			if err != nil {
				return fmt.Errorf("workspace root: %w", err)
			}

			result, err := planRelease(planParams{
				eventType:      eventType,
				requestedApps:  apps,
				version:        version,
				incrementMajor: incrementMajor,
				incrementMinor: incrementMinor,
				incrementPatch: incrementPatch,
				baseCommit:     baseCommit,
				includeDemo:    includeDemo,
				bazel:          defaultBazel,
				git:            defaultGit,
				fs:             defaultFS,
				workspaceRoot:  workspaceRoot,
			})
			if err != nil {
				fmt.Fprintf(cmd.ErrOrStderr(), "Error: %v\n", err)
				return err
			}

			if format == "github" {
				matrixJSON, _ := json.Marshal(result.Matrix)
				fmt.Fprintf(cmd.OutOrStdout(), "matrix=%s\n", matrixJSON)
				fmt.Fprintf(cmd.OutOrStdout(), "apps=%s\n", strings.Join(result.Apps, " "))
				return nil
			}
			enc := json.NewEncoder(cmd.OutOrStdout())
			enc.SetIndent("", "  ")
			return enc.Encode(result)
		},
	}

	cmd.Flags().StringVar(&eventType, "event-type", "", "Type of trigger event")
	cmd.Flags().StringVar(&apps, "apps", "", "Comma-separated list of apps, domain names, or 'all'")
	cmd.Flags().StringVar(&version, "version", "", "Release version")
	cmd.Flags().BoolVar(&incrementMajor, "increment-major", false, "Auto-increment major version")
	cmd.Flags().BoolVar(&incrementMinor, "increment-minor", false, "Auto-increment minor version")
	cmd.Flags().BoolVar(&incrementPatch, "increment-patch", false, "Auto-increment patch version")
	cmd.Flags().StringVar(&baseCommit, "base-commit", "", "Compare changes against this commit")
	cmd.Flags().StringVar(&format, "format", "json", "Output format (json or github)")
	cmd.Flags().BoolVar(&includeDemo, "include-demo", false, "Include demo domain apps when using 'all'")
	return cmd
}

type planParams struct {
	eventType      string
	requestedApps  string
	version        string
	incrementMajor bool
	incrementMinor bool
	incrementPatch bool
	baseCommit     string
	includeDemo    bool
	bazel          BazelRunner
	git            GitRunner
	fs             FileSystem
	workspaceRoot  string
}

func planRelease(p planParams) (*PlanResult, error) {
	var releaseApps []AppMetadata
	// perAppVersions holds versions assigned per-app (workflow_dispatch's
	// auto-increment path assigns a different version to each app). Empty
	// for every other event type, where every app uses p.version uniformly.
	perAppVersions := map[string]string{}

	switch p.eventType {
	case "workflow_dispatch":
		if p.requestedApps == "" {
			return nil, fmt.Errorf("manual releases require --apps to be specified")
		}
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
		if err := assignVersions(releaseApps, p.version, p.incrementMajor, p.incrementMinor, p.incrementPatch, p.git, perAppVersions); err != nil {
			return nil, err
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
		for i := range releaseApps {
			releaseApps[i].BazelTarget = releaseApps[i].BazelTarget // keep as-is
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

	return buildPlanResult(releaseApps, p.version, p.eventType, perAppVersions), nil
}

// buildPlanResult assembles the CI matrix. perAppVersions overrides version
// on a per-app basis (set by assignVersions for workflow_dispatch's
// auto-increment path); every other app uses version uniformly.
func buildPlanResult(apps []AppMetadata, version, eventType string, perAppVersions map[string]string) *PlanResult {
	include := make([]map[string]string, 0, len(apps))
	appNames := make([]string, 0, len(apps))
	versions := make(map[string]string, len(apps))

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

	var versionPtr *string
	if version != "" {
		versionPtr = &version
	}

	return &PlanResult{
		Matrix:    map[string]interface{}{"include": include},
		Apps:      appNames,
		Version:   versionPtr,
		Versions:  versions,
		EventType: eventType,
	}
}

// assignVersions resolves per-app versions either from the explicit version
// flag or by auto-incrementing based on git tags, recording each into out
// (keyed by the app's FullName). AppManifest.Version — the manifest's own
// declared default (normally "latest") — is left untouched: it describes
// the app definition, not the version being planned for this release.
func assignVersions(apps []AppMetadata, version string, major, minor, patch bool, git GitRunner, out map[string]string) error {
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
			newVer, err := autoIncrementVersion(apps[i].Domain, apps[i].Name, incrementType, git)
			if err != nil {
				return fmt.Errorf("auto-increment for %s: %w", apps[i].FullName(), err)
			}
			out[apps[i].FullName()] = newVer
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
