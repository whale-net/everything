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
//
// This file (postgres_integration_helpers_test.go) holds the shared test
// fixtures and constructors used across the other postgres_integration_*
// split files (app/artifact/adoption/environment/promotion) -- see those
// files for the actual Test* functions, grouped by which repository
// (app.go / artifact.go / environment.go / promotion.go / writeback.go)
// they primarily exercise.
package postgres

import (
	"context"
	"database/sql"
	"fmt"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	_ "github.com/jackc/pgx/v5/stdlib" // database/sql driver for the migration runner

	"github.com/whale-net/everything/libs/go/dbtest"
	"github.com/whale-net/everything/libs/go/grpcauth"
	"github.com/whale-net/everything/libs/go/htmxauth"
	"github.com/whale-net/everything/libs/go/migrate"
	"github.com/whale-net/everything/tools/app_registry/migrate/schema"
	"github.com/whale-net/everything/tools/app_registry/server/auth"
	"github.com/whale-net/everything/tools/app_registry/server/repository"

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
// App Registry migrations plus htmxauth's bundled ui_sessions migration --
// the same sources the migration binary merges via migrate.WithSource, see
// migrate/main.go -- and returns a Registry plus the raw pool for fixture
// setup / assertions.
func newTestRegistry(t *testing.T) (*Registry, *pgxpool.Pool) {
	t.Helper()
	ctx := context.Background()

	db := dbtest.NewPostgres(ctx, t, dbtest.Options{})

	sqlDB, err := sql.Open("pgx", db.ConnString)
	if err != nil {
		t.Fatalf("open database/sql handle for migration runner: %v", err)
	}
	defer sqlDB.Close()

	runner, err := migrate.NewMultiRunner(sqlDB,
		migrate.Source{FS: schema.Migrations, Dir: schema.Dir},
		migrate.Source{FS: htmxauth.Migrations, Dir: "migrations"},
	)
	if err != nil {
		t.Fatalf("build migration runner: %v", err)
	}
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

// seedAppManifest writes one app_manifest CONTENT row plus an OPEN
// app_manifest_history interval directly (migration 010, AR-8), bypassing
// protojson -- a raw SQL fixture matching EXACTLY the key shape
// postgres/app.go's manifestJSONMarshal produces (UseProtoNames,
// EmitUnpopulated): snake_case keys, deploy_unit as the protojson enum NAME
// ("DEPLOY_UNIT_IMAGE" etc, matching the generated-column CASE expression),
// so tests can seed a specific (owner, git_sha) history entry directly --
// e.g. to test resolveManifestForPublish's exact-commit preference. appID
// is always freshly created by seedApp's caller, so there is never a
// pre-existing open interval to close here -- this is a straight open, not
// the full close-and-open compare-swap postgres/app.go's
// recordAppManifestSweep performs.
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
	ctx := context.Background()
	var contentID string
	if err := pool.QueryRow(ctx, `
		INSERT INTO app_manifest (owner_id, manifest_json)
		VALUES ($1, $2::jsonb)
		ON CONFLICT (owner_id, manifest_hash) DO UPDATE SET manifest_json = EXCLUDED.manifest_json
		RETURNING app_manifest_id`, appID, manifestJSON).Scan(&contentID); err != nil {
		t.Fatalf("seed app_manifest content for %s/%s: %v", domain, name, err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO app_manifest_history (owner_id, app_manifest_id, valid_from, first_git_sha, last_git_sha)
		VALUES ($1, $2, NOW(), $3, $3)`, appID, contentID, gitSHA); err != nil {
		t.Fatalf("seed app_manifest_history for %s/%s: %v", domain, name, err)
	}
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
// they do not open one themselves. RecordArtifact unconditionally requires
// a prior BeginPublish (there is no direct-create fallback), so this
// helper seeds one first, on a fresh
// synthetic build row. A BeginPublish failure here is swallowed rather than
// propagated: a caller reusing this helper for the SAME (owner, kind,
// version) a second time (replay/conflict tests) legitimately hits it
// again ("published" is ErrFailedPrecondition) -- RecordArtifact's own
// digest/identity lookup, not this helper, is what decides replay vs
// conflict in that case.
func recordArtifactTx(t *testing.T, reg *Registry, a repository.Artifact, contains []repository.ContainedImageInput) (*repository.Artifact, bool, error) {
	t.Helper()
	buildID := seedBuild(t, reg.pool, "record-artifact-tx-"+uuid.NewString())
	var out *repository.Artifact
	var already bool
	err := reg.WithTx(context.Background(), func(ctx context.Context, r repository.Registry) error {
		_, _ = r.Artifacts().BeginPublish(ctx, a.Kind, ownerIDOf(a), a.Version, buildID, a.Repository, repository.VersionSourceTag)
		var ferr error
		out, already, ferr = r.Artifacts().RecordArtifact(ctx, a, contains)
		return ferr
	})
	return out, already, err
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
