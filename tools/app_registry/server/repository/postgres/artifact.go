package postgres

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/whale-net/everything/tools/app_registry/server/repository"
	appmetapb "github.com/whale-net/everything/tools/appmeta/proto"
)

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
const artifactSelectBase = `
	SELECT a.artifact_id, a.kind, a.app_id, a.chart_id, a.repository, a.version, a.digest, a.build_id, a.published_at,
	       COALESCE(app.deploy_unit, chart.deploy_unit) AS owner_deploy_unit
	FROM artifact a
	LEFT JOIN app ON a.app_id = app.app_id
	LEFT JOIN chart ON a.chart_id = chart.chart_id`

func scanArtifact(row pgx.Row) (repository.Artifact, error) {
	var a repository.Artifact
	var kind string
	var appID, chartID *string
	var ownerDeployUnit *string
	if err := row.Scan(&a.ArtifactID, &kind, &appID, &chartID, &a.Repository, &a.Version, &a.Digest, &a.BuildID, &a.PublishedAt, &ownerDeployUnit); err != nil {
		return repository.Artifact{}, err
	}
	a.Kind = repository.ArtifactKind(kind)
	if appID != nil {
		a.AppID = *appID
	}
	if chartID != nil {
		a.ChartID = *chartID
	}
	var du appmetapb.DeployUnit
	if ownerDeployUnit != nil {
		du = deployUnitFromDB(*ownerDeployUnit)
	}
	a.Promotability = repository.DerivePromotability(a.Kind, du)
	return a, nil
}

func (r *artifactRepo) RecordArtifact(ctx context.Context, a repository.Artifact, contains []repository.ContainedImageInput) (*repository.Artifact, bool, error) {
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

	a.ArtifactID = uuid.NewString()
	if a.PublishedAt.IsZero() {
		a.PublishedAt = time.Now().UTC()
	}

	var appID, chartID any
	if a.Kind == repository.ArtifactKindImage {
		appID = a.AppID
	} else {
		chartID = a.ChartID
	}
	if _, err := r.ex.Exec(ctx, `
		INSERT INTO artifact (artifact_id, kind, app_id, chart_id, repository, version, digest, build_id, published_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)`,
		a.ArtifactID, string(a.Kind), appID, chartID, a.Repository, a.Version, a.Digest, a.BuildID, a.PublishedAt); err != nil {
		return nil, false, fmt.Errorf("record artifact %s: %w", a.Digest, err)
	}

	if a.Kind == repository.ArtifactKindChart {
		links := make([]repository.ArtifactLink, 0, len(contains))
		for _, ci := range contains {
			imgRow := r.ex.QueryRow(ctx, `SELECT artifact_id, app_id FROM artifact WHERE digest = $1 AND kind = 'image'`, ci.Digest)
			var imgArtifactID string
			var imgAppID *string
			if err := imgRow.Scan(&imgArtifactID, &imgAppID); err != nil {
				if errors.Is(err, pgx.ErrNoRows) {
					return nil, false, fmt.Errorf("%w: chart pins unrecorded image digest %s", repository.ErrInvalidArgument, ci.Digest)
				}
				return nil, false, err
			}
			imgApp := ""
			if imgAppID != nil {
				imgApp = *imgAppID
			}
			if _, err := r.ex.Exec(ctx, `
				INSERT INTO artifact_link (chart_artifact_id, image_artifact_id, app_id, repository, version, digest)
				VALUES ($1, $2, $3, $4, $5, $6)`,
				a.ArtifactID, imgArtifactID, imgApp, ci.Repository, ci.Version, ci.Digest); err != nil {
				return nil, false, fmt.Errorf("link chart artifact %s to image %s: %w", a.ArtifactID, ci.Digest, err)
			}
			links = append(links, repository.ArtifactLink{
				ImageArtifactID: imgArtifactID, AppID: imgApp,
				Repository: ci.Repository, Version: ci.Version, Digest: ci.Digest,
			})
		}
		a.Contains = links
	}

	// Re-derive promotability against the freshly-read owner deploy_unit,
	// rather than trusting whatever the caller set on the input struct.
	out, err := r.GetArtifact(ctx, repository.ArtifactLookup{ArtifactID: a.ArtifactID})
	if err != nil {
		return nil, false, err
	}
	return out, false, nil
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
