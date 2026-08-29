//go:build integration

// Real-Postgres integration coverage for appRepo (app.go): Reconcile,
// AssertApps, the reconcile watermark, chart/app manifest resolve+record,
// and ListReconcileRuns. See postgres_integration_helpers_test.go's doc
// comment for why this package builds these files under the "integration"
// tag, and TESTING.md for how to run them.
package postgres

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/whale-net/everything/libs/go/dbtest"
	"github.com/whale-net/everything/libs/go/migrate"
	"github.com/whale-net/everything/tools/app_registry/migrate/schema"
	"github.com/whale-net/everything/tools/app_registry/server/handlers"
	"github.com/whale-net/everything/tools/app_registry/server/repository"

	pb "github.com/whale-net/everything/tools/app_registry/protos"
	appmetapb "github.com/whale-net/everything/tools/appmeta/proto"
)

// watermarkRow reads the singleton reconcile_watermark row directly via
// SQL, bypassing the repository layer, so assertions are against ground
// truth rather than re-testing the same code path they're meant to verify.
func watermarkRow(t *testing.T, pool *pgxpool.Pool) (gitSha string, sourceCommittedAt, discoveredAt int64) {
	t.Helper()
	if err := pool.QueryRow(context.Background(),
		`SELECT git_sha, source_committed_at, discovered_at FROM reconcile_watermark WHERE id = 1`,
	).Scan(&gitSha, &sourceCommittedAt, &discoveredAt); err != nil {
		t.Fatalf("read reconcile_watermark: %v", err)
	}
	return gitSha, sourceCommittedAt, discoveredAt
}

func appStatus(t *testing.T, pool *pgxpool.Pool, appID string) string {
	t.Helper()
	var status string
	if err := pool.QueryRow(context.Background(), `SELECT status FROM app WHERE app_id = $1`, appID).Scan(&status); err != nil {
		t.Fatalf("read app status for %s: %v", appID, err)
	}
	return status
}

func chartStatus(t *testing.T, pool *pgxpool.Pool, chartID string) string {
	t.Helper()
	var status string
	if err := pool.QueryRow(context.Background(), `SELECT status FROM chart WHERE chart_id = $1`, chartID).Scan(&status); err != nil {
		t.Fatalf("read chart status for %s: %v", chartID, err)
	}
	return status
}

// TestMigration006SeedsSentinelWatermarkRow proves migration 006's seed:
// exactly one row, the documented sentinel (empty git_sha, zero
// timestamps) -- see that migration's comments for why a seeded sentinel,
// not a genuinely empty table, is what "no watermark yet" means at the SQL
// layer.
func TestMigration006SeedsSentinelWatermarkRow(t *testing.T) {
	_, pool := newTestRegistry(t)

	var rowCount int
	if err := pool.QueryRow(context.Background(), `SELECT count(*) FROM reconcile_watermark`).Scan(&rowCount); err != nil {
		t.Fatalf("count reconcile_watermark rows: %v", err)
	}
	if rowCount != 1 {
		t.Fatalf("expected exactly 1 seeded row, found %d", rowCount)
	}

	gitSha, sourceCommittedAt, discoveredAt := watermarkRow(t, pool)
	if gitSha != "" || sourceCommittedAt != 0 || discoveredAt != 0 {
		t.Fatalf("expected the documented sentinel (empty git_sha, zero timestamps), got git_sha=%q source_committed_at=%d discovered_at=%d",
			gitSha, sourceCommittedAt, discoveredAt)
	}
}

// TestReconcileWatermark_FirstCallAppliesAgainstEmptyWatermark proves the
// "empty table (sentinel row) means accept the first call" guarantee, and
// that a successful apply advances the watermark to the incoming call's
// ordering metadata.
func TestReconcileWatermark_FirstCallAppliesAgainstEmptyWatermark(t *testing.T) {
	reg, pool := newTestRegistry(t)
	ctx := authedCtx()
	srv := handlers.NewAppServer(reg)

	resp, err := srv.ReconcileApps(ctx, &pb.ReconcileAppsRequest{
		Manifests:      reconcileManifests("sha-1", 1000, 1100, []*appmetapb.AppManifest{oneAppManifest("acme", "widget")}, nil),
		IdempotencyKey: "watermark-first-1",
	})
	if err != nil {
		t.Fatalf("reconcile against empty watermark: %v", err)
	}
	if resp.SkippedStale {
		t.Fatalf("expected the first-ever reconcile to apply, got SkippedStale=true")
	}
	if len(resp.CreatedApps) != 1 {
		t.Fatalf("expected 1 created app, got %+v", resp.CreatedApps)
	}

	gitSha, sourceCommittedAt, discoveredAt := watermarkRow(t, pool)
	if gitSha != "sha-1" || sourceCommittedAt != 1000 || discoveredAt != 1100 {
		t.Fatalf("expected watermark to advance to (sha-1, 1000, 1100), got (%q, %d, %d)", gitSha, sourceCommittedAt, discoveredAt)
	}
}

// TestReconcileWatermark_StaleCallSkippedAndMutatesNothing is the headline
// scenario from issue #545: an older commit's reconcile call lands AFTER a
// newer one's, which had correctly flagged an app MISSING. It proves three
// things a fake-backed test can assert too, but which matter most against
// real Postgres because they hinge on the transaction actually rolling
// back cleanly: (1) the call is a no-op success (SkippedStale=true, not an
// error), (2) it names the commit it lost to, and (3) NOTHING was
// written -- most importantly, the MISSING flag the newer call set survives
// completely untouched, proving the stale call didn't revert it.
func TestReconcileWatermark_StaleCallSkippedAndMutatesNothing(t *testing.T) {
	reg, pool := newTestRegistry(t)
	ctx := authedCtx()
	srv := handlers.NewAppServer(reg)

	// 1. An early commit reconciles two apps.
	first, err := srv.ReconcileApps(ctx, &pb.ReconcileAppsRequest{
		Manifests: reconcileManifests("newer-sha", 2000, 2000,
			[]*appmetapb.AppManifest{oneAppManifest("acme", "widget"), oneAppManifest("acme", "gadget")}, nil),
		IdempotencyKey: "stale-1",
	})
	if err != nil {
		t.Fatalf("first reconcile: %v", err)
	}
	if len(first.CreatedApps) != 2 {
		t.Fatalf("expected 2 created apps, got %+v", first.CreatedApps)
	}
	widgetID, gadgetID := first.CreatedApps[0].AppId, first.CreatedApps[1].AppId

	// 2. A LATER, newer commit correctly drops "gadget" -- it's flagged
	// MISSING, exactly as ARCHITECTURE.md's triage lifecycle says it should
	// be.
	second, err := srv.ReconcileApps(ctx, &pb.ReconcileAppsRequest{
		Manifests: reconcileManifests("newest-sha", 3000, 3000,
			[]*appmetapb.AppManifest{oneAppManifest("acme", "widget")}, nil),
		IdempotencyKey: "stale-2",
	})
	if err != nil {
		t.Fatalf("second (newer) reconcile: %v", err)
	}
	if len(second.NewlyMissingApps) != 1 || second.NewlyMissingApps[0].AppId != gadgetID {
		t.Fatalf("expected gadget (%s) newly missing, got %+v", gadgetID, second.NewlyMissingApps)
	}

	beforeGadgetStatus := appStatus(t, pool, gadgetID)
	beforeWidgetStatus := appStatus(t, pool, widgetID)
	if beforeGadgetStatus != "missing" {
		t.Fatalf("expected gadget to be MISSING before the stale call, got %q", beforeGadgetStatus)
	}
	beforeGitSha, beforeSCA, beforeDA := watermarkRow(t, pool)

	// 3. A STALE call: an older commit (source_committed_at between the
	// first and second) re-runs -- e.g. a manually re-run older CI
	// workflow, issue #545's headline case. If applied, this would re-mark
	// "gadget" ACTIVE, silently reverting the second call's correct MISSING
	// flag.
	stale, err := srv.ReconcileApps(ctx, &pb.ReconcileAppsRequest{
		Manifests: reconcileManifests("older-rerun-sha", 2500, 2500,
			[]*appmetapb.AppManifest{oneAppManifest("acme", "widget"), oneAppManifest("acme", "gadget")}, nil),
		IdempotencyKey: "stale-3",
	})
	if err != nil {
		t.Fatalf("stale reconcile call: %v", err)
	}
	if !stale.SkippedStale {
		t.Fatalf("expected the older call to be skipped as stale, got %+v", stale)
	}
	if stale.CurrentWatermarkGitSha != "newest-sha" {
		t.Fatalf("expected current_watermark_git_sha to name the commit it lost to (newest-sha), got %q", stale.CurrentWatermarkGitSha)
	}
	if n := len(stale.CreatedApps) + len(stale.UpdatedApps) + len(stale.NewlyMissingApps) + len(stale.RecoveredApps) +
		len(stale.CreatedCharts) + len(stale.UpdatedCharts) + len(stale.NewlyMissingCharts) + len(stale.RecoveredCharts); n != 0 {
		t.Fatalf("expected a completely empty result for a skipped-stale call, got %+v", stale)
	}

	// Prove nothing was mutated: gadget is STILL MISSING (the stale call
	// did not revert it to ACTIVE), widget is untouched, and the watermark
	// did not move.
	if got := appStatus(t, pool, gadgetID); got != "missing" {
		t.Fatalf("stale call reverted gadget's MISSING flag -- now %q; this is exactly the bug issue #545 exists to prevent", got)
	}
	if got := appStatus(t, pool, gadgetID); got != beforeGadgetStatus {
		t.Fatalf("stale call mutated gadget's status: was %q, now %q", beforeGadgetStatus, got)
	}
	if got := appStatus(t, pool, widgetID); got != beforeWidgetStatus {
		t.Fatalf("stale call mutated widget's status: was %q, now %q", beforeWidgetStatus, got)
	}
	gotGitSha, gotSCA, gotDA := watermarkRow(t, pool)
	if gotGitSha != beforeGitSha || gotSCA != beforeSCA || gotDA != beforeDA {
		t.Fatalf("stale call advanced the watermark: was (%q,%d,%d), now (%q,%d,%d)",
			beforeGitSha, beforeSCA, beforeDA, gotGitSha, gotSCA, gotDA)
	}
}

// TestReconcileWatermark_EqualOrderingKeyDifferentGitShaApplies proves the
// deliberate equal-timestamp tie-break: two different commits landing with
// the same source_committed_at (a same-second merge, or two calls both
// falling back to discovered_at) must not block each other.
func TestReconcileWatermark_EqualOrderingKeyDifferentGitShaApplies(t *testing.T) {
	reg, pool := newTestRegistry(t)
	ctx := authedCtx()
	srv := handlers.NewAppServer(reg)

	if _, err := srv.ReconcileApps(ctx, &pb.ReconcileAppsRequest{
		Manifests:      reconcileManifests("sha-a", 5000, 5000, []*appmetapb.AppManifest{oneAppManifest("acme", "widget")}, nil),
		IdempotencyKey: "tie-1",
	}); err != nil {
		t.Fatalf("first reconcile: %v", err)
	}

	resp, err := srv.ReconcileApps(ctx, &pb.ReconcileAppsRequest{
		Manifests: reconcileManifests("sha-b", 5000, 5000,
			[]*appmetapb.AppManifest{oneAppManifest("acme", "widget"), oneAppManifest("acme", "gadget")}, nil),
		IdempotencyKey: "tie-2",
	})
	if err != nil {
		t.Fatalf("tied-timestamp reconcile: %v", err)
	}
	if resp.SkippedStale {
		t.Fatalf("expected an equal-timestamp, different-git_sha call to apply, got SkippedStale=true")
	}
	if len(resp.CreatedApps) != 1 || resp.CreatedApps[0].FullName != "acme-gadget" {
		t.Fatalf("expected gadget to be newly created, got %+v", resp.CreatedApps)
	}

	gitSha, sourceCommittedAt, _ := watermarkRow(t, pool)
	if gitSha != "sha-b" || sourceCommittedAt != 5000 {
		t.Fatalf("expected watermark to advance to (sha-b, 5000), got (%q, %d)", gitSha, sourceCommittedAt)
	}
}

// TestReconcileWatermark_IdenticalGitShaAppliesRegardlessOfTimestamp proves
// tie-break rule 2: the identical commit reconciled twice always applies,
// even with an older ordering key the second time (clock skew between two
// sweeps of the same commit must never produce a false-stale rejection).
func TestReconcileWatermark_IdenticalGitShaAppliesRegardlessOfTimestamp(t *testing.T) {
	reg, pool := newTestRegistry(t)
	ctx := authedCtx()
	srv := handlers.NewAppServer(reg)

	if _, err := srv.ReconcileApps(ctx, &pb.ReconcileAppsRequest{
		Manifests:      reconcileManifests("same-sha", 9000, 9000, []*appmetapb.AppManifest{oneAppManifest("acme", "widget")}, nil),
		IdempotencyKey: "same-1",
	}); err != nil {
		t.Fatalf("first reconcile: %v", err)
	}

	resp, err := srv.ReconcileApps(ctx, &pb.ReconcileAppsRequest{
		Manifests: reconcileManifests("same-sha", 1000, 1000, // older than 9000
			[]*appmetapb.AppManifest{oneAppManifest("acme", "widget"), oneAppManifest("acme", "gadget")}, nil),
		IdempotencyKey: "same-2",
	})
	if err != nil {
		t.Fatalf("identical-git_sha reconcile: %v", err)
	}
	if resp.SkippedStale {
		t.Fatalf("expected an identical git_sha call to apply regardless of its (older) timestamp, got SkippedStale=true")
	}
	if len(resp.CreatedApps) != 1 || resp.CreatedApps[0].FullName != "acme-gadget" {
		t.Fatalf("expected gadget to be newly created, got %+v", resp.CreatedApps)
	}

	gitSha, sourceCommittedAt, _ := watermarkRow(t, pool)
	if gitSha != "same-sha" || sourceCommittedAt != 1000 {
		t.Fatalf("expected watermark to be refreshed to (same-sha, 1000), got (%q, %d)", gitSha, sourceCommittedAt)
	}
}

// TestReconcileWatermark_DryRunNeverConsultsOrAdvancesWatermark proves dry
// run is unaffected by the watermark in EITHER direction: a dry run
// carrying an ordering key far older than the current watermark still
// computes a normal diff (proving the watermark is never consulted to
// decide skip-or-apply), and the watermark row is byte-for-byte unchanged
// afterward (proving it's never advanced either).
func TestReconcileWatermark_DryRunNeverConsultsOrAdvancesWatermark(t *testing.T) {
	reg, pool := newTestRegistry(t)
	ctx := authedCtx()
	srv := handlers.NewAppServer(reg)

	if _, err := srv.ReconcileApps(ctx, &pb.ReconcileAppsRequest{
		Manifests:      reconcileManifests("real-sha", 999999, 999999, []*appmetapb.AppManifest{oneAppManifest("acme", "widget")}, nil),
		IdempotencyKey: "dry-1",
	}); err != nil {
		t.Fatalf("real reconcile: %v", err)
	}
	beforeGitSha, beforeSCA, beforeDA := watermarkRow(t, pool)

	resp, err := srv.ReconcileApps(ctx, &pb.ReconcileAppsRequest{
		Manifests: reconcileManifests("dry-run-old-sha", 1, 1,
			[]*appmetapb.AppManifest{oneAppManifest("acme", "widget"), oneAppManifest("acme", "new-app")}, nil),
		DryRun: true,
	})
	if err != nil {
		t.Fatalf("dry run reconcile: %v", err)
	}
	if resp.SkippedStale {
		t.Fatalf("dry run must never consult the watermark, so it must never report SkippedStale; got true despite carrying a far-older ordering key")
	}
	if len(resp.CreatedApps) != 1 || resp.CreatedApps[0].FullName != "acme-new-app" {
		t.Fatalf("expected dry run to compute a normal diff (1 created app), got %+v", resp.CreatedApps)
	}

	gotGitSha, gotSCA, gotDA := watermarkRow(t, pool)
	if gotGitSha != beforeGitSha || gotSCA != beforeSCA || gotDA != beforeDA {
		t.Fatalf("dry run advanced the watermark: was (%q,%d,%d), now (%q,%d,%d)",
			beforeGitSha, beforeSCA, beforeDA, gotGitSha, gotSCA, gotDA)
	}

	var count int
	if err := pool.QueryRow(context.Background(), `SELECT count(*) FROM app WHERE domain = 'acme' AND name = 'new-app'`).Scan(&count); err != nil {
		t.Fatalf("count app rows: %v", err)
	}
	if count != 0 {
		t.Fatalf("dry run must write nothing; found %d 'new-app' rows", count)
	}
}

// TestReconcileWatermark_ConcurrentCallsSerializeOnTheLockedRow drives two
// real concurrent Reconcile calls -- a lower ordering key and a higher
// one -- through separate goroutines and asserts the final watermark is
// always the HIGHER key, regardless of which goroutine's transaction
// happened to start first. Without the SELECT ... FOR UPDATE lock on
// reconcile_watermark, both transactions could read "no watermark yet"
// concurrently and both apply, racing on which one's final INSERT ... ON
// CONFLICT DO UPDATE commits last -- which could non-deterministically
// leave the LOWER key as the final watermark. That would be exactly the
// kind of unserialized race issue #545 exists to close, so this test
// would flake/fail on an unlucky interleaving if the locking read were
// ever removed or weakened.
func TestReconcileWatermark_ConcurrentCallsSerializeOnTheLockedRow(t *testing.T) {
	reg, pool := newTestRegistry(t)
	ctx := authedCtx()
	srv := handlers.NewAppServer(reg)

	var wg sync.WaitGroup
	wg.Add(2)
	errs := make([]error, 2)

	go func() {
		defer wg.Done()
		_, err := srv.ReconcileApps(ctx, &pb.ReconcileAppsRequest{
			Manifests:      reconcileManifests("low-sha", 100, 100, []*appmetapb.AppManifest{oneAppManifest("acme", "widget")}, nil),
			IdempotencyKey: "race-low",
		})
		errs[0] = err
	}()
	go func() {
		defer wg.Done()
		_, err := srv.ReconcileApps(ctx, &pb.ReconcileAppsRequest{
			Manifests:      reconcileManifests("high-sha", 200, 200, []*appmetapb.AppManifest{oneAppManifest("acme", "widget")}, nil),
			IdempotencyKey: "race-high",
		})
		errs[1] = err
	}()
	wg.Wait()

	for i, err := range errs {
		if err != nil {
			t.Fatalf("goroutine %d: %v", i, err)
		}
	}

	gitSha, sourceCommittedAt, _ := watermarkRow(t, pool)
	if gitSha != "high-sha" || sourceCommittedAt != 200 {
		t.Fatalf("expected the higher ordering key (high-sha, 200) to win regardless of goroutine scheduling, got (%q, %d) -- this means the two concurrent Reconcile calls were NOT properly serialized by the watermark's locking read",
			gitSha, sourceCommittedAt)
	}
}

// ============================================================================
// AR-7a: sweep robustness -- partial-apply Reconcile
// ============================================================================
//
// PLAN.md's AR-7a calls out a real-Postgres integration test explicitly: the
// fake (server/repository/fake) cannot catch a transaction-rollback
// regression here, because its dryRun path is a superficial in-memory clone,
// not a real database transaction. Pre-AR-7a, resolveChartApps returning any
// error propagated straight out of Reconcile and aborted the WHOLE WithTx
// transaction -- see TestRecordArtifact_ChartLinkFailureRollsBackTransaction
// above for the established "real rollback" pattern this mirrors. AR-7a's
// fix only works if the unresolved-chart error stays IN-BAND (the
// *chartResolutionError path in postgres/app.go's Reconcile) rather than
// propagating -- if that regressed, this test would see the whole call fail
// and nothing committed, exactly like the pre-AR-7a behavior.

// TestReconcile_UnresolvedChartDoesNotRollBackWholeTransaction is AR-7a's
// headline exit criterion: "a chart manifest naming a nonexistent app leaves
// every other app/chart registered, advances the watermark, and reports the
// offending chart."
func TestReconcile_UnresolvedChartDoesNotRollBackWholeTransaction(t *testing.T) {
	reg, pool := newTestRegistry(t)
	ctx := authedCtx()
	srv := handlers.NewAppServer(reg)

	resp, err := srv.ReconcileApps(ctx, &pb.ReconcileAppsRequest{
		Manifests: reconcileManifests("sha-ar7a-1", 10000, 10000,
			[]*appmetapb.AppManifest{oneAppManifest("acme", "good-app")},
			[]*appmetapb.ChartManifest{
				{Domain: "acme", Name: "bad-chart", Apps: []string{"nonexistent-app"}},
				{Domain: "acme", Name: "good-chart", Apps: []string{"good-app"}},
			},
		),
		IdempotencyKey: "ar7a-1",
	})
	if err != nil {
		t.Fatalf("reconcile with one bad chart must not fail the whole call: %v", err)
	}

	// 1. The offending chart is reported, not fatal.
	if len(resp.UnresolvedCharts) != 1 {
		t.Fatalf("expected exactly 1 unresolved chart, got %+v", resp.UnresolvedCharts)
	}
	uc := resp.UnresolvedCharts[0]
	if uc.Domain != "acme" || uc.Name != "bad-chart" {
		t.Fatalf("expected unresolved chart acme/bad-chart, got %+v", uc)
	}
	if len(uc.AppRefs) != 1 || uc.AppRefs[0] != "nonexistent-app" {
		t.Fatalf("expected offending app_refs=[nonexistent-app], got %+v", uc.AppRefs)
	}
	if uc.Reason == "" {
		t.Fatal("expected a non-empty reason")
	}

	// 2. Every OTHER app and chart in the same call still registered -- the
	// assertion only a real Postgres transaction proves: a regression back to
	// whole-transaction rollback would make good-app/good-chart vanish along
	// with bad-chart.
	if len(resp.CreatedApps) != 1 || resp.CreatedApps[0].Name != "good-app" {
		t.Fatalf("expected good-app to be created, got %+v", resp.CreatedApps)
	}
	if len(resp.CreatedCharts) != 1 || resp.CreatedCharts[0].Name != "good-chart" {
		t.Fatalf("expected only good-chart to be created, got %+v", resp.CreatedCharts)
	}

	appRow, err := reg.Apps().GetAppByFullName(ctx, "acme-good-app")
	if err != nil {
		t.Fatalf("good-app must be queryable after commit: %v", err)
	}
	if appRow.Status != repository.StatusActive {
		t.Fatalf("expected good-app ACTIVE, got %s", appRow.Status)
	}

	chartRow, err := reg.Apps().GetChartByFullName(ctx, "acme-good-chart")
	if err != nil {
		t.Fatalf("good-chart must be queryable after commit: %v", err)
	}
	if chartRow.Status != repository.StatusActive {
		t.Fatalf("expected good-chart ACTIVE, got %s", chartRow.Status)
	}

	// bad-chart must not have been written at all.
	if _, err := reg.Apps().GetChartByFullName(ctx, "acme-bad-chart"); !errors.Is(err, repository.ErrNotFound) {
		t.Fatalf("expected bad-chart to not exist, got err=%v", err)
	}

	// 3. The watermark still advanced -- pre-AR-7a this call would have
	// rolled back before ever reaching the watermark write.
	gitSha, sourceCommittedAt, _ := watermarkRow(t, pool)
	if gitSha != "sha-ar7a-1" || sourceCommittedAt != 10000 {
		t.Fatalf("expected the watermark to advance to (sha-ar7a-1, 10000) despite the unresolved chart, got (%q, %d)", gitSha, sourceCommittedAt)
	}
}

// TestReconcile_UnresolvedChartNotMarkedMissing_Postgres proves the
// deliberate semantics ARCHITECTURE.md "AssertApps (additive) vs.
// ReconcileApps (absence sweep)" states: a chart already registered that
// becomes unresolvable in a later reconcile is skipped, not swept into
// MISSING -- against real Postgres, where the absence sweep is a real SQL
// statement scanning `status = 'active'` (see Reconcile's `SELECT ... FROM
// chart WHERE status = 'active'`), not the fake's map iteration.
func TestReconcile_UnresolvedChartNotMarkedMissing_Postgres(t *testing.T) {
	reg, pool := newTestRegistry(t)
	ctx := authedCtx()
	srv := handlers.NewAppServer(reg)

	first, err := srv.ReconcileApps(ctx, &pb.ReconcileAppsRequest{
		Manifests: reconcileManifests("sha-ar7a-2a", 20000, 20000,
			[]*appmetapb.AppManifest{oneAppManifest("acme", "svc")},
			[]*appmetapb.ChartManifest{{Domain: "acme", Name: "chart", Apps: []string{"svc"}}},
		),
		IdempotencyKey: "ar7a-2a",
	})
	if err != nil {
		t.Fatalf("initial reconcile: %v", err)
	}
	if len(first.CreatedCharts) != 1 {
		t.Fatalf("expected 1 created chart, got %+v", first.CreatedCharts)
	}
	chartID := first.CreatedCharts[0].ChartId

	// Reconcile again: the chart's manifest now references an app that does
	// not exist ("svc" renamed without updating the chart) -- an unresolvable
	// reference. The chart itself is still present in this call's manifest
	// set, unlike an app/chart that simply drops out.
	second, err := srv.ReconcileApps(ctx, &pb.ReconcileAppsRequest{
		Manifests: reconcileManifests("sha-ar7a-2b", 21000, 21000,
			[]*appmetapb.AppManifest{oneAppManifest("acme", "svc")},
			[]*appmetapb.ChartManifest{{Domain: "acme", Name: "chart", Apps: []string{"renamed-away"}}},
		),
		IdempotencyKey: "ar7a-2b",
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

	if got := chartStatus(t, pool, chartID); got != "active" {
		t.Fatalf("expected the chart to remain ACTIVE (present, not absent) after becoming unresolvable, got %q", got)
	}
}

// TestReconcile_DomainQualifiedAppRefsResolveUnambiguously_Postgres proves
// AR-7a's fix for cross-domain bare-name ambiguity against real Postgres:
// two apps sharing a bare name in different domains previously made
// resolveChartApps's `SELECT app_id FROM app WHERE name = $1` return 2 rows
// and fail; a chart using AppRefs (domain-qualified) resolves
// deterministically via getAppByDomainName instead, bypassing that query
// entirely.
func TestReconcile_DomainQualifiedAppRefsResolveUnambiguously_Postgres(t *testing.T) {
	reg, _ := newTestRegistry(t)
	ctx := authedCtx()
	srv := handlers.NewAppServer(reg)

	resp, err := srv.ReconcileApps(ctx, &pb.ReconcileAppsRequest{
		Manifests: reconcileManifests("sha-ar7a-3", 30000, 30000,
			[]*appmetapb.AppManifest{oneAppManifest("domain-a", "shared-name"), oneAppManifest("domain-b", "shared-name")},
			[]*appmetapb.ChartManifest{
				{Domain: "domain-b", Name: "chart", AppRefs: []string{"domain-b/shared-name"}},
			},
		),
		IdempotencyKey: "ar7a-3",
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

	domainBApp, err := reg.Apps().GetAppByFullName(ctx, "domain-b-shared-name")
	if err != nil {
		t.Fatalf("get domain-b's app: %v", err)
	}
	if resp.CreatedCharts[0].AppIds[0] != domainBApp.AppID {
		t.Fatalf("expected the chart to resolve to domain-b's app (id=%s), got %+v", domainBApp.AppID, resp.CreatedCharts[0].AppIds)
	}
}

// ============================================================================
// 10. App identity / manifest snapshot split (AR-7c, migration 008, issue #558)
// ============================================================================

// appManifestReleaseRow reads back one app_manifest_release row's joined
// content generated columns, for asserting the generated-column derivation
// directly against real Postgres (not just through v_current_app) at an
// exact release commit (migration 010, AR-8).
func appManifestReleaseRow(t *testing.T, pool *pgxpool.Pool, appID, gitSHA string) (deployUnit, imageRepository string, found bool) {
	t.Helper()
	err := pool.QueryRow(context.Background(), `
		SELECT m.deploy_unit, m.image_repository
		FROM app_manifest_release r
		JOIN app_manifest m ON m.app_manifest_id = r.app_manifest_id
		WHERE r.owner_id = $1 AND r.git_sha = $2`, appID, gitSHA).Scan(&deployUnit, &imageRepository)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", "", false
	}
	if err != nil {
		t.Fatalf("read app_manifest_release for %s/%s: %v", appID, gitSHA, err)
	}
	return deployUnit, imageRepository, true
}

// appManifestContentCount counts DISTINCT-manifest content rows (migration
// 010, AR-8) -- one per manifest ever seen for this owner, not one per
// commit.
func appManifestContentCount(t *testing.T, pool *pgxpool.Pool, appID string) int {
	t.Helper()
	var n int
	if err := pool.QueryRow(context.Background(), `SELECT count(*) FROM app_manifest WHERE owner_id = $1`, appID).Scan(&n); err != nil {
		t.Fatalf("count app_manifest content rows for %s: %v", appID, err)
	}
	return n
}

// appManifestHistoryCount counts app_manifest_history rows (the `main`
// sweep timeline, migration 010, AR-8) for this owner.
func appManifestHistoryCount(t *testing.T, pool *pgxpool.Pool, appID string) int {
	t.Helper()
	var n int
	if err := pool.QueryRow(context.Background(), `SELECT count(*) FROM app_manifest_history WHERE owner_id = $1`, appID).Scan(&n); err != nil {
		t.Fatalf("count app_manifest_history rows for %s: %v", appID, err)
	}
	return n
}

// TestAssertApps_CreatesIdentityAndSnapshot_Postgres proves AR-7c's AssertApps
// against real Postgres, updated for AR-8/migration 010's content+release
// split: identity row created, exactly one app_manifest content row and one
// app_manifest_release row written, the generated deploy_unit/
// image_repository columns resolve correctly straight off manifest_json, and
// (the AR-8 acceptance criterion) AssertApps NEVER writes to
// app_manifest_history -- that stays ReconcileApps/sweep-only.
func TestAssertApps_CreatesIdentityAndSnapshot_Postgres(t *testing.T) {
	reg, pool := newTestRegistry(t)
	ctx := authedCtx()
	srv := handlers.NewAppServer(reg)

	am := &appmetapb.AppManifest{
		Domain: "acme", Name: "assert-svc", DeployUnit: appmetapb.DeployUnit_DEPLOY_UNIT_IMAGE,
		Registry: "ghcr.io", Organization: "acme", RepoName: "acme-assert-svc",
	}
	resp, err := srv.AssertApps(ctx, &pb.AssertAppsRequest{
		Manifests:      reconcileManifests("sha-assert-1", 100, 100, []*appmetapb.AppManifest{am}, nil),
		IdempotencyKey: "assert-pg-1",
	})
	if err != nil {
		t.Fatalf("assert: %v", err)
	}
	if len(resp.CreatedApps) != 1 || resp.CreatedApps[0].Status != pb.AppStatus_APP_STATUS_ACTIVE {
		t.Fatalf("expected 1 created ACTIVE app, got %+v", resp.CreatedApps)
	}
	appID := resp.CreatedApps[0].AppId

	if n := appManifestContentCount(t, pool, appID); n != 1 {
		t.Fatalf("expected exactly 1 app_manifest content row, got %d", n)
	}
	du, repoCol, found := appManifestReleaseRow(t, pool, appID, "sha-assert-1")
	if !found {
		t.Fatalf("expected an app_manifest_release row keyed (owner_id=%s, git_sha=sha-assert-1)", appID)
	}
	if du != "image" {
		t.Fatalf("expected the generated deploy_unit column to resolve 'image', got %q", du)
	}
	if repoCol != "ghcr.io/acme/acme-assert-svc" {
		t.Fatalf("expected the generated image_repository column to resolve 'ghcr.io/acme/acme-assert-svc', got %q", repoCol)
	}

	if n := appManifestHistoryCount(t, pool, appID); n != 0 {
		t.Fatalf("expected AssertApps to write zero app_manifest_history rows (sweep-only), got %d", n)
	}
}

// TestAppManifestSnapshot_IdempotentOnOwnerGitSha proves migration 010's
// UNIQUE (owner_id, git_sha) on app_manifest_release is what makes repeated
// AssertApps calls for the SAME commit naturally idempotent -- a real
// release re-run (same git_sha, different idempotency_key) writes no new
// release row. Also proves the AR-8 content-addressing point directly: a
// genuinely NEW commit with IDENTICAL manifest content writes a new release
// row (a new commit was observed) but NOT a new content row (the content
// already exists) -- row count now scales with content change, not commits.
func TestAppManifestSnapshot_IdempotentOnOwnerGitSha(t *testing.T) {
	reg, pool := newTestRegistry(t)
	ctx := authedCtx()
	srv := handlers.NewAppServer(reg)

	appManifestReleaseCount := func(appID string) int {
		t.Helper()
		var n int
		if err := pool.QueryRow(context.Background(), `SELECT count(*) FROM app_manifest_release WHERE owner_id = $1`, appID).Scan(&n); err != nil {
			t.Fatalf("count app_manifest_release rows for %s: %v", appID, err)
		}
		return n
	}

	am := oneAppManifest("acme", "idem-svc")
	first, err := srv.AssertApps(ctx, &pb.AssertAppsRequest{
		Manifests:      reconcileManifests("sha-idem", 100, 100, []*appmetapb.AppManifest{am}, nil),
		IdempotencyKey: "assert-idem-1",
	})
	if err != nil {
		t.Fatalf("first assert: %v", err)
	}
	appID := first.CreatedApps[0].AppId

	// Re-run with a DIFFERENT idempotency_key (a real second CI attempt, not
	// a replay) but the SAME git_sha -- this must still write no NEW release
	// row, because ON CONFLICT (owner_id, git_sha) DO NOTHING is what
	// carries the idempotency here, not the idempotency_key table.
	if _, err := srv.AssertApps(ctx, &pb.AssertAppsRequest{
		Manifests:      reconcileManifests("sha-idem", 100, 100, []*appmetapb.AppManifest{am}, nil),
		IdempotencyKey: "assert-idem-2",
	}); err != nil {
		t.Fatalf("second assert (same git_sha): %v", err)
	}
	if n := appManifestContentCount(t, pool, appID); n != 1 {
		t.Fatalf("expected exactly 1 content row after two calls for the SAME git_sha, got %d", n)
	}
	if n := appManifestReleaseCount(appID); n != 1 {
		t.Fatalf("expected exactly 1 release row after two calls for the SAME git_sha, got %d", n)
	}

	// A DIFFERENT git_sha, SAME manifest content: a new release row (a new
	// commit was genuinely observed), but content stays deduped at 1 row.
	if _, err := srv.AssertApps(ctx, &pb.AssertAppsRequest{
		Manifests:      reconcileManifests("sha-idem-2", 200, 200, []*appmetapb.AppManifest{am}, nil),
		IdempotencyKey: "assert-idem-3",
	}); err != nil {
		t.Fatalf("third assert (new git_sha): %v", err)
	}
	if n := appManifestContentCount(t, pool, appID); n != 1 {
		t.Fatalf("expected content to stay deduped at 1 row (identical manifest, new commit), got %d", n)
	}
	if n := appManifestReleaseCount(appID); n != 2 {
		t.Fatalf("expected 2 release rows after a genuinely new commit, got %d", n)
	}
}

// TestAssertApps_RejectsArchivedApp_Postgres is PLAN.md's AR-7c exit
// criterion against real Postgres: AssertApps against an ARCHIVED app is
// rejected per item, every other app/chart in the same call still applies,
// and the archived app's status is untouched.
func TestAssertApps_RejectsArchivedApp_Postgres(t *testing.T) {
	reg, pool := newTestRegistry(t)
	ctx := authedCtx()
	appSrv := handlers.NewAppServer(reg)

	created, err := appSrv.ReconcileApps(ctx, &pb.ReconcileAppsRequest{
		Manifests: reconcileManifests("sha-arch-1", 100, 100,
			[]*appmetapb.AppManifest{oneAppManifest("acme", "gone"), oneAppManifest("acme", "stays")}, nil),
		IdempotencyKey: "arch-pg-1",
	})
	if err != nil {
		t.Fatalf("reconcile create: %v", err)
	}
	var goneID string
	for _, a := range created.CreatedApps {
		if a.Name == "gone" {
			goneID = a.AppId
		}
	}
	if goneID == "" {
		t.Fatalf("expected to find created app 'gone': %+v", created.CreatedApps)
	}

	if _, err := appSrv.ReconcileApps(ctx, &pb.ReconcileAppsRequest{
		Manifests:      reconcileManifests("sha-arch-2", 200, 200, []*appmetapb.AppManifest{oneAppManifest("acme", "stays")}, nil),
		IdempotencyKey: "arch-pg-2",
	}); err != nil {
		t.Fatalf("reconcile drop gone: %v", err)
	}
	if _, err := appSrv.SetAppStatus(ctx, &pb.SetAppStatusRequest{
		AppId: goneID, Status: pb.AppStatus_APP_STATUS_ARCHIVED, Reason: "gone for good",
	}); err != nil {
		t.Fatalf("archive: %v", err)
	}

	resp, err := appSrv.AssertApps(ctx, &pb.AssertAppsRequest{
		Manifests: reconcileManifests("sha-arch-3", 300, 300,
			[]*appmetapb.AppManifest{oneAppManifest("acme", "gone"), oneAppManifest("acme", "stays")}, nil),
		IdempotencyKey: "arch-pg-3",
	})
	if err != nil {
		t.Fatalf("assert (call itself must succeed): %v", err)
	}
	if len(resp.RejectedApps) != 1 || resp.RejectedApps[0].Name != "gone" {
		t.Fatalf("expected 'gone' rejected, got %+v", resp.RejectedApps)
	}
	if len(resp.UpdatedApps) != 1 || resp.UpdatedApps[0].Name != "stays" {
		t.Fatalf("expected 'stays' to still apply in the same call, got %+v", resp.UpdatedApps)
	}
	if got := appStatus(t, pool, goneID); got != "archived" {
		t.Fatalf("expected 'gone' to remain archived (not resurrected), got %q", got)
	}
}

// TestAssertApps_ThenRecordArtifact_NoReconcileNeeded_Postgres is AR-7c's
// central exit criterion against real Postgres: a release from a ref that
// NEVER merges (simulated here simply as "no ReconcileApps call ever ran")
// calls AssertApps first, then records its build and artifact successfully
// -- exit 3 / ReasonOwnerNotReconciled (issue #547) is unreachable. Also
// proves the "writes no mutable state anything else can observe" half of
// the exit criterion: app is pure identity, so there is nothing for
// AssertApps to have mutated beyond the identity row and its own
// manifest snapshot.
func TestAssertApps_ThenRecordArtifact_NoReconcileNeeded_Postgres(t *testing.T) {
	reg, pool := newTestRegistry(t)
	ctx := authedCtx()
	appSrv := handlers.NewAppServer(reg)
	artSrv := handlers.NewArtifactServer(reg)

	am := &appmetapb.AppManifest{
		Domain: "acme", Name: "unmerged-branch-app", DeployUnit: appmetapb.DeployUnit_DEPLOY_UNIT_IMAGE,
		Registry: "ghcr.io", Organization: "acme", RepoName: "acme-unmerged-branch-app",
	}
	if _, err := appSrv.AssertApps(ctx, &pb.AssertAppsRequest{
		Manifests:      reconcileManifests("sha-unmerged-1", 100, 100, []*appmetapb.AppManifest{am}, nil),
		IdempotencyKey: "unmerged-assert-1",
	}); err != nil {
		t.Fatalf("assert: %v", err)
	}

	build, err := artSrv.RecordBuild(ctx, &pb.RecordBuildRequest{
		GitSha: "sha-unmerged-1", WorkflowRunId: "run-unmerged", IdempotencyKey: "unmerged-build-1",
	})
	if err != nil {
		t.Fatalf("record build: %v", err)
	}

	if _, err := artSrv.BeginPublish(ctx, &pb.BeginPublishRequest{
		BuildId: build.Build.BuildId, Kind: pb.ArtifactKind_ARTIFACT_KIND_IMAGE,
		OwnerFullName: "acme-unmerged-branch-app", Version: "v1.0.0",
		Repository:     "ghcr.io/acme/unmerged-branch-app",
		IdempotencyKey: "unmerged-artifact-1-begin",
	}); err != nil {
		t.Fatalf("BeginPublish: %v", err)
	}

	artResp, err := artSrv.RecordArtifact(ctx, &pb.RecordArtifactRequest{
		BuildId: build.Build.BuildId, Kind: pb.ArtifactKind_ARTIFACT_KIND_IMAGE,
		OwnerFullName: "acme-unmerged-branch-app", Version: "v1.0.0", Digest: "sha256:unmerged1",
		IdempotencyKey: "unmerged-artifact-1",
	})
	if err != nil {
		t.Fatalf("RecordArtifact should succeed with no ReconcileApps ever having run: %v", err)
	}
	if artResp.Artifact.Promotability != pb.Promotability_PROMOTABILITY_PROMOTABLE {
		t.Fatalf("expected PROMOTABLE (deploy_unit=image), got %v", artResp.Artifact.Promotability)
	}

	// "no mutable state anything else can observe": ReconcileApps's own
	// absence sweep has never run, so nothing about `app` beyond identity
	// (domain/name/status/timestamps) was ever written for this owner --
	// confirmed by the fact this app never shows up in chart_app (there is
	// no chart here), its ONLY manifest content row is the AssertApps one at
	// sha-unmerged-1, and app_manifest_history (sweep-only) has zero rows.
	appID, err := reg.Apps().GetAppByFullName(ctx, "acme-unmerged-branch-app")
	if err != nil {
		t.Fatalf("get app: %v", err)
	}
	if n := appManifestContentCount(t, pool, appID.AppID); n != 1 {
		t.Fatalf("expected exactly 1 manifest content row (the AssertApps one), got %d", n)
	}
	if n := appManifestHistoryCount(t, pool, appID.AppID); n != 0 {
		t.Fatalf("expected zero app_manifest_history rows -- ReconcileApps never ran, got %d", n)
	}
}

// currentAppHistoryRow reads back the owner's OPEN app_manifest_history
// interval (valid_to IS NULL) -- there must be exactly one, per the unique
// partial index app_manifest_history_current_idx.
func currentAppHistoryRow(t *testing.T, pool *pgxpool.Pool, appID string) (contentID, firstGitSHA, lastGitSHA string) {
	t.Helper()
	err := pool.QueryRow(context.Background(), `
		SELECT app_manifest_id, first_git_sha, last_git_sha
		FROM app_manifest_history WHERE owner_id = $1 AND valid_to IS NULL`, appID).
		Scan(&contentID, &firstGitSHA, &lastGitSHA)
	if err != nil {
		t.Fatalf("read current app_manifest_history row for %s: %v", appID, err)
	}
	return contentID, firstGitSHA, lastGitSHA
}

// TestReconcile_ManifestHistorySCD2_Postgres proves migration 010's SCD2
// close-and-open write path (postgres/app.go's recordAppManifestSweep)
// against real Postgres -- AR-8's central acceptance criteria: a sweep where
// nothing changed writes ZERO new content/history rows (only last_git_sha
// advances), and a sweep where the manifest changed closes exactly the open
// interval and opens exactly one new one.
func TestReconcile_ManifestHistorySCD2_Postgres(t *testing.T) {
	reg, pool := newTestRegistry(t)
	ctx := authedCtx()
	srv := handlers.NewAppServer(reg)

	am := &appmetapb.AppManifest{Domain: "acme", Name: "scd2-svc", DeployUnit: appmetapb.DeployUnit_DEPLOY_UNIT_IMAGE}
	created, err := srv.ReconcileApps(ctx, &pb.ReconcileAppsRequest{
		Manifests:      reconcileManifests("sha-scd2-1", 100, 100, []*appmetapb.AppManifest{am}, nil),
		IdempotencyKey: "scd2-1",
	})
	if err != nil {
		t.Fatalf("initial reconcile: %v", err)
	}
	appID := created.CreatedApps[0].AppId

	if n := appManifestContentCount(t, pool, appID); n != 1 {
		t.Fatalf("expected 1 content row after the first sweep, got %d", n)
	}
	if n := appManifestHistoryCount(t, pool, appID); n != 1 {
		t.Fatalf("expected 1 history row (opened) after the first sweep, got %d", n)
	}
	firstContentID, _, lastGitSHA := currentAppHistoryRow(t, pool, appID)
	if lastGitSHA != "sha-scd2-1" {
		t.Fatalf("expected last_git_sha 'sha-scd2-1', got %q", lastGitSHA)
	}

	// A second sweep of the SAME manifest content, a NEW commit: zero new
	// content/history rows -- only last_git_sha advances.
	if _, err := srv.ReconcileApps(ctx, &pb.ReconcileAppsRequest{
		Manifests:      reconcileManifests("sha-scd2-2", 200, 200, []*appmetapb.AppManifest{am}, nil),
		IdempotencyKey: "scd2-2",
	}); err != nil {
		t.Fatalf("no-op sweep: %v", err)
	}
	if n := appManifestContentCount(t, pool, appID); n != 1 {
		t.Fatalf("expected content to STAY at 1 row after a no-op sweep, got %d", n)
	}
	if n := appManifestHistoryCount(t, pool, appID); n != 1 {
		t.Fatalf("expected history to STAY at 1 row after a no-op sweep, got %d", n)
	}
	unchangedContentID, _, lastGitSHA := currentAppHistoryRow(t, pool, appID)
	if unchangedContentID != firstContentID {
		t.Fatalf("expected the SAME content row after a no-op sweep, got a different app_manifest_id")
	}
	if lastGitSHA != "sha-scd2-2" {
		t.Fatalf("expected last_git_sha to advance to 'sha-scd2-2', got %q", lastGitSHA)
	}

	// A third sweep with a GENUINELY different manifest: closes the open
	// interval and opens exactly one new one.
	changed := &appmetapb.AppManifest{Domain: "acme", Name: "scd2-svc", DeployUnit: appmetapb.DeployUnit_DEPLOY_UNIT_CHART}
	if _, err := srv.ReconcileApps(ctx, &pb.ReconcileAppsRequest{
		Manifests:      reconcileManifests("sha-scd2-3", 300, 300, []*appmetapb.AppManifest{changed}, nil),
		IdempotencyKey: "scd2-3",
	}); err != nil {
		t.Fatalf("changed sweep: %v", err)
	}
	if n := appManifestContentCount(t, pool, appID); n != 2 {
		t.Fatalf("expected 2 content rows after a genuinely changed sweep, got %d", n)
	}
	if n := appManifestHistoryCount(t, pool, appID); n != 2 {
		t.Fatalf("expected exactly 2 history rows (one closed, one opened) after a changed sweep, got %d", n)
	}
	newContentID, firstGitSHA, lastGitSHA := currentAppHistoryRow(t, pool, appID)
	if newContentID == firstContentID {
		t.Fatalf("expected the NEW open interval to point at a NEW content row")
	}
	if firstGitSHA != "sha-scd2-3" || lastGitSHA != "sha-scd2-3" {
		t.Fatalf("expected the new interval's first/last_git_sha to both be 'sha-scd2-3', got first=%q last=%q", firstGitSHA, lastGitSHA)
	}
	var closedValidTo *time.Time
	if err := pool.QueryRow(context.Background(), `
		SELECT valid_to FROM app_manifest_history WHERE owner_id = $1 AND app_manifest_id = $2`, appID, firstContentID).
		Scan(&closedValidTo); err != nil {
		t.Fatalf("read closed interval's valid_to: %v", err)
	}
	if closedValidTo == nil {
		t.Fatalf("expected the superseded interval's valid_to to be set (closed), got NULL")
	}
}

// TestReconcile_AThenBThenAProducesThreeNonOverlappingIntervals_Postgres is
// AR-8's A -> B -> A acceptance criterion: editing a manifest, then
// reverting it, must NOT merge the two "A" periods into one interval that
// spans "B" -- it must produce three separate, correctly-ordered,
// non-overlapping intervals.
func TestReconcile_AThenBThenAProducesThreeNonOverlappingIntervals_Postgres(t *testing.T) {
	reg, pool := newTestRegistry(t)
	ctx := authedCtx()
	srv := handlers.NewAppServer(reg)

	manifestA := &appmetapb.AppManifest{Domain: "acme", Name: "aba-svc", DeployUnit: appmetapb.DeployUnit_DEPLOY_UNIT_IMAGE}
	manifestB := &appmetapb.AppManifest{Domain: "acme", Name: "aba-svc", DeployUnit: appmetapb.DeployUnit_DEPLOY_UNIT_CHART}

	created, err := srv.ReconcileApps(ctx, &pb.ReconcileAppsRequest{
		Manifests:      reconcileManifests("sha-aba-1", 100, 100, []*appmetapb.AppManifest{manifestA}, nil),
		IdempotencyKey: "aba-1",
	})
	if err != nil {
		t.Fatalf("sweep A: %v", err)
	}
	appID := created.CreatedApps[0].AppId

	if _, err := srv.ReconcileApps(ctx, &pb.ReconcileAppsRequest{
		Manifests:      reconcileManifests("sha-aba-2", 200, 200, []*appmetapb.AppManifest{manifestB}, nil),
		IdempotencyKey: "aba-2",
	}); err != nil {
		t.Fatalf("sweep B: %v", err)
	}
	if _, err := srv.ReconcileApps(ctx, &pb.ReconcileAppsRequest{
		Manifests:      reconcileManifests("sha-aba-3", 300, 300, []*appmetapb.AppManifest{manifestA}, nil),
		IdempotencyKey: "aba-3",
	}); err != nil {
		t.Fatalf("sweep A again: %v", err)
	}

	if n := appManifestContentCount(t, pool, appID); n != 2 {
		t.Fatalf("expected 2 DISTINCT content rows (A and B; A is not re-inserted), got %d", n)
	}
	if n := appManifestHistoryCount(t, pool, appID); n != 3 {
		t.Fatalf("expected 3 history intervals (A, B, A) -- not 2 (which would mean the second A merged into the first), got %d", n)
	}

	rows, err := pool.Query(context.Background(), `
		SELECT first_git_sha, valid_from, valid_to FROM app_manifest_history
		WHERE owner_id = $1 ORDER BY valid_from`, appID)
	if err != nil {
		t.Fatalf("query intervals: %v", err)
	}
	defer rows.Close()
	var shas []string
	var froms []time.Time
	var tos []*time.Time
	for rows.Next() {
		var sha string
		var from time.Time
		var to *time.Time
		if err := rows.Scan(&sha, &from, &to); err != nil {
			t.Fatalf("scan interval: %v", err)
		}
		shas = append(shas, sha)
		froms = append(froms, from)
		tos = append(tos, to)
	}
	if len(shas) != 3 {
		t.Fatalf("expected 3 ordered intervals, got %d", len(shas))
	}
	if shas[0] != "sha-aba-1" || shas[1] != "sha-aba-2" || shas[2] != "sha-aba-3" {
		t.Fatalf("expected intervals in commit order sha-aba-1, sha-aba-2, sha-aba-3, got %v", shas)
	}
	if tos[2] != nil {
		t.Fatalf("expected the LAST interval to still be open (valid_to NULL), got %v", *tos[2])
	}
	// Non-overlapping and correctly chained: each interval's valid_to equals
	// the next interval's valid_from.
	if tos[0] == nil || !tos[0].Equal(froms[1]) {
		t.Fatalf("expected interval 1's valid_to to equal interval 2's valid_from")
	}
	if tos[1] == nil || !tos[1].Equal(froms[2]) {
		t.Fatalf("expected interval 2's valid_to to equal interval 3's valid_from")
	}
}

// TestMigration008BackfillsSnapshotsFromExistingRows applies migrations
// 001-007 against a fresh database, seeds `app`/`chart` rows in the
// PRE-008 shape (mutable columns directly on the table, as every
// pre-AR-7c row in a real deployed environment would be), then applies
// migration 008 and proves: exactly one app_manifest/chart_manifest
// snapshot per existing row, attributed to reconcile_watermark's current
// git_sha, and v_current_app/v_current_chart reproduce the SAME
// deploy_unit/image_repository/status the pre-migration flat columns held
// "nothing loses metadata," PLAN.md's AR-7c backfill requirement.
func TestMigration008BackfillsSnapshotsFromExistingRows(t *testing.T) {
	ctx := context.Background()
	db := dbtest.NewPostgres(ctx, t, dbtest.Options{})

	sqlDB, err := sql.Open("pgx", db.ConnString)
	if err != nil {
		t.Fatalf("open database/sql handle: %v", err)
	}
	defer sqlDB.Close()

	runner := migrate.NewRunner(sqlDB, schema.Migrations, schema.Dir)
	if err := runner.Steps(7); err != nil {
		t.Fatalf("apply migrations 001-007: %v", err)
	}

	// Seed a pre-AR-7c app/chart pair directly, matching the schema shape
	// migrations 001-007 produce (deploy_unit/image_repository/
	// chart_repository still live on the base tables).
	var appID string
	if err := db.Pool.QueryRow(ctx, `
		INSERT INTO app (domain, name, description, language, app_type, deploy_unit, bazel_label, image_repository, status)
		VALUES ('acme', 'backfill-app', 'a description', 'go', 'worker', 'image', '//acme:bin', 'ghcr.io/acme/backfill-app', 'active')
		RETURNING app_id`).Scan(&appID); err != nil {
		t.Fatalf("seed pre-008 app: %v", err)
	}
	var chartID string
	if err := db.Pool.QueryRow(ctx, `
		INSERT INTO chart (domain, name, status) VALUES ('acme', 'backfill-chart', 'active')
		RETURNING chart_id`).Scan(&chartID); err != nil {
		t.Fatalf("seed pre-008 chart: %v", err)
	}
	if _, err := db.Pool.Exec(ctx, `INSERT INTO chart_app (chart_id, app_id) VALUES ($1, $2)`, chartID, appID); err != nil {
		t.Fatalf("seed pre-008 chart_app: %v", err)
	}

	// Advance the reconcile watermark, exactly as a real prior ReconcileApps
	// call would have -- migration 008's backfill attributes every
	// synthesized snapshot to THIS git_sha.
	if _, err := db.Pool.Exec(ctx, `
		UPDATE reconcile_watermark SET git_sha = 'sha-pre-008', source_committed_at = 500, discovered_at = 500 WHERE id = 1`); err != nil {
		t.Fatalf("advance watermark: %v", err)
	}

	// Steps(1), not Up() -- migrations 009/010 also exist beyond 008 now;
	// this test isolates 008's OWN backfill logic in the exact pre-009/010
	// schema shape it was written against. Migration 010's own backfill (a
	// different, later transformation of these same tables) has its own
	// test below.
	if err := runner.Steps(1); err != nil {
		t.Fatalf("apply migration 008: %v", err)
	}

	// Exactly one snapshot each, attributed to the watermark's git_sha.
	if n := appManifestContentCount(t, db.Pool, appID); n != 1 {
		t.Fatalf("expected exactly 1 backfilled app_manifest row, got %d", n)
	}
	var appSnapGitSHA string
	if err := db.Pool.QueryRow(ctx, `SELECT source_git_sha FROM app_manifest WHERE owner_id = $1`, appID).Scan(&appSnapGitSHA); err != nil {
		t.Fatalf("read backfilled app_manifest git_sha: %v", err)
	}
	if appSnapGitSHA != "sha-pre-008" {
		t.Fatalf("expected the backfilled snapshot attributed to the watermark's git_sha 'sha-pre-008', got %q", appSnapGitSHA)
	}

	var chartSnapCount int
	if err := db.Pool.QueryRow(ctx, `SELECT count(*) FROM chart_manifest WHERE owner_id = $1`, chartID).Scan(&chartSnapCount); err != nil {
		t.Fatalf("count backfilled chart_manifest rows: %v", err)
	}
	if chartSnapCount != 1 {
		t.Fatalf("expected exactly 1 backfilled chart_manifest row, got %d", chartSnapCount)
	}

	// v_current_app reproduces the SAME deploy_unit/image_repository/status
	// the pre-migration flat columns held -- nothing lost.
	var deployUnit, imageRepository, status, description string
	if err := db.Pool.QueryRow(ctx, `
		SELECT deploy_unit, image_repository, status, description FROM v_current_app WHERE app_id = $1`, appID).
		Scan(&deployUnit, &imageRepository, &status, &description); err != nil {
		t.Fatalf("read v_current_app: %v", err)
	}
	if deployUnit != "image" {
		t.Fatalf("expected v_current_app.deploy_unit = 'image' (backfilled), got %q", deployUnit)
	}
	if imageRepository != "ghcr.io/acme/backfill-app" {
		t.Fatalf("expected v_current_app.image_repository backfilled, got %q", imageRepository)
	}
	if status != "active" {
		t.Fatalf("expected v_current_app.status = 'active', got %q", status)
	}
	if description != "a description" {
		t.Fatalf("expected v_current_app.description backfilled, got %q", description)
	}

	// app/chart no longer carry the dropped columns at all.
	var appHasDeployUnit bool
	if err := db.Pool.QueryRow(ctx, `
		SELECT EXISTS (SELECT 1 FROM information_schema.columns WHERE table_name = 'app' AND column_name = 'deploy_unit')`).
		Scan(&appHasDeployUnit); err != nil {
		t.Fatalf("check app.deploy_unit column existence: %v", err)
	}
	if appHasDeployUnit {
		t.Fatalf("expected migration 008 to have dropped app.deploy_unit")
	}
}

// TestMigration010BackfillsHistoryFromExistingRows applies migrations
// 001-009 (the full migration-008 per-commit-snapshot shape), seeds an app
// with three sweep snapshots forming an A -> B -> A sequence plus one
// release-provenance snapshot from a divergent commit, and an artifact row
// whose manifest_id points at the OLD per-commit "B" row, then applies
// migration 010 and proves every AR-8 backfill acceptance criterion at
// once: content deduplicates to one row per DISTINCT manifest (3 here: A's
// content, B's content, the release's content -- A's second occurrence is
// NOT a 4th row), app_manifest_history collapses to exactly 3
// non-overlapping intervals in commit order with the LAST one open and
// pointing at A's content again (the revert), app_manifest_release gets
// exactly the one release row, v_current_app reflects the reverted-to-A
// content, and artifact.manifest_id is remapped to the NEW content row that
// has the SAME manifest_json the old "B" row had.
func TestMigration010BackfillsHistoryFromExistingRows(t *testing.T) {
	ctx := context.Background()
	db := dbtest.NewPostgres(ctx, t, dbtest.Options{})

	sqlDB, err := sql.Open("pgx", db.ConnString)
	if err != nil {
		t.Fatalf("open database/sql handle: %v", err)
	}
	defer sqlDB.Close()

	runner := migrate.NewRunner(sqlDB, schema.Migrations, schema.Dir)
	if err := runner.Steps(9); err != nil {
		t.Fatalf("apply migrations 001-009: %v", err)
	}

	var appID string
	if err := db.Pool.QueryRow(ctx, `
		INSERT INTO app (domain, name) VALUES ('acme', 'aba-migration-app')
		RETURNING app_id`).Scan(&appID); err != nil {
		t.Fatalf("seed app: %v", err)
	}
	var chartID string
	if err := db.Pool.QueryRow(ctx, `
		INSERT INTO chart (domain, name) VALUES ('acme', 'aba-migration-chart')
		RETURNING chart_id`).Scan(&chartID); err != nil {
		t.Fatalf("seed chart: %v", err)
	}

	seedOldAppManifest := func(gitSHA string, committedAt int64, provenance, deployUnit string) string {
		t.Helper()
		manifestJSON := fmt.Sprintf(`{"domain":"acme","name":"aba-migration-app","deploy_unit":%q,"registry":"","organization":"","repo_name":""}`, deployUnit)
		var id string
		if err := db.Pool.QueryRow(ctx, `
			INSERT INTO app_manifest (owner_id, source_git_sha, source_committed_at, provenance, manifest_json)
			VALUES ($1, $2, $3, $4, $5::jsonb)
			RETURNING app_manifest_id`, appID, gitSHA, committedAt, provenance, manifestJSON).Scan(&id); err != nil {
			t.Fatalf("seed old app_manifest %s: %v", gitSHA, err)
		}
		return id
	}
	seedOldAppManifest("sha-mig-a1", 100, "sweep", "DEPLOY_UNIT_IMAGE")          // A
	bRowID := seedOldAppManifest("sha-mig-b", 200, "sweep", "DEPLOY_UNIT_CHART") // B
	seedOldAppManifest("sha-mig-a2", 300, "sweep", "DEPLOY_UNIT_IMAGE")          // A again (reverts)
	seedOldAppManifest("sha-mig-release", 150, "release", "DEPLOY_UNIT_NONE")    // divergent release

	if _, err := db.Pool.Exec(ctx, `
		INSERT INTO chart_manifest (owner_id, source_git_sha, source_committed_at, provenance, manifest_json)
		VALUES ($1, 'sha-mig-chart', 100, 'sweep', '{"domain":"acme","name":"aba-migration-chart"}'::jsonb)`,
		chartID); err != nil {
		t.Fatalf("seed chart_manifest: %v", err)
	}

	buildID := seedBuild(t, db.Pool, "run-migration-010")
	var artifactID string
	if err := db.Pool.QueryRow(ctx, `
		INSERT INTO artifact (kind, app_id, repository, version, digest, build_id, state, provenance, version_source, promotability, manifest_id)
		VALUES ('image', $1, 'ghcr.io/acme/aba-migration-app', 'v1.0.0', 'sha256:mig1', $2, 'published', 'observed', 'tag', 'via_chart', $3)
		RETURNING artifact_id`, appID, buildID, bRowID).Scan(&artifactID); err != nil {
		t.Fatalf("seed artifact pointing at B's old snapshot: %v", err)
	}

	if err := runner.Steps(1); err != nil {
		t.Fatalf("apply migration 010: %v", err)
	}

	if n := appManifestContentCount(t, db.Pool, appID); n != 3 {
		t.Fatalf("expected 3 distinct content rows (A, B, release), got %d", n)
	}
	if n := appManifestHistoryCount(t, db.Pool, appID); n != 3 {
		t.Fatalf("expected 3 history intervals (A, B, A again -- not collapsed), got %d", n)
	}

	rows, err := db.Pool.Query(ctx, `
		SELECT first_git_sha, valid_to FROM app_manifest_history WHERE owner_id = $1 ORDER BY valid_from`, appID)
	if err != nil {
		t.Fatalf("query backfilled intervals: %v", err)
	}
	var shas []string
	var openCount int
	for rows.Next() {
		var sha string
		var validTo *time.Time
		if err := rows.Scan(&sha, &validTo); err != nil {
			t.Fatalf("scan interval: %v", err)
		}
		shas = append(shas, sha)
		if validTo == nil {
			openCount++
		}
	}
	rows.Close()
	if len(shas) != 3 || shas[0] != "sha-mig-a1" || shas[1] != "sha-mig-b" || shas[2] != "sha-mig-a2" {
		t.Fatalf("expected backfilled intervals in commit order [sha-mig-a1 sha-mig-b sha-mig-a2], got %v", shas)
	}
	if openCount != 1 {
		t.Fatalf("expected exactly 1 open interval, got %d", openCount)
	}

	var releaseCount int
	if err := db.Pool.QueryRow(ctx, `SELECT count(*) FROM app_manifest_release WHERE owner_id = $1 AND git_sha = 'sha-mig-release'`, appID).Scan(&releaseCount); err != nil {
		t.Fatalf("count app_manifest_release: %v", err)
	}
	if releaseCount != 1 {
		t.Fatalf("expected exactly 1 backfilled app_manifest_release row, got %d", releaseCount)
	}

	var chartContentCount, chartHistoryCount int
	if err := db.Pool.QueryRow(ctx, `SELECT count(*) FROM chart_manifest WHERE owner_id = $1`, chartID).Scan(&chartContentCount); err != nil {
		t.Fatalf("count chart_manifest content rows: %v", err)
	}
	if chartContentCount != 1 {
		t.Fatalf("expected 1 backfilled chart content row, got %d", chartContentCount)
	}
	if err := db.Pool.QueryRow(ctx, `SELECT count(*) FROM chart_manifest_history WHERE owner_id = $1`, chartID).Scan(&chartHistoryCount); err != nil {
		t.Fatalf("count chart_manifest_history rows: %v", err)
	}
	if chartHistoryCount != 1 {
		t.Fatalf("expected 1 backfilled chart_manifest_history row, got %d", chartHistoryCount)
	}

	// v_current_app must reflect the REVERTED-TO-A content (the newest
	// sweep), not B.
	var currentDeployUnit string
	if err := db.Pool.QueryRow(ctx, `SELECT deploy_unit FROM v_current_app WHERE app_id = $1`, appID).Scan(&currentDeployUnit); err != nil {
		t.Fatalf("read v_current_app: %v", err)
	}
	if currentDeployUnit != "image" {
		t.Fatalf("expected v_current_app.deploy_unit = 'image' (A, the reverted-to content), got %q", currentDeployUnit)
	}

	// artifact.manifest_id was remapped to a NEW content row carrying the
	// SAME manifest_json B's old row had.
	var remappedID string
	var remappedDeployUnit string
	if err := db.Pool.QueryRow(ctx, `SELECT manifest_id FROM artifact WHERE artifact_id = $1`, artifactID).Scan(&remappedID); err != nil {
		t.Fatalf("read remapped artifact.manifest_id: %v", err)
	}
	if remappedID == bRowID {
		t.Fatalf("expected manifest_id to be remapped away from the OLD per-commit row id")
	}
	if err := db.Pool.QueryRow(ctx, `SELECT deploy_unit FROM app_manifest WHERE app_manifest_id = $1`, remappedID).Scan(&remappedDeployUnit); err != nil {
		t.Fatalf("read remapped content row: %v", err)
	}
	if remappedDeployUnit != "chart" {
		t.Fatalf("expected the remapped content row to carry B's manifest (deploy_unit='chart'), got %q", remappedDeployUnit)
	}
}

// TestMigration010DownRestoresPreMigrationShape proves 010_*.down.sql
// round-trips: after applying 010 over seeded pre-010 data and then rolling
// it back, app_manifest/chart_manifest exist again in the migration-008
// shape (source_git_sha/provenance columns) and v_current_app still resolves
// -- matching this migration set's established "best-effort restore, not
// full fidelity" down-migration convention (migrations 003/007/008).
func TestMigration010DownRestoresPreMigrationShape(t *testing.T) {
	ctx := context.Background()
	db := dbtest.NewPostgres(ctx, t, dbtest.Options{})

	sqlDB, err := sql.Open("pgx", db.ConnString)
	if err != nil {
		t.Fatalf("open database/sql handle: %v", err)
	}
	defer sqlDB.Close()

	runner := migrate.NewRunner(sqlDB, schema.Migrations, schema.Dir)
	if err := runner.Steps(10); err != nil {
		t.Fatalf("apply migrations 001-010: %v", err)
	}

	// Write real post-010 data through the actual repository, not raw SQL,
	// so the round-trip exercises the real write path.
	reg := NewRepository(db.Pool)
	ctxAuthed := authedCtx()
	srv := handlers.NewAppServer(reg)
	if _, err := srv.ReconcileApps(ctxAuthed, &pb.ReconcileAppsRequest{
		Manifests: reconcileManifests("sha-down-1", 100, 100,
			[]*appmetapb.AppManifest{{Domain: "acme", Name: "down-svc", DeployUnit: appmetapb.DeployUnit_DEPLOY_UNIT_IMAGE}}, nil),
		IdempotencyKey: "down-1",
	}); err != nil {
		t.Fatalf("seed via real ReconcileApps: %v", err)
	}

	if err := runner.Steps(-1); err != nil {
		t.Fatalf("roll back migration 010: %v", err)
	}

	var appHasSourceGitSha bool
	if err := db.Pool.QueryRow(ctx, `
		SELECT EXISTS (SELECT 1 FROM information_schema.columns WHERE table_name = 'app_manifest' AND column_name = 'source_git_sha')`).
		Scan(&appHasSourceGitSha); err != nil {
		t.Fatalf("check app_manifest.source_git_sha column existence: %v", err)
	}
	if !appHasSourceGitSha {
		t.Fatalf("expected the down migration to restore app_manifest.source_git_sha")
	}

	var deployUnit string
	if err := db.Pool.QueryRow(ctx, `SELECT deploy_unit FROM v_current_app WHERE domain = 'acme' AND name = 'down-svc'`).Scan(&deployUnit); err != nil {
		t.Fatalf("read v_current_app after rollback: %v", err)
	}
	if deployUnit != "image" {
		t.Fatalf("expected v_current_app to still resolve deploy_unit='image' after rollback, got %q", deployUnit)
	}

	var newTablesExist bool
	if err := db.Pool.QueryRow(ctx, `
		SELECT EXISTS (SELECT 1 FROM information_schema.tables WHERE table_name = 'app_manifest_history')`).
		Scan(&newTablesExist); err != nil {
		t.Fatalf("check app_manifest_history table existence: %v", err)
	}
	if newTablesExist {
		t.Fatalf("expected the down migration to drop app_manifest_history")
	}
}

// --- 12. ListReconcileRuns pagination/since/ordering (AR-8, issue #610) ----

// seedReconcileRunRow inserts a reconcile_run row directly -- there is no
// repository write path exercised by this test file that lets a caller pin
// an exact applied_at (Reconcile's own bookkeeping always uses time.Now(),
// see reconcile.go/postgres/app.go), and this test specifically needs
// duplicate applied_at values across rows, which a real sweep cannot
// reliably construct without racing the clock.
func seedReconcileRunRow(t *testing.T, pool *pgxpool.Pool, id string, appliedAt time.Time, appsSeen, chartsSeen int) {
	t.Helper()
	_, err := pool.Exec(context.Background(), `
		INSERT INTO reconcile_run (reconcile_run_id, git_sha, source_committed_at, applied_at, apps_seen, charts_seen)
		VALUES ($1, $2, $3, $4, $5, $6)`,
		id, "sha-"+id, appliedAt.Unix(), appliedAt, appsSeen, chartsSeen)
	if err != nil {
		t.Fatalf("seed reconcile_run %s: %v", id, err)
	}
}

// TestListReconcileRuns_Pagination_MatchesFullOrderedScan_Postgres is the
// "real Postgres ordering/index-free full sort" behavior the fake cannot
// exercise: pages through with a small page_size and confirms the full
// traversal matches a single unfiltered `ORDER BY applied_at DESC,
// reconcile_run_id DESC` scan exactly, in order, with no duplicates and no
// omissions -- including across a duplicate-applied_at pair, which is
// exactly the case a naive `ORDER BY applied_at DESC LIMIT n` keyset
// implementation (missing the reconcile_run_id tie-break) gets wrong.
func TestListReconcileRuns_Pagination_MatchesFullOrderedScan_Postgres(t *testing.T) {
	reg, pool := newTestRegistry(t)
	ctx := context.Background()

	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	ids := []string{
		uuid.NewString(), uuid.NewString(), uuid.NewString(),
		uuid.NewString(), uuid.NewString(), uuid.NewString(), uuid.NewString(),
	}
	// ids[2] and ids[3] share the exact same applied_at -- the
	// duplicate-timestamp pair the keyset tie-break must still order
	// deterministically.
	tie := base.Add(3 * time.Hour)
	seedReconcileRunRow(t, pool, ids[0], base, 1, 1)
	seedReconcileRunRow(t, pool, ids[1], base.Add(1*time.Hour), 1, 1)
	seedReconcileRunRow(t, pool, ids[2], tie, 1, 1)
	seedReconcileRunRow(t, pool, ids[3], tie, 1, 1)
	seedReconcileRunRow(t, pool, ids[4], base.Add(4*time.Hour), 1, 1)
	seedReconcileRunRow(t, pool, ids[5], base.Add(5*time.Hour), 1, 1)
	seedReconcileRunRow(t, pool, ids[6], base.Add(6*time.Hour), 1, 1)

	// Ground truth: a single unfiltered scan in the same order the
	// repository contract promises.
	rows, err := pool.Query(ctx, `SELECT reconcile_run_id FROM reconcile_run ORDER BY applied_at DESC, reconcile_run_id DESC`)
	if err != nil {
		t.Fatalf("ground-truth scan: %v", err)
	}
	var want []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			t.Fatalf("scan ground-truth row: %v", err)
		}
		want = append(want, id)
	}
	rows.Close()
	if len(want) != len(ids) {
		t.Fatalf("expected %d rows in ground-truth scan, got %d", len(ids), len(want))
	}

	var got []string
	seen := map[string]bool{}
	token := ""
	for page := 0; page < len(ids)+1; page++ {
		runs, next, err := reg.Apps().ListReconcileRuns(ctx, time.Time{}, 2, token)
		if err != nil {
			t.Fatalf("ListReconcileRuns page %d: %v", page, err)
		}
		if len(runs) == 0 {
			t.Fatalf("page %d: got 0 rows", page)
		}
		if len(runs) > 2 {
			t.Fatalf("page %d: expected at most page_size=2 rows, got %d", page, len(runs))
		}
		for _, rr := range runs {
			if seen[rr.ReconcileRunID] {
				t.Fatalf("duplicate row %s across pages", rr.ReconcileRunID)
			}
			seen[rr.ReconcileRunID] = true
			got = append(got, rr.ReconcileRunID)
		}
		token = next
		if token == "" {
			break
		}
	}

	if len(got) != len(want) {
		t.Fatalf("expected %d rows across all pages, got %d (want order %v, got order %v)", len(want), len(got), want, got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("position %d: expected %s (ground-truth ORDER BY), got %s", i, want[i], got[i])
		}
	}
}

// TestListReconcileRuns_SinceComposesWithPagination_Postgres confirms since
// composes correctly with pagination: filtering excludes rows before the
// boundary on the first page, and every row across every subsequent page
// still satisfies the filter.
func TestListReconcileRuns_SinceComposesWithPagination_Postgres(t *testing.T) {
	reg, pool := newTestRegistry(t)
	ctx := context.Background()

	base := time.Date(2026, 2, 1, 0, 0, 0, 0, time.UTC)
	// 3 rows strictly before `since`, 5 at/after it.
	for i := 0; i < 3; i++ {
		seedReconcileRunRow(t, pool, uuid.NewString(), base.Add(time.Duration(i)*time.Hour), 1, 1)
	}
	since := base.Add(10 * time.Hour)
	for i := 0; i < 5; i++ {
		seedReconcileRunRow(t, pool, uuid.NewString(), since.Add(time.Duration(i)*time.Hour), 1, 1)
	}

	var got []repository.ReconcileRun
	token := ""
	for page := 0; page < 10; page++ {
		runs, next, err := reg.Apps().ListReconcileRuns(ctx, since, 2, token)
		if err != nil {
			t.Fatalf("ListReconcileRuns page %d: %v", page, err)
		}
		got = append(got, runs...)
		token = next
		if token == "" {
			break
		}
	}

	if len(got) != 5 {
		t.Fatalf("expected 5 runs at/after since across all pages, got %d", len(got))
	}
	for _, rr := range got {
		if rr.AppliedAt.Before(since) {
			t.Fatalf("row %s has applied_at %v before since %v -- since did not compose correctly with pagination", rr.ReconcileRunID, rr.AppliedAt, since)
		}
	}
}

// TestChartsForApp_ChartLinkedApp_Postgres proves ChartsForApp (exercised
// here both directly and via the GetApp RPC) runs against real Postgres for
// an app that IS referenced by a chart -- the SELECT joins v_current_chart
// to chart_app, and a real Postgres parser is what previously rejected an
// unqualified `chart_id` in that SELECT list as ambiguous (both tables have
// a chart_id column). A mocked repository never parses SQL, so this class
// of bug is invisible without a real database.
func TestChartsForApp_ChartLinkedApp_Postgres(t *testing.T) {
	reg, _ := newTestRegistry(t)
	ctx := authedCtx()
	srv := handlers.NewAppServer(reg)

	resp, err := srv.ReconcileApps(ctx, &pb.ReconcileAppsRequest{
		Manifests: reconcileManifests("sha-747-1", 30000, 30000,
			[]*appmetapb.AppManifest{oneAppManifest("acme", "linked-app")},
			[]*appmetapb.ChartManifest{{Domain: "acme", Name: "linked-chart", Apps: []string{"linked-app"}}},
		),
		IdempotencyKey: "issue747-1",
	})
	if err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if len(resp.CreatedApps) != 1 || len(resp.CreatedCharts) != 1 {
		t.Fatalf("expected 1 created app and 1 created chart, got apps=%+v charts=%+v", resp.CreatedApps, resp.CreatedCharts)
	}
	appID := resp.CreatedApps[0].AppId

	// Direct repository call: exercises ChartsForApp's real SQL query
	// (the ambiguous-column bug site) against Postgres.
	charts, err := reg.Apps().ChartsForApp(ctx, appID)
	if err != nil {
		t.Fatalf("ChartsForApp must not error for a chart-linked app: %v", err)
	}
	if len(charts) != 1 || charts[0].Name != "linked-chart" {
		t.Fatalf("expected exactly [linked-chart], got %+v", charts)
	}

	// GetApp RPC: the actual caller-facing path that AR-7a's validation
	// finding (#744) showed broke for every app.
	getResp, err := srv.GetApp(ctx, &pb.GetAppRequest{AppId: appID})
	if err != nil {
		t.Fatalf("GetApp must not error for a chart-linked app: %v", err)
	}
	if len(getResp.Charts) != 1 || getResp.Charts[0].Name != "linked-chart" {
		t.Fatalf("expected GetApp to return exactly [linked-chart], got %+v", getResp.Charts)
	}
}

// TestChartsForApp_StandaloneApp_Postgres proves ChartsForApp/GetApp return
// an empty chart list -- not an error -- for a standalone app with zero
// chart_app rows. This is the case that previously failed even though it
// has no chart join result to speak of: the ambiguous-column error fires
// during SQL parsing/planning, before any rows are considered, so it broke
// this case just as much as the chart-linked case.
func TestChartsForApp_StandaloneApp_Postgres(t *testing.T) {
	reg, _ := newTestRegistry(t)
	ctx := authedCtx()
	srv := handlers.NewAppServer(reg)

	resp, err := srv.ReconcileApps(ctx, &pb.ReconcileAppsRequest{
		Manifests: reconcileManifests("sha-747-2", 31000, 31000,
			[]*appmetapb.AppManifest{oneAppManifest("acme", "standalone-app")},
			nil, // no charts at all -- standalone-app has zero chart_app rows.
		),
		IdempotencyKey: "issue747-2",
	})
	if err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if len(resp.CreatedApps) != 1 {
		t.Fatalf("expected 1 created app, got %+v", resp.CreatedApps)
	}
	appID := resp.CreatedApps[0].AppId

	charts, err := reg.Apps().ChartsForApp(ctx, appID)
	if err != nil {
		t.Fatalf("ChartsForApp must not error for a standalone app: %v", err)
	}
	if len(charts) != 0 {
		t.Fatalf("expected an empty chart list for a standalone app, got %+v", charts)
	}

	getResp, err := srv.GetApp(ctx, &pb.GetAppRequest{AppId: appID})
	if err != nil {
		t.Fatalf("GetApp must not error for a standalone app: %v", err)
	}
	if len(getResp.Charts) != 0 {
		t.Fatalf("expected GetApp to return an empty chart list for a standalone app, got %+v", getResp.Charts)
	}
}
