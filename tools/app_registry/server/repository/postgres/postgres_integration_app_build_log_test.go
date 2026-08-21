//go:build integration

// This file covers app_build_log (migration 019, issue #923, FR8/FR9/FR10):
// exactly what server/repository/fake's in-memory version cannot -- that the
// real close-and-open write is committed unconditionally against the real
// schema (app_build_log_current_idx's partial unique index in particular),
// and that closed rows are actually preserved, not deleted, by a real
// UPDATE ... SET valid_to = NOW() rather than a DELETE. See
// postgres_integration_helpers_test.go's package doc comment for the
// integration-tag rationale shared by every postgres_integration_*_test.go
// file.
package postgres

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/whale-net/everything/tools/app_registry/server/repository"
)

// recordBuildLogTx runs AppBuildLogs().RecordBuildLog inside a real WithTx
// transaction, exactly as handlers.ArtifactServer.RecordBuildLog does in
// production (see server/handlers/artifact.go's RecordBuildLog, wrapped by
// runIdempotent) -- repository.AppBuildLogRepository.RecordBuildLog's doc
// comment requires this for the close+open pair to be atomic.
func recordBuildLogTx(t *testing.T, reg *Registry, ownerID string, kind repository.ArtifactKind, gitSHA, buildID string) *repository.AppBuildLog {
	t.Helper()
	var out *repository.AppBuildLog
	err := reg.WithTx(context.Background(), func(ctx context.Context, r repository.Registry) error {
		var ferr error
		out, ferr = r.AppBuildLogs().RecordBuildLog(ctx, ownerID, kind, gitSHA, buildID)
		return ferr
	})
	if err != nil {
		t.Fatalf("RecordBuildLog(%s, %s, %s): %v", ownerID, kind, gitSHA, err)
	}
	return out
}

// TestAppBuildLog_RecordBuildLog_UnconditionalWrite_PreservesHistory proves
// FR8's defining behavior against the real schema: RecordBuildLog writes a
// brand new row on EVERY call, even when the incoming git_sha is identical
// to the prior current row's -- unlike app_manifest_history's
// recordAppManifestSweep, there is no same-content skip branch here (see
// migration 019's doc comment). It also proves the closed row from the
// first call is preserved (UPDATE, not DELETE), not just that the current
// pointer moved.
func TestAppBuildLog_RecordBuildLog_UnconditionalWrite_PreservesHistory(t *testing.T) {
	reg, pool := newTestRegistry(t)
	appID := seedApp(t, pool, "demo", "widget", "image")
	build1 := seedBuild(t, pool, "run-1")
	build2 := seedBuild(t, pool, "run-2")

	first := recordBuildLogTx(t, reg, appID, repository.ArtifactKindImage, "same-sha", build1)
	if first.ValidTo != nil {
		t.Fatalf("first row expected to be open (ValidTo nil) immediately after write, got %v", first.ValidTo)
	}

	// Second call carries the IDENTICAL git_sha -- FR8 requires a new row
	// anyway, regardless of content equality.
	second := recordBuildLogTx(t, reg, appID, repository.ArtifactKindImage, "same-sha", build2)
	if second.AppBuildLogID == first.AppBuildLogID {
		t.Fatalf("expected a distinct new row on the second call, got the same id %s", second.AppBuildLogID)
	}

	// Row count: both rows must still exist -- the close half of
	// close-and-open is an UPDATE (valid_to = NOW()), never a DELETE.
	var count int
	if err := pool.QueryRow(context.Background(), `SELECT count(*) FROM app_build_log WHERE owner_id = $1`, appID).Scan(&count); err != nil {
		t.Fatalf("count app_build_log rows: %v", err)
	}
	if count != 2 {
		t.Fatalf("expected 2 app_build_log rows after 2 unconditional writes (no dedupe), got %d", count)
	}

	// The first row must now be closed (valid_to set), not deleted, and
	// distinguishable by its own build_id.
	var firstValidTo *time.Time
	var firstBuildID string
	if err := pool.QueryRow(context.Background(), `SELECT valid_to, build_id FROM app_build_log WHERE app_build_log_id = $1`, first.AppBuildLogID).
		Scan(&firstValidTo, &firstBuildID); err != nil {
		t.Fatalf("query closed row: %v", err)
	}
	if firstValidTo == nil {
		t.Fatalf("expected the first row to be closed (valid_to set) once superseded, got NULL")
	}
	if firstBuildID != build1 {
		t.Fatalf("expected the first (now-closed) row to still carry its original build_id %s, got %s -- history was mutated, not just closed", build1, firstBuildID)
	}

	// Current-pointer lookup resolves to the SECOND row.
	current, err := reg.AppBuildLogs().GetCurrentBuildLog(context.Background(), appID)
	if err != nil {
		t.Fatalf("GetCurrentBuildLog: %v", err)
	}
	if current.AppBuildLogID != second.AppBuildLogID {
		t.Fatalf("expected current row to be the second write %s, got %s", second.AppBuildLogID, current.AppBuildLogID)
	}
	if current.BuildID != build2 {
		t.Fatalf("expected current row's build_id to be %s (from the second call), got %s", build2, current.BuildID)
	}
}

// TestAppBuildLog_GetCurrentBuildLog_NoRowYet_ReturnsNotFound proves FR10's
// precondition against the real schema: an owner with no app_build_log row
// yet returns repository.ErrNotFound (not a zero-value row), which is what
// buildref.go's resolveBuildRef translates into the literal-branch-name
// fallback.
func TestAppBuildLog_GetCurrentBuildLog_NoRowYet_ReturnsNotFound(t *testing.T) {
	reg, pool := newTestRegistry(t)
	appID := seedApp(t, pool, "demo", "fresh", "image")

	_, err := reg.AppBuildLogs().GetCurrentBuildLog(context.Background(), appID)
	if err == nil {
		t.Fatal("expected ErrNotFound for an owner with no app_build_log row, got nil")
	}
	if !errors.Is(err, repository.ErrNotFound) {
		t.Fatalf("expected repository.ErrNotFound, got %v", err)
	}
}
