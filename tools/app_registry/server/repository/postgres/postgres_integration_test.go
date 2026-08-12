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
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
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
	appmetapb "github.com/whale-net/everything/tools/appmeta/proto"
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

// seedApp inserts pure identity (migration 008, AR-7c) plus ONE
// 'sweep'-provenance app_manifest snapshot carrying deployUnit -- so
// v_current_app (which every read path now goes through) resolves the same
// deploy_unit callers of this helper have always passed. git_sha is a
// per-call-unique "seed-<uuid>" value: uniqueness only matters relative to
// this app's OWN app_id (the unique index is (owner_id, source_git_sha)),
// and every call here creates a fresh app_id anyway, so a constant literal
// would already be safe -- the uuid form is used only so a test that seeds
// the SAME app twice (deliberately, to test update/recovery flows) doesn't
// have to think about it either.
func seedApp(t *testing.T, pool *pgxpool.Pool, domain, name, deployUnit string) string {
	t.Helper()
	ctx := context.Background()
	var appID string
	err := pool.QueryRow(ctx, `
		INSERT INTO app (domain, name) VALUES ($1, $2)
		RETURNING app_id`, domain, name).Scan(&appID)
	if err != nil {
		t.Fatalf("seed app %s/%s: %v", domain, name, err)
	}
	seedAppManifest(t, pool, appID, domain, name, deployUnit, "seed-"+uuid.NewString())
	return appID
}

// seedAppManifest writes one app_manifest snapshot row directly, bypassing
// protojson -- a raw SQL fixture matching EXACTLY the key shape
// postgres/app.go's manifestJSONMarshal produces (UseProtoNames,
// EmitUnpopulated): snake_case keys, deploy_unit as the protojson enum NAME
// ("DEPLOY_UNIT_IMAGE" etc, matching migration 008's generated-column CASE
// expression), so tests can seed a specific (owner, git_sha) snapshot
// directly -- e.g. to test resolveManifestForPublish's exact-commit
// preference, or app_manifest's (owner_id, source_git_sha) idempotency.
func seedAppManifest(t *testing.T, pool *pgxpool.Pool, appID, domain, name, deployUnit, gitSHA string) {
	t.Helper()
	protoDeployUnit := map[string]string{
		"image": "DEPLOY_UNIT_IMAGE",
		"none":  "DEPLOY_UNIT_NONE",
		"chart": "DEPLOY_UNIT_CHART",
		"":      "DEPLOY_UNIT_UNSPECIFIED",
	}[deployUnit]
	if protoDeployUnit == "" {
		protoDeployUnit = "DEPLOY_UNIT_UNSPECIFIED"
	}
	manifestJSON := fmt.Sprintf(`{"domain":%q,"name":%q,"deploy_unit":%q,"registry":"","organization":"","repo_name":""}`,
		domain, name, protoDeployUnit)
	_, err := pool.Exec(context.Background(), `
		INSERT INTO app_manifest (owner_id, source_git_sha, provenance, manifest_json)
		VALUES ($1, $2, 'sweep', $3::jsonb)
		ON CONFLICT (owner_id, source_git_sha) DO NOTHING`,
		appID, gitSHA, manifestJSON)
	if err != nil {
		t.Fatalf("seed app_manifest for %s/%s: %v", domain, name, err)
	}
}

// seedChart mirrors seedApp: pure identity plus one 'sweep'-provenance
// chart_manifest snapshot, so any test that goes on to publish a chart
// artifact finds a snapshot for resolveManifestForPublish to attribute it
// to -- without one, RecordArtifact/BeginPublish for a chart kind would
// fail with ErrFailedPrecondition ("no manifest snapshot recorded").
func seedChart(t *testing.T, pool *pgxpool.Pool, domain, name string) string {
	t.Helper()
	ctx := context.Background()
	var chartID string
	err := pool.QueryRow(ctx, `
		INSERT INTO chart (domain, name) VALUES ($1, $2)
		RETURNING chart_id`, domain, name).Scan(&chartID)
	if err != nil {
		t.Fatalf("seed chart %s/%s: %v", domain, name, err)
	}
	manifestJSON := fmt.Sprintf(`{"domain":%q,"name":%q}`, domain, name)
	if _, err := pool.Exec(ctx, `
		INSERT INTO chart_manifest (owner_id, source_git_sha, provenance, manifest_json)
		VALUES ($1, $2, 'sweep', $3::jsonb)
		ON CONFLICT (owner_id, source_git_sha) DO NOTHING`,
		chartID, "seed-"+uuid.NewString(), manifestJSON); err != nil {
		t.Fatalf("seed chart_manifest for %s/%s: %v", domain, name, err)
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
// they do not open one themselves. domainStage is
// repository.DomainAdoptionStageObserve, matching every domain's implicit
// default (no domain_adoption row) -- every pre-AR-7b test in this file
// relies on RecordArtifact's create-directly-as-published backward-compat
// path, which is legal only at "observe" (see ARCHITECTURE.md "Backward
// compatibility during rollout"); AR-7b's own tests call
// r.Artifacts().RecordArtifact directly when they need a different stage.
func recordArtifactTx(t *testing.T, reg *Registry, a repository.Artifact, contains []repository.ContainedImageInput) (*repository.Artifact, bool, error) {
	t.Helper()
	var out *repository.Artifact
	var already bool
	err := reg.WithTx(context.Background(), func(ctx context.Context, r repository.Registry) error {
		var ferr error
		out, already, ferr = r.Artifacts().RecordArtifact(ctx, a, contains, repository.DomainAdoptionStageObserve)
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
		_, _, ferr := r.Artifacts().RecordArtifact(ctx, chart, contains, repository.DomainAdoptionStageObserve)
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

// TestBeginPublishThenRecordArtifact_SharedIdempotencyKey_ExecuteIndependently_Postgres
// is the real-Postgres counterpart to the same-named test in
// server/handlers/artifact_test.go: a regression test for issue #575 proving
// migration 009's `(idempotency_key, method)` primary key -- not just the Go
// call site -- is what makes two different RPCs sharing one idempotency key
// execute independently instead of the second replaying the first's stored
// response.
//
// release.yml used to give BeginPublish and RecordArtifact the SAME key for
// a release leg. Because BeginPublishResponse and RecordArtifactResponse
// both put an Artifact at proto field 1, RecordArtifact's call unmarshaled
// BeginPublish's already-committed row without error, and runIdempotent
// treated it as a valid replay -- RecordArtifact's real write never ran, no
// error surfaced, and the artifact row was left stuck 'publishing' forever.
func TestBeginPublishThenRecordArtifact_SharedIdempotencyKey_ExecuteIndependently_Postgres(t *testing.T) {
	reg, pool := newTestRegistry(t)
	ctx := authedCtx()
	appSrv := handlers.NewAppServer(reg)
	artSrv := handlers.NewArtifactServer(reg)

	// Registry/Organization/RepoName (unlike oneAppManifest's bare fixture)
	// are required here: BeginPublish's ∅ -> publishing fresh-create branch
	// needs a real image_repository to stamp onto the artifact row.
	am := &appmetapb.AppManifest{
		Domain: "acme", Name: "shared-key-app", DeployUnit: appmetapb.DeployUnit_DEPLOY_UNIT_IMAGE,
		Registry: "ghcr.io", Organization: "acme", RepoName: "acme-shared-key-app",
	}
	if _, err := appSrv.ReconcileApps(ctx, &pb.ReconcileAppsRequest{
		Manifests:      reconcileManifests("sha-575", 100, 100, []*appmetapb.AppManifest{am}, nil),
		IdempotencyKey: "shared-key-reconcile",
	}); err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	build, err := artSrv.RecordBuild(ctx, &pb.RecordBuildRequest{
		GitSha: "sha-575", WorkflowRunId: "run-575-pg", IdempotencyKey: "shared-key-build",
	})
	if err != nil {
		t.Fatalf("record build: %v", err)
	}

	// The exact collision shape issue #575 traced back to release.yml:
	// "Begin publish (image)" and "Record image artifact" both used
	// "${run_id}-${attempt}-${domain}-${app}-image".
	const sharedKey = "run-575-pg-attempt-1-acme-shared-key-app-image"

	begun, err := artSrv.BeginPublish(ctx, &pb.BeginPublishRequest{
		Kind: pb.ArtifactKind_ARTIFACT_KIND_IMAGE, OwnerFullName: "acme-shared-key-app",
		Version: "v1.0.0", BuildId: build.Build.BuildId, IdempotencyKey: sharedKey,
	})
	if err != nil {
		t.Fatalf("BeginPublish: %v", err)
	}
	if begun.Artifact.State != pb.ArtifactState_ARTIFACT_STATE_PUBLISHING || begun.Artifact.Digest != "" {
		t.Fatalf("expected state PUBLISHING with no digest after BeginPublish, got state=%v digest=%q", begun.Artifact.State, begun.Artifact.Digest)
	}

	recorded, err := artSrv.RecordArtifact(ctx, &pb.RecordArtifactRequest{
		BuildId: build.Build.BuildId, Kind: pb.ArtifactKind_ARTIFACT_KIND_IMAGE,
		OwnerFullName: "acme-shared-key-app", Digest: "sha256:shared-key-real-digest", Version: "v1.0.0",
		IdempotencyKey: sharedKey, // reuses BeginPublish's key -- this is the bug
	})
	if err != nil {
		t.Fatalf("RecordArtifact with a key already used by BeginPublish: %v", err)
	}
	if recorded.Artifact.State != pb.ArtifactState_ARTIFACT_STATE_PUBLISHED {
		t.Fatalf("RecordArtifact did not actually execute -- got state %v (bug: cross-method replay of BeginPublish's response)", recorded.Artifact.State)
	}
	if recorded.Artifact.Digest != "sha256:shared-key-real-digest" {
		t.Fatalf("RecordArtifact did not actually execute -- got digest %q (bug: cross-method replay of BeginPublish's response)", recorded.Artifact.Digest)
	}

	// Ground truth: migration 009's (idempotency_key, method) primary key
	// means both calls got their OWN row for the same idempotency_key text.
	rows, err := pool.Query(ctx, `SELECT method FROM idempotency_key WHERE idempotency_key = $1 ORDER BY method`, sharedKey)
	if err != nil {
		t.Fatalf("query idempotency_key rows: %v", err)
	}
	defer rows.Close()
	var methods []string
	for rows.Next() {
		var m string
		if err := rows.Scan(&m); err != nil {
			t.Fatalf("scan method: %v", err)
		}
		methods = append(methods, m)
	}
	if want := []string{"BeginPublish", "RecordArtifact"}; len(methods) != len(want) || methods[0] != want[0] || methods[1] != want[1] {
		t.Fatalf("expected one idempotency_key row per method (%v) for the reused key, got %v", want, methods)
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
		_, _, ferr := r.Artifacts().RecordArtifact(ctx, second, nil, repository.DomainAdoptionStageObserve)
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
// seedArtifact inserts a minimal, already-PUBLISHED image artifact row
// directly (bypassing the repository layer), for tests that only need a
// valid artifact_id to hang a promotion off of. state/provenance/
// version_source are NOT NULL as of migration 007 (AR-7b) -- state and
// version_source have no safe default (see that migration's comments), so
// every raw INSERT in this file must set them explicitly.
// seedArtifact seeds a 'published' image artifact directly. As of migration
// 008 (AR-7c), artifact_promotability_shape requires promotability
// NOT NULL whenever state = 'published' -- 'promotable' here matches
// seedApp's default "image" deploy_unit (DerivePromotability(IMAGE, IMAGE)
// = PROMOTABLE). manifest_id is left NULL: these promotion/writeback tests
// don't exercise manifest attribution, only that an artifact row exists to
// promote.
func seedArtifact(t *testing.T, pool *pgxpool.Pool, appID, buildID, digest, version string) string {
	t.Helper()
	var artifactID string
	err := pool.QueryRow(context.Background(), `
		INSERT INTO artifact (kind, app_id, repository, version, digest, build_id, state, provenance, version_source, promotability)
		VALUES ('image', $1, 'ghcr.io/acme/widget', $2, $3, $4, 'published', 'observed', 'tag', 'promotable')
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
// caller providing transactional scope. repo is the "ghcr.io/..." value
// AllocateVersion (AR-7b) now stamps onto the allocated artifact row's
// NOT-NULL repository column.
func allocateVersionTx(t *testing.T, reg *Registry, kind repository.ArtifactKind, ownerID, repo, increment, explicitVersion string) (*repository.VersionAllocation, error) {
	t.Helper()
	var out *repository.VersionAllocation
	err := reg.WithTx(context.Background(), func(ctx context.Context, r repository.Registry) error {
		var ferr error
		out, ferr = r.Artifacts().AllocateVersion(ctx, kind, ownerID, repo, increment, explicitVersion)
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

	alloc, err := allocateVersionTx(t, reg, repository.ArtifactKindImage, appID, "ghcr.io/acme/widget", "patch", "")
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
				alloc, err := allocateVersionTx(t, reg, repository.ArtifactKindImage, appID, "ghcr.io/acme/widget", "patch", "")
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

	// AR-7b: version_allocation is gone -- a successful allocation is now an
	// `artifact` row in state 'allocated' (see migration 007 and
	// postgres/artifact.go's AllocateVersion). The assertion this test
	// exists for is otherwise UNCHANGED: exactly one row per successful
	// concurrent allocation, proving the unique constraint backing
	// AllocateVersion (now artifact_version_idx alone, doing double duty --
	// see migration 007's comments) survived the storage move with no
	// double-writes from retries.
	var allocationRows int
	if err := pool.QueryRow(context.Background(), `SELECT count(*) FROM artifact WHERE owner_id = $1 AND state = 'allocated'`, appID).Scan(&allocationRows); err != nil {
		t.Fatalf("count allocated artifact rows: %v", err)
	}
	if allocationRows != workers {
		t.Fatalf("expected exactly %d allocated artifact rows (one per successful allocation, no double-writes from retries), found %d", workers, allocationRows)
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

	// AR-7b: a successful allocation is an `artifact` row in state
	// 'allocated' (migration 007) -- see
	// TestAllocateVersion_ConcurrentCallsNeverCollide's comment for why this
	// query changed from version_allocation.
	var allocationRows int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM artifact WHERE state = 'allocated'`).Scan(&allocationRows); err != nil {
		t.Fatalf("count allocated artifact rows: %v", err)
	}
	if allocationRows != 1 {
		t.Fatalf("idempotency replay double-wrote: expected exactly 1 allocated artifact row, found %d", allocationRows)
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

	// Seeded via raw SQL against the pre-migration-008 `app` shape --
	// app_manifest doesn't exist at migration 3, so seedApp/seedAppManifest
	// (which target the post-008 schema) can't be used here. Same pattern
	// as TestMigration007FoldsVersionAllocationIntoArtifact and
	// TestMigration008BackfillsSnapshotsFromExistingRows below.
	var appID string
	if err := db.Pool.QueryRow(ctx, `
		INSERT INTO app (domain, name, deploy_unit) VALUES ($1, $2, $3)
		RETURNING app_id`, "acme", "widget", "image").Scan(&appID); err != nil {
		t.Fatalf("seed pre-migration-008 app row: %v", err)
	}
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

// --- 8. chart composition pinning (issue #544) --------------------------
//
// See ARCHITECTURE.md "Resolved questions" #4 for the write-up this test
// backs. chart_app (rewritten wholesale by every Reconcile, see
// appRepo.setChartApps) is a live "what does this chart declare today" join
// -- it is never read on the promotion/writeback render path. What a
// promoted chart artifact actually renders comes from artifact_link, written
// once at RecordArtifact time from the CI-supplied contains list (the
// chart->image lockfile) and never mutated afterwards. This test proves
// that boundary holds against real Postgres.

// reconcileTx runs Apps().Reconcile inside a real WithTx transaction, exactly
// as handlers.AppServer.ReconcileApps does for a non-dry-run call. It
// synthesizes a fresh ReconcileSource (issue #545's watermark, see
// ARCHITECTURE.md "Reconcile watermark") from the current time on every
// call, so sequential calls in a single test always carry a strictly
// increasing ordering key and are never rejected as stale -- this test is
// about chart composition pinning (#544), not the watermark, so it always
// wants "apply".
func reconcileTx(t *testing.T, reg *Registry, apps []*appmetapb.AppManifest, charts []*appmetapb.ChartManifest) *repository.ReconcileResult {
	t.Helper()
	now := time.Now().UnixNano()
	source := repository.ReconcileSource{
		GitSHA:            fmt.Sprintf("test-sha-%d", now),
		SourceCommittedAt: now,
		DiscoveredAt:      now,
	}
	var result *repository.ReconcileResult
	err := reg.WithTx(context.Background(), func(ctx context.Context, r repository.Registry) error {
		var ferr error
		result, ferr = r.Apps().Reconcile(ctx, apps, charts, source, false)
		return ferr
	})
	if err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if result.SkippedStale {
		t.Fatalf("reconcile unexpectedly skipped as stale (watermark GitSHA=%s): this helper always synthesizes a strictly newer ordering key, so a stale skip here means the clock went backward or ShouldApplyReconcile regressed", source.GitSHA)
	}
	return result
}

func chartAppIDs(t *testing.T, pool *pgxpool.Pool, chartID string) []string {
	t.Helper()
	rows, err := pool.Query(context.Background(), `SELECT app_id FROM chart_app WHERE chart_id = $1 ORDER BY app_id`, chartID)
	if err != nil {
		t.Fatalf("query chart_app for %s: %v", chartID, err)
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			t.Fatalf("scan chart_app row: %v", err)
		}
		ids = append(ids, id)
	}
	return ids
}

func containsDigests(links []repository.ArtifactLink) map[string]bool {
	out := map[string]bool{}
	for _, l := range links {
		out[l.Digest] = true
	}
	return out
}

// TestChartArtifact_CompositionPinnedAtRecordTime_SurvivesLaterReconcile is
// the regression test for issue #544's central worry, in the owner's own
// words: "if I were to promote an old version and accidentally reconcile,
// we'd still be able to deploy the app list based on the digest that was
// provided."
//
// Sequence: reconcile a chart composed of {app-a, app-b}; record and promote
// a chart artifact pinning {app-a, app-b}'s image digests; reconcile AGAIN
// with the chart's declared composition changed to {app-a, app-c} (this
// destructively rewrites chart_app, see appRepo.setChartApps); then assert
// the ALREADY-RECORDED chart artifact -- read both directly via GetArtifact
// and through the exact handler GetEnvironmentState calls at promotion/
// deploy time -- still resolves to {app-a, app-b}'s digests, never app-c's.
func TestChartArtifact_CompositionPinnedAtRecordTime_SurvivesLaterReconcile(t *testing.T) {
	reg, pool := newTestRegistry(t)
	ctx := authedCtx()

	// Initial manifest set: chart "acme/widget-chart" composes app-a and app-b.
	reconcileTx(t, reg,
		[]*appmetapb.AppManifest{
			{Domain: "acme", Name: "app-a", DeployUnit: appmetapb.DeployUnit_DEPLOY_UNIT_IMAGE},
			{Domain: "acme", Name: "app-b", DeployUnit: appmetapb.DeployUnit_DEPLOY_UNIT_IMAGE},
			{Domain: "acme", Name: "app-c", DeployUnit: appmetapb.DeployUnit_DEPLOY_UNIT_IMAGE},
		},
		[]*appmetapb.ChartManifest{
			{Domain: "acme", Name: "widget-chart", Apps: []string{"app-a", "app-b"}},
		},
	)

	chart, err := reg.Apps().GetChartByFullName(ctx, "acme-widget-chart")
	if err != nil {
		t.Fatalf("get chart: %v", err)
	}
	appA, err := reg.Apps().GetAppByFullName(ctx, "acme-app-a")
	if err != nil {
		t.Fatalf("get app-a: %v", err)
	}
	appB, err := reg.Apps().GetAppByFullName(ctx, "acme-app-b")
	if err != nil {
		t.Fatalf("get app-b: %v", err)
	}

	if got := chartAppIDs(t, pool, chart.ChartID); len(got) != 2 {
		t.Fatalf("expected chart_app to seed {app-a, app-b} (2 rows), got %v", got)
	}

	buildID := seedBuild(t, pool, "run-544")

	imgA := repository.Artifact{
		Kind: repository.ArtifactKindImage, AppID: appA.AppID,
		Repository: "ghcr.io/acme/app-a", Version: "v1.0.0", Digest: "sha256:app-a-v1", BuildID: buildID,
	}
	imgB := repository.Artifact{
		Kind: repository.ArtifactKindImage, AppID: appB.AppID,
		Repository: "ghcr.io/acme/app-b", Version: "v1.0.0", Digest: "sha256:app-b-v1", BuildID: buildID,
	}
	if _, _, err := recordArtifactTx(t, reg, imgA, nil); err != nil {
		t.Fatalf("record app-a image: %v", err)
	}
	if _, _, err := recordArtifactTx(t, reg, imgB, nil); err != nil {
		t.Fatalf("record app-b image: %v", err)
	}

	// Chart artifact digest D pins exactly {app-a, app-b} at v1.0.0 -- the
	// chart->image lockfile CI would have produced at this chart's build.
	chartArtifact := repository.Artifact{
		Kind: repository.ArtifactKindChart, ChartID: chart.ChartID,
		Repository: "ghcr.io/acme/widget-chart", Version: "v1.2.3", Digest: "sha256:widget-chart-v1.2.3", BuildID: buildID,
	}
	contains := []repository.ContainedImageInput{
		{Repository: "ghcr.io/acme/app-a", Version: "v1.0.0", Digest: "sha256:app-a-v1"},
		{Repository: "ghcr.io/acme/app-b", Version: "v1.0.0", Digest: "sha256:app-b-v1"},
	}
	if _, _, err := recordArtifactTx(t, reg, chartArtifact, contains); err != nil {
		t.Fatalf("record chart artifact D: %v", err)
	}

	// Promote D -- an "old version" from this point forward, exactly the
	// scenario the issue describes.
	envID := devEnvironmentID(t, reg)
	targetKey := repository.TargetKey(repository.ArtifactKindChart, chart.FullName())
	recorded, err := reg.Artifacts().GetArtifact(ctx, repository.ArtifactLookup{Digest: "sha256:widget-chart-v1.2.3"})
	if err != nil {
		t.Fatalf("get chart artifact D: %v", err)
	}
	if _, _, err := promoteTx(t, reg, repository.Promotion{
		EnvironmentID: envID, EnvironmentKey: "dev", TargetKey: targetKey, ArtifactID: recorded.ArtifactID,
	}); err != nil {
		t.Fatalf("promote D: %v", err)
	}

	// Now reconcile again: the chart's DECLARED composition changes to
	// {app-a, app-c} -- app-b drops out, app-c joins. This is the
	// "accidentally reconcile" step. It destructively rewrites chart_app.
	reconcileTx(t, reg,
		[]*appmetapb.AppManifest{
			{Domain: "acme", Name: "app-a", DeployUnit: appmetapb.DeployUnit_DEPLOY_UNIT_IMAGE},
			{Domain: "acme", Name: "app-b", DeployUnit: appmetapb.DeployUnit_DEPLOY_UNIT_IMAGE},
			{Domain: "acme", Name: "app-c", DeployUnit: appmetapb.DeployUnit_DEPLOY_UNIT_IMAGE},
		},
		[]*appmetapb.ChartManifest{
			{Domain: "acme", Name: "widget-chart", Apps: []string{"app-a", "app-c"}},
		},
	)

	// Sanity check: chart_app really did change -- otherwise this test would
	// prove nothing. GetChartByFullName reads AppIDs off the now-current
	// chart_app join.
	updatedChart, err := reg.Apps().GetChartByFullName(ctx, "acme-widget-chart")
	if err != nil {
		t.Fatalf("get chart after reconcile: %v", err)
	}
	appC, err := reg.Apps().GetAppByFullName(ctx, "acme-app-c")
	if err != nil {
		t.Fatalf("get app-c: %v", err)
	}
	gotLive := map[string]bool{}
	for _, id := range updatedChart.AppIDs {
		gotLive[id] = true
	}
	if !gotLive[appA.AppID] || !gotLive[appC.AppID] || gotLive[appB.AppID] {
		t.Fatalf("expected live chart_app to now be {app-a, app-c} (not app-b) after reconcile, got app_ids=%v", updatedChart.AppIDs)
	}

	// --- The assertion the issue is actually asking for -----------------

	// 1. Repository layer: re-reading chart artifact D by its digest must
	//    still resolve to {app-a, app-b}'s image digests, not app-c's.
	afterReconcile, err := reg.Artifacts().GetArtifact(ctx, repository.ArtifactLookup{Digest: "sha256:widget-chart-v1.2.3"})
	if err != nil {
		t.Fatalf("get chart artifact D after reconcile: %v", err)
	}
	gotDigests := containsDigests(afterReconcile.Contains)
	wantDigests := map[string]bool{"sha256:app-a-v1": true, "sha256:app-b-v1": true}
	if len(gotDigests) != len(wantDigests) || !gotDigests["sha256:app-a-v1"] || !gotDigests["sha256:app-b-v1"] {
		t.Fatalf("promoted chart artifact D's Contains changed after a later Reconcile: got %v, want {app-a-v1, app-b-v1}", gotDigests)
	}
	if gotDigests["sha256:app-c-v1"] {
		t.Fatalf("promoted chart artifact D picked up app-c after Reconcile changed the chart's live composition -- this is exactly the non-determinism issue #544 warns about")
	}

	// 2. Handler layer: GetEnvironmentState -- the exact RPC the writeback
	//    worker and deploy tooling call -- must render the same pinned set,
	//    not whatever chart_app says today.
	promotionSrv := handlers.NewPromotionServer(reg)
	stateResp, err := promotionSrv.GetEnvironmentState(ctx, &pb.GetEnvironmentStateRequest{EnvironmentKey: "dev"})
	if err != nil {
		t.Fatalf("GetEnvironmentState: %v", err)
	}
	if len(stateResp.Entries) != 1 {
		t.Fatalf("expected exactly 1 environment state entry for the promoted chart, got %d", len(stateResp.Entries))
	}
	entry := stateResp.Entries[0]
	if entry.Artifact.Digest != "sha256:widget-chart-v1.2.3" {
		t.Fatalf("expected the rendered entry to be chart artifact D, got digest %s", entry.Artifact.Digest)
	}
	renderedDigests := map[string]bool{}
	for _, img := range entry.Images {
		renderedDigests[img.Digest] = true
	}
	if len(renderedDigests) != 2 || !renderedDigests["sha256:app-a-v1"] || !renderedDigests["sha256:app-b-v1"] {
		t.Fatalf("GetEnvironmentState rendered a different app list than what was promoted: got %v, want {app-a-v1, app-b-v1}", renderedDigests)
	}
	if renderedDigests["sha256:app-c-v1"] {
		t.Fatalf("GetEnvironmentState rendered app-c's image for an already-promoted chart artifact after a later Reconcile changed chart_app -- deploy-time non-determinism")
	}
	if len(entry.Drift) != 0 {
		t.Fatalf("expected no drift entries (nothing was promoted with allow_override), got %+v", entry.Drift)
	}
}

// ============================================================================
// Reconcile watermark (issue #545)
// ============================================================================
//
// These tests exercise exactly what server/repository/fake's reconcile
// watermark logic cannot: the real migration 006 schema (the seeded
// sentinel row, the CHECK(id=1) singleton guard), and — most importantly —
// that SELECT ... FOR UPDATE against the real `reconcile_watermark` row
// actually serializes two concurrent Reconcile transactions rather than
// both reading the same stale watermark. See ARCHITECTURE.md "Reconcile
// watermark" for the full comparison/tie-break rules asserted here.

// reconcileManifests builds an AppManifestSet carrying the ordering
// metadata Reconcile checks against reconcile_watermark directly, so each
// test can control git_sha/source_committed_at/discovered_at precisely
// rather than going through release_helper_go.
func reconcileManifests(gitSha string, sourceCommittedAt, discoveredAt int64, apps []*appmetapb.AppManifest, charts []*appmetapb.ChartManifest) *appmetapb.AppManifestSet {
	return &appmetapb.AppManifestSet{
		GitSha:            gitSha,
		SourceCommittedAt: sourceCommittedAt,
		DiscoveredAt:      discoveredAt,
		Apps:              apps,
		Charts:            charts,
	}
}

func oneAppManifest(domain, name string) *appmetapb.AppManifest {
	return &appmetapb.AppManifest{
		Domain: domain, Name: name, Description: "d", Language: "go", AppType: "worker",
		DeployUnit: appmetapb.DeployUnit_DEPLOY_UNIT_IMAGE,
	}
}

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

// --- 9. artifact lifecycle (AR-7b, issue #558) --------------------------
//
// See ARCHITECTURE.md "Artifact lifecycle: allocated -> publishing ->
// published" for the legal-transition table these tests exercise against
// real Postgres (not just server/repository/fake): ∅ -> allocated
// (AllocateVersion), ∅ -> publishing / allocated -> publishing / failed ->
// publishing (BeginPublish), publishing -> published (RecordArtifact),
// publishing -> failed (FailPublish). published is terminal.

// beginPublishTx runs Artifacts().BeginPublish inside a real WithTx
// transaction, exactly as handlers.ArtifactServer.BeginPublish does in
// production.
func beginPublishTx(t *testing.T, reg *Registry, kind repository.ArtifactKind, ownerID, version, buildID, repositoryHint string, versionSource repository.VersionSource) (*repository.Artifact, error) {
	t.Helper()
	var out *repository.Artifact
	err := reg.WithTx(context.Background(), func(ctx context.Context, r repository.Registry) error {
		var ferr error
		out, ferr = r.Artifacts().BeginPublish(ctx, kind, ownerID, version, buildID, repositoryHint, versionSource)
		return ferr
	})
	return out, err
}

// failPublishTx runs Artifacts().FailPublish inside a real WithTx
// transaction, exactly as handlers.ArtifactServer.FailPublish does in
// production.
func failPublishTx(t *testing.T, reg *Registry, kind repository.ArtifactKind, ownerID, version, reason string) (*repository.Artifact, error) {
	t.Helper()
	var out *repository.Artifact
	err := reg.WithTx(context.Background(), func(ctx context.Context, r repository.Registry) error {
		var ferr error
		out, ferr = r.Artifacts().FailPublish(ctx, kind, ownerID, version, reason)
		return ferr
	})
	return out, err
}

// artifactStateRow reads back state/fail_reason/digest/build_id for one
// artifact row directly -- assertions the repository.Artifact struct a
// call returns doesn't need to carry every raw column for.
func artifactStateRow(t *testing.T, pool *pgxpool.Pool, artifactID string) (state, failReason string, hasDigest, hasBuildID bool) {
	t.Helper()
	var digest, buildID *string
	if err := pool.QueryRow(context.Background(), `
		SELECT state, fail_reason, digest, build_id FROM artifact WHERE artifact_id = $1`, artifactID).
		Scan(&state, &failReason, &digest, &buildID); err != nil {
		t.Fatalf("read artifact state row %s: %v", artifactID, err)
	}
	return state, failReason, digest != nil, buildID != nil
}

// TestArtifactLifecycle_LegalTransitions walks EVERY legal transition in
// ARCHITECTURE.md's "Artifact lifecycle" table against real Postgres, in
// one continuous sequence for the same (owner, kind, version): ∅ ->
// allocated (AllocateVersion), allocated -> publishing (BeginPublish),
// publishing -> failed (FailPublish), failed -> publishing (BeginPublish
// retry), publishing -> published (RecordArtifact). A second, independent
// (owner, kind, version) proves the ∅ -> publishing branch (BeginPublish
// with no prior allocation, the pre-cutover path) -- and, by coexisting
// with the first sequence's NULL-digest rows at the same instant, proves
// artifact_digest_idx's partial `WHERE digest IS NOT NULL` uniqueness
// doesn't collide multiple digest-less rows.
func TestArtifactLifecycle_LegalTransitions(t *testing.T) {
	reg, pool := newTestRegistry(t)
	appID := seedApp(t, pool, "acme", "lifecycle", "image")
	buildID := seedBuild(t, pool, "run-lifecycle")
	setDomainAdoptionStage(t, pool, "acme", "allocate")

	// ∅ -> allocated
	alloc, err := allocateVersionTx(t, reg, repository.ArtifactKindImage, appID, "ghcr.io/acme/lifecycle", "patch", "")
	if err != nil {
		t.Fatalf("AllocateVersion (∅ -> allocated): %v", err)
	}
	version := alloc.Version

	allocated, err := reg.Artifacts().GetArtifact(context.Background(), repository.ArtifactLookup{
		OwnerFullName: "acme-lifecycle", Kind: repository.ArtifactKindImage, Version: version,
	})
	if err != nil {
		t.Fatalf("GetArtifact after allocation: %v", err)
	}
	if allocated.State != repository.ArtifactStateAllocated {
		t.Fatalf("expected state allocated, got %q", allocated.State)
	}
	if state, _, hasDigest, hasBuildID := artifactStateRow(t, pool, allocated.ArtifactID); state != "allocated" || hasDigest || hasBuildID {
		t.Fatalf("expected allocated row with NULL digest/build_id, got state=%s hasDigest=%v hasBuildID=%v", state, hasDigest, hasBuildID)
	}

	// allocated -> publishing
	publishing, err := beginPublishTx(t, reg, repository.ArtifactKindImage, appID, version, buildID, "", repository.VersionSourceTag)
	if err != nil {
		t.Fatalf("BeginPublish (allocated -> publishing): %v", err)
	}
	if publishing.State != repository.ArtifactStatePublishing {
		t.Fatalf("expected state publishing, got %q", publishing.State)
	}
	if publishing.VersionSource != repository.VersionSourceRegistry {
		t.Fatalf("expected version_source to stay REGISTRY from AllocateVersion (BeginPublish's own versionSource arg is ignored on this branch), got %q", publishing.VersionSource)
	}
	if state, _, hasDigest, hasBuildID := artifactStateRow(t, pool, publishing.ArtifactID); state != "publishing" || hasDigest || !hasBuildID {
		t.Fatalf("expected publishing row with NULL digest and a build_id, got state=%s hasDigest=%v hasBuildID=%v", state, hasDigest, hasBuildID)
	}

	// publishing -> failed
	failed, err := failPublishTx(t, reg, repository.ArtifactKindImage, appID, version, "push failed")
	if err != nil {
		t.Fatalf("FailPublish (publishing -> failed): %v", err)
	}
	if failed.State != repository.ArtifactStateFailed {
		t.Fatalf("expected state failed, got %q", failed.State)
	}
	if failed.FailReason != "push failed" {
		t.Fatalf("expected fail_reason recorded, got %q", failed.FailReason)
	}

	// failed -> publishing (retry)
	retried, err := beginPublishTx(t, reg, repository.ArtifactKindImage, appID, version, buildID, "", repository.VersionSourceTag)
	if err != nil {
		t.Fatalf("BeginPublish (failed -> publishing): %v", err)
	}
	if retried.State != repository.ArtifactStatePublishing {
		t.Fatalf("expected state publishing after retry, got %q", retried.State)
	}
	if retried.FailReason != "" {
		t.Fatalf("expected fail_reason cleared after a successful retry, got %q", retried.FailReason)
	}

	// ∅ -> publishing, a SEPARATE (owner, kind, version) -- coexists with
	// the retried row above, both currently digest-less, while the ORIGINAL
	// sequence continues below.
	freshPublishing, err := beginPublishTx(t, reg, repository.ArtifactKindImage, appID, "v9.9.9", buildID, "ghcr.io/acme/lifecycle", repository.VersionSourceTag)
	if err != nil {
		t.Fatalf("BeginPublish (∅ -> publishing): %v", err)
	}
	if freshPublishing.State != repository.ArtifactStatePublishing {
		t.Fatalf("expected state publishing for the ∅ -> publishing branch, got %q", freshPublishing.State)
	}
	if freshPublishing.VersionSource != repository.VersionSourceTag {
		t.Fatalf("expected version_source TAG for the ∅ -> publishing branch, got %q", freshPublishing.VersionSource)
	}

	// publishing -> published
	published, _, err := recordArtifactTx(t, reg, repository.Artifact{
		Kind: repository.ArtifactKindImage, AppID: appID,
		Repository: "ghcr.io/acme/lifecycle", Version: version,
		Digest: "sha256:lifecycle-final", BuildID: buildID,
	}, nil)
	if err != nil {
		t.Fatalf("RecordArtifact (publishing -> published): %v", err)
	}
	if published.State != repository.ArtifactStatePublished {
		t.Fatalf("expected state published, got %q", published.State)
	}
	if published.Digest != "sha256:lifecycle-final" {
		t.Fatalf("expected digest stamped, got %q", published.Digest)
	}
	if state, _, hasDigest, hasBuildID := artifactStateRow(t, pool, published.ArtifactID); state != "published" || !hasDigest || !hasBuildID {
		t.Fatalf("expected published row with digest and build_id set, got state=%s hasDigest=%v hasBuildID=%v", state, hasDigest, hasBuildID)
	}
}

// TestArtifactLifecycle_IllegalTransitionsRejected proves every state that
// is NOT a legal starting point for BeginPublish/FailPublish/RecordArtifact
// is rejected against real Postgres (not just server/repository/fake) --
// FailedPrecondition for an out-of-order transition, AlreadyExists for
// RecordArtifact's specific different-digest-on-an-already-published-
// version conflict.
func TestArtifactLifecycle_IllegalTransitionsRejected(t *testing.T) {
	reg, pool := newTestRegistry(t)
	appID := seedApp(t, pool, "acme", "illegal", "image")
	buildID := seedBuild(t, pool, "run-illegal")
	setDomainAdoptionStage(t, pool, "acme", "allocate")

	t.Run("BeginPublish rejects an already-published row", func(t *testing.T) {
		if _, _, err := recordArtifactTx(t, reg, repository.Artifact{
			Kind: repository.ArtifactKindImage, AppID: appID,
			Repository: "ghcr.io/acme/illegal", Version: "v1.0.0",
			Digest: "sha256:illegal-published", BuildID: buildID,
		}, nil); err != nil {
			t.Fatalf("seed published artifact: %v", err)
		}
		_, err := beginPublishTx(t, reg, repository.ArtifactKindImage, appID, "v1.0.0", buildID, "", repository.VersionSourceTag)
		if !errors.Is(err, repository.ErrFailedPrecondition) {
			t.Fatalf("expected ErrFailedPrecondition for BeginPublish against a published row, got %v", err)
		}
	})

	t.Run("FailPublish rejects an allocated row", func(t *testing.T) {
		alloc, err := allocateVersionTx(t, reg, repository.ArtifactKindImage, appID, "ghcr.io/acme/illegal", "minor", "")
		if err != nil {
			t.Fatalf("AllocateVersion: %v", err)
		}
		_, err = failPublishTx(t, reg, repository.ArtifactKindImage, appID, alloc.Version, "should be rejected")
		if !errors.Is(err, repository.ErrFailedPrecondition) {
			t.Fatalf("expected ErrFailedPrecondition for FailPublish against an allocated row, got %v", err)
		}
	})

	t.Run("RecordArtifact rejects an allocated row with no prior BeginPublish, at a non-observe stage", func(t *testing.T) {
		alloc, err := allocateVersionTx(t, reg, repository.ArtifactKindImage, appID, "ghcr.io/acme/illegal", "major", "")
		if err != nil {
			t.Fatalf("AllocateVersion: %v", err)
		}
		err = reg.WithTx(context.Background(), func(ctx context.Context, r repository.Registry) error {
			_, _, ferr := r.Artifacts().RecordArtifact(ctx, repository.Artifact{
				Kind: repository.ArtifactKindImage, AppID: appID,
				Repository: "ghcr.io/acme/illegal", Version: alloc.Version,
				Digest: "sha256:illegal-skip-publish", BuildID: buildID,
			}, nil, repository.DomainAdoptionStageAllocate)
			return ferr
		})
		if !errors.Is(err, repository.ErrFailedPrecondition) {
			t.Fatalf("expected ErrFailedPrecondition for RecordArtifact against an allocated row at domainStage=allocate, got %v", err)
		}
	})

	t.Run("RecordArtifact rejects a different digest for an already-published version", func(t *testing.T) {
		// v9.9.9: distinct from any version the earlier subtests in this
		// function may have allocated on the same appID (they share `reg`
		// and run sequentially), so this subtest's own collision is the
		// only one in play.
		if _, _, err := recordArtifactTx(t, reg, repository.Artifact{
			Kind: repository.ArtifactKindImage, AppID: appID,
			Repository: "ghcr.io/acme/illegal", Version: "v9.9.9",
			Digest: "sha256:illegal-conflict-original", BuildID: buildID,
		}, nil); err != nil {
			t.Fatalf("seed published artifact: %v", err)
		}
		_, _, err := recordArtifactTx(t, reg, repository.Artifact{
			Kind: repository.ArtifactKindImage, AppID: appID,
			Repository: "ghcr.io/acme/illegal", Version: "v9.9.9",
			Digest: "sha256:illegal-conflict-different", BuildID: buildID,
		}, nil)
		if !errors.Is(err, repository.ErrAlreadyExists) {
			t.Fatalf("expected ErrAlreadyExists for a different digest on an already-published version, got %v", err)
		}
	})
}

// seedRawArtifact inserts an artifact row directly in the given state,
// bypassing the repository layer -- for the reaper test below, which needs
// to control state_changed_at independently of wall-clock time.
func seedRawArtifact(t *testing.T, pool *pgxpool.Pool, appID string, state repository.ArtifactState, version, buildID string) string {
	t.Helper()
	var artifactID string
	var buildIDArg any
	if buildID != "" {
		buildIDArg = buildID
	}
	err := pool.QueryRow(context.Background(), `
		INSERT INTO artifact (kind, app_id, repository, version, build_id, state, provenance, version_source)
		VALUES ('image', $1, 'ghcr.io/acme/reaper', $2, $3, $4, 'observed', 'tag')
		RETURNING artifact_id`, appID, version, buildIDArg, string(state)).Scan(&artifactID)
	if err != nil {
		t.Fatalf("seed raw artifact %s %s: %v", version, state, err)
	}
	return artifactID
}

// backdateStateChangedAt moves artifactID's state_changed_at back by ago,
// simulating a row that has been sitting in its current state for a while
// -- without a real sleep.
func backdateStateChangedAt(t *testing.T, pool *pgxpool.Pool, artifactID string, ago time.Duration) {
	t.Helper()
	// Compute the target timestamp in Go and set it directly, rather than
	// subtracting a Go time.Duration from state_changed_at in SQL -- pgx
	// has no implicit conversion from time.Duration to Postgres INTERVAL.
	target := time.Now().UTC().Add(-ago)
	if _, err := pool.Exec(context.Background(), `
		UPDATE artifact SET state_changed_at = $1 WHERE artifact_id = $2`,
		target, artifactID); err != nil {
		t.Fatalf("backdate state_changed_at for %s: %v", artifactID, err)
	}
}

// TestExpireStale_ReaperTimeout proves ExpireStale (the AR-7b stale-row
// reaper's sweep) moves BOTH a stale "allocated" row and a stale
// "publishing" row to "failed" with reason "stale" once their
// state_changed_at exceeds the timeout, and leaves a FRESH row (well within
// the timeout) untouched -- see ARCHITECTURE.md "The reaper is not
// optional": a cancelled release run would otherwise hold a version number
// in the (owner_id, kind, version) unique index forever.
func TestExpireStale_ReaperTimeout(t *testing.T) {
	reg, pool := newTestRegistry(t)
	appID := seedApp(t, pool, "acme", "reaper", "image")
	buildID := seedBuild(t, pool, "run-reaper")

	staleAllocatedID := seedRawArtifact(t, pool, appID, repository.ArtifactStateAllocated, "v1.0.0", "")
	stalePublishingID := seedRawArtifact(t, pool, appID, repository.ArtifactStatePublishing, "v1.1.0", buildID)
	freshAllocatedID := seedRawArtifact(t, pool, appID, repository.ArtifactStateAllocated, "v1.2.0", "")

	backdateStateChangedAt(t, pool, staleAllocatedID, 2*time.Hour)
	backdateStateChangedAt(t, pool, stalePublishingID, 2*time.Hour)
	// freshAllocatedID is left at its just-inserted (NOW()) state_changed_at.

	n, err := reg.Artifacts().ExpireStale(context.Background(), time.Hour)
	if err != nil {
		t.Fatalf("ExpireStale: %v", err)
	}
	if n != 2 {
		t.Fatalf("expected exactly 2 rows expired, got %d", n)
	}

	if state, reason, _, _ := artifactStateRow(t, pool, staleAllocatedID); state != "failed" || reason != "stale" {
		t.Fatalf("expected the stale allocated row to be failed/stale, got state=%s reason=%s", state, reason)
	}
	if state, reason, _, _ := artifactStateRow(t, pool, stalePublishingID); state != "failed" || reason != "stale" {
		t.Fatalf("expected the stale publishing row to be failed/stale, got state=%s reason=%s", state, reason)
	}
	if state, _, _, _ := artifactStateRow(t, pool, freshAllocatedID); state != "allocated" {
		t.Fatalf("expected the fresh allocated row to be left alone, got state=%s", state)
	}

	// A second sweep with nothing newly stale finds nothing left to expire.
	n2, err := reg.Artifacts().ExpireStale(context.Background(), time.Hour)
	if err != nil {
		t.Fatalf("second ExpireStale: %v", err)
	}
	if n2 != 0 {
		t.Fatalf("expected the second sweep to find nothing left to expire, got %d", n2)
	}
}

// TestMigration007FoldsVersionAllocationIntoArtifact proves migration 007's
// fold-in: a pre-existing version_allocation row (inserted directly via
// SQL, as it would have existed before this migration ran -- see AR-5a)
// becomes an `artifact` row in state 'allocated', provenance 'observed',
// version_source 'registry', with its repository DERIVED from the owning
// app's image_repository (version_allocation itself carried no repository
// column), and version_allocation itself is dropped afterward.
func TestMigration007FoldsVersionAllocationIntoArtifact(t *testing.T) {
	ctx := context.Background()
	db := dbtest.NewPostgres(ctx, t, dbtest.Options{})

	sqlDB, err := sql.Open("pgx", db.ConnString)
	if err != nil {
		t.Fatalf("open database/sql handle: %v", err)
	}
	defer sqlDB.Close()

	runner := migrate.NewRunner(sqlDB, schema.Migrations, schema.Dir)
	if err := runner.Steps(6); err != nil {
		t.Fatalf("apply migrations 001-006: %v", err)
	}

	// Seeded via raw SQL against the pre-migration-008 `app` shape (still
	// carrying deploy_unit/image_repository directly -- migration 008 is
	// what removes them, and app_manifest doesn't exist until it runs) --
	// deliberately NOT the seedApp/seedAppManifest helpers, which target the
	// POST-008 schema this test's seed point (migration 6) predates.
	var appID string
	if err := db.Pool.QueryRow(ctx, `
		INSERT INTO app (domain, name, deploy_unit, image_repository) VALUES ($1, $2, 'image', $3)
		RETURNING app_id`, "acme", "folded", "ghcr.io/acme/folded").Scan(&appID); err != nil {
		t.Fatalf("seed pre-migration-008 app row: %v", err)
	}

	var allocationID string
	if err := db.Pool.QueryRow(ctx, `
		INSERT INTO version_allocation (owner_id, kind, version, version_major, version_minor, version_patch)
		VALUES ($1, 'image', 'v3.4.5', 3, 4, 5)
		RETURNING version_allocation_id`, appID).Scan(&allocationID); err != nil {
		t.Fatalf("seed pre-migration-007 version_allocation row: %v", err)
	}

	if err := runner.Up(); err != nil {
		t.Fatalf("apply migration 007: %v", err)
	}

	var state, provenance, versionSource, repo string
	var digest, buildID *string
	if err := db.Pool.QueryRow(ctx, `
		SELECT state, provenance, version_source, repository, digest, build_id
		FROM artifact WHERE artifact_id = $1`, allocationID).
		Scan(&state, &provenance, &versionSource, &repo, &digest, &buildID); err != nil {
		t.Fatalf("read back folded artifact row (expected artifact_id == the old version_allocation_id): %v", err)
	}
	if state != "allocated" {
		t.Fatalf("expected folded row state 'allocated', got %q", state)
	}
	if provenance != "observed" {
		t.Fatalf("expected folded row provenance 'observed', got %q", provenance)
	}
	if versionSource != "registry" {
		t.Fatalf("expected folded row version_source 'registry', got %q", versionSource)
	}
	if repo != "ghcr.io/acme/folded" {
		t.Fatalf("expected folded row repository derived from the owning app's image_repository, got %q", repo)
	}
	if digest != nil || buildID != nil {
		t.Fatalf("expected folded row to carry no digest/build_id, got digest=%v build_id=%v", digest, buildID)
	}

	var tableExists bool
	if err := db.Pool.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM information_schema.tables WHERE table_name = 'version_allocation')`).Scan(&tableExists); err != nil {
		t.Fatalf("check version_allocation table existence: %v", err)
	}
	if tableExists {
		t.Fatalf("expected version_allocation to be dropped by migration 007")
	}
}

// ============================================================================
// 10. App identity / manifest snapshot split (AR-7c, migration 008, issue #558)
// ============================================================================

// appManifestRow reads back one app_manifest row's generated columns plus
// the raw manifest_json, for asserting the generated-column derivation
// directly against real Postgres (not just through v_current_app).
func appManifestRow(t *testing.T, pool *pgxpool.Pool, appID, gitSHA string) (deployUnit, imageRepository string, found bool) {
	t.Helper()
	err := pool.QueryRow(context.Background(), `
		SELECT deploy_unit, image_repository FROM app_manifest WHERE owner_id = $1 AND source_git_sha = $2`,
		appID, gitSHA).Scan(&deployUnit, &imageRepository)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", "", false
	}
	if err != nil {
		t.Fatalf("read app_manifest for %s/%s: %v", appID, gitSHA, err)
	}
	return deployUnit, imageRepository, true
}

func appManifestSnapshotCount(t *testing.T, pool *pgxpool.Pool, appID string) int {
	t.Helper()
	var n int
	if err := pool.QueryRow(context.Background(), `SELECT count(*) FROM app_manifest WHERE owner_id = $1`, appID).Scan(&n); err != nil {
		t.Fatalf("count app_manifest rows for %s: %v", appID, err)
	}
	return n
}

// TestAssertApps_CreatesIdentityAndSnapshot_Postgres proves AR-7c's AssertApps
// against real Postgres: identity row created, exactly one app_manifest
// snapshot written, and the generated deploy_unit/image_repository columns
// resolve correctly straight off manifest_json.
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

	if n := appManifestSnapshotCount(t, pool, appID); n != 1 {
		t.Fatalf("expected exactly 1 app_manifest snapshot, got %d", n)
	}
	du, repoCol, found := appManifestRow(t, pool, appID, "sha-assert-1")
	if !found {
		t.Fatalf("expected an app_manifest row keyed (owner_id=%s, source_git_sha=sha-assert-1)", appID)
	}
	if du != "image" {
		t.Fatalf("expected the generated deploy_unit column to resolve 'image', got %q", du)
	}
	if repoCol != "ghcr.io/acme/acme-assert-svc" {
		t.Fatalf("expected the generated image_repository column to resolve 'ghcr.io/acme/acme-assert-svc', got %q", repoCol)
	}

	// The provenance recorded is 'release', not 'sweep' -- AssertApps, not
	// ReconcileApps.
	var provenance string
	if err := pool.QueryRow(ctx, `SELECT provenance FROM app_manifest WHERE owner_id = $1`, appID).Scan(&provenance); err != nil {
		t.Fatalf("read provenance: %v", err)
	}
	if provenance != "release" {
		t.Fatalf("expected provenance 'release' for an AssertApps snapshot, got %q", provenance)
	}
}

// TestAppManifestSnapshot_IdempotentOnOwnerGitSha proves migration 008's
// UNIQUE (owner_id, source_git_sha) is what makes repeated AssertApps calls
// for the SAME commit naturally idempotent -- a real release re-run (same
// git_sha, different idempotency_key) writes no new snapshot row.
func TestAppManifestSnapshot_IdempotentOnOwnerGitSha(t *testing.T) {
	reg, pool := newTestRegistry(t)
	ctx := authedCtx()
	srv := handlers.NewAppServer(reg)

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
	// a replay) but the SAME git_sha -- this must still write no NEW
	// snapshot row, because ON CONFLICT (owner_id, source_git_sha) DO
	// NOTHING is what carries the idempotency here, not the idempotency_key
	// table.
	if _, err := srv.AssertApps(ctx, &pb.AssertAppsRequest{
		Manifests:      reconcileManifests("sha-idem", 100, 100, []*appmetapb.AppManifest{am}, nil),
		IdempotencyKey: "assert-idem-2",
	}); err != nil {
		t.Fatalf("second assert (same git_sha): %v", err)
	}
	if n := appManifestSnapshotCount(t, pool, appID); n != 1 {
		t.Fatalf("expected exactly 1 snapshot after two calls for the SAME git_sha, got %d", n)
	}

	// A DIFFERENT git_sha for the same owner DOES write a new snapshot.
	if _, err := srv.AssertApps(ctx, &pb.AssertAppsRequest{
		Manifests:      reconcileManifests("sha-idem-2", 200, 200, []*appmetapb.AppManifest{am}, nil),
		IdempotencyKey: "assert-idem-3",
	}); err != nil {
		t.Fatalf("third assert (new git_sha): %v", err)
	}
	if n := appManifestSnapshotCount(t, pool, appID); n != 2 {
		t.Fatalf("expected 2 snapshots after a genuinely new commit, got %d", n)
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

// TestRecordArtifact_RejectsArchivedOwner_Postgres is the OTHER AR-7c exit
// criterion: "RecordArtifact against an ARCHIVED owner is rejected too" --
// before AR-7c this succeeded silently.
func TestRecordArtifact_RejectsArchivedOwner_Postgres(t *testing.T) {
	reg, pool := newTestRegistry(t)
	ctx := authedCtx()
	appSrv := handlers.NewAppServer(reg)
	artSrv := handlers.NewArtifactServer(reg)

	created, err := appSrv.ReconcileApps(ctx, &pb.ReconcileAppsRequest{
		Manifests:      reconcileManifests("sha-arch-owner-1", 100, 100, []*appmetapb.AppManifest{oneAppManifest("acme", "archowner")}, nil),
		IdempotencyKey: "arch-owner-pg-1",
	})
	if err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	appID := created.CreatedApps[0].AppId

	if _, err := appSrv.ReconcileApps(ctx, &pb.ReconcileAppsRequest{
		Manifests:      reconcileManifests("sha-arch-owner-2", 200, 200, nil, nil),
		IdempotencyKey: "arch-owner-pg-2",
	}); err != nil {
		t.Fatalf("reconcile drop: %v", err)
	}
	if _, err := appSrv.SetAppStatus(ctx, &pb.SetAppStatusRequest{
		AppId: appID, Status: pb.AppStatus_APP_STATUS_ARCHIVED, Reason: "gone",
	}); err != nil {
		t.Fatalf("archive: %v", err)
	}

	buildID := seedBuild(t, pool, "run-arch-owner")
	_, err = artSrv.RecordArtifact(ctx, &pb.RecordArtifactRequest{
		BuildId: buildID, Kind: pb.ArtifactKind_ARTIFACT_KIND_IMAGE,
		OwnerFullName: "acme-archowner", Version: "v1.0.0", Digest: "sha256:archowner1",
		IdempotencyKey: "arch-owner-artifact-1",
	})
	if err == nil {
		t.Fatal("expected RecordArtifact against an ARCHIVED owner to be rejected")
	}
	st, _ := status.FromError(err)
	if st.Code() != codes.FailedPrecondition {
		t.Fatalf("expected FailedPrecondition, got %v (%v)", st.Code(), err)
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
	// no chart here) and its ONLY manifest snapshot is the AssertApps one
	// at sha-unmerged-1, never superseded by a 'sweep' snapshot.
	appID, err := reg.Apps().GetAppByFullName(ctx, "acme-unmerged-branch-app")
	if err != nil {
		t.Fatalf("get app: %v", err)
	}
	if n := appManifestSnapshotCount(t, pool, appID.AppID); n != 1 {
		t.Fatalf("expected exactly 1 manifest snapshot (the AssertApps one), got %d", n)
	}
}

// TestRecordArtifact_PromotabilityIsNotRetroactive_Postgres is PLAN.md's
// central AR-7c exit criterion against real Postgres: editing an app's
// deploy_unit after an artifact was published must not change that
// artifact's promotability. Mirrors
// handlers.TestRecordArtifact_PromotabilityIsNotRetroactive but exercised
// through the real scanArtifact/artifact.promotability column, not the fake.
func TestRecordArtifact_PromotabilityIsNotRetroactive_Postgres(t *testing.T) {
	reg, pool := newTestRegistry(t)
	ctx := authedCtx()
	appSrv := handlers.NewAppServer(reg)
	artSrv := handlers.NewArtifactServer(reg)

	created, err := appSrv.ReconcileApps(ctx, &pb.ReconcileAppsRequest{
		Manifests: reconcileManifests("sha-retro-1", 100, 100,
			[]*appmetapb.AppManifest{{Domain: "acme", Name: "retro-app", DeployUnit: appmetapb.DeployUnit_DEPLOY_UNIT_IMAGE}}, nil),
		IdempotencyKey: "retro-pg-1",
	})
	if err != nil {
		t.Fatalf("reconcile create: %v", err)
	}
	_ = created

	buildID := seedBuild(t, pool, "run-retro-pg")
	published, err := artSrv.RecordArtifact(ctx, &pb.RecordArtifactRequest{
		BuildId: buildID, Kind: pb.ArtifactKind_ARTIFACT_KIND_IMAGE,
		OwnerFullName: "acme-retro-app", Version: "v1.0.0", Digest: "sha256:retropg1",
		IdempotencyKey: "retro-pg-artifact-1",
	})
	if err != nil {
		t.Fatalf("RecordArtifact: %v", err)
	}
	if published.Artifact.Promotability != pb.Promotability_PROMOTABILITY_PROMOTABLE {
		t.Fatalf("expected PROMOTABLE at publish time, got %v", published.Artifact.Promotability)
	}

	// Edit deploy_unit to CHART via a later reconcile -- a real
	// release_app.bzl change reaching main.
	if _, err := appSrv.ReconcileApps(ctx, &pb.ReconcileAppsRequest{
		Manifests: reconcileManifests("sha-retro-2", 200, 200,
			[]*appmetapb.AppManifest{{Domain: "acme", Name: "retro-app", DeployUnit: appmetapb.DeployUnit_DEPLOY_UNIT_CHART}}, nil),
		IdempotencyKey: "retro-pg-2",
	}); err != nil {
		t.Fatalf("reconcile with edited deploy_unit: %v", err)
	}

	// The stored artifact.promotability column, read directly, must be
	// unaffected.
	var storedPromotability string
	if err := pool.QueryRow(ctx, `SELECT promotability FROM artifact WHERE artifact_id = $1`, published.Artifact.ArtifactId).Scan(&storedPromotability); err != nil {
		t.Fatalf("read stored promotability: %v", err)
	}
	if storedPromotability != "promotable" {
		t.Fatalf("retroactivity bug: expected the stored artifact.promotability column to STAY 'promotable' after image-app's deploy_unit changed, got %q", storedPromotability)
	}

	// GetArtifact (the read path) must agree.
	reread, err := artSrv.GetArtifact(ctx, &pb.GetArtifactRequest{ArtifactId: published.Artifact.ArtifactId})
	if err != nil {
		t.Fatalf("GetArtifact: %v", err)
	}
	if reread.Artifact.Promotability != pb.Promotability_PROMOTABILITY_PROMOTABLE {
		t.Fatalf("retroactivity bug via GetArtifact: expected PROMOTABLE, got %v", reread.Artifact.Promotability)
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

	if err := runner.Up(); err != nil {
		t.Fatalf("apply migration 008: %v", err)
	}

	// Exactly one snapshot each, attributed to the watermark's git_sha.
	if n := appManifestSnapshotCount(t, db.Pool, appID); n != 1 {
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

// ============================================================================
// AR-7d (issue #558): the run log -- GetBuildByWorkflowRun and the
// BuildID-filtered ListArtifacts query GetReleaseRun is built from.
// ============================================================================

// seedBuildAttempt is seedBuild with an explicit workflow_attempt, for the
// latest-attempt tests below (a re-run shares workflow_run_id but gets a
// new attempt).
func seedBuildAttempt(t *testing.T, pool *pgxpool.Pool, workflowRunID string, attempt int32) string {
	t.Helper()
	var buildID string
	err := pool.QueryRow(context.Background(), `
		INSERT INTO build (git_sha, workflow_run_id, workflow_attempt) VALUES ('deadbeef', $1, $2)
		RETURNING build_id`, workflowRunID, attempt).Scan(&buildID)
	if err != nil {
		t.Fatalf("seed build %s attempt %d: %v", workflowRunID, attempt, err)
	}
	return buildID
}

// TestGetBuildByWorkflowRun_LatestAttemptByDefault proves attempt == 0
// resolves to the highest workflow_attempt recorded for a run id against
// real Postgres (not just server/repository/fake's map iteration, which
// needed the ORDER BY ... DESC LIMIT 1 to be real, not incidental), and
// that an explicit attempt still selects exactly that one.
func TestGetBuildByWorkflowRun_LatestAttemptByDefault(t *testing.T) {
	reg, pool := newTestRegistry(t)
	build1 := seedBuildAttempt(t, pool, "run-attempts-pg", 1)
	build2 := seedBuildAttempt(t, pool, "run-attempts-pg", 2)
	build3 := seedBuildAttempt(t, pool, "run-attempts-pg", 3)

	latest, err := reg.Builds().GetBuildByWorkflowRun(context.Background(), "run-attempts-pg", 0)
	if err != nil {
		t.Fatalf("GetBuildByWorkflowRun (attempt=0): %v", err)
	}
	if latest.BuildID != build3 {
		t.Fatalf("expected the highest attempt (3)'s build, got %s want %s", latest.BuildID, build3)
	}

	exact, err := reg.Builds().GetBuildByWorkflowRun(context.Background(), "run-attempts-pg", 1)
	if err != nil {
		t.Fatalf("GetBuildByWorkflowRun (attempt=1): %v", err)
	}
	if exact.BuildID != build1 {
		t.Fatalf("expected attempt 1's own build, got %s want %s", exact.BuildID, build1)
	}

	middle, err := reg.Builds().GetBuildByWorkflowRun(context.Background(), "run-attempts-pg", 2)
	if err != nil {
		t.Fatalf("GetBuildByWorkflowRun (attempt=2): %v", err)
	}
	if middle.BuildID != build2 {
		t.Fatalf("expected attempt 2's own build, got %s want %s", middle.BuildID, build2)
	}

	_, err = reg.Builds().GetBuildByWorkflowRun(context.Background(), "run-does-not-exist-pg", 0)
	if !errors.Is(err, repository.ErrNotFound) {
		t.Fatalf("expected ErrNotFound for an unrecorded run id, got %v", err)
	}
}

// TestListArtifacts_BuildIDFilter_AcrossAllFourStates proves the
// BuildID-filtered ListArtifacts query GetReleaseRun is built from returns
// exactly the artifacts that carry this build's build_id -- publishing,
// published, and failed all show up; a genuinely unrelated "allocated" row
// (which structurally can never carry a build_id -- artifact_state_shape,
// migration 007) is correctly excluded, not silently mis-attributed to
// this build.
func TestListArtifacts_BuildIDFilter_AcrossAllFourStates(t *testing.T) {
	reg, pool := newTestRegistry(t)
	ctx := context.Background()
	appAllocated := seedApp(t, pool, "acme", "run-log-allocated", "image")
	appPublishing := seedApp(t, pool, "acme", "run-log-publishing", "image")
	appPublished := seedApp(t, pool, "acme", "run-log-published", "image")
	appFailed := seedApp(t, pool, "acme", "run-log-failed", "image")
	buildID := seedBuild(t, pool, "run-log-states")
	setDomainAdoptionStage(t, pool, "acme", "allocate")

	// allocated -- NOT tied to this (or any) build; must not appear below.
	if _, err := allocateVersionTx(t, reg, repository.ArtifactKindImage, appAllocated, "ghcr.io/acme/run-log-allocated", "patch", ""); err != nil {
		t.Fatalf("AllocateVersion: %v", err)
	}

	// publishing.
	if _, err := beginPublishTx(t, reg, repository.ArtifactKindImage, appPublishing, "v1.0.0", buildID, "ghcr.io/acme/run-log-publishing", repository.VersionSourceTag); err != nil {
		t.Fatalf("BeginPublish (publishing): %v", err)
	}

	// published.
	if _, _, err := recordArtifactTx(t, reg, repository.Artifact{
		Kind: repository.ArtifactKindImage, AppID: appPublished,
		Repository: "ghcr.io/acme/run-log-published", Version: "v1.0.0",
		Digest: "sha256:run-log-published", BuildID: buildID,
	}, nil); err != nil {
		t.Fatalf("RecordArtifact (published): %v", err)
	}

	// failed.
	if _, err := beginPublishTx(t, reg, repository.ArtifactKindImage, appFailed, "v1.0.0", buildID, "ghcr.io/acme/run-log-failed", repository.VersionSourceTag); err != nil {
		t.Fatalf("BeginPublish before fail: %v", err)
	}
	if _, err := failPublishTx(t, reg, repository.ArtifactKindImage, appFailed, "v1.0.0", "push failed"); err != nil {
		t.Fatalf("FailPublish: %v", err)
	}

	artifacts, err := reg.Artifacts().ListArtifacts(ctx, repository.ArtifactListFilter{BuildID: buildID})
	if err != nil {
		t.Fatalf("ListArtifacts(BuildID=...): %v", err)
	}
	if len(artifacts) != 3 {
		t.Fatalf("expected exactly 3 artifacts tied to this build (publishing/published/failed), got %d: %+v", len(artifacts), artifacts)
	}
	states := map[repository.ArtifactState]int{}
	for _, a := range artifacts {
		if a.BuildID != buildID {
			t.Fatalf("expected every returned artifact to carry this build's id, got %q on %s", a.BuildID, a.ArtifactID)
		}
		states[a.State]++
	}
	if states[repository.ArtifactStatePublishing] != 1 || states[repository.ArtifactStatePublished] != 1 || states[repository.ArtifactStateFailed] != 1 {
		t.Fatalf("expected one artifact in each of publishing/published/failed, got %v", states)
	}
	if states[repository.ArtifactStateAllocated] != 0 {
		t.Fatalf("expected the unrelated allocated row to be excluded, got %d", states[repository.ArtifactStateAllocated])
	}
}

// TestGetReleaseRun_Postgres_AppNeverReachedStillReportsIncomplete is the
// full run-log path (BuildRepository.GetBuildByWorkflowRun +
// ArtifactRepository.ListArtifacts(BuildID=...)) against real Postgres,
// proving AR-7d's second exit criterion (PLAN.md): a run killed BEFORE
// reaching an app still reports that app as incomplete. Mirrors
// server/handlers/artifact_test.go's fake-backed
// TestGetReleaseRun_AppNeverReachedStillReportsIncomplete, here against the
// real schema/CHECK constraints instead of the in-memory fake.
func TestGetReleaseRun_Postgres_AppNeverReachedStillReportsIncomplete(t *testing.T) {
	reg, pool := newTestRegistry(t)
	ctx := context.Background()
	appOne := seedApp(t, pool, "acme", "killed-early-one", "image")
	appTwo := seedApp(t, pool, "acme", "killed-early-two", "image")
	buildID := seedBuild(t, pool, "run-killed-early-pg")

	// BeginPublishBatch's up-front intent write, simulated directly at the
	// repository layer: both targets get a "publishing" row before either
	// leg's own push.
	if _, err := beginPublishTx(t, reg, repository.ArtifactKindImage, appOne, "v1.0.0", buildID, "ghcr.io/acme/killed-early-one", repository.VersionSourceTag); err != nil {
		t.Fatalf("BeginPublish app-one (batch intent): %v", err)
	}
	if _, err := beginPublishTx(t, reg, repository.ArtifactKindImage, appTwo, "v1.0.0", buildID, "ghcr.io/acme/killed-early-two", repository.VersionSourceTag); err != nil {
		t.Fatalf("BeginPublish app-two (batch intent): %v", err)
	}

	// Only app-one's matrix leg ever "ran". app-two's never starts -- no
	// further call is made for it, simulating a run killed before that leg
	// was scheduled.
	if _, _, err := recordArtifactTx(t, reg, repository.Artifact{
		Kind: repository.ArtifactKindImage, AppID: appOne,
		Repository: "ghcr.io/acme/killed-early-one", Version: "v1.0.0",
		Digest: "sha256:killed-early-one", BuildID: buildID,
	}, nil); err != nil {
		t.Fatalf("RecordArtifact app-one: %v", err)
	}

	build, err := reg.Builds().GetBuildByWorkflowRun(ctx, "run-killed-early-pg", 0)
	if err != nil {
		t.Fatalf("GetBuildByWorkflowRun: %v", err)
	}
	artifacts, err := reg.Artifacts().ListArtifacts(ctx, repository.ArtifactListFilter{BuildID: build.BuildID})
	if err != nil {
		t.Fatalf("ListArtifacts(BuildID=...): %v", err)
	}
	if len(artifacts) != 2 {
		t.Fatalf("expected app-two's never-started leg to still appear as a child artifact, got %d", len(artifacts))
	}
	var published, publishing int
	for _, a := range artifacts {
		switch a.State {
		case repository.ArtifactStatePublished:
			published++
		case repository.ArtifactStatePublishing:
			publishing++
		}
	}
	if published != 1 || publishing != 1 {
		t.Fatalf("expected one published and one still-publishing (incomplete) child, got published=%d publishing=%d", published, publishing)
	}
}

// ============================================================================
// AR-7d follow-up: the reaping hazard BeginPublishBatch's plan-time write
// introduces, and the publishing -> publishing heartbeat that closes it.
//
// BeginPublishBatch stamps state_changed_at for EVERY run target at plan
// time, before the release matrix fans out -- see ARCHITECTURE.md "The run
// log" -> "As built (AR-7d)". Without the per-leg BeginPublish call
// re-arming that clock immediately before each target's own push, a target
// whose leg is still queued when the reaper's timeout elapses would be
// reaped to "failed" before it ever runs -- and if the leg then went ahead
// and pushed anyway, RecordArtifact would reject the completion
// (postgres/artifact.go's `default: // allocated, failed` branch) AFTER an
// already-successful GHCR push, which is exactly the failure mode ordering
// 3 and the rest of AR-7 exist to prevent.
// ============================================================================

// readStateChangedAt reads one artifact's raw state_changed_at directly --
// full TIMESTAMPTZ precision, unlike the wire Artifact.StateChangedAt
// (int64 seconds), needed to prove a heartbeat actually advanced the clock
// within a fast-running test.
func readStateChangedAt(t *testing.T, pool *pgxpool.Pool, artifactID string) time.Time {
	t.Helper()
	var ts time.Time
	if err := pool.QueryRow(context.Background(), `SELECT state_changed_at FROM artifact WHERE artifact_id = $1`, artifactID).Scan(&ts); err != nil {
		t.Fatalf("read state_changed_at for %s: %v", artifactID, err)
	}
	return ts
}

// TestBeginPublish_Heartbeat_Postgres proves publishing -> publishing
// against real Postgres: a repeat BeginPublish call against an
// already-"publishing" row is a legal, idempotent heartbeat that advances
// state_changed_at (and re-stamps build_id) without changing state or
// tripping artifact_state_shape (migration 007's CHECK constraint, which
// still requires digest IS NULL / build_id IS NOT NULL for "publishing").
func TestBeginPublish_Heartbeat_Postgres(t *testing.T) {
	reg, pool := newTestRegistry(t)
	appID := seedApp(t, pool, "acme", "heartbeat", "image")
	buildA := seedBuild(t, pool, "run-heartbeat-pg-a")

	first, err := beginPublishTx(t, reg, repository.ArtifactKindImage, appID, "v1.0.0", buildA, "ghcr.io/acme/heartbeat", repository.VersionSourceTag)
	if err != nil {
		t.Fatalf("BeginPublish (∅ -> publishing): %v", err)
	}
	if first.State != repository.ArtifactStatePublishing {
		t.Fatalf("expected state publishing, got %q", first.State)
	}

	// Backdate so a naive comparison against "now" can't accidentally pass --
	// the heartbeat must move state_changed_at forward from this, not just
	// leave it close to what it already was.
	backdateStateChangedAt(t, pool, first.ArtifactID, time.Hour)
	backdated := readStateChangedAt(t, pool, first.ArtifactID)

	buildB := seedBuildAttempt(t, pool, "run-heartbeat-pg-a", 2)
	second, err := beginPublishTx(t, reg, repository.ArtifactKindImage, appID, "v1.0.0", buildB, "ghcr.io/acme/heartbeat", repository.VersionSourceTag)
	if err != nil {
		t.Fatalf("expected publishing -> publishing to be a legal heartbeat, got error: %v", err)
	}
	if second.ArtifactID != first.ArtifactID {
		t.Fatalf("expected the heartbeat to touch the SAME row, got %s vs %s", second.ArtifactID, first.ArtifactID)
	}
	if second.State != repository.ArtifactStatePublishing {
		t.Fatalf("expected state to remain publishing, got %q", second.State)
	}
	if second.BuildID != buildB {
		t.Fatalf("expected the heartbeat to re-stamp build_id to the new attempt's build, got %q want %q", second.BuildID, buildB)
	}
	afterHeartbeat := readStateChangedAt(t, pool, second.ArtifactID)
	if !afterHeartbeat.After(backdated) {
		t.Fatalf("expected state_changed_at to advance past the backdated value: backdated=%v after=%v", backdated, afterHeartbeat)
	}
	if state, _, hasDigest, hasBuildID := artifactStateRow(t, pool, second.ArtifactID); state != "publishing" || hasDigest || !hasBuildID {
		t.Fatalf("expected a shape-valid publishing row (no digest, has build_id) after the heartbeat, got state=%s hasDigest=%v hasBuildID=%v", state, hasDigest, hasBuildID)
	}
}

// TestBeginPublish_ReapThenRevive_Postgres is AR-7d's reaping-hazard fix,
// proven end to end against real Postgres: a target declared by
// BeginPublishBatch at plan time, reaped to "failed" by the stale-row
// reaper while its own matrix leg was still queued, is revived by that
// leg's own BeginPublish call (the already-legal failed -> publishing
// transition) immediately before its push, and completes normally to
// "published" via RecordArtifact. Proves the specific hazard the
// coordinator flagged does not manifest: a reaped row does NOT make
// RecordArtifact reject the completion after a real push has already
// happened.
func TestBeginPublish_ReapThenRevive_Postgres(t *testing.T) {
	reg, pool := newTestRegistry(t)
	appID := seedApp(t, pool, "acme", "reap-revive", "image")
	buildID := seedBuild(t, pool, "run-reap-revive-pg")

	// 1. BeginPublishBatch's plan-time write: ∅ -> publishing, before the
	// matrix fans out.
	intent, err := beginPublishTx(t, reg, repository.ArtifactKindImage, appID, "v1.0.0", buildID, "ghcr.io/acme/reap-revive", repository.VersionSourceTag)
	if err != nil {
		t.Fatalf("BeginPublishBatch's plan-time write (∅ -> publishing): %v", err)
	}

	// 2. This target's own matrix leg is still queued when the reaper's
	// timeout elapses -- backdate past a 30-minute timeout and sweep.
	backdateStateChangedAt(t, pool, intent.ArtifactID, time.Hour)
	n, err := reg.Artifacts().ExpireStale(context.Background(), 30*time.Minute)
	if err != nil {
		t.Fatalf("ExpireStale: %v", err)
	}
	if n != 1 {
		t.Fatalf("expected exactly 1 row expired, got %d", n)
	}
	if state, reason, _, _ := artifactStateRow(t, pool, intent.ArtifactID); state != "failed" || reason != "stale" {
		t.Fatalf("expected the row to be reaped to failed/stale before the leg ever ran, got state=%s reason=%s", state, reason)
	}

	// 3. The leg finally runs. Its own per-leg BeginPublish call revives
	// the row (failed -> publishing) immediately before the push --
	// exactly what release.yml's restored per-leg step does.
	revived, err := beginPublishTx(t, reg, repository.ArtifactKindImage, appID, "v1.0.0", buildID, "ghcr.io/acme/reap-revive", repository.VersionSourceTag)
	if err != nil {
		t.Fatalf("expected failed -> publishing revival to succeed, got error: %v", err)
	}
	if revived.ArtifactID != intent.ArtifactID {
		t.Fatalf("expected revival to touch the SAME row, got %s vs %s", revived.ArtifactID, intent.ArtifactID)
	}
	if revived.State != repository.ArtifactStatePublishing {
		t.Fatalf("expected state publishing after revival, got %q", revived.State)
	}
	if revived.FailReason != "" {
		t.Fatalf("expected fail_reason cleared after revival, got %q", revived.FailReason)
	}

	// 4. The push actually happens (simulated), and RecordArtifact
	// completes it -- this MUST succeed. Before this fix, the row would
	// still be "failed" at this point and RecordArtifact's `default:
	// // allocated, failed` branch would reject it with
	// FailedPrecondition, after a real image was already pushed to GHCR.
	published, _, err := recordArtifactTx(t, reg, repository.Artifact{
		Kind: repository.ArtifactKindImage, AppID: appID,
		Repository: "ghcr.io/acme/reap-revive", Version: "v1.0.0",
		Digest: "sha256:reap-revive", BuildID: buildID,
	}, nil)
	if err != nil {
		t.Fatalf("RecordArtifact after reap-then-revive: %v", err)
	}
	if published.State != repository.ArtifactStatePublished {
		t.Fatalf("expected state published, got %q", published.State)
	}
	if published.Digest != "sha256:reap-revive" {
		t.Fatalf("expected digest stamped, got %q", published.Digest)
	}
}

// ============================================================================
// AdoptArtifact (AR-7e, issue #558)
// ============================================================================

// adoptArtifactTx runs Artifacts().AdoptArtifact inside a real WithTx
// transaction, exactly as handlers.ArtifactServer.AdoptArtifact does in
// production.
func adoptArtifactTx(t *testing.T, reg *Registry, a repository.Artifact, contains []repository.ContainedImageInput, reason, actor string) (*repository.Artifact, bool, error) {
	t.Helper()
	var out *repository.Artifact
	var already bool
	err := reg.WithTx(context.Background(), func(ctx context.Context, r repository.Registry) error {
		var ferr error
		out, already, ferr = r.Artifacts().AdoptArtifact(ctx, a, contains, reason, actor)
		return ferr
	})
	return out, already, err
}

// TestAdoptArtifact_NewRow_CreatesSyntheticBuild_Postgres is AdoptArtifact's
// primary case against real Postgres: no row exists for (owner, kind,
// version) yet, so a synthetic `build` row is created to satisfy
// artifact.build_id's real foreign key and migration 007's
// artifact_state_shape CHECK (build_id NOT NULL once "published"). Proves
// the synthetic row's shape: a non-numeric workflow_run_id that can never
// collide with a real GitHub Actions run id, and git_ref "adopted" as the
// same at-a-glance marker.
func TestAdoptArtifact_NewRow_CreatesSyntheticBuild_Postgres(t *testing.T) {
	reg, pool := newTestRegistry(t)
	appID := seedApp(t, pool, "acme", "adopt-new", "image")

	adopted, alreadyRecorded, err := adoptArtifactTx(t, reg, repository.Artifact{
		Kind: repository.ArtifactKindImage, AppID: appID,
		Repository: "ghcr.io/acme/adopt-new", Version: "v0.9.0", Digest: "sha256:adopt-new-pg",
	}, nil, "pre-dates the registry", "admin@example.com")
	if err != nil {
		t.Fatalf("AdoptArtifact (∅ -> published): %v", err)
	}
	if alreadyRecorded {
		t.Fatal("expected alreadyRecorded=false for a brand-new adoption")
	}
	if adopted.State != repository.ArtifactStatePublished {
		t.Fatalf("expected state published, got %q", adopted.State)
	}
	if adopted.Provenance != repository.ArtifactProvenanceAdopted {
		t.Fatalf("expected provenance adopted, got %q", adopted.Provenance)
	}
	if adopted.BuildID == "" {
		t.Fatal("expected a build_id to be stamped")
	}

	var gitRef, workflowRunID, actor string
	if err := pool.QueryRow(context.Background(),
		`SELECT git_ref, workflow_run_id, actor FROM build WHERE build_id = $1`, adopted.BuildID,
	).Scan(&gitRef, &workflowRunID, &actor); err != nil {
		t.Fatalf("read synthetic build row: %v", err)
	}
	if gitRef != "adopted" {
		t.Fatalf("expected synthetic build's git_ref = %q, got %q", "adopted", gitRef)
	}
	if !strings.HasPrefix(workflowRunID, "adopted:") {
		t.Fatalf("expected synthetic build's workflow_run_id to start with %q, got %q", "adopted:", workflowRunID)
	}
	if actor != "admin@example.com" {
		t.Fatalf("expected synthetic build's actor to be the calling admin, got %q", actor)
	}
}

// TestAdoptArtifact_UnblocksChartPin_Postgres is PLAN.md's AR-7e exit
// criterion against real Postgres, exercised through the handler layer end
// to end: a chart record fails on an unrecorded pin (simulating an image
// published before the registry existed), and adopting the image unblocks
// the SAME chart record -- one documented, audited command.
func TestAdoptArtifact_UnblocksChartPin_Postgres(t *testing.T) {
	reg, _ := newTestRegistry(t)
	ctx := authedCtx()
	appSrv := handlers.NewAppServer(reg)
	artSrv := handlers.NewArtifactServer(reg)

	if _, err := appSrv.ReconcileApps(ctx, &pb.ReconcileAppsRequest{
		Manifests: reconcileManifests("sha-adopt-unblock", 100, 100,
			[]*appmetapb.AppManifest{oneAppManifest("acme", "adopt-unblock-app")},
			[]*appmetapb.ChartManifest{{Domain: "acme", Name: "adopt-unblock-chart", Apps: []string{"adopt-unblock-app"}}}),
		IdempotencyKey: "adopt-unblock-reconcile",
	}); err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	build, err := artSrv.RecordBuild(ctx, &pb.RecordBuildRequest{
		GitSha: "sha-adopt-unblock", WorkflowRunId: "run-adopt-unblock-pg", IdempotencyKey: "adopt-unblock-build",
	})
	if err != nil {
		t.Fatalf("record build: %v", err)
	}

	// Fails first: the pinned image was never recorded.
	_, err = artSrv.RecordArtifact(ctx, &pb.RecordArtifactRequest{
		BuildId: build.Build.BuildId, Kind: pb.ArtifactKind_ARTIFACT_KIND_CHART,
		OwnerFullName: "acme-adopt-unblock-chart", Digest: "sha256:adopt-unblock-chart-pg", Version: "v1.0.0",
		Contains: []*pb.ContainedImage{
			{AppFullName: "acme-adopt-unblock-app", Repository: "ghcr.io/acme/adopt-unblock-app", Version: "v0.9.0", Digest: "sha256:adopt-unblock-image-pg"},
		},
		IdempotencyKey: "adopt-unblock-chart-1",
	})
	if err == nil || status.Code(err) != codes.InvalidArgument {
		t.Fatalf("expected the chart record to reject the unrecorded pin first, got %v", err)
	}

	// Adopt the pre-registry image.
	adoptResp, err := artSrv.AdoptArtifact(ctx, &pb.AdoptArtifactRequest{
		Kind: pb.ArtifactKind_ARTIFACT_KIND_IMAGE, OwnerFullName: "acme-adopt-unblock-app",
		Repository: "ghcr.io/acme/adopt-unblock-app", Version: "v0.9.0", Digest: "sha256:adopt-unblock-image-pg",
		Reason: "pre-dates the registry", IdempotencyKey: "adopt-unblock-adopt-1",
	})
	if err != nil {
		t.Fatalf("AdoptArtifact: %v", err)
	}
	if adoptResp.Artifact.Provenance != pb.ArtifactProvenance_ARTIFACT_PROVENANCE_ADOPTED {
		t.Fatalf("expected ADOPTED provenance, got %v", adoptResp.Artifact.Provenance)
	}

	// The SAME chart record now succeeds.
	chartResp, err := artSrv.RecordArtifact(ctx, &pb.RecordArtifactRequest{
		BuildId: build.Build.BuildId, Kind: pb.ArtifactKind_ARTIFACT_KIND_CHART,
		OwnerFullName: "acme-adopt-unblock-chart", Digest: "sha256:adopt-unblock-chart-pg", Version: "v1.0.0",
		Contains: []*pb.ContainedImage{
			{AppFullName: "acme-adopt-unblock-app", Repository: "ghcr.io/acme/adopt-unblock-app", Version: "v0.9.0", Digest: "sha256:adopt-unblock-image-pg"},
		},
		IdempotencyKey: "adopt-unblock-chart-2",
	})
	if err != nil {
		t.Fatalf("RecordArtifact after adoption should succeed: %v", err)
	}
	if len(chartResp.Artifact.Contains) != 1 || chartResp.Artifact.Contains[0].Digest != "sha256:adopt-unblock-image-pg" {
		t.Fatalf("expected the chart to pin the adopted image, got %+v", chartResp.Artifact.Contains)
	}

	// "distinguishable in one query": the image is ADOPTED, the chart is
	// OBSERVED, in one ListArtifacts(Provenance=ADOPTED) call.
	adoptedOnly, err := reg.Artifacts().ListArtifacts(context.Background(), repository.ArtifactListFilter{Provenance: repository.ArtifactProvenanceAdopted})
	if err != nil {
		t.Fatalf("ListArtifacts(Provenance=adopted): %v", err)
	}
	if len(adoptedOnly) != 1 || adoptedOnly[0].Digest != "sha256:adopt-unblock-image-pg" {
		t.Fatalf("expected exactly the adopted image, got %+v", adoptedOnly)
	}
}

// TestAdoptArtifact_NeverDowngradesObservedProvenance_Postgres is the
// critical invariant against real Postgres: adopting a digest that turns
// out to already be an "observed" row must be an idempotent no-op, never a
// rewrite.
func TestAdoptArtifact_NeverDowngradesObservedProvenance_Postgres(t *testing.T) {
	reg, pool := newTestRegistry(t)
	appID := seedApp(t, pool, "acme", "adopt-no-downgrade", "image")
	buildID := seedBuild(t, pool, "run-adopt-no-downgrade")

	recorded, _, err := recordArtifactTx(t, reg, repository.Artifact{
		Kind: repository.ArtifactKindImage, AppID: appID,
		Repository: "ghcr.io/acme/adopt-no-downgrade", Version: "v1.0.0",
		Digest: "sha256:observed-not-downgraded-pg", BuildID: buildID,
	}, nil)
	if err != nil {
		t.Fatalf("seed observed artifact: %v", err)
	}
	if recorded.Provenance != repository.ArtifactProvenanceObserved {
		t.Fatalf("expected provenance observed, got %q", recorded.Provenance)
	}

	adopted, alreadyRecorded, err := adoptArtifactTx(t, reg, repository.Artifact{
		Kind: repository.ArtifactKindImage, AppID: appID,
		Repository: "ghcr.io/acme/adopt-no-downgrade", Version: "v1.0.0",
		Digest: "sha256:observed-not-downgraded-pg",
	}, nil, "operator mistake -- already observed", "admin@example.com")
	if err != nil {
		t.Fatalf("AdoptArtifact against an already-observed digest should be an idempotent no-op, got %v", err)
	}
	if !alreadyRecorded {
		t.Fatal("expected alreadyRecorded=true")
	}
	if adopted.Provenance != repository.ArtifactProvenanceObserved {
		t.Fatalf("expected provenance to STAY observed (never downgraded to adopted), got %q", adopted.Provenance)
	}
	if adopted.BuildID != buildID {
		t.Fatalf("expected the original build_id to be untouched, got %q vs %q", adopted.BuildID, buildID)
	}
}

// TestAdoptArtifact_DifferentDigestConflict_Postgres proves adoption cannot
// silently overwrite an already-published version with a different digest.
func TestAdoptArtifact_DifferentDigestConflict_Postgres(t *testing.T) {
	reg, pool := newTestRegistry(t)
	appID := seedApp(t, pool, "acme", "adopt-conflict", "image")
	buildID := seedBuild(t, pool, "run-adopt-conflict")

	if _, _, err := recordArtifactTx(t, reg, repository.Artifact{
		Kind: repository.ArtifactKindImage, AppID: appID,
		Repository: "ghcr.io/acme/adopt-conflict", Version: "v1.0.0",
		Digest: "sha256:adopt-conflict-original", BuildID: buildID,
	}, nil); err != nil {
		t.Fatalf("seed: %v", err)
	}

	_, _, err := adoptArtifactTx(t, reg, repository.Artifact{
		Kind: repository.ArtifactKindImage, AppID: appID,
		Repository: "ghcr.io/acme/adopt-conflict", Version: "v1.0.0",
		Digest: "sha256:adopt-conflict-different",
	}, nil, "trying to overwrite", "admin@example.com")
	if !errors.Is(err, repository.ErrAlreadyExists) {
		t.Fatalf("expected ErrAlreadyExists, got %v", err)
	}
}

// TestAdoptArtifact_RejectsLiveStateRows_Postgres proves adoption rejects
// both an "allocated" row (a live version reservation) and a "publishing"
// row (a live in-flight publish) -- adoption is for when there is NO row,
// or a "failed" one, never a race against something still live.
func TestAdoptArtifact_RejectsLiveStateRows_Postgres(t *testing.T) {
	reg, pool := newTestRegistry(t)
	appID := seedApp(t, pool, "acme", "adopt-live", "image")
	buildID := seedBuild(t, pool, "run-adopt-live")

	t.Run("allocated", func(t *testing.T) {
		seedRawArtifact(t, pool, appID, repository.ArtifactStateAllocated, "v1.0.0", "")
		_, _, err := adoptArtifactTx(t, reg, repository.Artifact{
			Kind: repository.ArtifactKindImage, AppID: appID,
			Repository: "ghcr.io/acme/adopt-live", Version: "v1.0.0",
			Digest: "sha256:adopt-over-allocated-pg",
		}, nil, "should be rejected", "admin@example.com")
		if !errors.Is(err, repository.ErrFailedPrecondition) {
			t.Fatalf("expected ErrFailedPrecondition adopting over an allocated row, got %v", err)
		}
	})

	t.Run("publishing", func(t *testing.T) {
		seedRawArtifact(t, pool, appID, repository.ArtifactStatePublishing, "v2.0.0", buildID)
		_, _, err := adoptArtifactTx(t, reg, repository.Artifact{
			Kind: repository.ArtifactKindImage, AppID: appID,
			Repository: "ghcr.io/acme/adopt-live", Version: "v2.0.0",
			Digest: "sha256:adopt-over-publishing-pg",
		}, nil, "should be rejected", "admin@example.com")
		if !errors.Is(err, repository.ErrFailedPrecondition) {
			t.Fatalf("expected ErrFailedPrecondition adopting over a publishing row, got %v", err)
		}
	})
}

// TestAdoptArtifact_FailedRow_Postgres is the disaster-recovery case against
// real Postgres: a run already tried and failed (or was reaped), but the
// artifact demonstrably exists. Covers BOTH sub-cases of "does the failed
// row already carry a build_id": a row reaped from "publishing" does (that
// REAL build_id is reused); a row reaped from "allocated" does not (a
// synthetic one is minted, same as the ∅ -> published branch).
func TestAdoptArtifact_FailedRow_Postgres(t *testing.T) {
	reg, pool := newTestRegistry(t)
	appID := seedApp(t, pool, "acme", "adopt-failed", "image")
	buildID := seedBuild(t, pool, "run-adopt-failed")

	t.Run("reuses existing build_id when the failed row has one", func(t *testing.T) {
		seedRawArtifact(t, pool, appID, repository.ArtifactStateFailed, "v1.0.0", buildID)
		adopted, alreadyRecorded, err := adoptArtifactTx(t, reg, repository.Artifact{
			Kind: repository.ArtifactKindImage, AppID: appID,
			Repository: "ghcr.io/acme/adopt-failed", Version: "v1.0.0",
			Digest: "sha256:adopt-failed-reuse-pg",
		}, nil, "confirmed pushed despite recording failure", "admin@example.com")
		if err != nil {
			t.Fatalf("AdoptArtifact over a failed row with a build_id: %v", err)
		}
		if alreadyRecorded {
			t.Fatal("expected alreadyRecorded=false")
		}
		if adopted.State != repository.ArtifactStatePublished {
			t.Fatalf("expected state published, got %q", adopted.State)
		}
		if adopted.Provenance != repository.ArtifactProvenanceAdopted {
			t.Fatalf("expected provenance adopted, got %q", adopted.Provenance)
		}
		if adopted.BuildID != buildID {
			t.Fatalf("expected the REAL CI build_id %q to be reused (not a synthetic one), got %q", buildID, adopted.BuildID)
		}
	})

	t.Run("mints a synthetic build_id when the failed row has none", func(t *testing.T) {
		// A "failed" row reaped from "allocated" (not "publishing") never
		// had a build_id -- see ArtifactRepository.AdoptArtifact's doc
		// comment.
		seedRawArtifact(t, pool, appID, repository.ArtifactStateFailed, "v2.0.0", "")
		adopted, _, err := adoptArtifactTx(t, reg, repository.Artifact{
			Kind: repository.ArtifactKindImage, AppID: appID,
			Repository: "ghcr.io/acme/adopt-failed", Version: "v2.0.0",
			Digest: "sha256:adopt-failed-synthetic-pg",
		}, nil, "confirmed pushed despite recording failure", "admin@example.com")
		if err != nil {
			t.Fatalf("AdoptArtifact over a failed row with no build_id: %v", err)
		}
		if adopted.BuildID == "" {
			t.Fatal("expected a synthetic build_id to be stamped")
		}
		if adopted.BuildID == buildID {
			t.Fatal("expected a DIFFERENT (synthetic) build_id, not the other subtest's real one")
		}
		var gitRef string
		if err := pool.QueryRow(context.Background(), `SELECT git_ref FROM build WHERE build_id = $1`, adopted.BuildID).Scan(&gitRef); err != nil {
			t.Fatalf("read synthetic build row: %v", err)
		}
		if gitRef != "adopted" {
			t.Fatalf("expected synthetic build's git_ref = %q, got %q", "adopted", gitRef)
		}
	})
}
