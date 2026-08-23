package handlers

import (
	"testing"

	pb "github.com/whale-net/everything/tools/app_registry/protos"
	"github.com/whale-net/everything/tools/app_registry/server/repository/fake"
	appmetapb "github.com/whale-net/everything/tools/appmeta/proto"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func manifestSet(apps []*appmetapb.AppManifest, charts []*appmetapb.ChartManifest) *appmetapb.AppManifestSet {
	return &appmetapb.AppManifestSet{Apps: apps, Charts: charts}
}

func oneApp(domain, name string) *appmetapb.AppManifest {
	return &appmetapb.AppManifest{
		Domain: domain, Name: name, Description: "d", Language: "go", AppType: "worker",
		DeployUnit: appmetapb.DeployUnit_DEPLOY_UNIT_IMAGE,
	}
}

// TestReconcileApps_FullLifecycle walks create -> update -> absent-marks-MISSING
// -> reappear-recovers-ACTIVE, matching the AR-2a scope's required coverage.
func TestReconcileApps_FullLifecycle(t *testing.T) {
	ctx := authedCtx()
	srv := NewAppServer(fake.New())

	// 1. create
	resp, err := srv.ReconcileApps(ctx, &pb.ReconcileAppsRequest{
		Manifests:      manifestSet([]*appmetapb.AppManifest{oneApp("demo", "svc")}, nil),
		IdempotencyKey: "run-1-1",
	})
	if err != nil {
		t.Fatalf("create reconcile: %v", err)
	}
	if len(resp.CreatedApps) != 1 || resp.CreatedApps[0].Status != pb.AppStatus_APP_STATUS_ACTIVE {
		t.Fatalf("expected 1 created ACTIVE app, got %+v", resp.CreatedApps)
	}
	appID := resp.CreatedApps[0].AppId

	// 2. update (same app, different description)
	am := oneApp("demo", "svc")
	am.Description = "updated"
	resp, err = srv.ReconcileApps(ctx, &pb.ReconcileAppsRequest{
		Manifests:      manifestSet([]*appmetapb.AppManifest{am}, nil),
		IdempotencyKey: "run-1-2",
	})
	if err != nil {
		t.Fatalf("update reconcile: %v", err)
	}
	if len(resp.UpdatedApps) != 1 || resp.UpdatedApps[0].Description != "updated" {
		t.Fatalf("expected 1 updated app with new description, got %+v", resp.UpdatedApps)
	}

	// 3. absent -> MISSING
	resp, err = srv.ReconcileApps(ctx, &pb.ReconcileAppsRequest{
		Manifests:      manifestSet(nil, nil),
		IdempotencyKey: "run-1-3",
	})
	if err != nil {
		t.Fatalf("absent reconcile: %v", err)
	}
	if len(resp.NewlyMissingApps) != 1 || resp.NewlyMissingApps[0].AppId != appID {
		t.Fatalf("expected app %s newly missing, got %+v", appID, resp.NewlyMissingApps)
	}

	getResp, err := srv.GetApp(ctx, &pb.GetAppRequest{AppId: appID})
	if err != nil {
		t.Fatalf("get app after marking missing: %v", err)
	}
	if getResp.App.Status != pb.AppStatus_APP_STATUS_MISSING {
		t.Fatalf("expected MISSING, got %v", getResp.App.Status)
	}

	// 4. reappear -> recovered ACTIVE
	resp, err = srv.ReconcileApps(ctx, &pb.ReconcileAppsRequest{
		Manifests:      manifestSet([]*appmetapb.AppManifest{oneApp("demo", "svc")}, nil),
		IdempotencyKey: "run-1-4",
	})
	if err != nil {
		t.Fatalf("recover reconcile: %v", err)
	}
	if len(resp.RecoveredApps) != 1 || resp.RecoveredApps[0].Status != pb.AppStatus_APP_STATUS_ACTIVE {
		t.Fatalf("expected 1 recovered ACTIVE app, got %+v", resp.RecoveredApps)
	}
}

// TestReconcileApps_DryRunWritesNothing proves dry_run computes a diff
// without mutating the registry: a subsequent real reconcile still sees the
// app as newly created.
func TestReconcileApps_DryRunWritesNothing(t *testing.T) {
	ctx := authedCtx()
	srv := NewAppServer(fake.New())

	dryResp, err := srv.ReconcileApps(ctx, &pb.ReconcileAppsRequest{
		Manifests: manifestSet([]*appmetapb.AppManifest{oneApp("demo", "svc")}, nil),
		DryRun:    true,
	})
	if err != nil {
		t.Fatalf("dry run reconcile: %v", err)
	}
	if !dryResp.DryRun || len(dryResp.CreatedApps) != 1 {
		t.Fatalf("expected dry_run diff showing 1 created app, got %+v", dryResp)
	}

	listResp, err := srv.ListApps(ctx, &pb.ListAppsRequest{})
	if err != nil {
		t.Fatalf("list apps: %v", err)
	}
	if len(listResp.Apps) != 0 {
		t.Fatalf("dry_run must write nothing; found %d apps", len(listResp.Apps))
	}

	// A real reconcile afterwards should still see this as a fresh create.
	realResp, err := srv.ReconcileApps(ctx, &pb.ReconcileAppsRequest{
		Manifests:      manifestSet([]*appmetapb.AppManifest{oneApp("demo", "svc")}, nil),
		IdempotencyKey: "run-2-1",
	})
	if err != nil {
		t.Fatalf("real reconcile after dry run: %v", err)
	}
	if len(realResp.CreatedApps) != 1 {
		t.Fatalf("expected the real reconcile to still create the app, got %+v", realResp)
	}
}

// TestReconcileApps_IdempotencyReplaysWithoutDoubleWrite proves a repeated
// call with the same idempotency_key replays the original response and does
// not re-execute the write — asserted by row count, per AR-2a's required
// coverage.
func TestReconcileApps_IdempotencyReplaysWithoutDoubleWrite(t *testing.T) {
	ctx := authedCtx()
	srv := NewAppServer(fake.New())

	req := &pb.ReconcileAppsRequest{
		Manifests:      manifestSet([]*appmetapb.AppManifest{oneApp("demo", "svc")}, nil),
		IdempotencyKey: "same-key",
	}

	first, err := srv.ReconcileApps(ctx, req)
	if err != nil {
		t.Fatalf("first reconcile: %v", err)
	}
	if len(first.CreatedApps) != 1 {
		t.Fatalf("expected 1 created app, got %+v", first.CreatedApps)
	}

	second, err := srv.ReconcileApps(ctx, req)
	if err != nil {
		t.Fatalf("replayed reconcile: %v", err)
	}
	if len(second.CreatedApps) != 1 || second.CreatedApps[0].AppId != first.CreatedApps[0].AppId {
		t.Fatalf("replayed response should be byte-identical to the first, got %+v vs %+v", second, first)
	}

	listResp, err := srv.ListApps(ctx, &pb.ListAppsRequest{})
	if err != nil {
		t.Fatalf("list apps: %v", err)
	}
	if len(listResp.Apps) != 1 {
		t.Fatalf("idempotent replay must not double-write; expected 1 app row, got %d", len(listResp.Apps))
	}
}

// TestReconcileApps_UnknownChartAppSkipsChartOnly is AR-7a's headline
// scenario: a chart whose apps reference an unknown app no longer fails the
// whole reconcile (pre-AR-7a: TestReconcileApps_UnknownChartAppFailsWholeReconcile
// asserted the opposite). It must be reported in unresolved_charts and
// skipped, while every other app/chart in the same call still applies.
func TestReconcileApps_UnknownChartAppSkipsChartOnly(t *testing.T) {
	ctx := authedCtx()
	srv := NewAppServer(fake.New())

	resp, err := srv.ReconcileApps(ctx, &pb.ReconcileAppsRequest{
		Manifests: manifestSet(
			[]*appmetapb.AppManifest{oneApp("demo", "good-app")},
			[]*appmetapb.ChartManifest{
				{Domain: "demo", Name: "bad-chart", Apps: []string{"nonexistent"}},
				{Domain: "demo", Name: "good-chart", Apps: []string{"good-app"}},
			},
		),
		IdempotencyKey: "run-3-1",
	})
	if err != nil {
		t.Fatalf("reconcile with one bad chart must not fail the whole call: %v", err)
	}

	if len(resp.UnresolvedCharts) != 1 {
		t.Fatalf("expected exactly 1 unresolved chart, got %+v", resp.UnresolvedCharts)
	}
	uc := resp.UnresolvedCharts[0]
	if uc.Domain != "demo" || uc.Name != "bad-chart" {
		t.Fatalf("expected unresolved chart demo/bad-chart, got %+v", uc)
	}
	if len(uc.AppRefs) != 1 || uc.AppRefs[0] != "nonexistent" {
		t.Fatalf("expected offending app_refs=[nonexistent], got %+v", uc.AppRefs)
	}
	if uc.Reason == "" {
		t.Fatal("expected a non-empty reason")
	}

	// The good app and good chart still applied, and the bad chart was not
	// created.
	if len(resp.CreatedApps) != 1 {
		t.Fatalf("expected the good app to still be created, got %+v", resp.CreatedApps)
	}
	if len(resp.CreatedCharts) != 1 || resp.CreatedCharts[0].Name != "good-chart" {
		t.Fatalf("expected only good-chart to be created, got %+v", resp.CreatedCharts)
	}

	listResp, err := srv.ListCharts(ctx, &pb.ListChartsRequest{})
	if err != nil {
		t.Fatalf("list charts: %v", err)
	}
	if len(listResp.Charts) != 1 || listResp.Charts[0].Name != "good-chart" {
		t.Fatalf("expected only good-chart to be registered, got %+v", listResp.Charts)
	}
}

// TestReconcileApps_UnresolvedChartNotMarkedMissing proves the deliberate
// semantics called out in ARCHITECTURE.md "AssertApps (additive) vs.
// ReconcileApps (absence sweep)": a chart that was already registered, and
// becomes unresolvable in a later reconcile (its manifest now names an app
// that does not exist), is SKIPPED -- not swept into MISSING as a side
// effect of not being re-applied this call. A chart present in the manifest
// set but unresolvable is present, not absent.
func TestReconcileApps_UnresolvedChartNotMarkedMissing(t *testing.T) {
	ctx := authedCtx()
	srv := NewAppServer(fake.New())

	// 1. Register app + chart normally.
	first, err := srv.ReconcileApps(ctx, &pb.ReconcileAppsRequest{
		Manifests: manifestSet(
			[]*appmetapb.AppManifest{oneApp("demo", "svc")},
			[]*appmetapb.ChartManifest{{Domain: "demo", Name: "chart", Apps: []string{"svc"}}},
		),
		IdempotencyKey: "run-5-1",
	})
	if err != nil {
		t.Fatalf("initial reconcile: %v", err)
	}
	chartID := first.CreatedCharts[0].ChartId

	// 2. Reconcile again: the chart's manifest now references an app that
	// does not exist ("svc" renamed without updating the chart) -- an
	// unresolvable reference. The chart itself is still present in this
	// call's manifest set, unlike an app/chart that simply drops out.
	second, err := srv.ReconcileApps(ctx, &pb.ReconcileAppsRequest{
		Manifests: manifestSet(
			[]*appmetapb.AppManifest{oneApp("demo", "svc")},
			[]*appmetapb.ChartManifest{{Domain: "demo", Name: "chart", Apps: []string{"renamed-away"}}},
		),
		IdempotencyKey: "run-5-2",
	})
	if err != nil {
		t.Fatalf("second reconcile: %v", err)
	}
	if len(second.UnresolvedCharts) != 1 {
		t.Fatalf("expected the chart to be reported unresolved, got %+v", second.UnresolvedCharts)
	}
	if len(second.NewlyMissingCharts) != 0 {
		t.Fatalf("an unresolved chart must NOT be swept into newly_missing_charts, got %+v", second.NewlyMissingCharts)
	}

	listResp, err := srv.ListCharts(ctx, &pb.ListChartsRequest{})
	if err != nil {
		t.Fatalf("list charts: %v", err)
	}
	found := false
	for _, c := range listResp.Charts {
		if c.ChartId == chartID {
			found = true
			if c.Status == pb.AppStatus_APP_STATUS_MISSING {
				t.Fatalf("chart was incorrectly marked MISSING after becoming unresolvable, got %+v", c)
			}
		}
	}
	if !found {
		t.Fatalf("expected the chart to still be listed (active, untouched), got %+v", listResp.Charts)
	}
}

// TestReconcileApps_DomainQualifiedAppRefsResolveUnambiguously proves
// AR-7a's fix for cross-domain bare-name ambiguity: two apps sharing a bare
// name in different domains previously made resolveChartApps fail on the
// ambiguous bare name; a chart using AppRefs (domain-qualified) instead
// resolves deterministically to the correct app regardless of the collision.
func TestReconcileApps_DomainQualifiedAppRefsResolveUnambiguously(t *testing.T) {
	ctx := authedCtx()
	srv := NewAppServer(fake.New())

	resp, err := srv.ReconcileApps(ctx, &pb.ReconcileAppsRequest{
		Manifests: manifestSet(
			[]*appmetapb.AppManifest{oneApp("domain-a", "shared-name"), oneApp("domain-b", "shared-name")},
			[]*appmetapb.ChartManifest{
				{Domain: "domain-b", Name: "chart", AppRefs: []string{"domain-b/shared-name"}},
			},
		),
		IdempotencyKey: "run-6-1",
	})
	if err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if len(resp.UnresolvedCharts) != 0 {
		t.Fatalf("expected the domain-qualified ref to resolve without ambiguity, got unresolved=%+v", resp.UnresolvedCharts)
	}
	if len(resp.CreatedCharts) != 1 || len(resp.CreatedCharts[0].AppIds) != 1 {
		t.Fatalf("expected 1 chart composing exactly 1 app, got %+v", resp.CreatedCharts)
	}

	var domainBAppID string
	for _, a := range resp.CreatedApps {
		if a.Domain == "domain-b" {
			domainBAppID = a.AppId
		}
	}
	if resp.CreatedCharts[0].AppIds[0] != domainBAppID {
		t.Fatalf("expected the chart to resolve to domain-b's app (id=%s), got %+v", domainBAppID, resp.CreatedCharts[0].AppIds)
	}
}

// TestReconcileApps_AmbiguousBareNameSkipsChartNotWholeReconcile proves the
// deprecated bare-name compatibility path still rejects ambiguity -- but as
// a per-chart skip now, not a whole-reconcile failure (AR-7a).
func TestReconcileApps_AmbiguousBareNameSkipsChartNotWholeReconcile(t *testing.T) {
	ctx := authedCtx()
	srv := NewAppServer(fake.New())

	resp, err := srv.ReconcileApps(ctx, &pb.ReconcileAppsRequest{
		Manifests: manifestSet(
			[]*appmetapb.AppManifest{oneApp("domain-a", "shared-name"), oneApp("domain-b", "shared-name")},
			[]*appmetapb.ChartManifest{
				// domain "other" has no app named "shared-name" itself, so
				// the same-domain preference doesn't disambiguate, and both
				// domain-a and domain-b match -- genuinely ambiguous.
				{Domain: "other", Name: "chart", Apps: []string{"shared-name"}},
			},
		),
		IdempotencyKey: "run-7-1",
	})
	if err != nil {
		t.Fatalf("reconcile must not fail the whole call for an ambiguous bare name: %v", err)
	}
	if len(resp.UnresolvedCharts) != 1 {
		t.Fatalf("expected the ambiguous chart to be reported unresolved, got %+v", resp.UnresolvedCharts)
	}
	if len(resp.CreatedApps) != 2 {
		t.Fatalf("expected both apps to still be created despite the ambiguous chart, got %+v", resp.CreatedApps)
	}
}

// TestReconcileApps_ChartComposesApps covers the happy path of resolving a
// chart's bare app names to app_ids.
func TestReconcileApps_ChartComposesApps(t *testing.T) {
	ctx := authedCtx()
	srv := NewAppServer(fake.New())

	resp, err := srv.ReconcileApps(ctx, &pb.ReconcileAppsRequest{
		Manifests: manifestSet(
			[]*appmetapb.AppManifest{oneApp("demo", "svc")},
			[]*appmetapb.ChartManifest{{Domain: "demo", Name: "chart", Apps: []string{"svc"}}},
		),
		IdempotencyKey: "run-4-1",
	})
	if err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if len(resp.CreatedCharts) != 1 || len(resp.CreatedCharts[0].AppIds) != 1 {
		t.Fatalf("expected 1 chart composing 1 app, got %+v", resp.CreatedCharts)
	}
	if resp.CreatedCharts[0].AppIds[0] != resp.CreatedApps[0].AppId {
		t.Fatalf("chart's app_ids should resolve to the created app's id")
	}
}

// TestReconcileApps_ChartNameStripsHelmDomainPrefix covers the manifest
// shape release_helm_chart's Bazel macro actually emits: Name carries a
// "helm-{domain}-" prefix baked in for git tag/tarball naming (e.g.
// "helm-app-registry-app-registry" when a chart's domain and base name
// coincide, as with the app-registry chart itself). ReconcileApps must strip
// that prefix so Chart.FullName() matches the "{domain}-{name}" identifier
// the release pipeline uses for BeginPublish/RecordArtifact, rather than
// doubling up the domain (e.g. "app-registry-helm-app-registry-app-registry").
func TestReconcileApps_ChartNameStripsHelmDomainPrefix(t *testing.T) {
	ctx := authedCtx()
	srv := NewAppServer(fake.New())

	resp, err := srv.ReconcileApps(ctx, &pb.ReconcileAppsRequest{
		Manifests: manifestSet(
			[]*appmetapb.AppManifest{oneApp("app-registry", "svc")},
			[]*appmetapb.ChartManifest{{Domain: "app-registry", Name: "helm-app-registry-app-registry", Apps: []string{"svc"}}},
		),
		IdempotencyKey: "run-4-2",
	})
	if err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if len(resp.CreatedCharts) != 1 {
		t.Fatalf("expected 1 created chart, got %+v", resp.CreatedCharts)
	}
	chart := resp.CreatedCharts[0]
	if chart.Name != "app-registry" {
		t.Fatalf("expected helm-{domain}- prefix stripped from name, got %q", chart.Name)
	}
	if chart.FullName != "app-registry-app-registry" {
		t.Fatalf("expected FullName %q, got %q", "app-registry-app-registry", chart.FullName)
	}
}

// ============================================================================
// Reconcile watermark (issue #545) -- fake-backed handler coverage.
// See postgres_integration_app_test.go's "Reconcile watermark" section for the
// same scenarios exercised against real Postgres (real FOR UPDATE locking,
// real transaction rollback). These fake-backed versions exist so the
// business logic (repository.ShouldApplyReconcile and its wiring through
// the handler/repository layers) is covered by the fast unit tier too, not
// only the manual Postgres tier.
// ============================================================================

func manifestSetWithSource(gitSha string, sourceCommittedAt, discoveredAt int64, apps []*appmetapb.AppManifest, charts []*appmetapb.ChartManifest) *appmetapb.AppManifestSet {
	return &appmetapb.AppManifestSet{
		GitSha: gitSha, SourceCommittedAt: sourceCommittedAt, DiscoveredAt: discoveredAt,
		Apps: apps, Charts: charts,
	}
}

// TestReconcileApps_StaleCallSkippedWithoutError covers the headline
// scenario from issue #545: an older commit's call lands after a newer
// one's and must be a no-op SUCCESS (not an error), naming the commit it
// lost to, and leaving the newer commit's state (including a MISSING flag)
// untouched.
func TestReconcileApps_StaleCallSkippedWithoutError(t *testing.T) {
	ctx := authedCtx()
	srv := NewAppServer(fake.New())

	first, err := srv.ReconcileApps(ctx, &pb.ReconcileAppsRequest{
		Manifests:      manifestSetWithSource("newer-sha", 2000, 2000, []*appmetapb.AppManifest{oneApp("demo", "svc")}, nil),
		IdempotencyKey: "wm-1",
	})
	if err != nil {
		t.Fatalf("first reconcile: %v", err)
	}
	svcID := first.CreatedApps[0].AppId

	// A newer commit drops "svc" -- correctly MISSING.
	second, err := srv.ReconcileApps(ctx, &pb.ReconcileAppsRequest{
		Manifests:      manifestSetWithSource("newest-sha", 3000, 3000, nil, nil),
		IdempotencyKey: "wm-2",
	})
	if err != nil {
		t.Fatalf("second reconcile: %v", err)
	}
	if len(second.NewlyMissingApps) != 1 || second.NewlyMissingApps[0].AppId != svcID {
		t.Fatalf("expected svc newly missing, got %+v", second.NewlyMissingApps)
	}

	// A STALE call -- an older commit re-running -- must not revert that.
	stale, err := srv.ReconcileApps(ctx, &pb.ReconcileAppsRequest{
		Manifests:      manifestSetWithSource("older-rerun-sha", 2500, 2500, []*appmetapb.AppManifest{oneApp("demo", "svc")}, nil),
		IdempotencyKey: "wm-3",
	})
	if err != nil {
		t.Fatalf("stale reconcile must be a no-op SUCCESS, not an error: %v", err)
	}
	if !stale.SkippedStale {
		t.Fatalf("expected SkippedStale=true for an older-commit call, got %+v", stale)
	}
	if stale.CurrentWatermarkGitSha != "newest-sha" {
		t.Fatalf("expected current_watermark_git_sha=newest-sha, got %q", stale.CurrentWatermarkGitSha)
	}
	if len(stale.CreatedApps)+len(stale.UpdatedApps)+len(stale.RecoveredApps) != 0 {
		t.Fatalf("expected an empty result for a skipped-stale call, got %+v", stale)
	}

	// svc must STILL be MISSING -- the stale call did not revert it.
	getResp, err := srv.GetApp(ctx, &pb.GetAppRequest{AppId: svcID})
	if err != nil {
		t.Fatalf("get app after stale call: %v", err)
	}
	if getResp.App.Status != pb.AppStatus_APP_STATUS_MISSING {
		t.Fatalf("stale call reverted svc's MISSING flag -- now %v", getResp.App.Status)
	}
}

// TestReconcileApps_EqualOrderingKeyDifferentGitShaApplies proves the
// deliberate equal-timestamp tie-break: two different commits at the same
// ordering key must not block each other.
func TestReconcileApps_EqualOrderingKeyDifferentGitShaApplies(t *testing.T) {
	ctx := authedCtx()
	srv := NewAppServer(fake.New())

	if _, err := srv.ReconcileApps(ctx, &pb.ReconcileAppsRequest{
		Manifests:      manifestSetWithSource("sha-a", 5000, 5000, []*appmetapb.AppManifest{oneApp("demo", "svc")}, nil),
		IdempotencyKey: "tie-1",
	}); err != nil {
		t.Fatalf("first reconcile: %v", err)
	}

	resp, err := srv.ReconcileApps(ctx, &pb.ReconcileAppsRequest{
		Manifests: manifestSetWithSource("sha-b", 5000, 5000,
			[]*appmetapb.AppManifest{oneApp("demo", "svc"), oneApp("demo", "svc2")}, nil),
		IdempotencyKey: "tie-2",
	})
	if err != nil {
		t.Fatalf("tied-timestamp reconcile: %v", err)
	}
	if resp.SkippedStale {
		t.Fatalf("expected an equal-timestamp, different-git_sha call to apply, got SkippedStale=true")
	}
	if len(resp.CreatedApps) != 1 || resp.CreatedApps[0].FullName != "demo-svc2" {
		t.Fatalf("expected svc2 to be newly created, got %+v", resp.CreatedApps)
	}
}

// TestReconcileApps_DryRunNeverConsultsOrAdvancesWatermark proves dry_run
// is unaffected by the watermark in either direction, mirroring
// postgres_integration_app_test.go's version of this proof against real
// Postgres.
func TestReconcileApps_DryRunNeverConsultsOrAdvancesWatermark(t *testing.T) {
	ctx := authedCtx()
	srv := NewAppServer(fake.New())

	if _, err := srv.ReconcileApps(ctx, &pb.ReconcileAppsRequest{
		Manifests:      manifestSetWithSource("real-sha", 999999, 999999, []*appmetapb.AppManifest{oneApp("demo", "svc")}, nil),
		IdempotencyKey: "dry-wm-1",
	}); err != nil {
		t.Fatalf("real reconcile: %v", err)
	}

	// A dry run carrying a far-older ordering key must still compute a
	// normal diff -- if it consulted the watermark, this would come back
	// SkippedStale with an empty diff instead.
	dryResp, err := srv.ReconcileApps(ctx, &pb.ReconcileAppsRequest{
		Manifests: manifestSetWithSource("dry-run-old-sha", 1, 1,
			[]*appmetapb.AppManifest{oneApp("demo", "svc"), oneApp("demo", "new-app")}, nil),
		DryRun: true,
	})
	if err != nil {
		t.Fatalf("dry run reconcile: %v", err)
	}
	if dryResp.SkippedStale {
		t.Fatalf("dry run must never consult the watermark, got SkippedStale=true")
	}
	if len(dryResp.CreatedApps) != 1 || dryResp.CreatedApps[0].FullName != "demo-new-app" {
		t.Fatalf("expected dry run to compute a normal diff (1 created app), got %+v", dryResp.CreatedApps)
	}

	// And a REAL call carrying that same far-older key must still be
	// rejected as stale -- proving the dry run above did not advance the
	// watermark.
	afterDry, err := srv.ReconcileApps(ctx, &pb.ReconcileAppsRequest{
		Manifests: manifestSetWithSource("dry-run-old-sha", 1, 1,
			[]*appmetapb.AppManifest{oneApp("demo", "svc"), oneApp("demo", "new-app")}, nil),
		IdempotencyKey: "dry-wm-2",
	})
	if err != nil {
		t.Fatalf("post-dry-run real reconcile: %v", err)
	}
	if !afterDry.SkippedStale {
		t.Fatalf("expected the same far-older key to still be rejected as stale after the dry run, got SkippedStale=false -- the dry run must have advanced the watermark")
	}
}

// TestSetAppStatus_MissingToArchivedSucceeds covers the only transition a
// human triggers through this RPC: missing -> archived. missing -> active
// happens automatically via Reconcile's "recovered" path instead.
func TestSetAppStatus_MissingToArchivedSucceeds(t *testing.T) {
	ctx := authedCtx()
	srv := NewAppServer(fake.New())

	created, err := srv.ReconcileApps(ctx, &pb.ReconcileAppsRequest{
		Manifests:      manifestSet([]*appmetapb.AppManifest{oneApp("demo", "svc")}, nil),
		IdempotencyKey: "run-5-1",
	})
	if err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	svcID := created.CreatedApps[0].AppId

	// Drop "svc" from the manifest set so it becomes MISSING.
	if _, err := srv.ReconcileApps(ctx, &pb.ReconcileAppsRequest{
		Manifests:      manifestSet(nil, nil),
		IdempotencyKey: "run-5-2",
	}); err != nil {
		t.Fatalf("reconcile dropping svc: %v", err)
	}

	archived, err := srv.SetAppStatus(ctx, &pb.SetAppStatusRequest{
		AppId: svcID, Status: pb.AppStatus_APP_STATUS_ARCHIVED, Reason: "gone for good",
	})
	if err != nil {
		t.Fatalf("archive: %v", err)
	}
	if archived.App.Status != pb.AppStatus_APP_STATUS_ARCHIVED {
		t.Fatalf("expected ARCHIVED, got %v", archived.App.Status)
	}

	// archived -> archived is an idempotent no-op success, not an error.
	archivedAgain, err := srv.SetAppStatus(ctx, &pb.SetAppStatusRequest{
		AppId: svcID, Status: pb.AppStatus_APP_STATUS_ARCHIVED, Reason: "gone for good, again",
	})
	if err != nil {
		t.Fatalf("re-archive: %v", err)
	}
	if archivedAgain.App.Status != pb.AppStatus_APP_STATUS_ARCHIVED {
		t.Fatalf("expected ARCHIVED, got %v", archivedAgain.App.Status)
	}
}

// TestSetAppStatus_RejectsActiveToArchived covers the FailedPrecondition
// path: an app must be MISSING before it can be archived.
func TestSetAppStatus_RejectsActiveToArchived(t *testing.T) {
	ctx := authedCtx()
	srv := NewAppServer(fake.New())

	created, err := srv.ReconcileApps(ctx, &pb.ReconcileAppsRequest{
		Manifests:      manifestSet([]*appmetapb.AppManifest{oneApp("demo", "svc")}, nil),
		IdempotencyKey: "run-6-1",
	})
	if err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	svcID := created.CreatedApps[0].AppId

	_, err = srv.SetAppStatus(ctx, &pb.SetAppStatusRequest{
		AppId: svcID, Status: pb.AppStatus_APP_STATUS_ARCHIVED, Reason: "should not work",
	})
	if err == nil {
		t.Fatal("expected SetAppStatus to fail archiving an ACTIVE app")
	}
	st, _ := status.FromError(err)
	if st.Code() != codes.FailedPrecondition {
		t.Fatalf("expected FailedPrecondition, got %v", st.Code())
	}
}

// TestSetAppStatus_RejectsActiveTarget covers "ACTIVE is not a legal target
// at all" — dropped along with the last_seen_at heuristic that used to guard
// it, since Reconcile's recovered path is the only way back to ACTIVE.
func TestSetAppStatus_RejectsActiveTarget(t *testing.T) {
	ctx := authedCtx()
	srv := NewAppServer(fake.New())
	_, err := srv.SetAppStatus(ctx, &pb.SetAppStatusRequest{
		AppId: "x", Status: pb.AppStatus_APP_STATUS_ACTIVE, Reason: "manual reactivate",
	})
	if status.Code(err) != codes.InvalidArgument {
		t.Fatalf("expected InvalidArgument for status ACTIVE, got %v", err)
	}
}

func TestSetAppStatus_RequiresReason(t *testing.T) {
	ctx := authedCtx()
	srv := NewAppServer(fake.New())
	_, err := srv.SetAppStatus(ctx, &pb.SetAppStatusRequest{AppId: "x", Status: pb.AppStatus_APP_STATUS_ARCHIVED})
	if status.Code(err) != codes.InvalidArgument {
		t.Fatalf("expected InvalidArgument when reason is missing, got %v", err)
	}
}

// ============================================================================
// SetChartArgoApplicationNameOverride
// ============================================================================

// TestSetChartArgoApplicationNameOverride_SetAndClear covers the round trip
// through chartToPB by full_name lookup: setting a per-environment override,
// then clearing it (empty string) back to "no override" for that one
// environment -- see repository.Chart.ResolveArgoApplicationName.
func TestSetChartArgoApplicationNameOverride_SetAndClear(t *testing.T) {
	ctx := authedCtx()
	srv := NewAppServer(fake.New())

	if _, err := srv.ReconcileApps(ctx, &pb.ReconcileAppsRequest{
		Manifests: manifestSet([]*appmetapb.AppManifest{oneApp("demo", "svc")},
			[]*appmetapb.ChartManifest{{Domain: "demo", Name: "chart", Apps: []string{"svc"}}}),
		IdempotencyKey: "chartoverride-1",
	}); err != nil {
		t.Fatalf("reconcile: %v", err)
	}

	set, err := srv.SetChartArgoApplicationNameOverride(ctx, &pb.SetChartArgoApplicationNameOverrideRequest{
		FullName:            "demo-chart",
		EnvironmentKey:      "prod",
		ArgoApplicationName: "legacy-app-prod",
		Reason:              "ad-hoc deployment predates the naming convention",
	})
	if err != nil {
		t.Fatalf("set override: %v", err)
	}
	if got := set.Chart.GetArgoApplicationNameOverrides()["prod"]; got != "legacy-app-prod" {
		t.Fatalf("expected prod override on response, got %q", got)
	}

	got, err := srv.GetApp(ctx, &pb.GetAppRequest{FullName: "demo-svc"})
	if err != nil {
		t.Fatalf("get app: %v", err)
	}
	if len(got.Charts) != 1 || got.Charts[0].GetArgoApplicationNameOverrides()["prod"] != "legacy-app-prod" {
		t.Fatalf("expected override to persist through GetApp's chart list, got %+v", got.Charts)
	}

	cleared, err := srv.SetChartArgoApplicationNameOverride(ctx, &pb.SetChartArgoApplicationNameOverrideRequest{
		FullName:       "demo-chart",
		EnvironmentKey: "prod",
		Reason:         "reverting to the naming convention",
	})
	if err != nil {
		t.Fatalf("clear override: %v", err)
	}
	if got := cleared.Chart.GetArgoApplicationNameOverrides()["prod"]; got != "" {
		t.Fatalf("expected override cleared, got %q", got)
	}
}

// TestSetChartArgoApplicationNameOverride_EnvironmentsAreIndependent proves
// setting dev's override never touches prod's, and the two can carry
// completely unrelated names -- the scenario a single per-chart template
// (the design this superseded) couldn't express.
func TestSetChartArgoApplicationNameOverride_EnvironmentsAreIndependent(t *testing.T) {
	ctx := authedCtx()
	srv := NewAppServer(fake.New())

	if _, err := srv.ReconcileApps(ctx, &pb.ReconcileAppsRequest{
		Manifests: manifestSet([]*appmetapb.AppManifest{oneApp("demo", "svc")},
			[]*appmetapb.ChartManifest{{Domain: "demo", Name: "chart", Apps: []string{"svc"}}}),
		IdempotencyKey: "chartoverride-independent-1",
	}); err != nil {
		t.Fatalf("reconcile: %v", err)
	}

	if _, err := srv.SetChartArgoApplicationNameOverride(ctx, &pb.SetChartArgoApplicationNameOverrideRequest{
		FullName:            "demo-chart",
		EnvironmentKey:      "dev",
		ArgoApplicationName: "foo-dev-app",
		Reason:              "ad-hoc dev deployment",
	}); err != nil {
		t.Fatalf("set dev override: %v", err)
	}

	setProd, err := srv.SetChartArgoApplicationNameOverride(ctx, &pb.SetChartArgoApplicationNameOverrideRequest{
		FullName:            "demo-chart",
		EnvironmentKey:      "prod",
		ArgoApplicationName: "prod-svc-foo",
		Reason:              "ad-hoc prod deployment, unrelated name to dev",
	})
	if err != nil {
		t.Fatalf("set prod override: %v", err)
	}

	overrides := setProd.Chart.GetArgoApplicationNameOverrides()
	if overrides["dev"] != "foo-dev-app" {
		t.Fatalf("expected dev's override to survive setting prod's, got %q", overrides["dev"])
	}
	if overrides["prod"] != "prod-svc-foo" {
		t.Fatalf("expected prod override %q, got %q", "prod-svc-foo", overrides["prod"])
	}

	clearedDev, err := srv.SetChartArgoApplicationNameOverride(ctx, &pb.SetChartArgoApplicationNameOverrideRequest{
		FullName:       "demo-chart",
		EnvironmentKey: "dev",
		Reason:         "reverting dev to the naming convention",
	})
	if err != nil {
		t.Fatalf("clear dev override: %v", err)
	}
	overrides = clearedDev.Chart.GetArgoApplicationNameOverrides()
	if _, ok := overrides["dev"]; ok {
		t.Fatalf("expected dev's override cleared, got %+v", overrides)
	}
	if overrides["prod"] != "prod-svc-foo" {
		t.Fatalf("expected prod's override to survive clearing dev's, got %+v", overrides)
	}
}

func TestSetChartArgoApplicationNameOverride_RequiresReason(t *testing.T) {
	ctx := authedCtx()
	srv := NewAppServer(fake.New())
	_, err := srv.SetChartArgoApplicationNameOverride(ctx, &pb.SetChartArgoApplicationNameOverrideRequest{FullName: "demo-chart", EnvironmentKey: "prod"})
	if status.Code(err) != codes.InvalidArgument {
		t.Fatalf("expected InvalidArgument when reason is missing, got %v", err)
	}
}

func TestSetChartArgoApplicationNameOverride_RequiresEnvironmentKey(t *testing.T) {
	ctx := authedCtx()
	srv := NewAppServer(fake.New())
	_, err := srv.SetChartArgoApplicationNameOverride(ctx, &pb.SetChartArgoApplicationNameOverrideRequest{FullName: "demo-chart", Reason: "cleanup"})
	if status.Code(err) != codes.InvalidArgument {
		t.Fatalf("expected InvalidArgument when environment_key is missing, got %v", err)
	}
}

func TestSetChartArgoApplicationNameOverride_RequiresChartIdentifier(t *testing.T) {
	ctx := authedCtx()
	srv := NewAppServer(fake.New())
	_, err := srv.SetChartArgoApplicationNameOverride(ctx, &pb.SetChartArgoApplicationNameOverrideRequest{EnvironmentKey: "prod", Reason: "cleanup"})
	if status.Code(err) != codes.InvalidArgument {
		t.Fatalf("expected InvalidArgument when neither chart_id nor full_name is set, got %v", err)
	}
}

func TestSetChartArgoApplicationNameOverride_UnknownChartNotFound(t *testing.T) {
	ctx := authedCtx()
	srv := NewAppServer(fake.New())
	_, err := srv.SetChartArgoApplicationNameOverride(ctx, &pb.SetChartArgoApplicationNameOverrideRequest{
		FullName: "demo-does-not-exist", EnvironmentKey: "prod", Reason: "cleanup",
	})
	if status.Code(err) != codes.NotFound {
		t.Fatalf("expected NotFound for an unknown chart, got %v", err)
	}
}

// ============================================================================
// AssertApps (AR-7c, issue #558)
// ============================================================================

// TestAssertApps_CreatesIdentity covers the additive create path: a brand
// new app/chart, asserted from what release.yml treats as "the released
// ref" -- exercised here with no prior ReconcileApps call at all, proving
// AssertApps needs no main-sweep to have run first.
func TestAssertApps_CreatesIdentity(t *testing.T) {
	ctx := authedCtx()
	srv := NewAppServer(fake.New())

	resp, err := srv.AssertApps(ctx, &pb.AssertAppsRequest{
		Manifests: manifestSet(
			[]*appmetapb.AppManifest{oneApp("demo", "svc")},
			[]*appmetapb.ChartManifest{{Domain: "demo", Name: "chart"}},
		),
		IdempotencyKey: "assert-1",
	})
	if err != nil {
		t.Fatalf("assert: %v", err)
	}
	if len(resp.CreatedApps) != 1 || resp.CreatedApps[0].Status != pb.AppStatus_APP_STATUS_ACTIVE {
		t.Fatalf("expected 1 created ACTIVE app, got %+v", resp.CreatedApps)
	}
	if len(resp.CreatedCharts) != 1 || resp.CreatedCharts[0].Status != pb.AppStatus_APP_STATUS_ACTIVE {
		t.Fatalf("expected 1 created ACTIVE chart, got %+v", resp.CreatedCharts)
	}
}

// TestAssertApps_NeverMarksMissing proves the defining difference from
// ReconcileApps: calling AssertApps with an EMPTY manifest set (as a
// divergent/unmerged ref legitimately might, if the app doesn't exist on
// that ref) must never mark a previously-asserted app MISSING -- only
// ReconcileApps's absence sweep does that, and only from main.
func TestAssertApps_NeverMarksMissing(t *testing.T) {
	ctx := authedCtx()
	srv := NewAppServer(fake.New())

	if _, err := srv.AssertApps(ctx, &pb.AssertAppsRequest{
		Manifests:      manifestSet([]*appmetapb.AppManifest{oneApp("demo", "svc")}, nil),
		IdempotencyKey: "assert-2-1",
	}); err != nil {
		t.Fatalf("first assert: %v", err)
	}

	resp, err := srv.AssertApps(ctx, &pb.AssertAppsRequest{
		Manifests:      manifestSet(nil, nil),
		IdempotencyKey: "assert-2-2",
	})
	if err != nil {
		t.Fatalf("empty-set assert: %v", err)
	}
	if len(resp.CreatedApps) != 0 || len(resp.UpdatedApps) != 0 || len(resp.RecoveredApps) != 0 {
		t.Fatalf("expected an empty-set AssertApps call to touch nothing, got %+v", resp)
	}

	got, err := srv.GetApp(ctx, &pb.GetAppRequest{FullName: "demo-svc"})
	if err != nil {
		t.Fatalf("get app: %v", err)
	}
	if got.App.Status != pb.AppStatus_APP_STATUS_ACTIVE {
		t.Fatalf("expected demo-svc to remain ACTIVE (AssertApps must never mark it MISSING), got %v", got.App.Status)
	}
}

// TestAssertApps_RecoversMissingApp: AssertApps shares Reconcile's
// automatic MISSING -> ACTIVE recovery -- it's an ADDITIVE write path, not
// a read-only one, so a release for an app main's sweep had flagged
// MISSING still un-flags it.
func TestAssertApps_RecoversMissingApp(t *testing.T) {
	ctx := authedCtx()
	srv := NewAppServer(fake.New())

	created, err := srv.ReconcileApps(ctx, &pb.ReconcileAppsRequest{
		Manifests:      manifestSet([]*appmetapb.AppManifest{oneApp("demo", "svc")}, nil),
		IdempotencyKey: "assert-3-1",
	})
	if err != nil {
		t.Fatalf("reconcile create: %v", err)
	}
	svcID := created.CreatedApps[0].AppId

	if _, err := srv.ReconcileApps(ctx, &pb.ReconcileAppsRequest{
		Manifests:      manifestSet(nil, nil),
		IdempotencyKey: "assert-3-2",
	}); err != nil {
		t.Fatalf("reconcile drop: %v", err)
	}
	if got, _ := srv.GetApp(ctx, &pb.GetAppRequest{AppId: svcID}); got.App.Status != pb.AppStatus_APP_STATUS_MISSING {
		t.Fatalf("expected svc MISSING after being dropped from the sweep, got %v", got.App.Status)
	}

	resp, err := srv.AssertApps(ctx, &pb.AssertAppsRequest{
		Manifests:      manifestSet([]*appmetapb.AppManifest{oneApp("demo", "svc")}, nil),
		IdempotencyKey: "assert-3-3",
	})
	if err != nil {
		t.Fatalf("assert: %v", err)
	}
	if len(resp.RecoveredApps) != 1 || resp.RecoveredApps[0].Status != pb.AppStatus_APP_STATUS_ACTIVE {
		t.Fatalf("expected svc recovered to ACTIVE, got %+v", resp)
	}
}

// TestAssertApps_RejectsArchivedAppPerItem is the exit criterion named in
// PLAN.md's AR-7c scope: AssertApps against an ARCHIVED app is rejected --
// but per item, not for the whole call, matching UnresolvedChart's
// established skip-and-report shape.
func TestAssertApps_RejectsArchivedAppPerItem(t *testing.T) {
	ctx := authedCtx()
	srv := NewAppServer(fake.New())

	created, err := srv.ReconcileApps(ctx, &pb.ReconcileAppsRequest{
		Manifests: manifestSet(
			[]*appmetapb.AppManifest{oneApp("demo", "gone"), oneApp("demo", "other")}, nil),
		IdempotencyKey: "assert-4-1",
	})
	if err != nil {
		t.Fatalf("reconcile create: %v", err)
	}
	goneID := created.CreatedApps[0].AppId
	if created.CreatedApps[0].Name != "gone" {
		goneID = created.CreatedApps[1].AppId
	}

	if _, err := srv.ReconcileApps(ctx, &pb.ReconcileAppsRequest{
		Manifests:      manifestSet([]*appmetapb.AppManifest{oneApp("demo", "other")}, nil),
		IdempotencyKey: "assert-4-2",
	}); err != nil {
		t.Fatalf("reconcile drop gone: %v", err)
	}
	if _, err := srv.SetAppStatus(ctx, &pb.SetAppStatusRequest{
		AppId: goneID, Status: pb.AppStatus_APP_STATUS_ARCHIVED, Reason: "gone for good",
	}); err != nil {
		t.Fatalf("archive gone: %v", err)
	}

	resp, err := srv.AssertApps(ctx, &pb.AssertAppsRequest{
		Manifests: manifestSet(
			[]*appmetapb.AppManifest{oneApp("demo", "gone"), oneApp("demo", "other")}, nil),
		IdempotencyKey: "assert-4-3",
	})
	if err != nil {
		t.Fatalf("assert should succeed for the call as a whole: %v", err)
	}
	if len(resp.RejectedApps) != 1 || resp.RejectedApps[0].Name != "gone" {
		t.Fatalf("expected 'gone' rejected as ARCHIVED, got rejected=%+v", resp.RejectedApps)
	}
	// The OTHER app in the same call must still apply -- a per-item skip,
	// not a whole-call failure.
	if len(resp.UpdatedApps) != 1 || resp.UpdatedApps[0].Name != "other" {
		t.Fatalf("expected 'other' to still apply in the same call, got updated=%+v", resp.UpdatedApps)
	}

	got, err := srv.GetApp(ctx, &pb.GetAppRequest{AppId: goneID})
	if err != nil {
		t.Fatalf("get archived app: %v", err)
	}
	if got.App.Status != pb.AppStatus_APP_STATUS_ARCHIVED {
		t.Fatalf("AssertApps must not resurrect an ARCHIVED app: expected still ARCHIVED, got %v", got.App.Status)
	}
}

// TestAssertApps_ThenRecordArtifact_NoReconcileNeeded is the AR-7c exit
// criterion closing issue #547's gap: a release from a ref calls AssertApps
// first, then RecordArtifact for a BRAND NEW app succeeds immediately --
// with NO ReconcileApps call anywhere in this test. Before AR-7c this
// sequence would hit ErrOwnerNotReconciled / exit code 3 (see
// ARCHITECTURE.md "Release-vs-reconcile gap").
func TestAssertApps_ThenRecordArtifact_NoReconcileNeeded(t *testing.T) {
	ctx := authedCtx()
	repo := fake.New()
	appSrv := NewAppServer(repo)
	artSrv := NewArtifactServer(repo)

	if _, err := appSrv.AssertApps(ctx, &pb.AssertAppsRequest{
		Manifests:      manifestSet([]*appmetapb.AppManifest{oneApp("demo", "new-app")}, nil),
		IdempotencyKey: "assert-5-1",
	}); err != nil {
		t.Fatalf("assert: %v", err)
	}

	build, err := artSrv.RecordBuild(ctx, &pb.RecordBuildRequest{
		GitSha: "sha1", WorkflowRunId: "run-1", IdempotencyKey: "build-1",
	})
	if err != nil {
		t.Fatalf("record build: %v", err)
	}

	_, err = artSrv.RecordArtifact(ctx, &pb.RecordArtifactRequest{
		BuildId: build.Build.BuildId, Kind: pb.ArtifactKind_ARTIFACT_KIND_IMAGE,
		OwnerFullName: "demo-new-app", Version: "v1.0.0", Digest: "sha256:new1",
		IdempotencyKey: "artifact-1",
	})
	if err != nil {
		t.Fatalf("RecordArtifact should succeed immediately after AssertApps, with no ReconcileApps ever having run: %v", err)
	}
}

// TestAssertApps_ThenRecordArtifact_ChartOwnerFullNameMatches is the chart
// counterpart of the test above, using the manifest shape release_helm_chart's
// Bazel macro actually emits (Name carrying a baked-in "helm-{domain}-"
// prefix) and the "{domain}-{name}" owner string release.yml's PUBLISHED_NAME
// derives from it (CHART with the literal "helm-" prefix trimmed). Before
// AssertApps normalized ChartManifest.Name, these two never matched -- see
// Chart.FullName's doc comment -- and RecordArtifact's chart-owner lookup
// failed with "not found -- has it been reconciled?" for every chart, most
// visibly for app-registry's own chart where domain == base name.
func TestAssertApps_ThenRecordArtifact_ChartOwnerFullNameMatches(t *testing.T) {
	ctx := authedCtx()
	repo := fake.New()
	appSrv := NewAppServer(repo)
	artSrv := NewArtifactServer(repo)

	if _, err := appSrv.AssertApps(ctx, &pb.AssertAppsRequest{
		Manifests: manifestSet(
			[]*appmetapb.AppManifest{oneApp("app-registry", "svc")},
			[]*appmetapb.ChartManifest{{Domain: "app-registry", Name: "helm-app-registry-app-registry", Apps: []string{"svc"}}},
		),
		IdempotencyKey: "assert-6-1",
	}); err != nil {
		t.Fatalf("assert: %v", err)
	}

	build, err := artSrv.RecordBuild(ctx, &pb.RecordBuildRequest{
		GitSha: "sha1", WorkflowRunId: "run-1", IdempotencyKey: "build-2",
	})
	if err != nil {
		t.Fatalf("record build: %v", err)
	}

	// PUBLISHED_NAME in release.yml: "${CHART#helm-}" against
	// "helm-app-registry-app-registry" -- exactly what release.yml passes as
	// --owner to BeginPublish/RecordArtifact for this chart.
	_, err = artSrv.RecordArtifact(ctx, &pb.RecordArtifactRequest{
		BuildId: build.Build.BuildId, Kind: pb.ArtifactKind_ARTIFACT_KIND_CHART,
		OwnerFullName: "app-registry-app-registry", Version: "v0.0.13", Digest: "sha256:chart13",
		IdempotencyKey: "artifact-2",
	})
	if err != nil {
		t.Fatalf("RecordArtifact should find the chart AssertApps just created by the release pipeline's owner name: %v", err)
	}
}

// TestRecordArtifact_RejectsArchivedOwner is the second AR-7c exit
// criterion named in PLAN.md: "RecordArtifact against an ARCHIVED owner is
// rejected too" -- before AR-7c this succeeded silently.
func TestRecordArtifact_RejectsArchivedOwner(t *testing.T) {
	ctx := authedCtx()
	repo := fake.New()
	appSrv := NewAppServer(repo)
	artSrv := NewArtifactServer(repo)

	created, err := appSrv.ReconcileApps(ctx, &pb.ReconcileAppsRequest{
		Manifests:      manifestSet([]*appmetapb.AppManifest{oneApp("demo", "svc")}, nil),
		IdempotencyKey: "archived-owner-1",
	})
	if err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	svcID := created.CreatedApps[0].AppId

	if _, err := appSrv.ReconcileApps(ctx, &pb.ReconcileAppsRequest{
		Manifests:      manifestSet(nil, nil),
		IdempotencyKey: "archived-owner-2",
	}); err != nil {
		t.Fatalf("reconcile drop: %v", err)
	}
	if _, err := appSrv.SetAppStatus(ctx, &pb.SetAppStatusRequest{
		AppId: svcID, Status: pb.AppStatus_APP_STATUS_ARCHIVED, Reason: "gone",
	}); err != nil {
		t.Fatalf("archive: %v", err)
	}

	build, err := artSrv.RecordBuild(ctx, &pb.RecordBuildRequest{
		GitSha: "sha1", WorkflowRunId: "run-1", IdempotencyKey: "build-2",
	})
	if err != nil {
		t.Fatalf("record build: %v", err)
	}

	_, err = artSrv.RecordArtifact(ctx, &pb.RecordArtifactRequest{
		BuildId: build.Build.BuildId, Kind: pb.ArtifactKind_ARTIFACT_KIND_IMAGE,
		OwnerFullName: "demo-svc", Version: "v1.0.0", Digest: "sha256:archived1",
		IdempotencyKey: "artifact-2",
	})
	if err == nil {
		t.Fatal("expected RecordArtifact against an ARCHIVED owner to be rejected")
	}
	if status.Code(err) != codes.FailedPrecondition {
		t.Fatalf("expected FailedPrecondition, got %v (%v)", status.Code(err), err)
	}
}
