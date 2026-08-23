package main

import (
	"context"
	"fmt"
	"sort"
	"strings"

	pb "github.com/whale-net/everything/tools/app_registry/protos"
)

// demoDomain is the one domain FR1's default "all" exclusion applies to,
// matching release.yml's include_demo workflow_dispatch input
// (.github/workflows/release.yml lines ~29-32) -- an explicit domain-name
// or comma-list entry naming "demo" is never filtered; only "all" defaults
// to excluding it.
const demoDomain = "demo"

// releaseTargetKindFromApp mirrors tools/release_helper_go/cmd/metadata.go's
// determineArtifactKind exactly (cli/binary -> BINARY, firmware ->
// FIRMWARE, default -> IMAGE). Deliberately duplicated rather than
// imported: release_helper_go/cmd pulls in cobra and Bazel/workspace-
// dependent helpers (ListAllApps shells out to `bazel query` against a
// local checkout) that make no sense inside this UI's deployed container,
// and matrix.go's isBinaryTool check already establishes the precedent of
// duplicating this exact cli/binary distinction in this package rather
// than importing across that boundary -- see matrix/matrix.go's
// isBinaryTool. Keep both in sync if AppType grows a new value.
func releaseTargetKindFromApp(app *pb.App) pb.ArtifactKind {
	switch app.GetAppType() {
	case "cli", "binary":
		return pb.ArtifactKind_ARTIFACT_KIND_BINARY
	case "firmware":
		return pb.ArtifactKind_ARTIFACT_KIND_FIRMWARE
	default:
		return pb.ArtifactKind_ARTIFACT_KIND_IMAGE
	}
}

// scopeCatalog is the releasable universe a scope string resolves against:
// every ACTIVE app and chart (never MISSING/ARCHIVED -- releasing a target
// the registry no longer sees in the latest manifest reconcile, or that a
// human has already archived, makes no sense). This includes cli/binary
// apps -- see targetsFromAppsAndCharts' doc comment for why they're
// releasable (as IMAGE-kind targets) despite releaseTargetKindFromApp
// reporting BINARY for them. Fetched once per resolveReleaseScope call via
// one ListApps({}) + one ListCharts({}), the same unfiltered-then-filter-
// client-side pattern every other screen in this package already uses (see
// e.g. deployments_data.go, apps_data.go) -- not a second, RPC-side
// reimplementation of domain/status filtering.
type scopeCatalog struct {
	apps   []*pb.App
	charts []*pb.Chart
}

func fetchReleasableCatalog(ctx context.Context, registry *RegistryClient) (*scopeCatalog, error) {
	appsResp, err := registry.App.ListApps(ctx, &pb.ListAppsRequest{})
	if err != nil {
		return nil, fmt.Errorf("ListApps: %w", err)
	}
	chartsResp, err := registry.App.ListCharts(ctx, &pb.ListChartsRequest{})
	if err != nil {
		return nil, fmt.Errorf("ListCharts: %w", err)
	}

	cat := &scopeCatalog{}
	for _, a := range appsResp.GetApps() {
		if a.GetStatus() == pb.AppStatus_APP_STATUS_ACTIVE {
			cat.apps = append(cat.apps, a)
		}
	}
	for _, c := range chartsResp.GetCharts() {
		if c.GetStatus() == pb.AppStatus_APP_STATUS_ACTIVE {
			cat.charts = append(cat.charts, c)
		}
	}
	return cat, nil
}

func filterAppsExcludingDomain(apps []*pb.App, domain string) []*pb.App {
	out := make([]*pb.App, 0, len(apps))
	for _, a := range apps {
		if a.GetDomain() != domain {
			out = append(out, a)
		}
	}
	return out
}

func filterChartsExcludingDomain(charts []*pb.Chart, domain string) []*pb.Chart {
	out := make([]*pb.Chart, 0, len(charts))
	for _, c := range charts {
		if c.GetDomain() != domain {
			out = append(out, c)
		}
	}
	return out
}

// targetsFromAppsAndCharts builds the ReleaseTargetInput list TriggerRelease
// expects (issue #888's Auth scope note: TriggerRelease itself does not
// interpret "all"/domain/comma-list -- this is that expansion). Sorted by
// owner full name so a given scope resolves deterministically regardless of
// ListApps/ListCharts' own ordering.
//
// Every app target's Kind is ARTIFACT_KIND_IMAGE, regardless of the app's
// own AppType (cli/binary apps included) -- NOT releaseTargetKindFromApp's
// BINARY/FIRMWARE mapping, which answers a different question (what kind of
// build artifact this app produces, for apps_data.go's artifact lookups).
// At the release_run/TriggerRelease level, "image" vs "chart" only
// distinguishes an app target from a chart target (repository.
// ReleaseRunTarget.Kind's doc comment: "ArtifactKindImage or
// ArtifactKindChart only"); the image-vs-CLI-binary distinction is resolved
// downstream, per full_name, driven by the app's AppType -- ResolvePlan
// (worker/release/plan.go) passes AppType straight through to
// `release_helper_go plan`, and FinalizePublish's cliBinaryTargets map
// (worker/release/finalize.go) already routes known cli apps (e.g.
// "tools-release_helper_go", "tools-app-registry") to an S3 binary publish
// instead of a GHCR image retag. Mapping cli/binary apps to
// ARTIFACT_KIND_BINARY here would make TriggerRelease reject them outright
// (server/handlers/release.go only accepts IMAGE/CHART), hiding release
// support that already exists end to end.
//
// Firmware apps have no equivalent downstream support (no Firmware case in
// ResolvePlan, no firmware handling in finalize.go) and none are currently
// registered; if one ever is, ResolvePlan's default case reports it as an
// unsupported target kind.
func targetsFromAppsAndCharts(apps []*pb.App, charts []*pb.Chart) []*pb.ReleaseTargetInput {
	targets := make([]*pb.ReleaseTargetInput, 0, len(apps)+len(charts))
	for _, a := range apps {
		targets = append(targets, &pb.ReleaseTargetInput{
			OwnerFullName: a.GetFullName(),
			Kind:          pb.ArtifactKind_ARTIFACT_KIND_IMAGE,
		})
	}
	for _, c := range charts {
		targets = append(targets, &pb.ReleaseTargetInput{
			OwnerFullName: c.GetFullName(),
			Kind:          pb.ArtifactKind_ARTIFACT_KIND_CHART,
		})
	}
	sort.Slice(targets, func(i, j int) bool {
		return targets[i].GetOwnerFullName() < targets[j].GetOwnerFullName()
	})
	return targets
}

// resolveReleaseScope expands rawScope -- "all", a single domain name, or a
// comma-separated list of app/chart names (full name, short name, or a
// domain name as one list entry) -- into the already-expanded target list
// TriggerRelease (#888) requires, following the exact grammar
// release.yml's apps/helm_charts workflow_dispatch inputs and
// release-plan's matrix resolution already use (release_helper_go/cmd/
// plan.go's resolveApps/selectHelmCharts), just unified across apps and
// charts in one field instead of two. includeDemo mirrors release.yml's
// include_demo input: only "all" is demo-filtered by default; an explicit
// domain or list entry naming "demo" is never filtered.
//
// This does not reimplement domain-membership logic from scratch -- it
// resolves entirely against ListApps/ListCharts (fetchReleasableCatalog),
// the registry's own live view of app/chart identity, rather than a second
// Bazel-query-based source of truth (which release_helper_go's ListAllApps
// uses and which is not reachable from this UI's deployed container).
func resolveReleaseScope(ctx context.Context, registry *RegistryClient, rawScope string, includeDemo bool) ([]*pb.ReleaseTargetInput, error) {
	scope := strings.TrimSpace(rawScope)
	if scope == "" {
		return nil, fmt.Errorf(`scope is required ("all", a domain name, or a comma-separated app/chart list)`)
	}

	cat, err := fetchReleasableCatalog(ctx, registry)
	if err != nil {
		return nil, err
	}

	if strings.EqualFold(scope, "all") {
		apps, charts := cat.apps, cat.charts
		if !includeDemo {
			apps = filterAppsExcludingDomain(apps, demoDomain)
			charts = filterChartsExcludingDomain(charts, demoDomain)
		}
		if len(apps) == 0 && len(charts) == 0 {
			return nil, fmt.Errorf(`scope "all" resolved to no releasable apps or charts`)
		}
		return targetsFromAppsAndCharts(apps, charts), nil
	}

	return resolveScopeTokens(scope, cat)
}

// resolveScopeTokens resolves a comma-separated scope string against cat.
// Each token is tried, in order, as: an exact full name ("<domain>-<name>"),
// a domain name (every releasable app+chart under it), then a short
// (unqualified) name -- which must be unique across apps and charts
// combined, mirroring release_helper_go's resolveApps ambiguity rule
// (plan.go). A token matching both an app and a chart by full name, or
// matching more than one entity by short name, is reported as ambiguous
// rather than silently picking one (same "invalid apps" error-accumulation
// shape as resolveApps, so every bad token in one request is reported
// together instead of one-at-a-time).
func resolveScopeTokens(scope string, cat *scopeCatalog) ([]*pb.ReleaseTargetInput, error) {
	byFullApp := make(map[string]*pb.App, len(cat.apps))
	byNameApp := make(map[string][]*pb.App, len(cat.apps))
	byDomainApp := make(map[string][]*pb.App, len(cat.apps))
	for _, a := range cat.apps {
		byFullApp[a.GetFullName()] = a
		byNameApp[a.GetName()] = append(byNameApp[a.GetName()], a)
		byDomainApp[a.GetDomain()] = append(byDomainApp[a.GetDomain()], a)
	}

	byFullChart := make(map[string]*pb.Chart, len(cat.charts))
	byNameChart := make(map[string][]*pb.Chart, len(cat.charts))
	byDomainChart := make(map[string][]*pb.Chart, len(cat.charts))
	for _, c := range cat.charts {
		byFullChart[c.GetFullName()] = c
		byNameChart[c.GetName()] = append(byNameChart[c.GetName()], c)
		byDomainChart[c.GetDomain()] = append(byDomainChart[c.GetDomain()], c)
	}

	var resultApps []*pb.App
	var resultCharts []*pb.Chart
	seenApp := map[string]bool{}
	seenChart := map[string]bool{}
	var invalid []string

	addApp := func(a *pb.App) {
		if !seenApp[a.GetFullName()] {
			seenApp[a.GetFullName()] = true
			resultApps = append(resultApps, a)
		}
	}
	addChart := func(c *pb.Chart) {
		if !seenChart[c.GetFullName()] {
			seenChart[c.GetFullName()] = true
			resultCharts = append(resultCharts, c)
		}
	}

	for _, raw := range strings.Split(scope, ",") {
		token := strings.TrimSpace(raw)
		if token == "" {
			continue
		}

		app, appOK := byFullApp[token]
		chart, chartOK := byFullChart[token]
		if appOK && chartOK {
			invalid = append(invalid, fmt.Sprintf("%s (ambiguous: matches both app %s and chart %s)", token, app.GetFullName(), chart.GetFullName()))
			continue
		}
		if appOK {
			addApp(app)
			continue
		}
		if chartOK {
			addChart(chart)
			continue
		}

		domainApps, hasDomainApps := byDomainApp[token]
		domainCharts, hasDomainCharts := byDomainChart[token]
		if hasDomainApps || hasDomainCharts {
			for _, a := range domainApps {
				addApp(a)
			}
			for _, c := range domainCharts {
				addChart(c)
			}
			continue
		}

		nameApps := byNameApp[token]
		nameCharts := byNameChart[token]
		switch total := len(nameApps) + len(nameCharts); {
		case total == 1 && len(nameApps) == 1:
			addApp(nameApps[0])
		case total == 1 && len(nameCharts) == 1:
			addChart(nameCharts[0])
		case total > 1:
			names := make([]string, 0, total)
			for _, a := range nameApps {
				names = append(names, a.GetFullName()+" (app)")
			}
			for _, c := range nameCharts {
				names = append(names, c.GetFullName()+" (chart)")
			}
			invalid = append(invalid, fmt.Sprintf("%s (ambiguous: %s)", token, strings.Join(names, ", ")))
		default:
			invalid = append(invalid, token)
		}
	}

	if len(invalid) > 0 {
		return nil, fmt.Errorf("invalid or ambiguous scope entries: %s", strings.Join(invalid, "; "))
	}
	if len(resultApps) == 0 && len(resultCharts) == 0 {
		return nil, fmt.Errorf("scope %q resolved to no releasable apps or charts", scope)
	}
	return targetsFromAppsAndCharts(resultApps, resultCharts), nil
}

// pickerKindDomain/pickerKindApp/pickerKindChart are the "kind:value"
// checkbox-value prefixes the release-trigger picker's <input name="target">
// checkboxes use (issue #889 follow-up) -- "domain:<name>" |
// "app:<full_name>" | "chart:<full_name>". pages.PickerToken (release_trigger.
// templ) builds these same strings when rendering the checkboxes; this
// package only parses them back (resolveSelectedTargets below). Duplicated
// rather than shared from a common package, deliberately -- these are three
// string literals, and pages cannot import this (main) package (the
// reverse: main already imports pages) -- same "local, minimal,
// positionally coupled" precedent as e.g. worker/release/plan.go's
// appMetadataInput mirroring release_helper_go's AppMetadataInput.
const (
	pickerKindDomain = "domain"
	pickerKindApp    = "app"
	pickerKindChart  = "chart"
)

// resolveSelectedTargets expands the picker's checkbox selections (already-
// disambiguated domain/app/chart tokens, see pickerToken) into the
// ReleaseTargetInput list TriggerRelease expects -- the picker's counterpart
// to resolveReleaseScope's comma-string grammar above. Selections need no
// short-name ambiguity resolution (unlike resolveScopeTokens): every token
// already names its kind and, for app/chart tokens, its full name exactly
// as the picker rendered it.
func resolveSelectedTargets(ctx context.Context, registry *RegistryClient, selections []string) ([]*pb.ReleaseTargetInput, error) {
	if len(selections) == 0 {
		return nil, fmt.Errorf("select at least one domain, app, or chart to release")
	}

	cat, err := fetchReleasableCatalog(ctx, registry)
	if err != nil {
		return nil, err
	}

	byFullApp := make(map[string]*pb.App, len(cat.apps))
	byDomainApp := make(map[string][]*pb.App, len(cat.apps))
	for _, a := range cat.apps {
		byFullApp[a.GetFullName()] = a
		byDomainApp[a.GetDomain()] = append(byDomainApp[a.GetDomain()], a)
	}
	byFullChart := make(map[string]*pb.Chart, len(cat.charts))
	byDomainChart := make(map[string][]*pb.Chart, len(cat.charts))
	for _, c := range cat.charts {
		byFullChart[c.GetFullName()] = c
		byDomainChart[c.GetDomain()] = append(byDomainChart[c.GetDomain()], c)
	}

	var resultApps []*pb.App
	var resultCharts []*pb.Chart
	seenApp := map[string]bool{}
	seenChart := map[string]bool{}
	addApp := func(a *pb.App) {
		if !seenApp[a.GetFullName()] {
			seenApp[a.GetFullName()] = true
			resultApps = append(resultApps, a)
		}
	}
	addChart := func(c *pb.Chart) {
		if !seenChart[c.GetFullName()] {
			seenChart[c.GetFullName()] = true
			resultCharts = append(resultCharts, c)
		}
	}

	var invalid []string
	for _, token := range selections {
		kind, value, ok := strings.Cut(token, ":")
		if !ok {
			invalid = append(invalid, token)
			continue
		}
		switch kind {
		case pickerKindDomain:
			apps, hasApps := byDomainApp[value]
			charts, hasCharts := byDomainChart[value]
			if !hasApps && !hasCharts {
				invalid = append(invalid, fmt.Sprintf("domain %q has no releasable apps or charts", value))
				continue
			}
			for _, a := range apps {
				addApp(a)
			}
			for _, c := range charts {
				addChart(c)
			}
		case pickerKindApp:
			a, ok := byFullApp[value]
			if !ok {
				invalid = append(invalid, fmt.Sprintf("app %q not found", value))
				continue
			}
			addApp(a)
		case pickerKindChart:
			c, ok := byFullChart[value]
			if !ok {
				invalid = append(invalid, fmt.Sprintf("chart %q not found", value))
				continue
			}
			addChart(c)
		default:
			invalid = append(invalid, token)
		}
	}

	if len(invalid) > 0 {
		return nil, fmt.Errorf("invalid selection: %s", strings.Join(invalid, "; "))
	}
	if len(resultApps) == 0 && len(resultCharts) == 0 {
		return nil, fmt.Errorf("selection resolved to no releasable apps or charts")
	}
	return targetsFromAppsAndCharts(resultApps, resultCharts), nil
}
