//go:build integration

// This file only builds under the "integration" build tag so `bazel test
// //...` (which builds and runs the whole tree, including on Docker-less
// machines) never even compiles it, let alone runs it. See the go_test
// target's gotags in BUILD.bazel and TESTING.md for how to run it.
//
// These tests exercise exactly what server/repository/fake (the in-memory
// registry backing handlers/*_test.go) cannot: real Postgres transaction
// abort/rollback semantics, idempotency-key replay against a real
// idempotency_key table, and unique-index enforcement created by the real
// migrations in migrate/schema. Schema comes from applying
// migrate/schema.Migrations via libs/go/migrate — not hand-written DDL — so
// drift between this package's SQL and the shipped migrations is caught
// too.
package postgres

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	_ "github.com/jackc/pgx/v5/stdlib" // database/sql driver for the migration runner
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/whale-net/everything/libs/go/dbtest"
	"github.com/whale-net/everything/libs/go/grpcauth"
	"github.com/whale-net/everything/libs/go/migrate"
	"github.com/whale-net/everything/tools/app_registry/migrate/schema"
	"github.com/whale-net/everything/tools/app_registry/server/auth"
	"github.com/whale-net/everything/tools/app_registry/server/handlers"
	"github.com/whale-net/everything/tools/app_registry/server/repository"

	pb "github.com/whale-net/everything/tools/app_registry/protos"
)

// authedCtx returns a context carrying a principal holding every app-registry
// role, as the server interceptor would supply. Tests here exercise repository
// and idempotency behaviour, not authorization -- the authorization checks
// themselves are covered by server/auth and server/handlers/authz_test.go.
func authedCtx() context.Context {
	return grpcauth.ContextWithClaims(context.Background(), &grpcauth.Claims{
		Subject: "integration-test",
		Roles:   auth.AllRoles(),
	})
}

// newTestRegistry starts a real Postgres container, applies the real
// App Registry migrations (migrate/schema.Migrations, the same embed.FS the
// migration binary runs), and returns a Registry plus the raw pool for
// fixture setup / assertions.
func newTestRegistry(t *testing.T) (*Registry, *pgxpool.Pool) {
	t.Helper()
	ctx := context.Background()

	db := dbtest.NewPostgres(ctx, t, dbtest.Options{})

	sqlDB, err := sql.Open("pgx", db.ConnString)
	if err != nil {
		t.Fatalf("open database/sql handle for migration runner: %v", err)
	}
	defer sqlDB.Close()

	runner := migrate.NewRunner(sqlDB, schema.Migrations, schema.Dir)
	if err := runner.Up(); err != nil {
		t.Fatalf("apply real migrations: %v", err)
	}

	return NewRepository(db.Pool), db.Pool
}

// --- fixtures ---------------------------------------------------------

func seedApp(t *testing.T, pool *pgxpool.Pool, domain, name, deployUnit string) string {
	t.Helper()
	var appID string
	err := pool.QueryRow(context.Background(), `
		INSERT INTO app (domain, name, deploy_unit) VALUES ($1, $2, $3)
		RETURNING app_id`, domain, name, deployUnit).Scan(&appID)
	if err != nil {
		t.Fatalf("seed app %s/%s: %v", domain, name, err)
	}
	return appID
}

func seedChart(t *testing.T, pool *pgxpool.Pool, domain, name string) string {
	t.Helper()
	var chartID string
	err := pool.QueryRow(context.Background(), `
		INSERT INTO chart (domain, name) VALUES ($1, $2)
		RETURNING chart_id`, domain, name).Scan(&chartID)
	if err != nil {
		t.Fatalf("seed chart %s/%s: %v", domain, name, err)
	}
	return chartID
}

func seedBuild(t *testing.T, pool *pgxpool.Pool, workflowRunID string) string {
	t.Helper()
	var buildID string
	err := pool.QueryRow(context.Background(), `
		INSERT INTO build (git_sha, workflow_run_id) VALUES ('deadbeef', $1)
		RETURNING build_id`, workflowRunID).Scan(&buildID)
	if err != nil {
		t.Fatalf("seed build %s: %v", workflowRunID, err)
	}
	return buildID
}

// recordArtifactTx runs RecordArtifact inside a real WithTx transaction,
// exactly as handlers.runIdempotent does in production -- the postgres
// repository methods rely on their caller providing transactional scope,
// they do not open one themselves.
func recordArtifactTx(t *testing.T, reg *Registry, a repository.Artifact, contains []repository.ContainedImageInput) (*repository.Artifact, bool, error) {
	t.Helper()
	var out *repository.Artifact
	var already bool
	err := reg.WithTx(context.Background(), func(ctx context.Context, r repository.Registry) error {
		var ferr error
		out, already, ferr = r.Artifacts().RecordArtifact(ctx, a, contains)
		return ferr
	})
	return out, already, err
}

func artifactCount(t *testing.T, pool *pgxpool.Pool, digest string) int {
	t.Helper()
	var n int
	if err := pool.QueryRow(context.Background(), `SELECT count(*) FROM artifact WHERE digest = $1`, digest).Scan(&n); err != nil {
		t.Fatalf("count artifacts for digest %s: %v", digest, err)
	}
	return n
}

// --- 1. transaction abort behaviour ------------------------------------

// TestRecordArtifact_ChartLinkFailureRollsBackTransaction proves that when a
// statement fails partway through RecordArtifact's chart/contains write
// (here: a duplicate artifact_link primary key), the WHOLE transaction rolls
// back -- the artifact row inserted earlier in the same call does not
// survive. PLAN.md flags this exact hazard: without WithTx wrapping every
// statement, each pool.Exec auto-commits independently and a partial write
// (an artifact row with no valid links) would leak.
func TestRecordArtifact_ChartLinkFailureRollsBackTransaction(t *testing.T) {
	reg, pool := newTestRegistry(t)
	ctx := context.Background()

	appID := seedApp(t, pool, "acme", "widget", "image")
	buildID := seedBuild(t, pool, "run-1")

	img := repository.Artifact{
		Kind: repository.ArtifactKindImage, AppID: appID,
		Repository: "ghcr.io/acme/widget", Version: "v1.0.0",
		Digest: "sha256:image1", BuildID: buildID,
	}
	if _, _, err := recordArtifactTx(t, reg, img, nil); err != nil {
		t.Fatalf("seed image artifact: %v", err)
	}

	chartDigest := "sha256:chart1"
	chart := repository.Artifact{
		Kind: repository.ArtifactKindChart, ChartID: seedChart(t, pool, "acme", "widget-chart"),
		Repository: "ghcr.io/acme/widget-chart", Version: "v1.0.0",
		Digest: chartDigest, BuildID: buildID,
	}
	// The SAME image digest listed twice: the first artifact_link insert
	// succeeds, the second hits the (chart_artifact_id, image_artifact_id)
	// primary key and fails -- a real mid-transaction statement failure.
	contains := []repository.ContainedImageInput{
		{Repository: "ghcr.io/acme/widget", Version: "v1.0.0", Digest: "sha256:image1"},
		{Repository: "ghcr.io/acme/widget", Version: "v1.0.0", Digest: "sha256:image1"},
	}

	err := reg.WithTx(ctx, func(ctx context.Context, r repository.Registry) error {
		_, _, ferr := r.Artifacts().RecordArtifact(ctx, chart, contains)
		return ferr
	})

	if err == nil {
		t.Fatalf("expected RecordArtifact to fail on the duplicate artifact_link insert, got nil error")
	}
	if !errors.Is(err, repository.ErrAlreadyExists) {
		t.Fatalf("expected ErrAlreadyExists (translated unique-violation), got: %v", err)
	}

	if n := artifactCount(t, pool, chartDigest); n != 0 {
		t.Fatalf("transaction abort did not roll back: found %d artifact row(s) for chart digest %s that should never have committed", n, chartDigest)
	}
}

// --- 2. idempotency-key replay ------------------------------------------

// TestRecordBuild_IdempotencyKeyReplay_DoesNotDoubleWrite proves the replay
// path in handlers.runIdempotent: a repeated call with the SAME
// idempotency_key, even carrying DIFFERENT business data, returns the
// original stored response rather than re-executing. Varying the payload on
// the second call is deliberate -- it rules out the (weaker) possibility
// that the second call merely happened to hit the build table's own
// (workflow_run_id, workflow_attempt) natural-key dedup and coincidentally
// returned an equivalent result.
func TestRecordBuild_IdempotencyKeyReplay_DoesNotDoubleWrite(t *testing.T) {
	reg, pool := newTestRegistry(t)
	ctx := authedCtx()
	srv := handlers.NewArtifactServer(reg)

	first, err := srv.RecordBuild(ctx, &pb.RecordBuildRequest{
		GitSha: "sha-first", WorkflowRunId: "run-first", IdempotencyKey: "dup-key",
	})
	if err != nil {
		t.Fatalf("first RecordBuild: %v", err)
	}

	second, err := srv.RecordBuild(ctx, &pb.RecordBuildRequest{
		GitSha: "sha-second", WorkflowRunId: "run-second", IdempotencyKey: "dup-key",
	})
	if err != nil {
		t.Fatalf("replayed RecordBuild: %v", err)
	}

	if second.Build.BuildId != first.Build.BuildId {
		t.Fatalf("replay executed the second call's business logic instead of returning the stored response: first build_id=%s second build_id=%s", first.Build.BuildId, second.Build.BuildId)
	}
	if second.Build.GitSha != "sha-first" || second.Build.WorkflowRunId != "run-first" {
		t.Fatalf("replayed response should carry the FIRST call's data, got git_sha=%s workflow_run_id=%s", second.Build.GitSha, second.Build.WorkflowRunId)
	}

	var buildCount int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM build`).Scan(&buildCount); err != nil {
		t.Fatalf("count builds: %v", err)
	}
	if buildCount != 1 {
		t.Fatalf("idempotency replay double-wrote: expected exactly 1 build row, found %d", buildCount)
	}

	var keyCount int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM idempotency_key WHERE idempotency_key = $1`, "dup-key").Scan(&keyCount); err != nil {
		t.Fatalf("count idempotency_key rows: %v", err)
	}
	if keyCount != 1 {
		t.Fatalf("expected exactly 1 idempotency_key row for the reused key, found %d", keyCount)
	}
}

// --- 3. unique index enforcement -----------------------------------------

// TestRecordArtifact_DuplicateOwnerKindVersionRejectedByRealIndex proves the
// real artifact_version_idx UNIQUE(owner_id, kind, version) index -- not
// application logic -- is what stops a second artifact for the same
// owner/kind/version. RecordArtifact's own pre-check only looks up by
// digest, so a submission with a NEW digest but a colliding
// (owner, kind, version) reaches the INSERT and must be rejected by the
// database itself.
func TestRecordArtifact_DuplicateOwnerKindVersionRejectedByRealIndex(t *testing.T) {
	reg, pool := newTestRegistry(t)
	ctx := context.Background()

	appID := seedApp(t, pool, "acme", "widget", "image")
	buildID := seedBuild(t, pool, "run-1")

	first := repository.Artifact{
		Kind: repository.ArtifactKindImage, AppID: appID,
		Repository: "ghcr.io/acme/widget", Version: "v1.0.0",
		Digest: "sha256:first", BuildID: buildID,
	}
	if _, _, err := recordArtifactTx(t, reg, first, nil); err != nil {
		t.Fatalf("record first artifact: %v", err)
	}

	// Same owner/kind/version, different digest -- RecordArtifact's digest
	// pre-check will not short-circuit this; it must reach the INSERT.
	second := first
	second.Digest = "sha256:second"

	err := reg.WithTx(ctx, func(ctx context.Context, r repository.Registry) error {
		_, _, ferr := r.Artifacts().RecordArtifact(ctx, second, nil)
		return ferr
	})
	if err == nil {
		t.Fatalf("expected a colliding (owner, kind, version) artifact to be rejected, got nil error")
	}
	if !errors.Is(err, repository.ErrAlreadyExists) {
		t.Fatalf("expected ErrAlreadyExists (translated unique-violation on artifact_version_idx), got: %v", err)
	}

	if n := artifactCount(t, pool, "sha256:second"); n != 0 {
		t.Fatalf("rejected artifact should not have been committed, found %d row(s)", n)
	}
	if n := artifactCount(t, pool, "sha256:first"); n != 1 {
		t.Fatalf("original artifact should be unaffected, found %d row(s)", n)
	}
}

// --- 4. ResolveArtifact chart -> image join --------------------------------

// TestResolveArtifact_ChartToImageJoin proves the real JOIN chain
// (artifact -> artifact_link -> artifact -> build) that ResolveArtifact
// walks resolves correctly against real data.
func TestResolveArtifact_ChartToImageJoin(t *testing.T) {
	reg, pool := newTestRegistry(t)
	ctx := context.Background()

	appID := seedApp(t, pool, "acme", "widget", "image")
	buildID := seedBuild(t, pool, "run-1")
	chartID := seedChart(t, pool, "acme", "widget-chart")

	img := repository.Artifact{
		Kind: repository.ArtifactKindImage, AppID: appID,
		Repository: "ghcr.io/acme/widget", Version: "v1.0.0",
		Digest: "sha256:image1", BuildID: buildID,
	}
	if _, _, err := recordArtifactTx(t, reg, img, nil); err != nil {
		t.Fatalf("record image artifact: %v", err)
	}

	chart := repository.Artifact{
		Kind: repository.ArtifactKindChart, ChartID: chartID,
		Repository: "ghcr.io/acme/widget-chart", Version: "v1.0.0",
		Digest: "sha256:chart1", BuildID: buildID,
	}
	contains := []repository.ContainedImageInput{
		{Repository: "ghcr.io/acme/widget", Version: "v1.0.0", Digest: "sha256:image1"},
	}
	if _, _, err := recordArtifactTx(t, reg, chart, contains); err != nil {
		t.Fatalf("record chart artifact: %v", err)
	}

	resolved, images, builds, err := reg.Artifacts().ResolveArtifact(ctx, repository.ArtifactLookup{Digest: "sha256:chart1"})
	if err != nil {
		t.Fatalf("ResolveArtifact: %v", err)
	}
	if resolved.Digest != "sha256:chart1" {
		t.Fatalf("expected resolved chart digest sha256:chart1, got %s", resolved.Digest)
	}
	if len(images) != 1 || images[0].Digest != "sha256:image1" {
		t.Fatalf("expected exactly the pinned image sha256:image1, got %+v", images)
	}
	if len(builds) != 1 || builds[0].BuildID != buildID {
		t.Fatalf("expected the pinned image's build %s, got %+v", buildID, builds)
	}
}

// --- 5. migration 002 / environment table (AR-3b) --------------------------

// TestMigration002SeedsDevStageProd proves migration 002 applies cleanly on
// top of 001 (newTestRegistry already ran both) and that its seed data
// landed: dev/stage/prod, ordered by rank ascending, none archived. This is
// the one seed assertion that can only be proven against a real migration
// run -- server/repository/fake has no migrations to apply.
func TestMigration002SeedsDevStageProd(t *testing.T) {
	_, pool := newTestRegistry(t)

	rows, err := pool.Query(context.Background(), `SELECT key, rank, archived FROM environment ORDER BY rank ASC`)
	if err != nil {
		t.Fatalf("query environment: %v", err)
	}
	defer rows.Close()

	type seeded struct {
		key      string
		rank     int32
		archived bool
	}
	var got []seeded
	for rows.Next() {
		var s seeded
		if err := rows.Scan(&s.key, &s.rank, &s.archived); err != nil {
			t.Fatalf("scan environment row: %v", err)
		}
		got = append(got, s)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate environment rows: %v", err)
	}

	want := []seeded{{"dev", 0, false}, {"stage", 10, false}, {"prod", 20, false}}
	if len(got) != len(want) {
		t.Fatalf("expected migration 002 to seed exactly %v, got %v", want, got)
	}
	for i, w := range want {
		if got[i] != w {
			t.Fatalf("expected seeded environment %+v at rank position %d, got %+v", w, i, got[i])
		}
	}
}

// TestEnvironment_KeyUniqueConstraint proves the real UNIQUE (key)
// constraint on the environment table -- not application logic -- rejects a
// second row for a key that already exists. The repository layer's Upsert
// never reaches this path on its own (it looks up by key before deciding
// whether to insert or update), so this issues the raw INSERT directly to
// exercise the constraint itself.
func TestEnvironment_KeyUniqueConstraint(t *testing.T) {
	_, pool := newTestRegistry(t)

	// "dev" already exists from migration 002's seed data.
	_, err := pool.Exec(context.Background(), `
		INSERT INTO environment (key, display_name, rank) VALUES ('dev', 'Duplicate Dev', 99)`)
	if err == nil {
		t.Fatalf("expected a duplicate key insert to be rejected by the UNIQUE (key) constraint, got nil error")
	}
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) {
		t.Fatalf("expected a *pgconn.PgError, got: %v (%T)", err, err)
	}
	if pgErr.Code != sqlStateUniqueViolation {
		t.Fatalf("expected SQLSTATE %s (unique_violation), got %s: %v", sqlStateUniqueViolation, pgErr.Code, err)
	}

	var count int
	if err := pool.QueryRow(context.Background(), `SELECT count(*) FROM environment WHERE key = 'dev'`).Scan(&count); err != nil {
		t.Fatalf("count dev rows: %v", err)
	}
	if count != 1 {
		t.Fatalf("expected exactly 1 'dev' row after the rejected duplicate insert, found %d", count)
	}
}

// --- 6. promotion (AR-3c) ---------------------------------------------

// devEnvironmentID looks up the "dev" environment_id seeded by migration
// 002, which newTestRegistry already applied.
func devEnvironmentID(t *testing.T, reg *Registry) string {
	t.Helper()
	env, err := reg.Environments().Get(context.Background(), "dev")
	if err != nil {
		t.Fatalf("get dev environment: %v", err)
	}
	return env.EnvironmentID
}

// promoteTx runs Promotions().Promote inside a real WithTx transaction,
// exactly as handlers.PromotionServer.Promote does in production.
func promoteTx(t *testing.T, reg *Registry, p repository.Promotion) (*repository.Promotion, *repository.Promotion, error) {
	t.Helper()
	var current, superseded *repository.Promotion
	err := reg.WithTx(context.Background(), func(ctx context.Context, r repository.Registry) error {
		var ferr error
		current, superseded, ferr = r.Promotions().Promote(ctx, p)
		return ferr
	})
	return current, superseded, err
}

func currentPromotionCount(t *testing.T, pool *pgxpool.Pool, environmentID, targetKey string) int {
	t.Helper()
	var n int
	if err := pool.QueryRow(context.Background(), `
		SELECT count(*) FROM promotion WHERE environment_id = $1 AND target_key = $2 AND valid_to IS NULL`,
		environmentID, targetKey).Scan(&n); err != nil {
		t.Fatalf("count current promotions: %v", err)
	}
	return n
}

// TestPromotion_CurrentIdxRejectsConcurrentCurrentRows proves the real
// partial unique index promotion_current_idx -- not application logic --
// makes two "current" rows for the same (environment_id, target_key)
// structurally impossible. PLAN.md's AR-2d carry-over note flags this
// exact index as deliberately deferred until the promotion table existed;
// this is that follow-up.
func TestPromotion_CurrentIdxRejectsConcurrentCurrentRows(t *testing.T) {
	reg, pool := newTestRegistry(t)
	ctx := context.Background()

	envID := devEnvironmentID(t, reg)
	appID := seedApp(t, pool, "acme", "widget", "image")
	buildID := seedBuild(t, pool, "run-promo-idx")
	artifactID := seedArtifact(t, pool, appID, buildID, "sha256:promo-idx", "v1.0.0")

	insert := func() error {
		_, err := pool.Exec(ctx, `
			INSERT INTO promotion (environment_id, target_key, artifact_id) VALUES ($1, $2, $3)`,
			envID, "image:acme-widget", artifactID)
		return err
	}

	if err := insert(); err != nil {
		t.Fatalf("first insert (should succeed, nothing current yet): %v", err)
	}
	err := insert()
	if err == nil {
		t.Fatalf("expected a second concurrent 'current' row for the same target to be rejected, got nil error")
	}
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) {
		t.Fatalf("expected a *pgconn.PgError, got: %v (%T)", err, err)
	}
	if pgErr.Code != sqlStateUniqueViolation {
		t.Fatalf("expected SQLSTATE %s (unique_violation) from promotion_current_idx, got %s: %v", sqlStateUniqueViolation, pgErr.Code, err)
	}
	if n := currentPromotionCount(t, pool, envID, "image:acme-widget"); n != 1 {
		t.Fatalf("expected exactly 1 current row to survive, found %d", n)
	}
}

// seedArtifact inserts a minimal image artifact row directly, for tests that
// only need a valid artifact_id to hang a promotion off of.
func seedArtifact(t *testing.T, pool *pgxpool.Pool, appID, buildID, digest, version string) string {
	t.Helper()
	var artifactID string
	err := pool.QueryRow(context.Background(), `
		INSERT INTO artifact (kind, app_id, repository, version, digest, build_id)
		VALUES ('image', $1, 'ghcr.io/acme/widget', $2, $3, $4)
		RETURNING artifact_id`, appID, version, digest, buildID).Scan(&artifactID)
	if err != nil {
		t.Fatalf("seed artifact %s: %v", digest, err)
	}
	return artifactID
}

// TestPromotionRepo_PromoteTwice_ExactlyOneCurrentRow proves the SCD2
// close-and-open write end to end through the repository layer against real
// Postgres: promoting a second artifact for the same target closes the
// first row (valid_to set) and leaves exactly one row with valid_to IS
// NULL.
func TestPromotionRepo_PromoteTwice_ExactlyOneCurrentRow(t *testing.T) {
	reg, pool := newTestRegistry(t)
	ctx := context.Background()

	envID := devEnvironmentID(t, reg)
	appID := seedApp(t, pool, "acme", "widget", "image")
	buildID := seedBuild(t, pool, "run-promo-twice")
	art1 := seedArtifact(t, pool, appID, buildID, "sha256:promo-twice-1", "v1.0.0")
	art2 := seedArtifact(t, pool, appID, buildID, "sha256:promo-twice-2", "v2.0.0")

	targetKey := "image:acme-widget"
	first, superseded1, err := promoteTx(t, reg, repository.Promotion{EnvironmentID: envID, TargetKey: targetKey, ArtifactID: art1})
	if err != nil {
		t.Fatalf("first promote: %v", err)
	}
	if superseded1 != nil {
		t.Fatalf("expected no superseded row on the first-ever promotion, got %+v", superseded1)
	}

	second, superseded2, err := promoteTx(t, reg, repository.Promotion{EnvironmentID: envID, TargetKey: targetKey, ArtifactID: art2})
	if err != nil {
		t.Fatalf("second promote: %v", err)
	}
	if superseded2 == nil || superseded2.PromotionID != first.PromotionID {
		t.Fatalf("expected the second promote to supersede the first, got %+v", superseded2)
	}
	if superseded2.ValidTo == nil {
		t.Fatalf("expected the superseded row's valid_to to be set")
	}
	if second.ValidTo != nil {
		t.Fatalf("expected the new current row's valid_to to be nil, got %v", second.ValidTo)
	}

	if n := currentPromotionCount(t, pool, envID, targetKey); n != 1 {
		t.Fatalf("expected exactly 1 row with valid_to IS NULL after two promotions, found %d", n)
	}
	var closedCount int
	if err := pool.QueryRow(ctx, `
		SELECT count(*) FROM promotion WHERE environment_id = $1 AND target_key = $2 AND valid_to IS NOT NULL`,
		envID, targetKey).Scan(&closedCount); err != nil {
		t.Fatalf("count closed rows: %v", err)
	}
	if closedCount != 1 {
		t.Fatalf("expected exactly 1 closed (superseded) row, found %d", closedCount)
	}
}

// TestPromotionRepo_StateAt_HistoricalWindow proves the SCD2 "state at time
// T" window query (StateAt) against real Postgres: a timestamp between two
// promotions returns the first artifact, not the second -- the exact
// property GetEnvironmentState --at <T> depends on. Unlike the
// handler-level tests, this controls valid_from/valid_to directly via SQL
// so it isn't subject to wall-clock/second-resolution flakiness.
func TestPromotionRepo_StateAt_HistoricalWindow(t *testing.T) {
	reg, pool := newTestRegistry(t)
	ctx := context.Background()

	envID := devEnvironmentID(t, reg)
	appID := seedApp(t, pool, "acme", "widget", "image")
	buildID := seedBuild(t, pool, "run-stateat")
	art1 := seedArtifact(t, pool, appID, buildID, "sha256:stateat-1", "v1.0.0")
	art2 := seedArtifact(t, pool, appID, buildID, "sha256:stateat-2", "v2.0.0")
	targetKey := "image:acme-widget"

	t0 := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
	t1 := t0.Add(1 * time.Hour)         // v1 promoted
	t2 := t0.Add(2 * time.Hour)         // v1 superseded by v2
	between := t0.Add(90 * time.Minute) // strictly between t1 and t2

	var promo1ID string
	if err := pool.QueryRow(ctx, `
		INSERT INTO promotion (environment_id, target_key, artifact_id, valid_from, valid_to)
		VALUES ($1, $2, $3, $4, $5) RETURNING promotion_id`,
		envID, targetKey, art1, t1, t2).Scan(&promo1ID); err != nil {
		t.Fatalf("seed historical promotion 1: %v", err)
	}
	var promo2ID string
	if err := pool.QueryRow(ctx, `
		INSERT INTO promotion (environment_id, target_key, artifact_id, valid_from, valid_to)
		VALUES ($1, $2, $3, $4, NULL) RETURNING promotion_id`,
		envID, targetKey, art2, t2).Scan(&promo2ID); err != nil {
		t.Fatalf("seed current promotion 2: %v", err)
	}

	state, err := reg.Promotions().StateAt(ctx, envID, &between)
	if err != nil {
		t.Fatalf("StateAt(between): %v", err)
	}
	if len(state) != 1 || state[0].PromotionID != promo1ID || state[0].Digest != "sha256:stateat-1" {
		t.Fatalf("expected exactly promotion 1 (v1) live at %v, got %+v", between, state)
	}

	stateNow, err := reg.Promotions().StateAt(ctx, envID, nil)
	if err != nil {
		t.Fatalf("StateAt(now): %v", err)
	}
	if len(stateNow) != 1 || stateNow[0].PromotionID != promo2ID {
		t.Fatalf("expected current state to be promotion 2 (v2), got %+v", stateNow)
	}
}

// TestPromotionRepo_Rollback_RoundTrips proves GetPrevious + Promote --
// exactly what handlers.PromotionServer.Rollback composes -- round-trips
// correctly against real Postgres: rolling back after v1 -> v2 re-promotes
// v1 and supersedes v2.
func TestPromotionRepo_Rollback_RoundTrips(t *testing.T) {
	reg, pool := newTestRegistry(t)
	ctx := context.Background()

	envID := devEnvironmentID(t, reg)
	appID := seedApp(t, pool, "acme", "widget", "image")
	buildID := seedBuild(t, pool, "run-rollback")
	art1 := seedArtifact(t, pool, appID, buildID, "sha256:rollback-1", "v1.0.0")
	art2 := seedArtifact(t, pool, appID, buildID, "sha256:rollback-2", "v2.0.0")
	targetKey := "image:acme-widget"

	v1, _, err := promoteTx(t, reg, repository.Promotion{EnvironmentID: envID, TargetKey: targetKey, ArtifactID: art1})
	if err != nil {
		t.Fatalf("promote v1: %v", err)
	}
	v2, _, err := promoteTx(t, reg, repository.Promotion{EnvironmentID: envID, TargetKey: targetKey, ArtifactID: art2})
	if err != nil {
		t.Fatalf("promote v2: %v", err)
	}

	previous, err := reg.Promotions().GetPrevious(ctx, envID, targetKey)
	if err != nil {
		t.Fatalf("GetPrevious: %v", err)
	}
	if previous.PromotionID != v1.PromotionID {
		t.Fatalf("expected GetPrevious to return v1's promotion %s, got %s", v1.PromotionID, previous.PromotionID)
	}

	rollback, supersededByRollback, err := promoteTx(t, reg, repository.Promotion{
		EnvironmentID: envID, TargetKey: targetKey, ArtifactID: previous.ArtifactID,
	})
	if err != nil {
		t.Fatalf("rollback promote: %v", err)
	}
	if rollback.ArtifactID != art1 {
		t.Fatalf("expected rollback to re-promote v1's artifact %s, got %s", art1, rollback.ArtifactID)
	}
	if supersededByRollback == nil || supersededByRollback.PromotionID != v2.PromotionID {
		t.Fatalf("expected rollback to supersede v2's promotion %s, got %+v", v2.PromotionID, supersededByRollback)
	}
	if n := currentPromotionCount(t, pool, envID, targetKey); n != 1 {
		t.Fatalf("expected exactly 1 current row after rollback, found %d", n)
	}
}

// TestPromotionRepo_Promote_TransactionAbortLeavesNoPartialWrite covers the
// hazard AGENTS.md and this phase's assignment both flag explicitly: a
// failed statement aborts the whole transaction, so the close half of
// close-and-open must not survive if the open half fails. Here the INSERT
// is forced to fail by pointing ArtifactID at a nonexistent artifact_id
// (violates the artifact_id FK) *after* a real prior promotion exists to
// close -- proving the earlier UPDATE (the close) does not commit on its
// own when the surrounding transaction as a whole rolls back.
func TestPromotionRepo_Promote_TransactionAbortLeavesNoPartialWrite(t *testing.T) {
	reg, pool := newTestRegistry(t)
	ctx := context.Background()

	envID := devEnvironmentID(t, reg)
	appID := seedApp(t, pool, "acme", "widget", "image")
	buildID := seedBuild(t, pool, "run-abort")
	art1 := seedArtifact(t, pool, appID, buildID, "sha256:abort-1", "v1.0.0")
	targetKey := "image:acme-widget"

	first, _, err := promoteTx(t, reg, repository.Promotion{EnvironmentID: envID, TargetKey: targetKey, ArtifactID: art1})
	if err != nil {
		t.Fatalf("seed first promotion: %v", err)
	}

	_, _, err = promoteTx(t, reg, repository.Promotion{
		EnvironmentID: envID, TargetKey: targetKey, ArtifactID: "00000000-0000-0000-0000-000000000000",
	})
	if err == nil {
		t.Fatalf("expected the second promote (nonexistent artifact_id) to fail on the artifact_id FK, got nil error")
	}

	// The whole transaction -- both the UPDATE that closed `first` and the
	// failed INSERT -- must have rolled back together. If it didn't, `first`
	// would now show valid_to set with no new current row: a target
	// promoted once that now has ZERO current rows, exactly the partial
	// write PLAN.md and AGENTS.md warn about.
	if n := currentPromotionCount(t, pool, envID, targetKey); n != 1 {
		t.Fatalf("expected the original promotion to still be the sole current row after the aborted transaction, found %d current rows", n)
	}
	var validTo *time.Time
	if err := pool.QueryRow(ctx, `SELECT valid_to FROM promotion WHERE promotion_id = $1`, first.PromotionID).Scan(&validTo); err != nil {
		t.Fatalf("read back first promotion: %v", err)
	}
	if validTo != nil {
		t.Fatalf("expected the original promotion's valid_to to remain NULL after the aborted transaction, got %v", *validTo)
	}
}

// --- 7. writeback_outbox (AR-4b) ---------------------------------------

// promoteWithOutboxTx mirrors handlers.PromotionServer.Promote's real
// write path end to end: Promote (SCD2 close-and-open), RecordEvent, then
// Enqueue -- all inside one WithTx transaction, exactly like
// server/handlers/promotion.go's enqueueWriteback. forceBadEventID, when
// non-empty, is used as the outbox row's event_id instead of the real
// event's id, so the INSERT trips the event_id foreign key -- used by
// TestWriteback_EnqueueFailureRollsBackWholeTransaction below to prove the
// promotion does not survive when the outbox insert fails.
func promoteWithOutboxTx(t *testing.T, reg *Registry, p repository.Promotion, forceBadEventID string) (*repository.Promotion, *repository.WritebackOutbox, error) {
	t.Helper()
	var current *repository.Promotion
	var outbox *repository.WritebackOutbox
	err := reg.WithTx(context.Background(), func(ctx context.Context, r repository.Registry) error {
		var perr error
		current, _, perr = r.Promotions().Promote(ctx, p)
		if perr != nil {
			return perr
		}
		event, eerr := r.Promotions().RecordEvent(ctx, repository.PromotionEvent{
			PromotionID: current.PromotionID,
			Action:      repository.PromotionActionPromote,
			Actor:       "integration-test",
		})
		if eerr != nil {
			return eerr
		}
		eventID := event.EventID
		if forceBadEventID != "" {
			eventID = forceBadEventID
		}
		var oerr error
		outbox, oerr = r.Writeback().Enqueue(ctx, repository.WritebackOutbox{
			PromotionID:    current.PromotionID,
			EnvironmentID:  p.EnvironmentID,
			EnvironmentKey: p.EnvironmentKey,
			EventID:        eventID,
			StateHash:      "test-hash",
		})
		return oerr
	})
	return current, outbox, err
}

// TestWriteback_EnqueueCommitsAtomicallyWithPromotion proves the core
// AR-4b property end to end against real Postgres: a promotion and its
// outbox row are written by the same transaction and both are visible
// after commit, with the outbox row correctly linked back to the
// promotion.
func TestWriteback_EnqueueCommitsAtomicallyWithPromotion(t *testing.T) {
	reg, pool := newTestRegistry(t)

	envID := devEnvironmentID(t, reg)
	envKey := "dev"
	appID := seedApp(t, pool, "acme", "widget", "image")
	buildID := seedBuild(t, pool, "run-outbox-atomic")
	art := seedArtifact(t, pool, appID, buildID, "sha256:outbox-atomic", "v1.0.0")

	current, outbox, err := promoteWithOutboxTx(t, reg, repository.Promotion{
		EnvironmentID: envID, EnvironmentKey: envKey, TargetKey: "image:acme-widget", ArtifactID: art,
	}, "")
	if err != nil {
		t.Fatalf("promote+enqueue: %v", err)
	}
	if outbox.PromotionID != current.PromotionID {
		t.Fatalf("expected outbox row promotion_id %s to match the promotion %s", outbox.PromotionID, current.PromotionID)
	}
	if outbox.Status != repository.WritebackOutboxStatusPending {
		t.Fatalf("expected a freshly enqueued outbox row to be pending, got %q", outbox.Status)
	}

	// Read back with a fresh query against the pool (not through Registry),
	// confirming the row genuinely committed rather than only existing in
	// the transaction-scoped Go struct returned above.
	var count int
	if err := pool.QueryRow(context.Background(), `
		SELECT count(*) FROM writeback_outbox WHERE promotion_id = $1 AND status = 'pending'`,
		current.PromotionID).Scan(&count); err != nil {
		t.Fatalf("count outbox rows: %v", err)
	}
	if count != 1 {
		t.Fatalf("expected exactly 1 committed pending outbox row for promotion %s, found %d", current.PromotionID, count)
	}
}

// TestWriteback_EnqueueFailureRollsBackWholeTransaction is the atomicity
// hazard in the other direction: a failing outbox insert (event_id foreign
// key violation, forced via promoteWithOutboxTx's forceBadEventID) must
// roll back the promotion and promotion_event rows written earlier in the
// same transaction, too -- otherwise the registry would believe a
// promotion succeeded with no writeback intent ever recorded for it, the
// exact split-brain PLAN.md and ARCHITECTURE.md's "Writeback: outbox ->
// Temporal" warn about.
func TestWriteback_EnqueueFailureRollsBackWholeTransaction(t *testing.T) {
	reg, pool := newTestRegistry(t)

	envID := devEnvironmentID(t, reg)
	appID := seedApp(t, pool, "acme", "widget", "image")
	buildID := seedBuild(t, pool, "run-outbox-abort")
	art := seedArtifact(t, pool, appID, buildID, "sha256:outbox-abort", "v1.0.0")
	targetKey := "image:acme-widget"

	_, _, err := promoteWithOutboxTx(t, reg, repository.Promotion{
		EnvironmentID: envID, EnvironmentKey: "dev", TargetKey: targetKey, ArtifactID: art,
	}, "00000000-0000-0000-0000-000000000000")
	if err == nil {
		t.Fatalf("expected the outbox insert (bad event_id) to fail the transaction, got nil error")
	}

	if n := currentPromotionCount(t, pool, envID, targetKey); n != 0 {
		t.Fatalf("expected the promotion to have rolled back along with the failed outbox insert, found %d current promotion(s)", n)
	}
	var eventCount, outboxCount int
	if err := pool.QueryRow(context.Background(), `SELECT count(*) FROM promotion_event`).Scan(&eventCount); err != nil {
		t.Fatalf("count promotion_event rows: %v", err)
	}
	if err := pool.QueryRow(context.Background(), `SELECT count(*) FROM writeback_outbox`).Scan(&outboxCount); err != nil {
		t.Fatalf("count writeback_outbox rows: %v", err)
	}
	if eventCount != 0 {
		t.Fatalf("expected the promotion_event row written earlier in the same aborted transaction to have rolled back too, found %d", eventCount)
	}
	if outboxCount != 0 {
		t.Fatalf("expected zero writeback_outbox rows after the aborted transaction, found %d", outboxCount)
	}
}

// TestWritebackOutbox_ClaimBatch_SkipsLockedAndReclaimsStale exercises the
// worker-facing side of the outbox against real Postgres: ClaimBatch's
// single atomic statement (`UPDATE ... WHERE outbox_id IN (SELECT ... FOR
// UPDATE SKIP LOCKED)`) claims pending rows, a second call claims nothing
// more (nothing pending, nothing stale yet), and after the claim is treated
// as stale (staleAfter=0) a second worker successfully reclaims it -- the
// mechanism that makes a worker killed mid-run (AR-4b's exit criterion)
// recoverable instead of stuck.
func TestWritebackOutbox_ClaimBatch_SkipsLockedAndReclaimsStale(t *testing.T) {
	reg, pool := newTestRegistry(t)
	ctx := context.Background()

	envID := devEnvironmentID(t, reg)
	appID := seedApp(t, pool, "acme", "widget", "image")
	buildID := seedBuild(t, pool, "run-outbox-claim")
	art := seedArtifact(t, pool, appID, buildID, "sha256:outbox-claim", "v1.0.0")

	current, _, err := promoteWithOutboxTx(t, reg, repository.Promotion{
		EnvironmentID: envID, EnvironmentKey: "dev", TargetKey: "image:acme-widget", ArtifactID: art,
	}, "")
	if err != nil {
		t.Fatalf("promote+enqueue: %v", err)
	}

	// worker-a claims the only pending row.
	claimedA, err := reg.Writeback().ClaimBatch(ctx, "worker-a", 10, time.Hour)
	if err != nil {
		t.Fatalf("worker-a claim: %v", err)
	}
	if len(claimedA) != 1 || claimedA[0].PromotionID != current.PromotionID {
		t.Fatalf("expected worker-a to claim exactly the 1 pending row, got %+v", claimedA)
	}

	// Immediately after: nothing pending, and the claim is fresh (well
	// within a 1-hour staleness window), so worker-b claims nothing --
	// this is the "SKIP LOCKED prevents double-claim" property, observed
	// through the staleness window rather than true concurrency (which
	// would need two goroutines racing inside the same transaction
	// window; the UPDATE...SELECT...FOR UPDATE SKIP LOCKED subquery
	// pattern is what Postgres guarantees atomic here, not this test).
	claimedB, err := reg.Writeback().ClaimBatch(ctx, "worker-b", 10, time.Hour)
	if err != nil {
		t.Fatalf("worker-b claim (should find nothing): %v", err)
	}
	if len(claimedB) != 0 {
		t.Fatalf("expected worker-b to claim nothing while worker-a's claim is fresh, got %+v", claimedB)
	}

	// A worker killed mid-run leaves its claim stale. staleAfter=0 makes
	// every claimed row immediately eligible, standing in for "time has
	// passed the staleness window" without a real sleep.
	reclaimed, err := reg.Writeback().ClaimBatch(ctx, "worker-c", 10, 0)
	if err != nil {
		t.Fatalf("worker-c reclaim: %v", err)
	}
	if len(reclaimed) != 1 || reclaimed[0].OutboxID != claimedA[0].OutboxID {
		t.Fatalf("expected worker-c to reclaim the stale-claimed row, got %+v", reclaimed)
	}
	if reclaimed[0].Attempts != 2 {
		t.Fatalf("expected attempts to increment across the two claims, got %d", reclaimed[0].Attempts)
	}

	// MarkDone retires it -- no further claim, however stale, picks it up.
	if err := reg.Writeback().MarkDone(ctx, reclaimed[0].OutboxID, current.PromotionID, "run-1"); err != nil {
		t.Fatalf("mark done: %v", err)
	}
	final, err := reg.Writeback().ClaimBatch(ctx, "worker-d", 10, 0)
	if err != nil {
		t.Fatalf("worker-d claim after done: %v", err)
	}
	if len(final) != 0 {
		t.Fatalf("expected a done row to never be reclaimed, got %+v", final)
	}
}

// TestEnvironmentRepo_UpsertCreateThenUpdate exercises the repository layer
// (not raw SQL) end to end against real Postgres: a fresh key creates a row,
// a repeated key updates every field but Key and Archived.
func TestEnvironmentRepo_UpsertCreateThenUpdate(t *testing.T) {
	reg, _ := newTestRegistry(t)
	ctx := context.Background()

	created, wasCreated, err := reg.Environments().Upsert(ctx, repository.Environment{
		Key: "canary", DisplayName: "Canary", Rank: 5, GitopsPath: "environments/canary",
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if !wasCreated || created.EnvironmentID == "" {
		t.Fatalf("expected a newly created environment, got %+v (created=%v)", created, wasCreated)
	}

	updated, wasCreated, err := reg.Environments().Upsert(ctx, repository.Environment{
		Key: "canary", DisplayName: "Canary (renamed)", Rank: 6, RequiresApproval: true,
		AllowedPrincipals: []string{"alice@example.com", "bob@example.com"},
	})
	if err != nil {
		t.Fatalf("update: %v", err)
	}
	if wasCreated {
		t.Fatalf("expected created=false on the second upsert of the same key")
	}
	if updated.EnvironmentID != created.EnvironmentID {
		t.Fatalf("expected the same environment_id across upserts, got %s vs %s", updated.EnvironmentID, created.EnvironmentID)
	}
	if updated.DisplayName != "Canary (renamed)" || updated.Rank != 6 || !updated.RequiresApproval {
		t.Fatalf("expected fields updated in place, got %+v", updated)
	}
	if len(updated.AllowedPrincipals) != 2 {
		t.Fatalf("expected allowed_principals persisted as a real TEXT[] round-trip, got %+v", updated.AllowedPrincipals)
	}
}

// --- 7. version allocation (AR-5a) --------------------------------------

// setDomainAdoptionStage cuts domain over directly via SQL -- there is no
// RPC to do this in AR-5a (see PLAN.md's AR-5 status), the same way a real
// operator would today: `INSERT ... ON CONFLICT DO UPDATE` against
// domain_adoption.
func setDomainAdoptionStage(t *testing.T, pool *pgxpool.Pool, domain, stage string) {
	t.Helper()
	_, err := pool.Exec(context.Background(), `
		INSERT INTO domain_adoption (domain, stage) VALUES ($1, $2)
		ON CONFLICT (domain) DO UPDATE SET stage = EXCLUDED.stage`, domain, stage)
	if err != nil {
		t.Fatalf("set domain_adoption stage for %s: %v", domain, err)
	}
}

// allocateVersionTx runs Artifacts().AllocateVersion inside a real WithTx
// transaction, exactly as handlers.ArtifactServer.AllocateVersion's retry
// loop does in production -- the postgres repository method relies on its
// caller providing transactional scope.
func allocateVersionTx(t *testing.T, reg *Registry, kind repository.ArtifactKind, ownerID, increment, explicitVersion string) (*repository.VersionAllocation, error) {
	t.Helper()
	var out *repository.VersionAllocation
	err := reg.WithTx(context.Background(), func(ctx context.Context, r repository.Registry) error {
		var ferr error
		out, ferr = r.Artifacts().AllocateVersion(ctx, kind, ownerID, increment, explicitVersion)
		return ferr
	})
	return out, err
}

// TestAllocateVersion_OrderingIsNumericNotLexical is the specific bug
// PLAN.md's AR-5 addendum item 2 exists to prevent: with a v1.9.0 artifact
// already recorded, the next patch allocation must be v1.9.1 -- if
// "latest" were computed by ORDER BY on the TEXT `version` column instead
// of the version_major/minor/patch integer columns, "v1.9.0" would sort
// ABOVE "v1.10.0" lexically and this would go wrong the moment a major/minor
// version reaches double digits. This seeds v1.10.0 as a decoy: if ordering
// ever reverted to the TEXT column, "v1.10.0" (lexically greater than
// "v1.9.0") would be picked as "latest" and patched to v1.10.1 -- silently
// wrong versus the numerically-correct v1.9.1.
func TestAllocateVersion_OrderingIsNumericNotLexical(t *testing.T) {
	reg, pool := newTestRegistry(t)

	appID := seedApp(t, pool, "acme", "widget", "image")
	buildID := seedBuild(t, pool, "run-order")
	setDomainAdoptionStage(t, pool, "acme", "allocate")

	// Record v1.10.0 FIRST, then v1.9.0 -- insertion order must not matter;
	// only the numeric columns should. Both use recordArtifactTx so
	// version_major/minor/patch are populated exactly as production does.
	if _, _, err := recordArtifactTx(t, reg, repository.Artifact{
		Kind: repository.ArtifactKindImage, AppID: appID,
		Repository: "ghcr.io/acme/widget", Version: "v1.10.0",
		Digest: "sha256:order-v1-10-0", BuildID: buildID,
	}, nil); err != nil {
		t.Fatalf("seed v1.10.0: %v", err)
	}
	if _, _, err := recordArtifactTx(t, reg, repository.Artifact{
		Kind: repository.ArtifactKindImage, AppID: appID,
		Repository: "ghcr.io/acme/widget", Version: "v1.9.0",
		Digest: "sha256:order-v1-9-0", BuildID: buildID,
	}, nil); err != nil {
		t.Fatalf("seed v1.9.0: %v", err)
	}

	alloc, err := allocateVersionTx(t, reg, repository.ArtifactKindImage, appID, "patch", "")
	if err != nil {
		t.Fatalf("AllocateVersion: %v", err)
	}
	if alloc.PreviousVersion != "v1.10.0" {
		t.Fatalf("expected numeric ordering to pick v1.10.0 as latest (not v1.9.0), got previous_version=%q", alloc.PreviousVersion)
	}
	if alloc.Version != "v1.10.1" {
		t.Fatalf("expected the next patch after v1.10.0 to be v1.10.1, got %q (a lexical-ordering bug would produce v1.9.1's sibling from the wrong base)", alloc.Version)
	}
}

// TestAllocateVersion_ConcurrentCallsNeverCollide drives real concurrent
// goroutines, each opening its own transaction via reg.WithTx, racing to
// allocate the same owner's next patch version. The unique index on
// version_allocation (owner_id, kind, version) — not application-level
// locking — is what PLAN.md and ARCHITECTURE.md promise makes this safe;
// this test would go red if AllocateVersion were changed to compute "next"
// without that constraint backing it (e.g. reading and returning a version
// without inserting anything transactionally first).
func TestAllocateVersion_ConcurrentCallsNeverCollide(t *testing.T) {
	reg, pool := newTestRegistry(t)
	appID := seedApp(t, pool, "acme", "widget", "image")
	setDomainAdoptionStage(t, pool, "acme", "allocate")

	const workers = 8
	var wg sync.WaitGroup
	versions := make([]string, workers)
	errs := make([]error, workers)

	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			// Mirrors handlers.ArtifactServer.AllocateVersion's retry loop:
			// a unique-violation aborts the transaction, so each retry opens
			// a FRESH one via allocateVersionTx.
			for attempt := 0; attempt < 20; attempt++ {
				alloc, err := allocateVersionTx(t, reg, repository.ArtifactKindImage, appID, "patch", "")
				if err == nil {
					versions[idx] = alloc.Version
					return
				}
				if !errors.Is(err, repository.ErrAlreadyExists) {
					errs[idx] = err
					return
				}
			}
			errs[idx] = fmt.Errorf("worker %d: exhausted retries", idx)
		}(i)
	}
	wg.Wait()

	seen := map[string]int{}
	for i, err := range errs {
		if err != nil {
			t.Fatalf("worker %d: %v", i, err)
		}
		seen[versions[i]]++
	}
	for v, count := range seen {
		if count > 1 {
			t.Fatalf("version %s was allocated %d times concurrently -- expected every one of %d concurrent allocations to be unique, got %v", v, count, workers, versions)
		}
	}
	if len(seen) != workers {
		t.Fatalf("expected %d distinct versions from %d concurrent allocations, got %d: %v", workers, workers, len(seen), versions)
	}

	var allocationRows int
	if err := pool.QueryRow(context.Background(), `SELECT count(*) FROM version_allocation WHERE owner_id = $1`, appID).Scan(&allocationRows); err != nil {
		t.Fatalf("count version_allocation rows: %v", err)
	}
	if allocationRows != workers {
		t.Fatalf("expected exactly %d version_allocation rows (one per successful allocation, no double-writes from retries), found %d", workers, allocationRows)
	}
}

// TestAllocateVersion_IdempotencyKeyReplay proves the same idempotency-key
// replay guarantee ArtifactServer's other write RPCs have, driven through
// the real handler against real Postgres (not the fake) -- see
// handlers/artifact_test.go's TestAllocateVersion_IdempotencyKeyReplay for
// the fake-backed version of this same proof.
func TestAllocateVersion_IdempotencyKeyReplay(t *testing.T) {
	reg, pool := newTestRegistry(t)
	ctx := authedCtx()
	seedApp(t, pool, "acme", "widget", "image")
	setDomainAdoptionStage(t, pool, "acme", "allocate")

	srv := handlers.NewArtifactServer(reg)
	req := &pb.AllocateVersionRequest{
		Kind: pb.ArtifactKind_ARTIFACT_KIND_IMAGE, OwnerFullName: "acme-widget",
		Increment: "patch", IdempotencyKey: "replay-key",
	}

	first, err := srv.AllocateVersion(ctx, req)
	if err != nil {
		t.Fatalf("first AllocateVersion: %v", err)
	}
	if first.AlreadyAllocated {
		t.Fatalf("expected already_allocated=false on the first call")
	}

	second, err := srv.AllocateVersion(ctx, req)
	if err != nil {
		t.Fatalf("replayed AllocateVersion: %v", err)
	}
	if second.Version != first.Version {
		t.Fatalf("replay allocated a NEW version instead of returning the stored one: first=%q second=%q", first.Version, second.Version)
	}
	if !second.AlreadyAllocated {
		t.Fatalf("expected already_allocated=true on the replayed call")
	}

	var allocationRows int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM version_allocation`).Scan(&allocationRows); err != nil {
		t.Fatalf("count version_allocation rows: %v", err)
	}
	if allocationRows != 1 {
		t.Fatalf("idempotency replay double-wrote: expected exactly 1 version_allocation row, found %d", allocationRows)
	}
}

// TestAllocateVersion_AdoptionStageGateRejectsNonAllocateDomain proves the
// per-domain cutover gate end to end through the real handler: a domain
// with no domain_adoption row (every domain's implicit default -- see
// migration 001) is rejected, and only after being explicitly cut over does
// the identical request succeed.
func TestAllocateVersion_AdoptionStageGateRejectsNonAllocateDomain(t *testing.T) {
	reg, pool := newTestRegistry(t)
	ctx := authedCtx()
	seedApp(t, pool, "acme", "widget", "image")

	srv := handlers.NewArtifactServer(reg)
	req := &pb.AllocateVersionRequest{
		Kind: pb.ArtifactKind_ARTIFACT_KIND_IMAGE, OwnerFullName: "acme-widget",
		Increment: "patch", IdempotencyKey: "gate-1",
	}

	_, err := srv.AllocateVersion(ctx, req)
	if status.Code(err) != codes.FailedPrecondition {
		t.Fatalf("expected FailedPrecondition for a domain with no domain_adoption row, got %v", err)
	}

	setDomainAdoptionStage(t, pool, "acme", "allocate")
	resp, err := srv.AllocateVersion(ctx, req)
	if err != nil {
		t.Fatalf("expected success once 'acme' is cut over to 'allocate', got %v", err)
	}
	if resp.Version != "v0.0.1" {
		t.Fatalf("expected the first-ever patch allocation to be v0.0.1, got %q", resp.Version)
	}
}

// TestMigration004BackfillsVersionColumns proves migration 004's backfill:
// a pre-existing artifact row (inserted directly via SQL as it would have
// been before this migration existed) gets its version_major/minor/patch
// populated correctly, and an unparseable legacy version is backfilled to
// the documented 0/0/0 sentinel rather than blocking the migration -- see
// 005_version_allocation.up.sql's comments for the decision.
//
// newTestRegistry already applies every migration including 004 before this
// test runs, so this proves the backfill UPDATE that ran as PART of 004
// against rows that existed in 001's `artifact` table state at that point
// in the migration sequence.
func TestMigration004BackfillsVersionColumns(t *testing.T) {
	// This needs artifact rows to exist BEFORE migration 004 runs, so it
	// can't use newTestRegistry (which applies every migration up front).
	// Instead: apply only 001-003, insert rows directly (bypassing the
	// version_major/minor/patch columns that don't exist yet), then apply
	// 004 and assert the backfill.
	ctx := context.Background()
	db := dbtest.NewPostgres(ctx, t, dbtest.Options{})

	sqlDB, err := sql.Open("pgx", db.ConnString)
	if err != nil {
		t.Fatalf("open database/sql handle: %v", err)
	}
	defer sqlDB.Close()

	runner := migrate.NewRunner(sqlDB, schema.Migrations, schema.Dir)
	if err := runner.Steps(3); err != nil {
		t.Fatalf("apply migrations 001-003: %v", err)
	}

	appID := seedApp(t, db.Pool, "acme", "widget", "image")
	buildID := seedBuild(t, db.Pool, "run-backfill")

	type seed struct{ digest, version string }
	seeds := []seed{
		{"sha256:backfill-clean", "v3.4.5"},
		{"sha256:backfill-garbage", "not-a-version-at-all"},
	}
	for _, s := range seeds {
		if _, err := db.Pool.Exec(ctx, `
			INSERT INTO artifact (kind, app_id, repository, version, digest, build_id)
			VALUES ('image', $1, 'ghcr.io/acme/widget', $2, $3, $4)`,
			appID, s.version, s.digest, buildID); err != nil {
			t.Fatalf("seed pre-migration-004 artifact %s: %v", s.digest, err)
		}
	}

	if err := runner.Up(); err != nil {
		t.Fatalf("apply migration 004 (and any later): %v", err)
	}

	type got struct{ major, minor, patch int }
	fetch := func(digest string) got {
		var g got
		if err := db.Pool.QueryRow(ctx, `SELECT version_major, version_minor, version_patch FROM artifact WHERE digest = $1`, digest).Scan(&g.major, &g.minor, &g.patch); err != nil {
			t.Fatalf("read back backfilled columns for %s: %v", digest, err)
		}
		return g
	}

	if g := fetch("sha256:backfill-clean"); g != (got{3, 4, 5}) {
		t.Fatalf("expected v3.4.5 to backfill to (3,4,5), got %+v", g)
	}
	if g := fetch("sha256:backfill-garbage"); g != (got{0, 0, 0}) {
		t.Fatalf("expected an unparseable legacy version to backfill to the documented 0/0/0 sentinel, got %+v", g)
	}
}
