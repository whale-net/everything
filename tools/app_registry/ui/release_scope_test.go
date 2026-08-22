package main

import (
	"context"
	"strings"
	"testing"

	"google.golang.org/grpc"

	pb "github.com/whale-net/everything/tools/app_registry/protos"
)

// This file directly unit-tests resolveReleaseScope (release_scope.go) --
// FR1's "all" / single-domain / comma-list grammar, including default
// demo-domain exclusion (FR1) and the malformed/ambiguous-entry cases the
// handler layer doesn't otherwise exercise. handlers_release_test.go's own
// doc comment claims this coverage exists ("release_scope_test.go already
// covers"); before this file, it did not -- resolveReleaseScope had no
// direct test coverage at all, only incidental coverage through two
// handler tests (a single full-name scope and a two-app domain scope).

// scopeTestClient is a minimal stand-in for pb.AppRegistryClient, feeding
// resolveReleaseScope a fixed apps+charts catalog.
type scopeTestClient struct {
	pb.AppRegistryClient
	apps   []*pb.App
	charts []*pb.Chart
}

func (f *scopeTestClient) ListApps(ctx context.Context, in *pb.ListAppsRequest, opts ...grpc.CallOption) (*pb.ListAppsResponse, error) {
	return &pb.ListAppsResponse{Apps: f.apps}, nil
}

func (f *scopeTestClient) ListCharts(ctx context.Context, in *pb.ListChartsRequest, opts ...grpc.CallOption) (*pb.ListChartsResponse, error) {
	return &pb.ListChartsResponse{Charts: f.charts}, nil
}

// scopeTestCatalog is a mixed apps+charts catalog spanning two domains
// (including "demo", the domain FR1's default "all" exclusion applies to)
// plus one deliberately ambiguous short name ("worker" exists in both
// "platform" and "demo").
func scopeTestCatalog() ([]*pb.App, []*pb.Chart) {
	apps := []*pb.App{
		{AppId: "a1", FullName: "platform-worker", Name: "worker", Domain: "platform", Status: pb.AppStatus_APP_STATUS_ACTIVE, AppType: "service"},
		{AppId: "a2", FullName: "platform-api", Name: "api", Domain: "platform", Status: pb.AppStatus_APP_STATUS_ACTIVE, AppType: "service"},
		{AppId: "a3", FullName: "demo-worker", Name: "worker", Domain: "demo", Status: pb.AppStatus_APP_STATUS_ACTIVE, AppType: "service"},
		// archived: must never resolve, in any scope form.
		{AppId: "a4", FullName: "platform-archived", Name: "archived", Domain: "platform", Status: pb.AppStatus_APP_STATUS_ARCHIVED, AppType: "service"},
	}
	charts := []*pb.Chart{
		{ChartId: "c1", FullName: "platform-chart", Name: "chart", Domain: "platform", Status: pb.AppStatus_APP_STATUS_ACTIVE},
		{ChartId: "c2", FullName: "demo-chart", Name: "chart", Domain: "demo", Status: pb.AppStatus_APP_STATUS_ACTIVE},
	}
	return apps, charts
}

func fullNames(targets []*pb.ReleaseTargetInput) []string {
	out := make([]string, 0, len(targets))
	for _, tgt := range targets {
		out = append(out, tgt.GetOwnerFullName())
	}
	return out
}

func containsAll(got []string, want ...string) bool {
	set := make(map[string]bool, len(got))
	for _, g := range got {
		set[g] = true
	}
	for _, w := range want {
		if !set[w] {
			return false
		}
	}
	return true
}

// --- FR1: "all" scope ----------------------------------------------------

func TestResolveReleaseScope_All_DefaultExcludesDemoDomain(t *testing.T) {
	apps, charts := scopeTestCatalog()
	registry := &RegistryClient{App: &scopeTestClient{apps: apps, charts: charts}}

	targets, err := resolveReleaseScope(context.Background(), registry, "all", false)
	if err != nil {
		t.Fatalf("resolveReleaseScope(all) error: %v", err)
	}

	names := fullNames(targets)
	if !containsAll(names, "platform-worker", "platform-api", "platform-chart") {
		t.Errorf("expected all active non-demo apps/charts, got %v", names)
	}
	for _, n := range names {
		if strings.HasPrefix(n, "demo-") {
			t.Errorf("scope \"all\" without include_demo must exclude demo targets, got %v", names)
		}
		if n == "platform-archived" {
			t.Errorf("scope \"all\" must exclude archived apps, got %v", names)
		}
	}
}

func TestResolveReleaseScope_All_IncludeDemo_ResolvesDemoTargetsToo(t *testing.T) {
	apps, charts := scopeTestCatalog()
	registry := &RegistryClient{App: &scopeTestClient{apps: apps, charts: charts}}

	targets, err := resolveReleaseScope(context.Background(), registry, "all", true)
	if err != nil {
		t.Fatalf("resolveReleaseScope(all, includeDemo) error: %v", err)
	}

	names := fullNames(targets)
	if !containsAll(names, "demo-worker", "demo-chart", "platform-worker", "platform-chart") {
		t.Errorf("expected demo targets included alongside platform targets, got %v", names)
	}
}

func TestResolveReleaseScope_All_CaseInsensitive(t *testing.T) {
	apps, charts := scopeTestCatalog()
	registry := &RegistryClient{App: &scopeTestClient{apps: apps, charts: charts}}

	targets, err := resolveReleaseScope(context.Background(), registry, "ALL", false)
	if err != nil {
		t.Fatalf("resolveReleaseScope(ALL) error: %v", err)
	}
	if len(targets) == 0 {
		t.Fatal("expected \"ALL\" to resolve case-insensitively like \"all\"")
	}
}

// --- FR1: single domain scope ---------------------------------------------

func TestResolveReleaseScope_SingleDomain_ResolvesEveryTargetInThatDomain(t *testing.T) {
	apps, charts := scopeTestCatalog()
	registry := &RegistryClient{App: &scopeTestClient{apps: apps, charts: charts}}

	targets, err := resolveReleaseScope(context.Background(), registry, "platform", false)
	if err != nil {
		t.Fatalf("resolveReleaseScope(platform) error: %v", err)
	}

	names := fullNames(targets)
	if !containsAll(names, "platform-worker", "platform-api", "platform-chart") {
		t.Errorf("expected every active platform app/chart, got %v", names)
	}
	for _, n := range names {
		if n == "platform-archived" {
			t.Errorf("domain scope must exclude archived apps, got %v", names)
		}
	}
}

// A domain named explicitly (not via "all") must NOT be demo-filtered --
// only "all"'s default exclusion applies to the demo domain (matches
// release.yml's include_demo semantics, per release_scope.go's doc
// comment).
func TestResolveReleaseScope_ExplicitDemoDomain_IsNeverFiltered(t *testing.T) {
	apps, charts := scopeTestCatalog()
	registry := &RegistryClient{App: &scopeTestClient{apps: apps, charts: charts}}

	targets, err := resolveReleaseScope(context.Background(), registry, "demo", false)
	if err != nil {
		t.Fatalf("resolveReleaseScope(demo) error: %v", err)
	}

	names := fullNames(targets)
	if !containsAll(names, "demo-worker", "demo-chart") {
		t.Errorf("an explicit \"demo\" domain scope must resolve demo targets even with includeDemo=false, got %v", names)
	}
}

// --- FR1: comma-separated list scope --------------------------------------

func TestResolveReleaseScope_CommaList_MixesFullNamesDomainsAndShortNames(t *testing.T) {
	apps, charts := scopeTestCatalog()
	registry := &RegistryClient{App: &scopeTestClient{apps: apps, charts: charts}}

	// "platform-api" (full name), "demo" (a whole domain as one list
	// entry), "chart" would be ambiguous (matches both platform-chart and
	// demo-chart by short name) so use the unambiguous full name instead
	// where a short name is intentionally tested separately below.
	targets, err := resolveReleaseScope(context.Background(), registry, "platform-api, demo", false)
	if err != nil {
		t.Fatalf("resolveReleaseScope(comma-list) error: %v", err)
	}

	names := fullNames(targets)
	if !containsAll(names, "platform-api", "demo-worker", "demo-chart") {
		t.Errorf("expected the full-name entry plus every target in the domain entry, got %v", names)
	}
	if len(names) != 3 {
		t.Errorf("expected exactly 3 resolved targets, got %v", names)
	}
}

func TestResolveReleaseScope_CommaList_UnambiguousShortName_Resolves(t *testing.T) {
	apps, charts := scopeTestCatalog()
	registry := &RegistryClient{App: &scopeTestClient{apps: apps, charts: charts}}

	// "api" is unique across the whole catalog (only platform-api).
	targets, err := resolveReleaseScope(context.Background(), registry, "api", false)
	if err != nil {
		t.Fatalf("resolveReleaseScope(api) error: %v", err)
	}
	names := fullNames(targets)
	if len(names) != 1 || names[0] != "platform-api" {
		t.Errorf("expected the unambiguous short name to resolve to platform-api, got %v", names)
	}
}

func TestResolveReleaseScope_CommaList_MalformedEntries_ExtraCommasAndWhitespaceIgnored(t *testing.T) {
	apps, charts := scopeTestCatalog()
	registry := &RegistryClient{App: &scopeTestClient{apps: apps, charts: charts}}

	targets, err := resolveReleaseScope(context.Background(), registry, "  platform-api ,, platform-chart ,  ", false)
	if err != nil {
		t.Fatalf("resolveReleaseScope with stray commas/whitespace error: %v", err)
	}
	names := fullNames(targets)
	if !containsAll(names, "platform-api", "platform-chart") || len(names) != 2 {
		t.Errorf("expected stray commas/whitespace to be tolerated and empty entries skipped, got %v", names)
	}
}

func TestResolveReleaseScope_CommaList_AmbiguousShortName_IsRejected(t *testing.T) {
	apps, charts := scopeTestCatalog()
	registry := &RegistryClient{App: &scopeTestClient{apps: apps, charts: charts}}

	// "worker" matches both platform-worker and demo-worker by short name.
	_, err := resolveReleaseScope(context.Background(), registry, "worker", false)
	if err == nil {
		t.Fatal("expected an error for an ambiguous short name, got nil")
	}
	if !strings.Contains(err.Error(), "ambiguous") {
		t.Errorf("expected an \"ambiguous\" error, got: %v", err)
	}
}

func TestResolveReleaseScope_CommaList_UnknownEntry_IsRejected(t *testing.T) {
	apps, charts := scopeTestCatalog()
	registry := &RegistryClient{App: &scopeTestClient{apps: apps, charts: charts}}

	_, err := resolveReleaseScope(context.Background(), registry, "platform-api, does-not-exist", false)
	if err == nil {
		t.Fatal("expected an error for an unknown scope entry, got nil")
	}
	if !strings.Contains(err.Error(), "does-not-exist") {
		t.Errorf("expected the invalid entry to be named in the error, got: %v", err)
	}
}

func TestResolveReleaseScope_ArchivedApp_NeverResolvesByFullName(t *testing.T) {
	apps, charts := scopeTestCatalog()
	registry := &RegistryClient{App: &scopeTestClient{apps: apps, charts: charts}}

	_, err := resolveReleaseScope(context.Background(), registry, "platform-archived", false)
	if err == nil {
		t.Fatal("expected an archived app to be unreleasable (not found in the catalog), got nil")
	}
}

// --- edge cases ------------------------------------------------------------

func TestResolveReleaseScope_EmptyScope_IsRejected(t *testing.T) {
	registry := &RegistryClient{App: &scopeTestClient{}}

	_, err := resolveReleaseScope(context.Background(), registry, "   ", false)
	if err == nil {
		t.Fatal("expected an error for an empty/whitespace-only scope, got nil")
	}
}

func TestResolveReleaseScope_All_ResolvesToNothing_IsRejected(t *testing.T) {
	// Every app/chart is in the demo domain; excluding it by default
	// leaves nothing releasable.
	apps := []*pb.App{{AppId: "a1", FullName: "demo-worker", Name: "worker", Domain: "demo", Status: pb.AppStatus_APP_STATUS_ACTIVE}}
	registry := &RegistryClient{App: &scopeTestClient{apps: apps}}

	_, err := resolveReleaseScope(context.Background(), registry, "all", false)
	if err == nil {
		t.Fatal("expected scope \"all\" resolving to nothing (all-demo catalog, demo excluded by default) to error, got nil")
	}
}

// --- resolveSelectedTargets (issue #889 follow-up: the checkbox-tree picker) ---

// This directly unit-tests resolveSelectedTargets (release_scope.go) --
// the picker's "domain:<name>" | "app:<full_name>" | "chart:<full_name>"
// token grammar, the picker's counterpart to resolveReleaseScope's
// comma-string grammar above. Reuses scopeTestCatalog/scopeTestClient.

func TestResolveSelectedTargets_DomainToken_ResolvesEverythingUnderIt(t *testing.T) {
	apps, charts := scopeTestCatalog()
	registry := &RegistryClient{App: &scopeTestClient{apps: apps, charts: charts}}

	targets, err := resolveSelectedTargets(context.Background(), registry, []string{"domain:platform"})
	if err != nil {
		t.Fatalf("resolveSelectedTargets: %v", err)
	}
	names := fullNames(targets)
	if !containsAll(names, "platform-worker", "platform-api", "platform-chart") {
		t.Errorf("expected every active platform app/chart, got %v", names)
	}
	if len(names) != 3 {
		t.Errorf("expected exactly 3 targets (platform-archived must never resolve), got %v", names)
	}
}

func TestResolveSelectedTargets_AppToken_DoesNotRequireItsChart(t *testing.T) {
	apps, charts := scopeTestCatalog()
	registry := &RegistryClient{App: &scopeTestClient{apps: apps, charts: charts}}

	// The user's explicit requirement: picking one chart never implies or
	// requires picking its member apps, and vice versa -- an app token
	// alone must resolve to exactly that one app, nothing else.
	targets, err := resolveSelectedTargets(context.Background(), registry, []string{"app:platform-worker"})
	if err != nil {
		t.Fatalf("resolveSelectedTargets: %v", err)
	}
	if names := fullNames(targets); len(names) != 1 || names[0] != "platform-worker" {
		t.Errorf("expected exactly [platform-worker], got %v", names)
	}
}

func TestResolveSelectedTargets_ChartToken_DoesNotRequireItsApps(t *testing.T) {
	apps, charts := scopeTestCatalog()
	registry := &RegistryClient{App: &scopeTestClient{apps: apps, charts: charts}}

	targets, err := resolveSelectedTargets(context.Background(), registry, []string{"chart:platform-chart"})
	if err != nil {
		t.Fatalf("resolveSelectedTargets: %v", err)
	}
	if names := fullNames(targets); len(names) != 1 || names[0] != "platform-chart" {
		t.Errorf("expected exactly [platform-chart], got %v", names)
	}
}

func TestResolveSelectedTargets_MixedTokens_UnionsAcrossKinds(t *testing.T) {
	apps, charts := scopeTestCatalog()
	registry := &RegistryClient{App: &scopeTestClient{apps: apps, charts: charts}}

	targets, err := resolveSelectedTargets(context.Background(), registry, []string{
		"app:platform-api", "chart:demo-chart", "domain:demo",
	})
	if err != nil {
		t.Fatalf("resolveSelectedTargets: %v", err)
	}
	names := fullNames(targets)
	if !containsAll(names, "platform-api", "demo-chart", "demo-worker") {
		t.Errorf("expected the union of all three selections (with demo-chart deduplicated against domain:demo), got %v", names)
	}
	if len(names) != 3 {
		t.Errorf("expected exactly 3 deduplicated targets, got %v", names)
	}
}

func TestResolveSelectedTargets_UnknownApp_IsRejected(t *testing.T) {
	apps, charts := scopeTestCatalog()
	registry := &RegistryClient{App: &scopeTestClient{apps: apps, charts: charts}}

	_, err := resolveSelectedTargets(context.Background(), registry, []string{"app:nonexistent-app"})
	if err == nil {
		t.Fatal("expected an error for a selection naming a nonexistent app, got nil")
	}
}

func TestResolveSelectedTargets_MalformedToken_IsRejected(t *testing.T) {
	apps, charts := scopeTestCatalog()
	registry := &RegistryClient{App: &scopeTestClient{apps: apps, charts: charts}}

	_, err := resolveSelectedTargets(context.Background(), registry, []string{"platform-worker"})
	if err == nil {
		t.Fatal("expected an error for a token with no \"kind:\" prefix, got nil")
	}
}

func TestResolveSelectedTargets_NoSelections_IsRejected(t *testing.T) {
	registry := &RegistryClient{App: &scopeTestClient{}}

	_, err := resolveSelectedTargets(context.Background(), registry, nil)
	if err == nil {
		t.Fatal("expected an error when nothing is selected, got nil")
	}
}
