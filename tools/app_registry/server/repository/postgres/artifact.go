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

// ============================================================================
// ArtifactRepository
// ============================================================================

type artifactRepo struct{ ex dbtx }

// artifactRow is what every artifact SELECT returns: the artifact plus the
// owning app's/chart's deploy_unit, which is all repository.DerivePromotability
// needs. Promotability is never persisted — see ARCHITECTURE.md "Promotability".
// digest/build_id/published_at are nullable as of migration 007 (AR-7b) —
// an "allocated" row has neither, a "publishing" row has a build but no
// digest — so they scan into pointers and get zero-valued below.
const artifactSelectBase = `
	SELECT a.artifact_id, a.kind, a.app_id, a.chart_id, a.repository, a.version, a.digest, a.build_id, a.published_at,
	       a.state, a.provenance, a.version_source, a.state_changed_at, a.fail_reason,
	       COALESCE(app.deploy_unit, chart.deploy_unit) AS owner_deploy_unit
	FROM artifact a
	LEFT JOIN app ON a.app_id = app.app_id
	LEFT JOIN chart ON a.chart_id = chart.chart_id`

func scanArtifact(row pgx.Row) (repository.Artifact, error) {
	var a repository.Artifact
	var kind, state, provenance, versionSource string
	var appID, chartID, digest, buildID *string
	var publishedAt *time.Time
	var ownerDeployUnit *string
	if err := row.Scan(
		&a.ArtifactID, &kind, &appID, &chartID, &a.Repository, &a.Version, &digest, &buildID, &publishedAt,
		&state, &provenance, &versionSource, &a.StateChangedAt, &a.FailReason,
		&ownerDeployUnit,
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
	var du appmetapb.DeployUnit
	if ownerDeployUnit != nil {
		du = deployUnitFromDB(*ownerDeployUnit)
	}
	a.Promotability = repository.DerivePromotability(a.Kind, du)
	return a, nil
}

// RecordArtifact implements repository.ArtifactRepository.RecordArtifact —
// the publishing -> published transition, plus (at adoption stage
// "observe" only) the backward-compatible direct-create path. See the
// interface doc comment for the full state-machine contract.
func (r *artifactRepo) RecordArtifact(ctx context.Context, a repository.Artifact, contains []repository.ContainedImageInput, domainStage repository.DomainAdoptionStage) (*repository.Artifact, bool, error) {
	// 1. Idempotent replay: an existing row with this EXACT digest. Only a
	// "published" row can ever match here (digest is NULL on every other
	// state, per migration 007's artifact_state_shape CHECK), so this is
	// unambiguously "already recorded", regardless of adoption stage.
	row := r.ex.QueryRow(ctx, artifactSelectBase+` WHERE a.digest = $1`, a.Digest)
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
		// No row at all for this (owner, kind, version). Creating directly
		// as "published" is the pre-AR-7b behaviour, kept only for domains
		// still at adoption stage "observe" -- see ARCHITECTURE.md
		// "Backward compatibility during rollout" and the interface doc
		// comment. At any later stage BeginPublish must have run first.
		if domainStage != repository.DomainAdoptionStageObserve {
			return nil, false, fmt.Errorf("%w: no publishing artifact found for %s %s -- BeginPublish must run before RecordArtifact once a domain has left adoption stage \"observe\"",
				repository.ErrFailedPrecondition, r.ownerFullName(ctx, a), a.Version)
		}
		out, _, err := r.insertArtifact(ctx, a, contains, repository.ArtifactStatePublished, repository.VersionSourceTag)
		return out, false, err
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
	if a.Kind == repository.ArtifactKindImage {
		return a.AppID
	}
	return a.ChartID
}

// insertArtifact writes a brand-new artifact row directly in state, with
// versionSource and Provenance "observed" (the only provenance this phase
// ever writes -- see ArtifactProvenance's doc comment). digest/build_id/
// published_at are written NULL when the corresponding field on a is empty
// -- correct for "allocated" (neither) and "publishing" (build_id only);
// contains is only consulted when state == published (RecordArtifact's
// direct-create path); BeginPublish and AllocateVersion always pass nil.
// Shared by RecordArtifact's direct-create path, BeginPublish's ∅ ->
// publishing branch, and AllocateVersion's ∅ -> allocated write.
func (r *artifactRepo) insertArtifact(ctx context.Context, a repository.Artifact, contains []repository.ContainedImageInput, state repository.ArtifactState, versionSource repository.VersionSource) (*repository.Artifact, bool, error) {
	a.ArtifactID = uuid.NewString()
	a.State = state
	a.Provenance = repository.ArtifactProvenanceObserved
	a.VersionSource = versionSource
	a.StateChangedAt = time.Now().UTC()
	if state == repository.ArtifactStatePublished && a.PublishedAt.IsZero() {
		a.PublishedAt = time.Now().UTC()
	}

	var appID, chartID any
	if a.Kind == repository.ArtifactKindImage {
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

	if _, err := r.ex.Exec(ctx, `
		INSERT INTO artifact (artifact_id, kind, app_id, chart_id, repository, version, digest, build_id, published_at,
		                       version_major, version_minor, version_patch, state, provenance, version_source, state_changed_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16)`,
		a.ArtifactID, string(a.Kind), appID, chartID, a.Repository, a.Version, digest, buildID, publishedAt,
		versionMajor, versionMinor, versionPatch, string(a.State), string(a.Provenance), string(a.VersionSource), a.StateChangedAt); err != nil {
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

	// Re-derive promotability against the freshly-read owner deploy_unit,
	// rather than trusting whatever the caller set on the input struct.
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
	if _, err := r.ex.Exec(ctx, `
		UPDATE artifact SET digest = $1, build_id = $2, published_at = $3, state = 'published', state_changed_at = $4
		WHERE artifact_id = $5`,
		a.Digest, buildID, publishedAt, now, existing.ArtifactID); err != nil {
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
		// ∅ -> publishing: the pre-cutover path (no prior AllocateVersion
		// call for this version) -- see ARCHITECTURE.md "Backward
		// compatibility during rollout".
		if repositoryHint == "" {
			return nil, fmt.Errorf("%w: repository is required to begin publishing %s %s with no prior allocation", repository.ErrInvalidArgument, string(kind), version)
		}
		a := repository.Artifact{Kind: kind, Repository: repositoryHint, Version: version, BuildID: buildID}
		if kind == repository.ArtifactKindImage {
			a.AppID = ownerID
		} else {
			a.ChartID = ownerID
		}
		out, _, err := r.insertArtifact(ctx, a, nil, repository.ArtifactStatePublishing, versionSource)
		return out, err
	case everr != nil:
		return nil, everr
	}

	if existing.State != repository.ArtifactStateAllocated && existing.State != repository.ArtifactStateFailed {
		return nil, fmt.Errorf("%w: artifact %s %s is %q, not \"allocated\" or \"failed\" -- BeginPublish cannot start from here",
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
	if a.Kind == repository.ArtifactKindImage {
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

func (r *artifactRepo) ListArtifacts(ctx context.Context, filter repository.ArtifactListFilter) ([]repository.Artifact, error) {
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
	query += " ORDER BY a.published_at"

	rows, err := r.ex.Query(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []repository.Artifact
	for rows.Next() {
		a, err := scanArtifact(rows)
		if err != nil {
			return nil, err
		}
		if filter.PromotableOnly && a.Promotability != repository.PromotabilityPromotable {
			continue
		}
		out = append(out, a)
	}
	return out, rows.Err()
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
	case lookup.OwnerFullName != "":
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
	if kind == repository.ArtifactKindImage {
		a.AppID = ownerID
	} else {
		a.ChartID = ownerID
	}
	if _, _, err := r.insertArtifact(ctx, a, nil, repository.ArtifactStateAllocated, repository.VersionSourceRegistry); err != nil {
		return nil, err
	}

	return &repository.VersionAllocation{Version: versionStr, PreviousVersion: previousVersion}, nil
}
