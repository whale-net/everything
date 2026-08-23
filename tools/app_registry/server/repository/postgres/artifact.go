package postgres

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/whale-net/everything/libs/go/semver"
	"github.com/whale-net/everything/tools/app_registry/server/repository"
	appmetapb "github.com/whale-net/everything/tools/appmeta/proto"
)

// parseVersionTriple parses v into (major, minor, patch) for the
// version_major/minor/patch columns migration 004 added. Mirrors the
// migration's own backfill decision exactly (see 005_version_allocation.up.sql):
// a version that doesn't parse becomes the 0/0/0 sentinel rather than
// failing the write, so an unparseable/legacy version can never win a
// "latest" ORDER BY ... DESC query but the row is still written. This is
// deliberately lenient (unlike AllocateVersion's ParseRelease, which
// rejects prerelease/build metadata outright) because RecordArtifact
// already accepts only v<major>.<minor>.<patch> at the handler layer
// (see handlers/artifact.go's semverRe) — by the time SQL sees a version
// here it is expected to be clean, but this must never be the reason a
// write fails.
func parseVersionTriple(v string) (major, minor, patch int) {
	parsed, err := semver.Parse(v)
	if err != nil {
		return 0, 0, 0
	}
	return parsed.Major, parsed.Minor, parsed.Patch
}

// ============================================================================
// BuildRepository
// ============================================================================

type buildRepo struct{ ex dbtx }

const buildColumns = `build_id, git_sha, git_ref, workflow_run_id, workflow_attempt, actor, started_at, recorded_at`

func scanBuild(row pgx.Row) (repository.Build, error) {
	var b repository.Build
	if err := row.Scan(&b.BuildID, &b.GitSHA, &b.GitRef, &b.WorkflowRunID, &b.WorkflowAttempt, &b.Actor, &b.StartedAt, &b.RecordedAt); err != nil {
		return repository.Build{}, err
	}
	return b, nil
}

func (r *buildRepo) RecordBuild(ctx context.Context, b repository.Build) (*repository.Build, bool, error) {
	row := r.ex.QueryRow(ctx, `SELECT `+buildColumns+` FROM build WHERE workflow_run_id = $1 AND workflow_attempt = $2`, b.WorkflowRunID, b.WorkflowAttempt)
	if existing, err := scanBuild(row); err == nil {
		return &existing, true, nil
	} else if !errors.Is(err, pgx.ErrNoRows) {
		return nil, false, err
	}

	b.BuildID = uuid.NewString()
	b.RecordedAt = time.Now().UTC()
	if _, err := r.ex.Exec(ctx, `
		INSERT INTO build (build_id, git_sha, git_ref, workflow_run_id, workflow_attempt, actor, started_at, recorded_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8)`,
		b.BuildID, b.GitSHA, b.GitRef, b.WorkflowRunID, b.WorkflowAttempt, b.Actor, b.StartedAt, b.RecordedAt); err != nil {
		if de, ok := translatePgError(err, fmt.Sprintf("build for workflow run %s attempt %d already recorded", b.WorkflowRunID, b.WorkflowAttempt)); ok {
			return nil, false, de
		}
		return nil, false, fmt.Errorf("record build %s attempt %d: %w", b.WorkflowRunID, b.WorkflowAttempt, err)
	}
	return &b, false, nil
}

func (r *buildRepo) GetBuild(ctx context.Context, buildID string) (*repository.Build, error) {
	row := r.ex.QueryRow(ctx, `SELECT `+buildColumns+` FROM build WHERE build_id = $1`, buildID)
	b, err := scanBuild(row)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, repository.ErrNotFound
		}
		return nil, err
	}
	return &b, nil
}

// GetBuildByWorkflowRun implements repository.BuildRepository.GetBuildByWorkflowRun
// -- AR-7d (issue #558), the run log's entry point. attempt == 0 selects
// the highest workflow_attempt recorded for workflowRunID rather than an
// exact match.
func (r *buildRepo) GetBuildByWorkflowRun(ctx context.Context, workflowRunID string, attempt int32) (*repository.Build, error) {
	var row pgx.Row
	if attempt > 0 {
		row = r.ex.QueryRow(ctx, `SELECT `+buildColumns+` FROM build WHERE workflow_run_id = $1 AND workflow_attempt = $2`, workflowRunID, attempt)
	} else {
		row = r.ex.QueryRow(ctx, `SELECT `+buildColumns+` FROM build WHERE workflow_run_id = $1 ORDER BY workflow_attempt DESC LIMIT 1`, workflowRunID)
	}
	b, err := scanBuild(row)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, repository.ErrNotFound
		}
		return nil, err
	}
	return &b, nil
}

// defaultBuildPageSize is what ListBuilds falls back to when the caller
// passes pageSize <= 0. Matches defaultReconcileRunPageSize (app.go) so
// both real-pagination RPCs added by #607/#608 behave consistently.
const defaultBuildPageSize = 50

// ListBuilds implements repository.BuildRepository.ListBuilds -- see that
// doc comment for the pagination contract. build.recorded_at has no
// supporting index (see ARCHITECTURE.md "ListBuilds"); at current/
// foreseeable build volume an unindexed ORDER BY ... LIMIT full sort is
// fine by deliberate choice, same tradeoff as ListReconcileRuns's
// applied_at.
func (r *buildRepo) ListBuilds(ctx context.Context, since time.Time, pageSize int32, pageToken string) ([]repository.Build, string, error) {
	if pageSize <= 0 {
		pageSize = defaultBuildPageSize
	}

	query := `SELECT ` + buildColumns + ` FROM build WHERE 1=1`
	var args []any
	if !since.IsZero() {
		args = append(args, since)
		query += fmt.Sprintf(" AND recorded_at >= $%d", len(args))
	}
	if pageToken != "" {
		cursorTS, cursorID, err := decodeKeysetCursor(pageToken)
		if err != nil {
			return nil, "", fmt.Errorf("list builds: %w", err)
		}
		// Keyset predicate for "resume strictly after (ts, id)" on an
		// ORDER BY recorded_at DESC, build_id DESC query -- explicit
		// casts because the row-value comparison below gives Postgres
		// nothing else to infer $N's type from.
		args = append(args, cursorTS, cursorID)
		query += fmt.Sprintf(" AND (recorded_at, build_id) < ($%d::timestamptz, $%d::uuid)", len(args)-1, len(args))
	}
	// Fetch one extra row so we can tell whether there is a next page
	// without a separate COUNT(*) query.
	args = append(args, pageSize+1)
	query += fmt.Sprintf(" ORDER BY recorded_at DESC, build_id DESC LIMIT $%d", len(args))

	rows, err := r.ex.Query(ctx, query, args...)
	if err != nil {
		return nil, "", err
	}
	defer rows.Close()
	var out []repository.Build
	for rows.Next() {
		b, err := scanBuild(rows)
		if err != nil {
			return nil, "", err
		}
		out = append(out, b)
	}
	if err := rows.Err(); err != nil {
		return nil, "", err
	}

	var nextPageToken string
	if int32(len(out)) > pageSize {
		// The extra row proves a next page exists; the cursor resumes
		// after the LAST row of THIS page (the pageSize-th, not the
		// extra one), which is then dropped.
		last := out[pageSize-1]
		nextPageToken = encodeKeysetCursor(last.RecordedAt, last.BuildID)
		out = out[:pageSize]
	}
	return out, nextPageToken, nil
}

// ============================================================================
// ArtifactRepository
// ============================================================================

type artifactRepo struct{ ex dbtx }

// artifactRow is what every artifact SELECT returns. As of issue #833,
// Promotability is derived LIVE on every read -- joined against the owning
// app's/chart's CURRENT deploy_unit (v_current_app/v_current_chart, the
// same views ListApps/GetApp/ListCharts/scanApp/scanChart in postgres/app.go
// already read) and passed through repository.DerivePromotability, exactly
// the way this query worked before migration 008 (AR-7c). AR-7c had changed
// this to a column stored once at publish time, to fix a retroactivity bug
// (editing an app's deploy_unit after publish silently changed past
// artifacts' promotability); in production that traded one correctness
// problem for another -- a DerivePromotability rule fix (#810) could never
// reach artifacts published before the fix landed, permanently stranding
// them on the old, wrong value. See ARCHITECTURE.md "Promotability" and
// architecture/08-release-lifecycle/02-manifest-snapshot.md "As built
// (issue #833, migration 014)" for the full tradeoff writeup.
// manifest_id remains a STORED column (unaffected by this change -- it is
// build-commit provenance, not promotability). digest/build_id/
// published_at/manifest_id are all nullable -- an "allocated" row has none
// of them yet; see migration 007's artifact_state_shape CHECK constraint
// for exactly which states may have which. Promotability itself is derived
// only for state == published (mirroring the old artifact_promotability_shape
// CHECK's nullability); allocated/publishing/failed rows report "" (see
// scanArtifact below), matching repository.Artifact.Promotability's doc
// comment.
// The LEFT JOINs now target v_current_app/v_current_chart (not the bare
// app/chart identity tables) so this one join serves BOTH purposes: owner
// full-name lookups (findArtifact's OwnerFullName branch, ListArtifacts'
// OwnerFullName filter, below) AND sourcing deploy_unit for promotability
// derivation. Same aliases (app/chart), same column names/order as the
// bare tables used to have, so every existing app.domain/chart.domain
// filter clause below is unchanged.
//
// app_release_fallback: v_current_app deliberately reads ONLY
// provenance = 'sweep' snapshots (migration 008/010's "what does this app
// look like today is a main-tree question" -- a release-time AssertApps
// snapshot from a divergent/unmerged ref must never leak into ListApps/
// GetApp), and COALESCEs to 'chart' when no sweep snapshot exists at all.
// That default would silently break AR-7c's own exit criterion (issue
// #547 / TestAssertApps_ThenRecordArtifact_NoReconcileNeeded_Postgres): a
// release from a ref that NEVER merges -- AssertApps only, ReconcileApps
// never having run for this owner -- must still get a CORRECT promotability
// from ITS OWN deploy_unit, not a 'chart' default manufactured by the
// absence of sweep data. This LATERAL join supplies that fallback: the
// most recently recorded app_manifest_release observation for this owner,
// but ONLY consulted (the NOT EXISTS guard) when no CURRENT sweep interval
// exists -- an owner that HAS been reconciled always uses the sweep value,
// live-updating exactly as issue #833 intends; an owner that has ONLY ever
// been asserted falls back to its own release content instead of a
// meaningless default. No such fallback is needed for chart: v_current_chart's
// deploy_unit is a hardcoded 'chart' constant that never depends on manifest
// content existing at all (see migration 008's "Why chart_manifest has no
// generated columns").
const artifactSelectBase = `
	SELECT a.artifact_id, a.kind, a.app_id, a.chart_id, a.repository, a.version, a.digest, a.build_id, a.published_at,
	       a.state, a.provenance, a.version_source, a.state_changed_at, a.fail_reason,
	       a.manifest_id, COALESCE(app_release_fallback.deploy_unit, app.deploy_unit), chart.deploy_unit
	FROM artifact a
	LEFT JOIN v_current_app app ON a.app_id = app.app_id
	LEFT JOIN LATERAL (
		SELECT m.deploy_unit
		FROM app_manifest_release r
		JOIN app_manifest m ON m.app_manifest_id = r.app_manifest_id
		WHERE r.owner_id = a.app_id
		  AND NOT EXISTS (
		      SELECT 1 FROM app_manifest_history h WHERE h.owner_id = a.app_id AND h.valid_to IS NULL
		  )
		ORDER BY r.recorded_at DESC
		LIMIT 1
	) app_release_fallback ON true
	LEFT JOIN v_current_chart chart ON a.chart_id = chart.chart_id`

func scanArtifact(row pgx.Row) (repository.Artifact, error) {
	var a repository.Artifact
	var kind, state, provenance, versionSource string
	var appID, chartID, digest, buildID, manifestID, appDeployUnit, chartDeployUnit *string
	var publishedAt *time.Time
	if err := row.Scan(
		&a.ArtifactID, &kind, &appID, &chartID, &a.Repository, &a.Version, &digest, &buildID, &publishedAt,
		&state, &provenance, &versionSource, &a.StateChangedAt, &a.FailReason,
		&manifestID, &appDeployUnit, &chartDeployUnit,
	); err != nil {
		return repository.Artifact{}, err
	}
	a.Kind = repository.ArtifactKind(kind)
	a.State = repository.ArtifactState(state)
	a.Provenance = repository.ArtifactProvenance(provenance)
	a.VersionSource = repository.VersionSource(versionSource)
	if appID != nil {
		a.AppID = *appID
	}
	if chartID != nil {
		a.ChartID = *chartID
	}
	if digest != nil {
		a.Digest = *digest
	}
	if buildID != nil {
		a.BuildID = *buildID
	}
	if publishedAt != nil {
		a.PublishedAt = *publishedAt
	}
	if manifestID != nil {
		a.ManifestID = *manifestID
	}
	// Live derivation (issue #833): only for published rows -- nothing to
	// derive promotability from until publish, matching the old (pre-#833)
	// stored column's nullability. A chart artifact's owner join is
	// v_current_chart (deploy_unit hardcoded 'chart'); every other kind
	// (image, binary, firmware) owns via app_id (migration 011) and joins
	// v_current_app.
	if a.State == repository.ArtifactStatePublished {
		var du string
		if a.Kind == repository.ArtifactKindChart {
			if chartDeployUnit != nil {
				du = *chartDeployUnit
			}
		} else if appDeployUnit != nil {
			du = *appDeployUnit
		}
		a.Promotability = repository.DerivePromotability(a.Kind, deployUnitFromDB(du))
	}
	return a, nil
}

// RecordArtifact implements repository.ArtifactRepository.RecordArtifact —
// the publishing -> published transition. See the interface doc comment for
// the full state-machine contract.
func (r *artifactRepo) RecordArtifact(ctx context.Context, a repository.Artifact, contains []repository.ContainedImageInput) (*repository.Artifact, bool, error) {
	// 1. Idempotent replay: an existing row with this EXACT digest AND the
	// SAME (owner, kind, version) identity as the request. Only a
	// "published" row can ever match here (digest is NULL on every other
	// state, per migration 007's artifact_state_shape CHECK). The identity
	// scoping matters: digest alone is not enough (issue #585) -- Bazel's
	// reproducible builds routinely produce a byte-identical image for an
	// app with no functional change between two releases, so a NEW
	// version's digest can equal an OLDER already-published version's
	// digest for the SAME owner. Without the identity check, this step
	// would match that older row and report success without ever touching
	// the row BeginPublish created for the new version, leaving it
	// orphaned in "publishing". Requiring the full identity match here
	// means a same-digest/different-version case correctly falls through
	// to step 2, which resolves it against the request's own (owner,
	// kind, version) row instead.
	row := r.ex.QueryRow(ctx, artifactSelectBase+`
		WHERE a.digest = $1 AND a.owner_id = $2 AND a.kind = $3 AND a.version = $4`,
		a.Digest, ownerIDOf(a), string(a.Kind), a.Version)
	if existing, err := scanArtifact(row); err == nil {
		if existing.Kind == repository.ArtifactKindChart {
			links, lerr := r.loadContains(ctx, existing.ArtifactID)
			if lerr != nil {
				return nil, false, lerr
			}
			existing.Contains = links
		}
		return &existing, true, nil
	} else if !errors.Is(err, pgx.ErrNoRows) {
		return nil, false, err
	}

	// 2. No row shares this digest. Look for one by (owner, kind, version)
	// instead — the state machine's completion/conflict path.
	existing, everr := r.findByOwnerKindVersion(ctx, a.Kind, ownerIDOf(a), a.Version)
	switch {
	case errors.Is(everr, pgx.ErrNoRows):
		// No row at all for this (owner, kind, version): BeginPublish must
		// always have run first, for every domain unconditionally.
		return nil, false, fmt.Errorf("%w: no publishing artifact found for %s %s -- BeginPublish must run before RecordArtifact",
			repository.ErrFailedPrecondition, r.ownerFullName(ctx, a), a.Version)
	case everr != nil:
		return nil, false, everr
	}

	switch existing.State {
	case repository.ArtifactStatePublishing:
		out, err := r.completePublish(ctx, existing, a, contains)
		return out, false, err
	case repository.ArtifactStatePublished:
		// Reached only when the digest lookup in step 1 missed -- i.e. a
		// DIFFERENT digest than what is already published for this
		// (owner, kind, version). A real conflict, not a retry.
		return nil, false, fmt.Errorf("%w: artifact %s %s already published with digest %s",
			repository.ErrAlreadyExists, r.ownerFullName(ctx, existing), a.Version, existing.Digest)
	default: // allocated, failed
		return nil, false, fmt.Errorf("%w: artifact %s %s is %q -- BeginPublish must transition it to \"publishing\" before RecordArtifact can complete it",
			repository.ErrFailedPrecondition, r.ownerFullName(ctx, existing), a.Version, existing.State)
	}
}

// findByOwnerKindVersion looks up the artifact row for (kind, ownerID,
// version) regardless of state, matching the real artifact_version_idx
// UNIQUE (owner_id, kind, version) index (migration 001) -- the same
// identity BeginPublish/FailPublish/RecordArtifact all key off of. Returns
// pgx.ErrNoRows (unwrapped) when no row exists, so callers can
// errors.Is(err, pgx.ErrNoRows) directly.
func (r *artifactRepo) findByOwnerKindVersion(ctx context.Context, kind repository.ArtifactKind, ownerID, version string) (repository.Artifact, error) {
	row := r.ex.QueryRow(ctx, artifactSelectBase+`
		WHERE a.kind = $1 AND a.version = $2 AND a.owner_id = $3`,
		string(kind), version, ownerID)
	return scanArtifact(row)
}

func ownerIDOf(a repository.Artifact) string {
	if a.Kind != repository.ArtifactKindChart {
		return a.AppID
	}
	return a.ChartID
}

// ============================================================================
// Manifest resolution at publish time (migration 010, AR-8; originally
// migration 008, AR-7c)
// ============================================================================

// resolveManifestForPublish resolves the app_manifest/chart_manifest
// CONTENT row to attribute a NEWLY published artifact to -- see
// ARCHITECTURE.md "App identity vs. per-build manifest snapshot". Called
// from insertArtifact/completePublish at the exact instant a row reaches
// "published", and ONLY then -- manifest_id is provenance (which build-time
// content a row was published against) and is stored, same as always.
//
// Promotability is NOT resolved here as of issue #833: it is derived LIVE
// on every read from the owner's CURRENT deploy_unit (scanArtifact, this
// package), not from the manifest content a build happened to be resolved
// against at publish time -- see scanArtifact's doc comment for why.
//
// Prefers the content recorded at the EXACT commit buildID's build was built
// from -- typically the one release.yml's AssertApps step just wrote, at
// this same run's git_sha, looked up in *_manifest_release. Falls back to
// the owner's CURRENT `main`-sweep content (*_manifest_history's open
// interval) when no exact release match exists (a domain that hasn't wired
// AssertApps into its release path yet, or a build whose commit predates
// AR-7c/AR-8) -- this is the same deliberate simplification migration 008
// documented: "attributed at publish time" means "the best content known
// at publish time," not "guaranteed to be the exact build commit."
//
// Errors (ErrFailedPrecondition) only if NEITHER a release row NOR a current
// history interval exists for this owner, which should be unreachable in
// practice: every write path that can create an app/chart identity row
// (Reconcile, AssertApps, and migration 010's backfill) always writes at
// least one manifest content row in the same call/migration -- resolveOwner
// (handlers/artifact.go) already guarantees the owner's IDENTITY exists
// before RecordArtifact/BeginPublish ever calls this, so a missing content
// row here would mean that invariant broke, not a normal operational
// condition.
func (r *artifactRepo) resolveManifestForPublish(ctx context.Context, kind repository.ArtifactKind, ownerID, buildID string) (manifestID string, err error) {
	var buildGitSHA string
	if buildID != "" {
		row := r.ex.QueryRow(ctx, `SELECT git_sha FROM build WHERE build_id = $1`, buildID)
		_ = row.Scan(&buildGitSHA) // best-effort -- falls through to "current" below regardless
	}

	if kind == repository.ArtifactKindChart {
		return r.currentChartManifestID(ctx, ownerID, buildGitSHA)
	}

	id, _, ferr := r.currentAppManifest(ctx, ownerID, buildGitSHA)
	if ferr != nil {
		return "", ferr
	}
	return id, nil
}

// currentAppManifest resolves the app_manifest content row to use, in three
// tiers (migration 010, AR-8 -- see resolveManifestForPublish's doc
// comment): (1) an exact-git_sha release observation, (2) the owner's
// CURRENT `main`-sweep interval when ITS first/last_git_sha matches the
// build's commit (the common case: the release's commit is what's currently
// live on main), (3) the current interval regardless of commit -- the new
// O(1) point lookup migration 010 exists to make possible, propagating the
// same perf win to publish time.
func (r *artifactRepo) currentAppManifest(ctx context.Context, ownerID, preferGitSHA string) (manifestID string, deployUnit appmetapb.DeployUnit, err error) {
	if preferGitSHA != "" {
		row := r.ex.QueryRow(ctx, `
			SELECT m.app_manifest_id, m.deploy_unit
			FROM app_manifest_release r
			JOIN app_manifest m ON m.app_manifest_id = r.app_manifest_id
			WHERE r.owner_id = $1 AND r.git_sha = $2`, ownerID, preferGitSHA)
		var id, du string
		if serr := row.Scan(&id, &du); serr == nil {
			return id, deployUnitFromDB(du), nil
		} else if !errors.Is(serr, pgx.ErrNoRows) {
			return "", 0, fmt.Errorf("resolve manifest for publish: %w", serr)
		}

		row = r.ex.QueryRow(ctx, `
			SELECT m.app_manifest_id, m.deploy_unit
			FROM app_manifest_history h
			JOIN app_manifest m ON m.app_manifest_id = h.app_manifest_id
			WHERE h.owner_id = $1 AND h.valid_to IS NULL
			  AND ($2 IN (h.first_git_sha, h.last_git_sha))`, ownerID, preferGitSHA)
		if serr := row.Scan(&id, &du); serr == nil {
			return id, deployUnitFromDB(du), nil
		} else if !errors.Is(serr, pgx.ErrNoRows) {
			return "", 0, fmt.Errorf("resolve manifest for publish: %w", serr)
		}
	}
	row := r.ex.QueryRow(ctx, `
		SELECT m.app_manifest_id, m.deploy_unit
		FROM app_manifest_history h
		JOIN app_manifest m ON m.app_manifest_id = h.app_manifest_id
		WHERE h.owner_id = $1 AND h.valid_to IS NULL`, ownerID)
	var id, du string
	if serr := row.Scan(&id, &du); serr != nil {
		if errors.Is(serr, pgx.ErrNoRows) {
			return "", 0, fmt.Errorf("%w: no manifest recorded for app owner %s -- AssertApps/ReconcileApps must run before publishing", repository.ErrFailedPrecondition, ownerID)
		}
		return "", 0, fmt.Errorf("resolve manifest for publish: %w", serr)
	}
	return id, deployUnitFromDB(du), nil
}

func (r *artifactRepo) currentChartManifestID(ctx context.Context, ownerID, preferGitSHA string) (string, error) {
	if preferGitSHA != "" {
		row := r.ex.QueryRow(ctx, `
			SELECT r.chart_manifest_id
			FROM chart_manifest_release r
			WHERE r.owner_id = $1 AND r.git_sha = $2`, ownerID, preferGitSHA)
		var id string
		if serr := row.Scan(&id); serr == nil {
			return id, nil
		} else if !errors.Is(serr, pgx.ErrNoRows) {
			return "", fmt.Errorf("resolve manifest for publish: %w", serr)
		}

		row = r.ex.QueryRow(ctx, `
			SELECT h.chart_manifest_id
			FROM chart_manifest_history h
			WHERE h.owner_id = $1 AND h.valid_to IS NULL
			  AND ($2 IN (h.first_git_sha, h.last_git_sha))`, ownerID, preferGitSHA)
		if serr := row.Scan(&id); serr == nil {
			return id, nil
		} else if !errors.Is(serr, pgx.ErrNoRows) {
			return "", fmt.Errorf("resolve manifest for publish: %w", serr)
		}
	}
	row := r.ex.QueryRow(ctx, `
		SELECT chart_manifest_id
		FROM chart_manifest_history
		WHERE owner_id = $1 AND valid_to IS NULL`, ownerID)
	var id string
	if serr := row.Scan(&id); serr != nil {
		if errors.Is(serr, pgx.ErrNoRows) {
			return "", fmt.Errorf("%w: no manifest recorded for chart owner %s -- AssertApps/ReconcileApps must run before publishing", repository.ErrFailedPrecondition, ownerID)
		}
		return "", fmt.Errorf("resolve manifest for publish: %w", serr)
	}
	return id, nil
}

// insertArtifact writes a brand-new artifact row directly in state, with the
// given versionSource/provenance. digest/build_id/published_at are written
// NULL when the corresponding field on a is empty -- correct for "allocated"
// (neither) and "publishing" (build_id only); contains is only consulted
// when state == published (RecordArtifact's direct-create path and
// AdoptArtifact's ∅ -> published path); BeginPublish and AllocateVersion
// always pass nil. Shared by RecordArtifact's direct-create path (provenance
// always ArtifactProvenanceObserved -- the only value this phase ever wrote
// before AR-7e), BeginPublish's ∅ -> publishing branch, AllocateVersion's
// ∅ -> allocated write, and AdoptArtifact's (AR-7e, issue #558) ∅ ->
// published branch (provenance ArtifactProvenanceAdopted).
func (r *artifactRepo) insertArtifact(ctx context.Context, a repository.Artifact, contains []repository.ContainedImageInput, state repository.ArtifactState, versionSource repository.VersionSource, provenance repository.ArtifactProvenance) (*repository.Artifact, bool, error) {
	a.ArtifactID = uuid.NewString()
	a.State = state
	a.Provenance = provenance
	a.VersionSource = versionSource
	a.StateChangedAt = time.Now().UTC()
	if state == repository.ArtifactStatePublished && a.PublishedAt.IsZero() {
		a.PublishedAt = time.Now().UTC()
	}

	var appID, chartID any
	if a.Kind != repository.ArtifactKindChart {
		appID = a.AppID
	} else {
		chartID = a.ChartID
	}
	var digest, buildID, publishedAt any
	if a.Digest != "" {
		digest = a.Digest
	}
	if a.BuildID != "" {
		buildID = a.BuildID
	}
	if !a.PublishedAt.IsZero() {
		publishedAt = a.PublishedAt
	}

	// Resolve the owner's display name *before* the insert. A constraint
	// violation aborts the surrounding transaction, so any query issued
	// afterwards fails too and ownerFullName would degrade to the raw UUID —
	// which is exactly what a client should never be shown.
	ownerName := r.ownerFullName(ctx, a)
	versionMajor, versionMinor, versionPatch := parseVersionTriple(a.Version)

	// manifest_id is resolved and STORED here, ONCE, only at the instant
	// this row reaches "published" -- never for allocated/publishing
	// (nothing to attribute a manifest to yet). Promotability is no longer
	// resolved/stored at write time (issue #833) -- it is derived live on
	// every read, see scanArtifact.
	var manifestID any
	if state == repository.ArtifactStatePublished {
		mid, err := r.resolveManifestForPublish(ctx, a.Kind, ownerIDOf(a), a.BuildID)
		if err != nil {
			return nil, false, err
		}
		manifestID = mid
	}

	if _, err := r.ex.Exec(ctx, `
		INSERT INTO artifact (artifact_id, kind, app_id, chart_id, repository, version, digest, build_id, published_at,
		                       version_major, version_minor, version_patch, state, provenance, version_source, state_changed_at,
		                       manifest_id)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17)`,
		a.ArtifactID, string(a.Kind), appID, chartID, a.Repository, a.Version, digest, buildID, publishedAt,
		versionMajor, versionMinor, versionPatch, string(a.State), string(a.Provenance), string(a.VersionSource), a.StateChangedAt,
		manifestID); err != nil {
		msg := fmt.Sprintf("artifact %s %s already recorded", ownerName, a.Version)
		if de, ok := translatePgError(err, msg); ok {
			return nil, false, de
		}
		return nil, false, fmt.Errorf("record artifact %s: %w", a.Digest, err)
	}

	if a.Kind == repository.ArtifactKindChart && state == repository.ArtifactStatePublished {
		if err := r.linkContains(ctx, a.ArtifactID, a.Digest, contains); err != nil {
			return nil, false, err
		}
	}

	out, err := r.GetArtifact(ctx, repository.ArtifactLookup{ArtifactID: a.ArtifactID})
	if err != nil {
		return nil, false, err
	}
	return out, false, nil
}

// completePublish is the publishing -> published transition RecordArtifact
// performs against an EXISTING row: stamps digest/build_id/published_at,
// flips state, and (for a chart) links contains -- exactly the work
// insertArtifact's direct-create path does, just as an UPDATE instead of an
// INSERT because the row (and its artifact_id) already exists.
func (r *artifactRepo) completePublish(ctx context.Context, existing, a repository.Artifact, contains []repository.ContainedImageInput) (*repository.Artifact, error) {
	publishedAt := a.PublishedAt
	if publishedAt.IsZero() {
		publishedAt = time.Now().UTC()
	}
	buildID := existing.BuildID
	if a.BuildID != "" {
		buildID = a.BuildID
	}
	now := time.Now().UTC()

	// manifest_id is resolved and stored ONCE, right here, at the instant
	// this row actually transitions to "published" -- see insertArtifact's
	// matching comment. Promotability is derived live on read (issue #833),
	// not resolved/stored here.
	manifestID, err := r.resolveManifestForPublish(ctx, existing.Kind, ownerIDOf(existing), buildID)
	if err != nil {
		return nil, err
	}

	if _, err := r.ex.Exec(ctx, `
		UPDATE artifact SET digest = $1, build_id = $2, published_at = $3, state = 'published', state_changed_at = $4,
		                     manifest_id = $5
		WHERE artifact_id = $6`,
		a.Digest, buildID, publishedAt, now, manifestID, existing.ArtifactID); err != nil {
		msg := fmt.Sprintf("artifact %s already recorded", a.Digest)
		if de, ok := translatePgError(err, msg); ok {
			return nil, de
		}
		return nil, fmt.Errorf("complete publish for artifact %s: %w", existing.ArtifactID, err)
	}

	if existing.Kind == repository.ArtifactKindChart {
		if err := r.linkContains(ctx, existing.ArtifactID, a.Digest, contains); err != nil {
			return nil, err
		}
	}

	return r.GetArtifact(ctx, repository.ArtifactLookup{ArtifactID: existing.ArtifactID})
}

// ============================================================================
// AdoptArtifact (AR-7e, issue #558)
// ============================================================================

// AdoptArtifact implements repository.ArtifactRepository.AdoptArtifact --
// the admin-only adoption / disaster-recovery path. See the interface doc
// comment for the full state-collision contract this method enforces.
func (r *artifactRepo) AdoptArtifact(ctx context.Context, a repository.Artifact, contains []repository.ContainedImageInput, reason, actor string) (*repository.Artifact, bool, error) {
	// 1. Idempotent replay: an existing PUBLISHED row with this EXACT digest
	// AND the SAME (owner, kind, version) identity as the request.
	row := r.ex.QueryRow(ctx, artifactSelectBase+`
		WHERE a.digest = $1 AND a.owner_id = $2 AND a.kind = $3 AND a.version = $4`,
		a.Digest, ownerIDOf(a), string(a.Kind), a.Version)
	if existing, err := scanArtifact(row); err == nil {
		if existing.Kind == repository.ArtifactKindChart {
			links, lerr := r.loadContains(ctx, existing.ArtifactID)
			if lerr != nil {
				return nil, false, lerr
			}
			existing.Contains = links
		}
		return &existing, true, nil
	} else if !errors.Is(err, pgx.ErrNoRows) {
		return nil, false, err
	}

	// 2. No row shares this digest. Look for one by (owner, kind, version).
	existing, everr := r.findByOwnerKindVersion(ctx, a.Kind, ownerIDOf(a), a.Version)
	switch {
	case errors.Is(everr, pgx.ErrNoRows):
		// ∅ -> published (adopted): the primary case.
		out, err := r.adoptNew(ctx, a, contains, actor)
		return out, false, err
	case everr != nil:
		return nil, false, everr
	}

	switch existing.State {
	case repository.ArtifactStateFailed, repository.ArtifactStatePublishing:
		// failed/publishing -> published (adopted): the disaster-recovery case --
		// a run already tried and gave up, was interrupted, or failed to record,
		// but the artifact demonstrably exists. See ArtifactRepository.AdoptArtifact's
		// doc comment for why the existing build_id is reused when present.
		out, err := r.completeAdoption(ctx, existing, a, contains, actor)
		return out, false, err
	case repository.ArtifactStatePublished:
		// Reached only when the digest lookup in step 1 missed -- a
		// DIFFERENT digest than what is already published for this
		// (owner, kind, version). A real conflict, not a retry -- adoption
		// must not silently overwrite a different recorded digest.
		return nil, false, fmt.Errorf("%w: artifact %s %s already published with digest %s",
			repository.ErrAlreadyExists, r.ownerFullName(ctx, existing), a.Version, existing.Digest)
	default: // allocated
		return nil, false, fmt.Errorf("%w: artifact %s %s is %q -- AdoptArtifact only applies when there is no row, or the row is \"failed\" or \"publishing\"; a live allocation must be let run its course (or explicitly failed via FailPublish) before it can be adopted",
			repository.ErrFailedPrecondition, r.ownerFullName(ctx, existing), a.Version, existing.State)
	}
}

// createAdoptionBuild inserts a synthetic `build` row backing an adopted
// artifact. AdoptArtifact still needs SOME build_id: migration 007's
// artifact_state_shape CHECK requires one once a row reaches "published",
// and artifact.build_id is a real foreign key into `build` -- but by
// definition there is no CI run behind a pre-registry artifact.
// workflow_run_id is stamped "adopted:<uuid>", deliberately non-numeric so
// it can never collide with a real GitHub Actions run id and reads as
// synthetic at a glance in `builds status`/GetReleaseRun output; git_ref
// carries the same "adopted" marker. git_sha is empty -- genuinely unknown
// for a pre-registry artifact. actor is the calling admin's own identity
// (grpcauth Subject), not a service account.
func (r *artifactRepo) createAdoptionBuild(ctx context.Context, actor string) (string, error) {
	buildID := uuid.NewString()
	now := time.Now().UTC()
	if _, err := r.ex.Exec(ctx, `
		INSERT INTO build (build_id, git_sha, git_ref, workflow_run_id, workflow_attempt, actor, recorded_at)
		VALUES ($1, '', 'adopted', $2, 1, $3, $4)`,
		buildID, "adopted:"+uuid.NewString(), actor, now); err != nil {
		return "", fmt.Errorf("create synthetic build for adoption: %w", err)
	}
	return buildID, nil
}

// adoptNew is AdoptArtifact's ∅ -> published(adopted) branch: no row exists
// at all for (owner, kind, version) yet.
func (r *artifactRepo) adoptNew(ctx context.Context, a repository.Artifact, contains []repository.ContainedImageInput, actor string) (*repository.Artifact, error) {
	buildID, err := r.createAdoptionBuild(ctx, actor)
	if err != nil {
		return nil, err
	}
	a.BuildID = buildID
	out, _, err := r.insertArtifact(ctx, a, contains, repository.ArtifactStatePublished, repository.VersionSourceTag, repository.ArtifactProvenanceAdopted)
	return out, err
}

// completeAdoption is AdoptArtifact's failed -> published(adopted) branch,
// against an EXISTING "failed" row. If that row already carries a build_id
// (true whenever the reaper reaped a "publishing" row; false when it reaped
// an "allocated" row, which never had one), it is reused rather than
// minting a synthetic one -- the real CI run that actually attempted the
// push is more honest provenance than a synthetic placeholder. Otherwise
// falls back to a synthetic build row, same as adoptNew.
func (r *artifactRepo) completeAdoption(ctx context.Context, existing, a repository.Artifact, contains []repository.ContainedImageInput, actor string) (*repository.Artifact, error) {
	buildID := existing.BuildID
	if buildID == "" {
		var err error
		buildID, err = r.createAdoptionBuild(ctx, actor)
		if err != nil {
			return nil, err
		}
	}
	publishedAt := a.PublishedAt
	if publishedAt.IsZero() {
		publishedAt = time.Now().UTC()
	}
	now := time.Now().UTC()

	manifestID, err := r.resolveManifestForPublish(ctx, existing.Kind, ownerIDOf(existing), buildID)
	if err != nil {
		return nil, err
	}

	if _, err := r.ex.Exec(ctx, `
		UPDATE artifact SET digest = $1, build_id = $2, published_at = $3, state = 'published', state_changed_at = $4,
		                     provenance = 'adopted', manifest_id = $5, fail_reason = ''
		WHERE artifact_id = $6`,
		a.Digest, buildID, publishedAt, now, manifestID, existing.ArtifactID); err != nil {
		msg := fmt.Sprintf("artifact %s already recorded", a.Digest)
		if de, ok := translatePgError(err, msg); ok {
			return nil, de
		}
		return nil, fmt.Errorf("complete adoption for artifact %s: %w", existing.ArtifactID, err)
	}

	if existing.Kind == repository.ArtifactKindChart {
		if err := r.linkContains(ctx, existing.ArtifactID, a.Digest, contains); err != nil {
			return nil, err
		}
	}

	return r.GetArtifact(ctx, repository.ArtifactLookup{ArtifactID: existing.ArtifactID})
}

// linkContains inserts one artifact_link row per entry in contains,
// pinning chartArtifactID to each already-PUBLISHED image digest. A chart
// may not pin an image that either doesn't exist or hasn't reached
// "published" yet (an allocated/publishing/failed image row has no digest
// to pin in the first place) -- see ARCHITECTURE.md "The problem: four
// cross-run orderings" #3.
func (r *artifactRepo) linkContains(ctx context.Context, chartArtifactID, chartDigest string, contains []repository.ContainedImageInput) error {
	for _, ci := range contains {
		imgRow := r.ex.QueryRow(ctx, `SELECT artifact_id, app_id FROM artifact WHERE digest = $1 AND kind = 'image' AND state = 'published'`, ci.Digest)
		var imgArtifactID string
		var imgAppID *string
		if err := imgRow.Scan(&imgArtifactID, &imgAppID); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return fmt.Errorf("%w: chart pins unrecorded image digest %s", repository.ErrInvalidArgument, ci.Digest)
			}
			return err
		}
		imgApp := ""
		if imgAppID != nil {
			imgApp = *imgAppID
		}
		if _, err := r.ex.Exec(ctx, `
			INSERT INTO artifact_link (chart_artifact_id, image_artifact_id, app_id, repository, version, digest)
			VALUES ($1, $2, $3, $4, $5, $6)`,
			chartArtifactID, imgArtifactID, imgApp, ci.Repository, ci.Version, ci.Digest); err != nil {
			msg := fmt.Sprintf("artifact %s already pins image digest %s", chartDigest, ci.Digest)
			if de, ok := translatePgError(err, msg); ok {
				return de
			}
			return fmt.Errorf("link chart artifact %s to image %s: %w", chartArtifactID, ci.Digest, err)
		}
	}
	return nil
}

// ============================================================================
// BeginPublish / FailPublish / ExpireStale (AR-7b)
// ============================================================================

// BeginPublish implements repository.ArtifactRepository.BeginPublish -- the
// ∅|allocated|failed -> publishing transition. See the interface doc
// comment for the full contract.
func (r *artifactRepo) BeginPublish(ctx context.Context, kind repository.ArtifactKind, ownerID, version, buildID, repositoryHint string, versionSource repository.VersionSource) (*repository.Artifact, error) {
	existing, everr := r.findByOwnerKindVersion(ctx, kind, ownerID, version)
	switch {
	case errors.Is(everr, pgx.ErrNoRows):
		// ∅ -> publishing: no prior AllocateVersion call for this version
		// (a kind that never allocates, or an explicit version) -- see
		// ARCHITECTURE.md "Artifact lifecycle".
		if repositoryHint == "" {
			return nil, fmt.Errorf("%w: repository is required to begin publishing %s %s with no prior allocation", repository.ErrInvalidArgument, string(kind), version)
		}
		a := repository.Artifact{Kind: kind, Repository: repositoryHint, Version: version, BuildID: buildID}
		if kind != repository.ArtifactKindChart {
			a.AppID = ownerID
		} else {
			a.ChartID = ownerID
		}
		out, _, err := r.insertArtifact(ctx, a, nil, repository.ArtifactStatePublishing, versionSource, repository.ArtifactProvenanceObserved)
		return out, err
	case everr != nil:
		return nil, everr
	}

	// AR-7d (issue #558): publishing -> publishing is a legal, idempotent
	// heartbeat/re-arm, not a rejection -- see ArtifactState's doc comment.
	// Needed because AR-7d's plan-time BeginPublishBatch stamps
	// state_changed_at for every target at plan time, before the release
	// matrix fans out; without a per-leg heartbeat call re-arming it right
	// before that target's own push, the stale-row reaper could reap a row
	// whose leg simply hadn't started yet, using the WHOLE run's duration
	// as its budget instead of one leg's. The UPDATE below already handles
	// this branch correctly unchanged: state_changed_at/build_id refresh
	// and fail_reason clears (already empty) whether the row was
	// allocated, failed, or already publishing.
	if existing.State != repository.ArtifactStateAllocated &&
		existing.State != repository.ArtifactStateFailed &&
		existing.State != repository.ArtifactStatePublishing {
		return nil, fmt.Errorf("%w: artifact %s %s is %q, not \"allocated\", \"failed\", or \"publishing\" -- BeginPublish cannot start from here",
			repository.ErrFailedPrecondition, r.ownerFullName(ctx, existing), version, existing.State)
	}

	now := time.Now().UTC()
	if _, err := r.ex.Exec(ctx, `
		UPDATE artifact SET build_id = $1, state = 'publishing', state_changed_at = $2, fail_reason = ''
		WHERE artifact_id = $3`,
		buildID, now, existing.ArtifactID); err != nil {
		return nil, fmt.Errorf("begin publish for artifact %s: %w", existing.ArtifactID, err)
	}
	return r.GetArtifact(ctx, repository.ArtifactLookup{ArtifactID: existing.ArtifactID})
}

// FailPublish implements repository.ArtifactRepository.FailPublish -- the
// publishing -> failed transition.
func (r *artifactRepo) FailPublish(ctx context.Context, kind repository.ArtifactKind, ownerID, version, reason string) (*repository.Artifact, error) {
	existing, everr := r.findByOwnerKindVersion(ctx, kind, ownerID, version)
	if errors.Is(everr, pgx.ErrNoRows) {
		return nil, fmt.Errorf("%w: no artifact found for %s %s %s", repository.ErrFailedPrecondition, string(kind), ownerID, version)
	}
	if everr != nil {
		return nil, everr
	}
	if existing.State != repository.ArtifactStatePublishing {
		return nil, fmt.Errorf("%w: artifact %s %s is %q, not \"publishing\" -- FailPublish cannot start from here",
			repository.ErrFailedPrecondition, r.ownerFullName(ctx, existing), version, existing.State)
	}

	now := time.Now().UTC()
	if _, err := r.ex.Exec(ctx, `
		UPDATE artifact SET state = 'failed', state_changed_at = $1, fail_reason = $2
		WHERE artifact_id = $3`,
		now, reason, existing.ArtifactID); err != nil {
		return nil, fmt.Errorf("fail publish for artifact %s: %w", existing.ArtifactID, err)
	}
	return r.GetArtifact(ctx, repository.ArtifactLookup{ArtifactID: existing.ArtifactID})
}

// ExpireStale implements repository.ArtifactRepository.ExpireStale -- the
// reaper's sweep. Backed by artifact_state_idx (migration 007), a partial
// index on exactly the (state, state_changed_at) this WHERE clause filters
// on.
func (r *artifactRepo) ExpireStale(ctx context.Context, olderThan time.Duration) (int, error) {
	cutoff := time.Now().UTC().Add(-olderThan)
	tag, err := r.ex.Exec(ctx, `
		UPDATE artifact SET state = 'failed', state_changed_at = NOW(), fail_reason = 'stale'
		WHERE state IN ('allocated', 'publishing') AND state_changed_at < $1`, cutoff)
	if err != nil {
		return 0, fmt.Errorf("expire stale artifacts: %w", err)
	}
	return int(tag.RowsAffected()), nil
}

// ownerFullName resolves a.AppID/a.ChartID to "domain-name" for error
// messages that must describe a conflict in domain terms. It is only called
// on the (rare) error path, never the hot insert path, so the extra query is
// cheap where it matters. Falls back to the raw id if the lookup fails.
func (r *artifactRepo) ownerFullName(ctx context.Context, a repository.Artifact) string {
	var row pgx.Row
	var fallback string
	if a.Kind != repository.ArtifactKindChart {
		row = r.ex.QueryRow(ctx, `SELECT domain || '-' || name FROM app WHERE app_id = $1`, a.AppID)
		fallback = a.AppID
	} else {
		row = r.ex.QueryRow(ctx, `SELECT domain || '-' || name FROM chart WHERE chart_id = $1`, a.ChartID)
		fallback = a.ChartID
	}
	var name string
	if err := row.Scan(&name); err != nil {
		return fallback
	}
	return name
}

func (r *artifactRepo) loadContains(ctx context.Context, chartArtifactID string) ([]repository.ArtifactLink, error) {
	rows, err := r.ex.Query(ctx, `SELECT image_artifact_id, app_id, repository, version, digest FROM artifact_link WHERE chart_artifact_id = $1`, chartArtifactID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []repository.ArtifactLink
	for rows.Next() {
		var l repository.ArtifactLink
		if err := rows.Scan(&l.ImageArtifactID, &l.AppID, &l.Repository, &l.Version, &l.Digest); err != nil {
			return nil, err
		}
		out = append(out, l)
	}
	return out, rows.Err()
}

// defaultArtifactPageSize is what ListArtifacts falls back to when the
// caller passes pageSize <= 0. Matches defaultBuildPageSize/
// defaultReconcileRunPageSize so every real-pagination RPC in this package
// behaves consistently (issue #603).
const defaultArtifactPageSize = 50

// ListArtifacts implements repository.ArtifactRepository.ListArtifacts --
// see that doc comment for the pagination contract. Ordered by
// state_changed_at DESC, tie-broken by artifact_id DESC: state_changed_at
// (unlike published_at) is NOT NULL for every row regardless of state (see
// artifact_state_shape, migration 007), which is what makes it safe as a
// keyset-pagination cursor column -- a NULL published_at on an
// allocated/publishing/failed row would otherwise never satisfy the "<"
// keyset predicate below and could silently drop rows from every page after
// the first. This also folds the old BuildID-only special case (previously
// the only caller ordered by state_changed_at, precisely to avoid the same
// NULL hazard -- see GetReleaseRun) into the general path.
func (r *artifactRepo) ListArtifacts(ctx context.Context, filter repository.ArtifactListFilter, pageSize int32, pageToken string) ([]repository.Artifact, string, error) {
	if pageSize <= 0 {
		pageSize = defaultArtifactPageSize
	}

	query := artifactSelectBase + ` WHERE 1=1`
	var args []any

	if filter.Kind != "" {
		args = append(args, string(filter.Kind))
		query += fmt.Sprintf(" AND a.kind = $%d", len(args))
	}
	if filter.OwnerFullName != "" {
		args = append(args, filter.OwnerFullName)
		query += fmt.Sprintf(" AND (app.domain || '-' || app.name = $%d OR chart.domain || '-' || chart.name = $%d)", len(args), len(args))
	}
	// AR-7e (issue #558): "which rows did we take on faith?" as a query --
	// the exit criterion's "distinguishable in one query" half.
	if filter.Provenance != "" {
		args = append(args, string(filter.Provenance))
		query += fmt.Sprintf(" AND a.provenance = $%d", len(args))
	}
	// GetReleaseRun's (AR-7d, issue #558) query: every artifact hanging off
	// one build, any state.
	if filter.BuildID != "" {
		args = append(args, filter.BuildID)
		query += fmt.Sprintf(" AND a.build_id = $%d", len(args))
	}
	// Pushed into SQL (rather than filtered client-side after the LIMIT
	// below) so it composes correctly with real pagination -- a client-side
	// filter after LIMIT pageSize+1 could drop a page below pageSize rows
	// while more matching rows exist further down the table. As of issue
	// #833 there is no stored a.promotability column left to filter on, so
	// this predicate is a hand-inlined copy of repository.DerivePromotability
	// restricted to exactly the PROMOTABLE outcome (VIA_CHART/NOT_PROMOTABLE
	// never match "promotable only", same as the old stored-column
	// comparison): chart and binary artifacts are unconditionally
	// PROMOTABLE; an image artifact is PROMOTABLE only when its owning app's
	// CURRENT deploy_unit (including the app_release_fallback tier -- see
	// artifactSelectBase's doc comment) is 'image'; firmware is never
	// PROMOTABLE. Must be kept in sync with promotability.go's
	// DerivePromotability by hand -- there are only four ArtifactKind values
	// and this rule changes rarely (see #810's history), so a shared SQL/Go
	// rule representation was not judged worth the complexity here.
	// state = 'published' is required explicitly: scanArtifact only derives
	// Promotability for published rows, and this predicate must match that
	// same nullability.
	if filter.PromotableOnly {
		query += ` AND a.state = 'published' AND (a.kind = 'binary' OR a.kind = 'chart' OR (a.kind = 'image' AND COALESCE(app_release_fallback.deploy_unit, app.deploy_unit) = 'image'))`
	}
	if pageToken != "" {
		cursorTS, cursorID, err := decodeKeysetCursor(pageToken)
		if err != nil {
			return nil, "", fmt.Errorf("list artifacts: %w", err)
		}
		// Keyset predicate for "resume strictly after (ts, id)" on an
		// ORDER BY state_changed_at DESC, artifact_id DESC query -- explicit
		// casts because the row-value comparison below gives Postgres
		// nothing else to infer $N's type from.
		args = append(args, cursorTS, cursorID)
		query += fmt.Sprintf(" AND (a.state_changed_at, a.artifact_id) < ($%d::timestamptz, $%d::uuid)", len(args)-1, len(args))
	}
	// Fetch one extra row so we can tell whether there is a next page
	// without a separate COUNT(*) query.
	args = append(args, pageSize+1)
	query += fmt.Sprintf(" ORDER BY a.state_changed_at DESC, a.artifact_id DESC LIMIT $%d", len(args))

	rows, err := r.ex.Query(ctx, query, args...)
	if err != nil {
		return nil, "", err
	}
	defer rows.Close()
	var out []repository.Artifact
	for rows.Next() {
		a, err := scanArtifact(rows)
		if err != nil {
			return nil, "", err
		}
		out = append(out, a)
	}
	if err := rows.Err(); err != nil {
		return nil, "", err
	}

	var nextPageToken string
	if int32(len(out)) > pageSize {
		// The extra row proves a next page exists; the cursor resumes
		// after the LAST row of THIS page (the pageSize-th, not the
		// extra one), which is then dropped.
		last := out[pageSize-1]
		nextPageToken = encodeKeysetCursor(last.StateChangedAt, last.ArtifactID)
		out = out[:pageSize]
	}
	return out, nextPageToken, nil
}

func (r *artifactRepo) GetArtifact(ctx context.Context, lookup repository.ArtifactLookup) (*repository.Artifact, error) {
	a, err := r.findArtifact(ctx, lookup)
	if err != nil {
		return nil, err
	}
	if a.Kind == repository.ArtifactKindChart {
		links, err := r.loadContains(ctx, a.ArtifactID)
		if err != nil {
			return nil, err
		}
		a.Contains = links
	}
	return a, nil
}

func (r *artifactRepo) findArtifact(ctx context.Context, lookup repository.ArtifactLookup) (*repository.Artifact, error) {
	var row pgx.Row
	switch {
	case lookup.ArtifactID != "":
		row = r.ex.QueryRow(ctx, artifactSelectBase+` WHERE a.artifact_id = $1`, lookup.ArtifactID)
	case lookup.Digest != "":
		row = r.ex.QueryRow(ctx, artifactSelectBase+` WHERE a.digest = $1`, lookup.Digest)
	case lookup.OwnerFullName != "" && lookup.LatestPublished:
		if lookup.BeforeVersion != "" {
			bMajor, bMinor, bPatch := parseVersionTriple(lookup.BeforeVersion)
			row = r.ex.QueryRow(ctx, artifactSelectBase+`
				WHERE a.kind = $1 AND a.state = 'published'
				  AND (app.domain || '-' || app.name = $2 OR chart.domain || '-' || chart.name = $2)
				  AND (a.version_major < $3
				       OR (a.version_major = $3 AND a.version_minor < $4)
				       OR (a.version_major = $3 AND a.version_minor = $4 AND a.version_patch < $5))
				ORDER BY a.version_major DESC, a.version_minor DESC, a.version_patch DESC
				LIMIT 1`,
				string(lookup.Kind), lookup.OwnerFullName, bMajor, bMinor, bPatch)
		} else {
			row = r.ex.QueryRow(ctx, artifactSelectBase+`
				WHERE a.kind = $1 AND a.state = 'published'
				  AND (app.domain || '-' || app.name = $2 OR chart.domain || '-' || chart.name = $2)
				ORDER BY a.version_major DESC, a.version_minor DESC, a.version_patch DESC
				LIMIT 1`,
				string(lookup.Kind), lookup.OwnerFullName)
		}
	case lookup.OwnerFullName != "" && lookup.Version != "":
		row = r.ex.QueryRow(ctx, artifactSelectBase+`
			WHERE a.kind = $1 AND a.version = $2
			  AND (app.domain || '-' || app.name = $3 OR chart.domain || '-' || chart.name = $3)`,
			string(lookup.Kind), lookup.Version, lookup.OwnerFullName)
	default:
		return nil, repository.ErrInvalidArgument
	}

	a, err := scanArtifact(row)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, repository.ErrNotFound
		}
		return nil, err
	}
	return &a, nil
}

func (r *artifactRepo) ResolveArtifact(ctx context.Context, lookup repository.ArtifactLookup) (*repository.Artifact, []repository.Artifact, []repository.Build, error) {
	a, err := r.findArtifact(ctx, lookup)
	if err != nil {
		return nil, nil, nil, err
	}
	if a.Kind != repository.ArtifactKindChart {
		return nil, nil, nil, fmt.Errorf("%w: artifact %s is not a chart", repository.ErrInvalidArgument, a.ArtifactID)
	}

	rows, err := r.ex.Query(ctx, artifactSelectBase+`
		JOIN artifact_link al ON al.image_artifact_id = a.artifact_id
		WHERE al.chart_artifact_id = $1`, a.ArtifactID)
	if err != nil {
		return nil, nil, nil, err
	}
	var images []repository.Artifact
	var buildIDs []string
	for rows.Next() {
		img, err := scanArtifact(rows)
		if err != nil {
			rows.Close()
			return nil, nil, nil, err
		}
		images = append(images, img)
		buildIDs = append(buildIDs, img.BuildID)
	}
	rows.Close()

	var builds []repository.Build
	for _, id := range buildIDs {
		b, err := (&buildRepo{ex: r.ex}).GetBuild(ctx, id)
		if err != nil {
			continue
		}
		builds = append(builds, *b)
	}

	links, err := r.loadContains(ctx, a.ArtifactID)
	if err != nil {
		return nil, nil, nil, err
	}
	a.Contains = links

	return a, images, builds, nil
}

// ListArtifactPins is ResolveArtifact's mirror image: given an image
// artifact lookup, returns the chart artifacts whose artifact_link row
// points at it.
func (r *artifactRepo) ListArtifactPins(ctx context.Context, lookup repository.ArtifactLookup) ([]repository.Artifact, error) {
	a, err := r.findArtifact(ctx, lookup)
	if err != nil {
		return nil, err
	}
	if a.Kind != repository.ArtifactKindImage {
		return nil, fmt.Errorf("%w: artifact %s is not an image", repository.ErrInvalidArgument, a.ArtifactID)
	}

	rows, err := r.ex.Query(ctx, artifactSelectBase+`
		JOIN artifact_link al ON al.chart_artifact_id = a.artifact_id
		WHERE al.image_artifact_id = $1`, a.ArtifactID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var chartArtifacts []repository.Artifact
	for rows.Next() {
		ca, err := scanArtifact(rows)
		if err != nil {
			return nil, err
		}
		chartArtifacts = append(chartArtifacts, ca)
	}
	return chartArtifacts, rows.Err()
}

// ============================================================================
// AllocateVersion (AR-5a; AR-7b re-homed storage into `artifact` itself)
// ============================================================================

// AllocateVersion implements repository.ArtifactRepository.AllocateVersion.
// As of migration 007 (AR-7b) this writes the ∅ -> allocated `artifact` row
// directly, rather than to the now-dropped `version_allocation` table --
// see ARCHITECTURE.md "Artifact lifecycle" and migration 007's comments.
// See server/handlers/artifact.go's AllocateVersion for the retry loop this
// is designed to be called from (a unique-constraint collision here aborts
// the surrounding transaction; the caller must retry in a fresh one).
func (r *artifactRepo) AllocateVersion(ctx context.Context, kind repository.ArtifactKind, ownerID, repo, increment, explicitVersion string) (*repository.VersionAllocation, error) {
	// "Next" is computed from the highest version already claimed for this
	// owner+kind against `artifact` alone -- this used to be a UNION with
	// the separate version_allocation table (AR-5a); now that an
	// "allocated" row IS an artifact row, one query covers both
	// already-published versions and reserved-but-not-yet-published ones,
	// so a version reserved a moment ago by a concurrent caller is still
	// never handed out twice.
	row := r.ex.QueryRow(ctx, `
		SELECT version, version_major, version_minor, version_patch
		  FROM artifact WHERE owner_id = $1 AND kind = $2
		 ORDER BY version_major DESC, version_minor DESC, version_patch DESC
		 LIMIT 1`, ownerID, string(kind))

	var previousVersion string
	var curMajor, curMinor, curPatch int
	hasPrevious := true
	switch err := row.Scan(&previousVersion, &curMajor, &curMinor, &curPatch); {
	case errors.Is(err, pgx.ErrNoRows):
		hasPrevious = false
	case err != nil:
		return nil, fmt.Errorf("determine current version for allocation: %w", err)
	}

	var next semver.Version
	if explicitVersion != "" {
		v, perr := semver.ParseRelease(explicitVersion)
		if perr != nil {
			return nil, fmt.Errorf("%w: %v", repository.ErrInvalidArgument, perr)
		}
		next = v
	} else {
		base := semver.Version{}
		if hasPrevious {
			base = semver.Version{Major: curMajor, Minor: curMinor, Patch: curPatch}
		}
		v, ierr := base.Increment(increment)
		if ierr != nil {
			return nil, fmt.Errorf("%w: %v", repository.ErrInvalidArgument, ierr)
		}
		next = v
	}

	versionStr := next.String()
	a := repository.Artifact{Kind: kind, Repository: repo, Version: versionStr}
	if kind != repository.ArtifactKindChart {
		a.AppID = ownerID
	} else {
		a.ChartID = ownerID
	}
	if _, _, err := r.insertArtifact(ctx, a, nil, repository.ArtifactStateAllocated, repository.VersionSourceRegistry, repository.ArtifactProvenanceObserved); err != nil {
		return nil, err
	}

	return &repository.VersionAllocation{Version: versionStr, PreviousVersion: previousVersion}, nil
}
