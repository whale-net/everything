//go:build integration

// Real-Postgres integration coverage for buildRepo and artifactRepo
// (artifact.go): RecordBuild/RecordArtifact, publish lifecycle
// (BeginPublish/FailPublish/ExpireStale), version allocation, chart
// artifact composition, ResolveArtifact/resolveManifestForPublish, and
// ListBuilds/ListArtifacts (including pagination). See
// postgres_integration_helpers_test.go's doc comment for why this package
// builds these files under the "integration" tag, and TESTING.md for how
// to run them.
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
	"github.com/jackc/pgx/v5/pgxpool"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/whale-net/everything/libs/go/dbtest"
	"github.com/whale-net/everything/libs/go/migrate"
	"github.com/whale-net/everything/tools/app_registry/migrate/schema"
	"github.com/whale-net/everything/tools/app_registry/server/handlers"
	"github.com/whale-net/everything/tools/app_registry/server/repository"

	pb "github.com/whale-net/everything/tools/app_registry/protos"
	appmetapb "github.com/whale-net/everything/tools/appmeta/proto"
)

// seedChart mirrors seedApp: pure identity plus one content row and one open
// chart_manifest_history interval, so any test that goes on to publish a
// chart artifact finds a CURRENT interval for resolveManifestForPublish to
// attribute it to -- without one, RecordArtifact/BeginPublish for a chart
// kind would fail with ErrFailedPrecondition ("no manifest recorded").
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
	var contentID string
	if err := pool.QueryRow(ctx, `
		INSERT INTO chart_manifest (owner_id, manifest_json)
		VALUES ($1, $2::jsonb)
		ON CONFLICT (owner_id, manifest_hash) DO UPDATE SET manifest_json = EXCLUDED.manifest_json
		RETURNING chart_manifest_id`, chartID, manifestJSON).Scan(&contentID); err != nil {
		t.Fatalf("seed chart_manifest content for %s/%s: %v", domain, name, err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO chart_manifest_history (owner_id, chart_manifest_id, valid_from, first_git_sha, last_git_sha)
		VALUES ($1, $2, NOW(), $3, $3)`, chartID, contentID, "seed-"+uuid.NewString()); err != nil {
		t.Fatalf("seed chart_manifest_history for %s/%s: %v", domain, name, err)
	}
	return chartID
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

// TestRecordArtifact_ReproducibleBuildSameDigestDifferentVersion is the
// regression test for issue #585: a Bazel reproducible build can produce a
// byte-identical image for two different versions of the SAME owner (no
// functional change between releases). Before the fix, RecordArtifact's
// step-1 replay lookup matched on digest ALONE, so the new version's
// RecordArtifact call silently matched the OLDER, already-published
// version's row and reported SUCCESS -- without ever touching the row
// BeginPublish created for the new version, leaving it stranded in
// "publishing" indefinitely while the CI job reported green.
//
// Scoping step 1 to the request's own (owner, kind, version) identity fixes
// the lie: a same-digest/different-version request no longer short-circuits
// against the wrong row. It falls through to step 2 and genuinely attempts
// to complete ITS OWN row -- which now correctly fails loudly on
// artifact_digest_idx's real UNIQUE constraint (digest must be globally
// unique; two published rows can never share one), instead of silently
// succeeding. An honest failure here is the intended outcome: it surfaces
// the problem to the release job rather than hiding it. The row stays
// exactly as BeginPublish left it (still "publishing", no digest) because
// the failed UPDATE rolls back within its own transaction.
func TestRecordArtifact_ReproducibleBuildSameDigestDifferentVersion(t *testing.T) {
	reg, pool := newTestRegistry(t)

	appID := seedApp(t, pool, "acme", "repro", "image")
	buildID1 := seedBuild(t, pool, "run-repro-1")
	buildID2 := seedBuild(t, pool, "run-repro-2")

	const sharedDigest = "sha256:reproducible"

	// v1.0.0 publishes normally with the shared digest.
	first, _, err := recordArtifactTx(t, reg, repository.Artifact{
		Kind: repository.ArtifactKindImage, AppID: appID,
		Repository: "ghcr.io/acme/repro", Version: "v1.0.0",
		Digest: sharedDigest, BuildID: buildID1,
	}, nil)
	if err != nil {
		t.Fatalf("record v1.0.0: %v", err)
	}
	if first.State != repository.ArtifactStatePublished {
		t.Fatalf("expected v1.0.0 published, got %q", first.State)
	}

	// v1.0.1 goes through the real BeginPublish -> RecordArtifact sequence
	// (as release.yml does), and the rebuild happens to be byte-identical
	// to v1.0.0 -- same digest, different version.
	publishing, err := beginPublishTx(t, reg, repository.ArtifactKindImage, appID, "v1.0.1", buildID2, "ghcr.io/acme/repro", repository.VersionSourceTag)
	if err != nil {
		t.Fatalf("BeginPublish v1.0.1: %v", err)
	}
	if publishing.State != repository.ArtifactStatePublishing {
		t.Fatalf("expected v1.0.1 publishing, got %q", publishing.State)
	}

	_, _, err = recordArtifactTx(t, reg, repository.Artifact{
		Kind: repository.ArtifactKindImage, AppID: appID,
		Repository: "ghcr.io/acme/repro", Version: "v1.0.1",
		Digest: sharedDigest, BuildID: buildID2,
	}, nil)
	if err == nil {
		t.Fatalf("expected RecordArtifact for v1.0.1 to fail honestly on the digest collision, got nil error (bug: silently matched v1.0.0's row instead)")
	}
	if !errors.Is(err, repository.ErrAlreadyExists) {
		t.Fatalf("expected ErrAlreadyExists (translated unique-violation on artifact_digest_idx), got: %v", err)
	}

	// v1.0.1's row must be untouched -- still exactly what BeginPublish left.
	if state, _, hasDigest, hasBuildID := artifactStateRow(t, pool, publishing.ArtifactID); state != "publishing" || hasDigest || !hasBuildID {
		t.Fatalf("expected v1.0.1's row to remain publishing with no digest after the failed completePublish, got state=%s hasDigest=%v hasBuildID=%v", state, hasDigest, hasBuildID)
	}

	// v1.0.0's row must be completely unaffected.
	v100, err := reg.Artifacts().GetArtifact(context.Background(), repository.ArtifactLookup{
		OwnerFullName: "acme-repro", Kind: repository.ArtifactKindImage, Version: "v1.0.0",
	})
	if err != nil {
		t.Fatalf("GetArtifact v1.0.0: %v", err)
	}
	if v100.State != repository.ArtifactStatePublished || v100.Digest != sharedDigest {
		t.Fatalf("expected v1.0.0 unchanged (published, digest=%s), got state=%s digest=%s", sharedDigest, v100.State, v100.Digest)
	}

	if n := artifactCount(t, pool, sharedDigest); n != 1 {
		t.Fatalf("expected exactly 1 row holding the shared digest (only v1.0.0), found %d", n)
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
	// GitSHA uses nanosecond precision purely for per-call UNIQUENESS (two
	// calls in the same test must never collide); SourceCommittedAt is Unix
	// SECONDS, matching the real git-committer-timestamp contract
	// (ARCHITECTURE.md "Reconcile watermark") -- migration 010's
	// recordAppManifestSweep feeds it through time.Unix(sec, 0) to compute
	// app_manifest_history.valid_from, so a nanosecond value here (as this
	// helper used before AR-8) overflows Postgres's timestamptz range.
	// ShouldApplyReconcile treats a TIE as "apply" (see its doc comment), so
	// two calls landing in the same wall-clock second is harmless.
	nowNano := time.Now().UnixNano()
	nowSec := time.Now().Unix()
	source := repository.ReconcileSource{
		GitSHA:            fmt.Sprintf("test-sha-%d", nowNano),
		SourceCommittedAt: nowSec,
		DiscoveredAt:      nowSec,
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

// TestResolveManifestForPublish_PrefersExactBuildCommit_Postgres proves
// currentAppManifest's tier-1 fallback (postgres/artifact.go): a build whose
// commit has an exact app_manifest_release observation is attributed to
// THAT content, not the owner's newest `main`-sweep content -- a build's
// promotability must derive from its OWN manifest.
func TestResolveManifestForPublish_PrefersExactBuildCommit_Postgres(t *testing.T) {
	reg, pool := newTestRegistry(t)
	ctx := authedCtx()
	appSrv := handlers.NewAppServer(reg)
	artSrv := handlers.NewArtifactServer(reg)

	// main's current sweep content: CHART (not promotable on its own).
	created, err := appSrv.ReconcileApps(ctx, &pb.ReconcileAppsRequest{
		Manifests: reconcileManifests("sha-exact-main", 100, 100,
			[]*appmetapb.AppManifest{{Domain: "acme", Name: "exact-svc", DeployUnit: appmetapb.DeployUnit_DEPLOY_UNIT_CHART}}, nil),
		IdempotencyKey: "exact-main-1",
	})
	if err != nil {
		t.Fatalf("sweep: %v", err)
	}
	_ = created

	// A divergent release branch at its OWN commit: IMAGE.
	if _, err := appSrv.AssertApps(ctx, &pb.AssertAppsRequest{
		Manifests: reconcileManifests("sha-exact-release", 50, 50,
			[]*appmetapb.AppManifest{{Domain: "acme", Name: "exact-svc", DeployUnit: appmetapb.DeployUnit_DEPLOY_UNIT_IMAGE}}, nil),
		IdempotencyKey: "exact-release-1",
	}); err != nil {
		t.Fatalf("assert: %v", err)
	}

	// A build recorded at the RELEASE branch's commit must resolve to that
	// release's IMAGE manifest (PROMOTABLE), not main's current CHART one
	// (VIA_CHART).
	var buildID string
	if err := pool.QueryRow(ctx, `
		INSERT INTO build (git_sha, workflow_run_id) VALUES ('sha-exact-release', 'run-exact')
		RETURNING build_id`).Scan(&buildID); err != nil {
		t.Fatalf("seed build: %v", err)
	}
	resp, err := artSrv.RecordArtifact(ctx, &pb.RecordArtifactRequest{
		BuildId: buildID, Kind: pb.ArtifactKind_ARTIFACT_KIND_IMAGE,
		OwnerFullName: "acme-exact-svc", Version: "v1.0.0", Digest: "sha256:exact1",
		IdempotencyKey: "exact-artifact-1",
	})
	if err != nil {
		t.Fatalf("RecordArtifact: %v", err)
	}
	if resp.Artifact.Promotability != pb.Promotability_PROMOTABILITY_PROMOTABLE {
		t.Fatalf("expected PROMOTABLE (the release commit's own IMAGE manifest), got %v -- resolveManifestForPublish did not prefer the exact build commit", resp.Artifact.Promotability)
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

	artifacts, _, err := reg.Artifacts().ListArtifacts(ctx, repository.ArtifactListFilter{BuildID: buildID}, 0, "")
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
	artifacts, _, err := reg.Artifacts().ListArtifacts(ctx, repository.ArtifactListFilter{BuildID: build.BuildID}, 0, "")
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

// --- 13. ListBuilds pagination/since/recorded_at ordering (issue #611) -----

// seedBuildRow inserts a build row directly with an exact, caller-chosen
// recorded_at (and optionally a nil started_at) -- there is no repository
// write path exercised by this test file that lets a caller pin an exact
// recorded_at (RecordBuild always stamps it from the clock, see
// postgres/artifact.go's buildRepo.RecordBuild), and this test specifically
// needs duplicate recorded_at values plus a NULL started_at row, neither of
// which a real write can reliably construct without racing the clock.
func seedBuildRow(t *testing.T, pool *pgxpool.Pool, workflowRunID string, recordedAt time.Time, startedAt *time.Time) string {
	t.Helper()
	var buildID string
	err := pool.QueryRow(context.Background(), `
		INSERT INTO build (git_sha, workflow_run_id, started_at, recorded_at)
		VALUES ('deadbeef', $1, $2, $3)
		RETURNING build_id`, workflowRunID, startedAt, recordedAt).Scan(&buildID)
	if err != nil {
		t.Fatalf("seed build %s: %v", workflowRunID, err)
	}
	return buildID
}

// TestListBuilds_Pagination_MatchesFullOrderedScan_Postgres is the "real
// Postgres ordering/index-free full sort" behavior the fake cannot exercise:
// pages through with a small page_size and confirms the full traversal
// matches a single unfiltered `ORDER BY recorded_at DESC, build_id DESC`
// scan exactly, in order, with no duplicates and no omissions -- including
// across a duplicate-recorded_at pair (the case a naive `ORDER BY
// recorded_at DESC LIMIT n` keyset implementation, missing the build_id
// tie-break, gets wrong) and a NULL-started_at row, which must sort by its
// own recorded_at position, not float to either end the way a naive `ORDER
// BY started_at` would place it.
func TestListBuilds_Pagination_MatchesFullOrderedScan_Postgres(t *testing.T) {
	reg, pool := newTestRegistry(t)
	ctx := context.Background()

	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	started := base.Add(-time.Minute)

	// tie: two rows sharing the exact same recorded_at -- the
	// duplicate-timestamp pair the keyset tie-break must still order
	// deterministically.
	tie := base.Add(3 * time.Hour)
	seedBuildRow(t, pool, "run-13-0", base, &started)
	seedBuildRow(t, pool, "run-13-1", base.Add(1*time.Hour), &started)
	seedBuildRow(t, pool, "run-13-2", tie, &started)
	seedBuildRow(t, pool, "run-13-3", tie, &started)
	// noStartedID has no started_at at all, and a recorded_at that lands
	// in the MIDDLE of the ordering (not first, not last) -- if ordering
	// ever fell back to started_at, this row would sort at either
	// extreme instead of here.
	noStartedID := seedBuildRow(t, pool, "run-13-4", base.Add(4*time.Hour), nil)
	seedBuildRow(t, pool, "run-13-5", base.Add(5*time.Hour), &started)
	seedBuildRow(t, pool, "run-13-6", base.Add(6*time.Hour), &started)

	// Ground truth: a single unfiltered scan in the same order the
	// repository contract promises.
	rows, err := pool.Query(ctx, `SELECT build_id FROM build ORDER BY recorded_at DESC, build_id DESC`)
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
	if len(want) != 7 {
		t.Fatalf("expected 7 rows in ground-truth scan, got %d", len(want))
	}

	// The NULL-started_at row must land at position 2 (0-indexed,
	// most-recent-first): after run-13-6/run-13-5, before the tied pair --
	// exactly where its own recorded_at (base+4h) places it, not first (as
	// a naive NULLS-FIRST ORDER BY started_at would put it) and not last.
	wantNullPos := -1
	for i, id := range want {
		if id == noStartedID {
			wantNullPos = i
		}
	}
	if wantNullPos != 2 {
		t.Fatalf("test setup sanity check failed: expected the NULL-started_at row at ground-truth position 2, got %d (want order %v)", wantNullPos, want)
	}

	var got []string
	seen := map[string]bool{}
	token := ""
	for page := 0; page < 8; page++ {
		builds, next, err := reg.Builds().ListBuilds(ctx, time.Time{}, 2, token)
		if err != nil {
			t.Fatalf("ListBuilds page %d: %v", page, err)
		}
		if len(builds) == 0 {
			t.Fatalf("page %d: got 0 rows", page)
		}
		if len(builds) > 2 {
			t.Fatalf("page %d: expected at most page_size=2 rows, got %d", page, len(builds))
		}
		for _, b := range builds {
			if seen[b.BuildID] {
				t.Fatalf("duplicate row %s across pages", b.BuildID)
			}
			seen[b.BuildID] = true
			got = append(got, b.BuildID)
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

// TestListBuilds_SinceComposesWithPagination_Postgres confirms since
// composes correctly with pagination -- filtering excludes rows before the
// boundary on the first page, and every row across every subsequent page
// still satisfies the filter -- including a row whose started_at is NULL,
// proving a NULL-started_at row is not silently excluded by a since filter
// it should satisfy (per the architect's open question 3 resolution on
// #601).
func TestListBuilds_SinceComposesWithPagination_Postgres(t *testing.T) {
	reg, pool := newTestRegistry(t)
	ctx := context.Background()

	base := time.Date(2026, 2, 1, 0, 0, 0, 0, time.UTC)
	started := base.Add(-time.Minute)
	// 3 rows strictly before `since`, all with a started_at.
	for i := 0; i < 3; i++ {
		seedBuildRow(t, pool, fmt.Sprintf("run-13-since-before-%d", i), base.Add(time.Duration(i)*time.Hour), &started)
	}
	since := base.Add(10 * time.Hour)
	// The row recorded exactly at `since` has NO started_at -- the boundary
	// case must still be included.
	noStartedID := seedBuildRow(t, pool, "run-13-since-boundary", since, nil)
	for i := 1; i < 5; i++ {
		seedBuildRow(t, pool, fmt.Sprintf("run-13-since-after-%d", i), since.Add(time.Duration(i)*time.Hour), &started)
	}

	var got []repository.Build
	token := ""
	for page := 0; page < 10; page++ {
		builds, next, err := reg.Builds().ListBuilds(ctx, since, 2, token)
		if err != nil {
			t.Fatalf("ListBuilds page %d: %v", page, err)
		}
		got = append(got, builds...)
		token = next
		if token == "" {
			break
		}
	}

	if len(got) != 5 {
		t.Fatalf("expected 5 builds at/after since across all pages, got %d", len(got))
	}
	foundBoundary := false
	for _, b := range got {
		if b.RecordedAt.Before(since) {
			t.Fatalf("row %s has recorded_at %v before since %v -- since did not compose correctly with pagination", b.BuildID, b.RecordedAt, since)
		}
		if b.BuildID == noStartedID {
			foundBoundary = true
			if b.StartedAt != nil {
				t.Fatalf("expected the boundary row's StartedAt to be nil, got %v", *b.StartedAt)
			}
		}
	}
	if !foundBoundary {
		t.Fatalf("expected the NULL-started_at boundary row to be included (recorded_at == since), but it was excluded")
	}
}

// seedArtifactRow inserts one `artifact` row directly, in state "published",
// bypassing the BeginPublish/RecordArtifact lifecycle -- issue #603's
// pagination tests need exact, caller-chosen state_changed_at values (and,
// for the PromotableOnly case, an exact promotability) that the real write
// path stamps from the clock/from a manifest join respectively.
func seedArtifactRow(t *testing.T, pool *pgxpool.Pool, appID, version, digest string, stateChangedAt time.Time, promotability repository.Promotability) string {
	t.Helper()
	buildID := seedBuild(t, pool, "run-artifact-page-"+uuid.NewString())
	var artifactID string
	err := pool.QueryRow(context.Background(), `
		INSERT INTO artifact (kind, app_id, repository, version, digest, build_id, published_at,
		                       state, provenance, version_source, state_changed_at, promotability)
		VALUES ('image', $1, 'ghcr.io/acme/artifact-page-test', $2, $3, $4, $5,
		        'published', 'observed', 'tag', $5, $6)
		RETURNING artifact_id`,
		appID, version, digest, buildID, stateChangedAt, string(promotability)).Scan(&artifactID)
	if err != nil {
		t.Fatalf("seed artifact %s: %v", digest, err)
	}
	return artifactID
}

// TestListArtifacts_Pagination_MatchesFullOrderedScan_Postgres is the real-
// Postgres analogue of TestListBuilds_Pagination_MatchesFullOrderedScan_Postgres
// for ListArtifacts (issue #603): pages through with a small page_size and
// confirms the full traversal matches a single unfiltered `ORDER BY
// state_changed_at DESC, artifact_id DESC` scan exactly, in order, with no
// duplicates and no omissions -- including across a duplicate-state_changed_at
// pair, the case a naive keyset implementation missing the artifact_id
// tie-break gets wrong.
func TestListArtifacts_Pagination_MatchesFullOrderedScan_Postgres(t *testing.T) {
	reg, pool := newTestRegistry(t)
	ctx := context.Background()
	appID := seedApp(t, pool, "acme", "artifact-page-app", "image")

	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	tie := base.Add(3 * time.Hour)
	seedArtifactRow(t, pool, appID, "v1.0.0", "sha256:page-0", base, repository.PromotabilityPromotable)
	seedArtifactRow(t, pool, appID, "v1.0.1", "sha256:page-1", base.Add(1*time.Hour), repository.PromotabilityPromotable)
	seedArtifactRow(t, pool, appID, "v1.0.2", "sha256:page-2", tie, repository.PromotabilityPromotable)
	seedArtifactRow(t, pool, appID, "v1.0.3", "sha256:page-3", tie, repository.PromotabilityPromotable)
	seedArtifactRow(t, pool, appID, "v1.0.4", "sha256:page-4", base.Add(4*time.Hour), repository.PromotabilityPromotable)

	rows, err := pool.Query(ctx, `SELECT artifact_id FROM artifact WHERE repository = 'ghcr.io/acme/artifact-page-test' ORDER BY state_changed_at DESC, artifact_id DESC`)
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
	if len(want) != 5 {
		t.Fatalf("expected 5 rows in ground-truth scan, got %d", len(want))
	}

	var got []string
	seen := map[string]bool{}
	token := ""
	for page := 0; page < 6; page++ {
		artifacts, next, err := reg.Artifacts().ListArtifacts(ctx, repository.ArtifactListFilter{OwnerFullName: "acme-artifact-page-app"}, 2, token)
		if err != nil {
			t.Fatalf("ListArtifacts page %d: %v", page, err)
		}
		if len(artifacts) == 0 {
			t.Fatalf("page %d: got 0 rows", page)
		}
		if len(artifacts) > 2 {
			t.Fatalf("page %d: expected at most page_size=2 rows, got %d", page, len(artifacts))
		}
		for _, a := range artifacts {
			if seen[a.ArtifactID] {
				t.Fatalf("duplicate row %s across pages", a.ArtifactID)
			}
			seen[a.ArtifactID] = true
			got = append(got, a.ArtifactID)
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

// TestListArtifacts_PromotableOnlyComposesWithPagination_Postgres proves
// PromotableOnly is enforced in SQL, not filtered client-side after the
// page's LIMIT (issue #603): with non-promotable rows interleaved among
// promotable ones in state_changed_at order, paging with PromotableOnly must
// still surface every promotable row, none of the non-promotable ones, and
// no short pages.
func TestListArtifacts_PromotableOnlyComposesWithPagination_Postgres(t *testing.T) {
	reg, pool := newTestRegistry(t)
	ctx := context.Background()
	appID := seedApp(t, pool, "acme", "artifact-promotable-page-app", "image")

	base := time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)
	var wantIDs []string
	for i := 0; i < 6; i++ {
		promotability := repository.PromotabilityNotPromotable
		if i%2 == 0 {
			promotability = repository.PromotabilityPromotable
		}
		id := seedArtifactRow(t, pool, appID, fmt.Sprintf("v1.0.%d", i), fmt.Sprintf("sha256:promotable-page-%d", i), base.Add(time.Duration(i)*time.Hour), promotability)
		if promotability == repository.PromotabilityPromotable {
			wantIDs = append(wantIDs, id)
		}
	}

	var got []string
	token := ""
	for page := 0; page < 4; page++ {
		artifacts, next, err := reg.Artifacts().ListArtifacts(ctx, repository.ArtifactListFilter{
			OwnerFullName:  "acme-artifact-promotable-page-app",
			PromotableOnly: true,
		}, 2, token)
		if err != nil {
			t.Fatalf("ListArtifacts page %d: %v", page, err)
		}
		if len(artifacts) == 0 {
			t.Fatalf("page %d: got 0 rows", page)
		}
		if remaining := len(wantIDs) - len(got); remaining >= 2 && len(artifacts) != 2 {
			t.Fatalf("page %d: expected exactly page_size=2 rows (PromotableOnly must not produce a short page while %d promotable rows remain), got %d", page, remaining, len(artifacts))
		}
		for _, a := range artifacts {
			if a.Promotability != repository.PromotabilityPromotable {
				t.Fatalf("returned non-promotable artifact %s (promotability=%s)", a.ArtifactID, a.Promotability)
			}
			got = append(got, a.ArtifactID)
		}
		token = next
		if token == "" {
			break
		}
	}

	if len(got) != len(wantIDs) {
		t.Fatalf("expected %d promotable rows across all pages, got %d", len(wantIDs), len(got))
	}
}
