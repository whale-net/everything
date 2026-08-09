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
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
	_ "github.com/jackc/pgx/v5/stdlib" // database/sql driver for the migration runner
	"github.com/jackc/pgx/v5/pgxpool"

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
