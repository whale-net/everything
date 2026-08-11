package postgres

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/whale-net/everything/tools/app_registry/server/repository"
	appmetapb "github.com/whale-net/everything/tools/appmeta/proto"
)

type appRepo struct{ ex dbtx }

const appColumns = `app_id, domain, name, description, language, app_type, deploy_unit, bazel_label, image_repository, status, first_seen_at, last_seen_at`

func scanApp(row pgx.Row) (repository.App, error) {
	var a repository.App
	var deployUnit, status string
	if err := row.Scan(&a.AppID, &a.Domain, &a.Name, &a.Description, &a.Language, &a.AppType, &deployUnit, &a.BazelLabel, &a.ImageRepository, &status, &a.FirstSeenAt, &a.LastSeenAt); err != nil {
		return repository.App{}, err
	}
	a.DeployUnit = deployUnitFromDB(deployUnit)
	a.Status = repository.Status(status)
	return a, nil
}

const chartColumns = `chart_id, domain, name, description, chart_repository, deploy_unit, status, first_seen_at, last_seen_at`

func scanChart(row pgx.Row) (repository.Chart, error) {
	var c repository.Chart
	var deployUnit, status string
	if err := row.Scan(&c.ChartID, &c.Domain, &c.Name, &c.Description, &c.ChartRepository, &deployUnit, &status, &c.FirstSeenAt, &c.LastSeenAt); err != nil {
		return repository.Chart{}, err
	}
	c.DeployUnit = deployUnitFromDB(deployUnit)
	c.Status = repository.Status(status)
	return c, nil
}

func deployUnitFromDB(s string) appmetapb.DeployUnit {
	switch s {
	case "chart":
		return appmetapb.DeployUnit_DEPLOY_UNIT_CHART
	case "image":
		return appmetapb.DeployUnit_DEPLOY_UNIT_IMAGE
	case "none":
		return appmetapb.DeployUnit_DEPLOY_UNIT_NONE
	default:
		return appmetapb.DeployUnit_DEPLOY_UNIT_UNSPECIFIED
	}
}

func deployUnitToDB(du appmetapb.DeployUnit) string {
	switch du {
	case appmetapb.DeployUnit_DEPLOY_UNIT_IMAGE:
		return "image"
	case appmetapb.DeployUnit_DEPLOY_UNIT_NONE:
		return "none"
	default:
		return "chart"
	}
}

// ============================================================================
// Reconcile
// ============================================================================

func (r *appRepo) Reconcile(ctx context.Context, apps []*appmetapb.AppManifest, charts []*appmetapb.ChartManifest, source repository.ReconcileSource, dryRun bool) (*repository.ReconcileResult, error) {
	now := time.Now().UTC()
	result := &repository.ReconcileResult{}

	// Watermark check -- see ARCHITECTURE.md "Reconcile watermark" and
	// watermark.go's ShouldApplyReconcile. A dry run never consults or
	// advances the watermark: it writes nothing, so there is nothing to
	// guard. `SELECT ... FOR UPDATE` locks the singleton row for the
	// remainder of this transaction, so a second concurrent Reconcile call
	// blocks here until this one commits or rolls back -- that is what
	// makes two racing callers serialize instead of both reading the same
	// stale watermark.
	if !dryRun {
		current, err := r.getReconcileWatermark(ctx)
		if err != nil {
			return nil, fmt.Errorf("reconcile: read watermark: %w", err)
		}
		if !repository.ShouldApplyReconcile(source, current) {
			result.SkippedStale = true
			result.CurrentWatermarkGitSHA = current.GitSHA
			return result, nil
		}
	}

	presentAppIDs := map[string]bool{}
	appIDByKey := map[string]string{} // "domain|name" -> app_id, for chart resolution

	for _, am := range apps {
		existing, err := r.getAppByDomainName(ctx, am.Domain, am.Name)
		if err != nil && !errors.Is(err, repository.ErrNotFound) {
			return nil, fmt.Errorf("reconcile: look up app %s/%s: %w", am.Domain, am.Name, err)
		}

		if existing == nil {
			appID := ""
			if !dryRun {
				appID = uuid.NewString()
				_, err := r.ex.Exec(ctx, `
					INSERT INTO app (app_id, domain, name, description, language, app_type, deploy_unit, bazel_label, image_repository, status, first_seen_at, last_seen_at)
					VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, 'active', $10, $10)`,
					appID, am.Domain, am.Name, am.Description, am.Language, am.AppType, deployUnitToDB(am.DeployUnit), am.BinaryTarget, imageRepository(am), now)
				if err != nil {
					if de, ok := translatePgError(err, fmt.Sprintf("app %s-%s already recorded", am.Domain, am.Name)); ok {
						return nil, de
					}
					return nil, fmt.Errorf("reconcile: insert app %s/%s: %w", am.Domain, am.Name, err)
				}
			}
			newApp := repository.App{
				AppID: appID, Domain: am.Domain, Name: am.Name, Description: am.Description,
				Language: am.Language, AppType: am.AppType, DeployUnit: normalizeDeployUnit(am.DeployUnit),
				BazelLabel: am.BinaryTarget, ImageRepository: imageRepository(am),
				Status: repository.StatusActive, FirstSeenAt: now, LastSeenAt: now,
			}
			result.CreatedApps = append(result.CreatedApps, newApp)
			if appID != "" {
				presentAppIDs[appID] = true
				appIDByKey[am.Domain+"|"+am.Name] = appID
			}
			continue
		}

		wasMissing := existing.Status == repository.StatusMissing
		updated := *existing
		updated.Description = am.Description
		updated.Language = am.Language
		updated.AppType = am.AppType
		updated.DeployUnit = normalizeDeployUnit(am.DeployUnit)
		updated.BazelLabel = am.BinaryTarget
		updated.ImageRepository = imageRepository(am)
		updated.Status = repository.StatusActive
		updated.LastSeenAt = now

		if !dryRun {
			_, err := r.ex.Exec(ctx, `
				UPDATE app SET description=$2, language=$3, app_type=$4, deploy_unit=$5, bazel_label=$6, image_repository=$7, status='active', last_seen_at=$8
				WHERE app_id=$1`,
				existing.AppID, updated.Description, updated.Language, updated.AppType, deployUnitToDB(updated.DeployUnit), updated.BazelLabel, updated.ImageRepository, now)
			if err != nil {
				return nil, fmt.Errorf("reconcile: update app %s/%s: %w", am.Domain, am.Name, err)
			}
		}
		presentAppIDs[existing.AppID] = true
		appIDByKey[am.Domain+"|"+am.Name] = existing.AppID

		if wasMissing {
			result.RecoveredApps = append(result.RecoveredApps, updated)
		} else {
			result.UpdatedApps = append(result.UpdatedApps, updated)
		}
	}

	// Anything active-and-absent becomes MISSING. last_seen_at is
	// deliberately left untouched — SetAppStatus's "present in the latest
	// reconcile" check compares it against the table-wide max.
	rows, err := r.ex.Query(ctx, `SELECT `+appColumns+` FROM app WHERE status = 'active'`)
	if err != nil {
		return nil, fmt.Errorf("reconcile: scan active apps: %w", err)
	}
	var toMarkMissing []repository.App
	for rows.Next() {
		a, err := scanApp(rows)
		if err != nil {
			rows.Close()
			return nil, fmt.Errorf("reconcile: scan active apps: %w", err)
		}
		if !presentAppIDs[a.AppID] {
			toMarkMissing = append(toMarkMissing, a)
		}
	}
	rows.Close()
	for _, a := range toMarkMissing {
		if !dryRun {
			if _, err := r.ex.Exec(ctx, `UPDATE app SET status = 'missing' WHERE app_id = $1`, a.AppID); err != nil {
				return nil, fmt.Errorf("reconcile: mark app %s missing: %w", a.FullName(), err)
			}
		}
		a.Status = repository.StatusMissing
		result.NewlyMissingApps = append(result.NewlyMissingApps, a)
	}

	presentChartIDs := map[string]bool{}
	for _, cm := range charts {
		appIDs, err := r.resolveChartApps(ctx, appIDByKey, cm)
		if err != nil {
			var re *chartResolutionError
			if !errors.As(err, &re) {
				// A real infrastructure error (failed query, etc) -- abort
				// the whole reconcile, unchanged from pre-AR-7a behavior.
				return nil, err
			}
			// AR-7a: this chart's apps didn't all resolve. Report it and
			// skip it -- every other app/chart in this call still applies,
			// and the watermark still advances (see Reconcile's doc comment
			// on repository.AppRepository and ARCHITECTURE.md "AssertApps
			// (additive) vs. ReconcileApps"). Deliberately NOT marked
			// MISSING as a side effect: if this chart already exists, add
			// its id to presentChartIDs so the absence sweep below leaves it
			// alone -- a chart present in the manifest set but unresolvable
			// is present, not absent. If it doesn't exist yet, there is
			// nothing to mark missing either way.
			result.UnresolvedCharts = append(result.UnresolvedCharts, repository.UnresolvedChart{
				Domain: cm.Domain, Name: cm.Name, AppRefs: re.offending, Reason: re.reason,
			})
			if existingChart, lookErr := r.getChartByDomainName(ctx, cm.Domain, cm.Name); lookErr == nil {
				presentChartIDs[existingChart.ChartID] = true
			} else if !errors.Is(lookErr, repository.ErrNotFound) {
				return nil, fmt.Errorf("reconcile: look up chart %s/%s after unresolved apps: %w", cm.Domain, cm.Name, lookErr)
			}
			continue
		}

		existing, err := r.getChartByDomainName(ctx, cm.Domain, cm.Name)
		if err != nil && !errors.Is(err, repository.ErrNotFound) {
			return nil, fmt.Errorf("reconcile: look up chart %s/%s: %w", cm.Domain, cm.Name, err)
		}

		if existing == nil {
			chartID := ""
			if !dryRun {
				chartID = uuid.NewString()
				if _, err := r.ex.Exec(ctx, `
					INSERT INTO chart (chart_id, domain, name, deploy_unit, status, first_seen_at, last_seen_at)
					VALUES ($1, $2, $3, 'chart', 'active', $4, $4)`,
					chartID, cm.Domain, cm.Name, now); err != nil {
					if de, ok := translatePgError(err, fmt.Sprintf("chart %s-%s already recorded", cm.Domain, cm.Name)); ok {
						return nil, de
					}
					return nil, fmt.Errorf("reconcile: insert chart %s/%s: %w", cm.Domain, cm.Name, err)
				}
				if err := r.setChartApps(ctx, chartID, appIDs); err != nil {
					return nil, err
				}
			}
			newChart := repository.Chart{
				ChartID: chartID, Domain: cm.Domain, Name: cm.Name, DeployUnit: appmetapb.DeployUnit_DEPLOY_UNIT_CHART,
				Status: repository.StatusActive, AppIDs: appIDs, FirstSeenAt: now, LastSeenAt: now,
			}
			result.CreatedCharts = append(result.CreatedCharts, newChart)
			if chartID != "" {
				presentChartIDs[chartID] = true
			}
			continue
		}

		wasMissing := existing.Status == repository.StatusMissing
		updated := *existing
		updated.AppIDs = appIDs
		updated.Status = repository.StatusActive
		updated.LastSeenAt = now

		if !dryRun {
			if _, err := r.ex.Exec(ctx, `UPDATE chart SET status='active', last_seen_at=$2 WHERE chart_id=$1`, existing.ChartID, now); err != nil {
				return nil, fmt.Errorf("reconcile: update chart %s/%s: %w", cm.Domain, cm.Name, err)
			}
			if err := r.setChartApps(ctx, existing.ChartID, appIDs); err != nil {
				return nil, err
			}
		}
		presentChartIDs[existing.ChartID] = true

		if wasMissing {
			result.RecoveredCharts = append(result.RecoveredCharts, updated)
		} else {
			result.UpdatedCharts = append(result.UpdatedCharts, updated)
		}
	}

	crows, err := r.ex.Query(ctx, `SELECT `+chartColumns+` FROM chart WHERE status = 'active'`)
	if err != nil {
		return nil, fmt.Errorf("reconcile: scan active charts: %w", err)
	}
	var chartsToMarkMissing []repository.Chart
	for crows.Next() {
		c, err := scanChart(crows)
		if err != nil {
			crows.Close()
			return nil, fmt.Errorf("reconcile: scan active charts: %w", err)
		}
		if !presentChartIDs[c.ChartID] {
			chartsToMarkMissing = append(chartsToMarkMissing, c)
		}
	}
	crows.Close()
	for _, c := range chartsToMarkMissing {
		if !dryRun {
			if _, err := r.ex.Exec(ctx, `UPDATE chart SET status = 'missing' WHERE chart_id = $1`, c.ChartID); err != nil {
				return nil, fmt.Errorf("reconcile: mark chart %s missing: %w", c.FullName(), err)
			}
		}
		c.Status = repository.StatusMissing
		result.NewlyMissingCharts = append(result.NewlyMissingCharts, c)
	}

	if !dryRun {
		if err := r.advanceReconcileWatermark(ctx, source, now); err != nil {
			return nil, fmt.Errorf("reconcile: advance watermark: %w", err)
		}
	}

	return result, nil
}

// getReconcileWatermark reads and locks the singleton reconcile_watermark
// row (migration 006) with FOR UPDATE, so the read-compare-write sequence
// in Reconcile is atomic with respect to a concurrent caller doing the
// same. Returns nil only if the row is somehow absent -- migration 006
// seeds exactly one row and nothing in this package ever deletes it, so
// this is a defensive fallback, not the expected "no watermark yet" signal;
// that signal is GitSHA == "" on a real row -- see
// repository.ShouldApplyReconcile's doc comment.
func (r *appRepo) getReconcileWatermark(ctx context.Context) (*repository.ReconcileWatermark, error) {
	row := r.ex.QueryRow(ctx, `
		SELECT git_sha, source_committed_at, discovered_at, updated_at
		FROM reconcile_watermark WHERE id = 1 FOR UPDATE`)
	var w repository.ReconcileWatermark
	if err := row.Scan(&w.GitSHA, &w.SourceCommittedAt, &w.DiscoveredAt, &w.UpdatedAt); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	return &w, nil
}

// advanceReconcileWatermark overwrites the singleton row with source's
// ordering metadata. Always an UPDATE in practice (migration 006 seeds the
// row), but written as an upsert so this stays correct even if the seed row
// were ever missing.
func (r *appRepo) advanceReconcileWatermark(ctx context.Context, source repository.ReconcileSource, now time.Time) error {
	_, err := r.ex.Exec(ctx, `
		INSERT INTO reconcile_watermark (id, git_sha, source_committed_at, discovered_at, updated_at)
		VALUES (1, $1, $2, $3, $4)
		ON CONFLICT (id) DO UPDATE SET
			git_sha = EXCLUDED.git_sha,
			source_committed_at = EXCLUDED.source_committed_at,
			discovered_at = EXCLUDED.discovered_at,
			updated_at = EXCLUDED.updated_at`,
		source.GitSHA, source.SourceCommittedAt, source.DiscoveredAt, now)
	return err
}

// setChartApps rebuilds chart_app for chartID from scratch.
func (r *appRepo) setChartApps(ctx context.Context, chartID string, appIDs []string) error {
	if _, err := r.ex.Exec(ctx, `DELETE FROM chart_app WHERE chart_id = $1`, chartID); err != nil {
		return fmt.Errorf("reconcile: clear chart_app for %s: %w", chartID, err)
	}
	for _, appID := range appIDs {
		if _, err := r.ex.Exec(ctx, `INSERT INTO chart_app (chart_id, app_id) VALUES ($1, $2)`, chartID, appID); err != nil {
			return fmt.Errorf("reconcile: insert chart_app (%s, %s): %w", chartID, appID, err)
		}
	}
	return nil
}

// chartResolutionError carries every app reference in one chart manifest
// that failed to resolve, so Reconcile's caller can build a precise
// ReconcileResult.UnresolvedCharts entry and skip only this chart -- AR-7a.
// Deliberately distinct from the plain errors resolveChartApps returns for a
// real infrastructure failure (a failed query): only THIS type is downgraded
// to a per-chart skip; anything else still aborts the whole Reconcile
// transaction, unchanged from pre-AR-7a behavior. See Reconcile's call site.
type chartResolutionError struct {
	offending []string // app refs that failed, exactly as they appeared in the manifest
	reason    string
}

func (e *chartResolutionError) Error() string { return e.reason }

// resolveChartApps resolves a chart manifest's app references to app_ids.
//
// Prefers cm.AppRefs, domain-qualified "<domain>/<name>" strings emitted by
// tools/bazel/release.bzl's helm_chart_metadata rule -- each reference
// carries its own domain, so it can never be ambiguous. Falls back to the
// DEPRECATED cm.Apps (bare names) only when AppRefs is empty, for one
// release cycle's backward compatibility with a chart metadata JSON built
// before AR-7a; same-domain matches are preferred there (checked first
// against appIDByKey, which includes apps created/updated earlier in this
// same reconcile call), and a cross-domain match is accepted only if unique.
// See ChartManifest.apps's doc comment in appmeta.proto and PLAN.md's AR-7a
// for the removal pointer.
//
// AR-7a: an unresolved reference no longer fails the whole reconcile. Every
// unresolved reference for this chart is collected and returned together as
// a *chartResolutionError; the caller downgrades that to a per-chart skip. A
// genuine query failure is returned as a plain error and still aborts the
// whole Reconcile, unchanged.
func (r *appRepo) resolveChartApps(ctx context.Context, appIDByKey map[string]string, cm *appmetapb.ChartManifest) ([]string, error) {
	qualified := len(cm.AppRefs) > 0
	refs := cm.AppRefs
	if !qualified {
		refs = cm.Apps
	}

	ids := make([]string, 0, len(refs))
	var offending, reasons []string

	for _, ref := range refs {
		if qualified {
			domain, name, ok := strings.Cut(ref, "/")
			if !ok {
				offending = append(offending, ref)
				reasons = append(reasons, fmt.Sprintf("app_ref %q is not domain-qualified", ref))
				continue
			}
			if id, ok := appIDByKey[domain+"|"+name]; ok {
				ids = append(ids, id)
				continue
			}
			existing, err := r.getAppByDomainName(ctx, domain, name)
			if err != nil {
				if errors.Is(err, repository.ErrNotFound) {
					offending = append(offending, ref)
					reasons = append(reasons, fmt.Sprintf("app_ref %q not found", ref))
					continue
				}
				return nil, fmt.Errorf("reconcile: resolve chart app_ref %q: %w", ref, err)
			}
			ids = append(ids, existing.AppID)
			continue
		}

		// DEPRECATED bare-name path -- see doc comment above.
		name := ref
		if id, ok := appIDByKey[cm.Domain+"|"+name]; ok {
			ids = append(ids, id)
			continue
		}

		rows, err := r.ex.Query(ctx, `SELECT app_id FROM app WHERE name = $1`, name)
		if err != nil {
			return nil, fmt.Errorf("reconcile: resolve chart app %q: %w", name, err)
		}
		var matches []string
		for rows.Next() {
			var id string
			if err := rows.Scan(&id); err != nil {
				rows.Close()
				return nil, fmt.Errorf("reconcile: resolve chart app %q: %w", name, err)
			}
			matches = append(matches, id)
		}
		rows.Close()

		switch len(matches) {
		case 0:
			offending = append(offending, name)
			reasons = append(reasons, fmt.Sprintf("unknown app %q", name))
		case 1:
			ids = append(ids, matches[0])
		default:
			offending = append(offending, name)
			reasons = append(reasons, fmt.Sprintf("app name %q is ambiguous across domains", name))
		}
	}

	if len(offending) > 0 {
		return nil, &chartResolutionError{
			offending: offending,
			reason:    fmt.Sprintf("chart %s/%s: %s", cm.Domain, cm.Name, strings.Join(reasons, "; ")),
		}
	}
	return ids, nil
}

func normalizeDeployUnit(du appmetapb.DeployUnit) appmetapb.DeployUnit {
	if du == appmetapb.DeployUnit_DEPLOY_UNIT_UNSPECIFIED {
		return appmetapb.DeployUnit_DEPLOY_UNIT_CHART
	}
	return du
}

func imageRepository(am *appmetapb.AppManifest) string {
	if am.Registry == "" && am.Organization == "" && am.RepoName == "" {
		return ""
	}
	return am.Registry + "/" + am.Organization + "/" + am.RepoName
}

// ============================================================================
// Reads
// ============================================================================

func (r *appRepo) getAppByDomainName(ctx context.Context, domain, name string) (*repository.App, error) {
	row := r.ex.QueryRow(ctx, `SELECT `+appColumns+` FROM app WHERE domain = $1 AND name = $2`, domain, name)
	a, err := scanApp(row)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, repository.ErrNotFound
		}
		return nil, err
	}
	return &a, nil
}

func (r *appRepo) getChartByDomainName(ctx context.Context, domain, name string) (*repository.Chart, error) {
	row := r.ex.QueryRow(ctx, `SELECT `+chartColumns+` FROM chart WHERE domain = $1 AND name = $2`, domain, name)
	c, err := scanChart(row)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, repository.ErrNotFound
		}
		return nil, err
	}
	if ids, err := r.chartAppIDs(ctx, c.ChartID); err == nil {
		c.AppIDs = ids
	}
	return &c, nil
}

func (r *appRepo) chartAppIDs(ctx context.Context, chartID string) ([]string, error) {
	rows, err := r.ex.Query(ctx, `SELECT app_id FROM chart_app WHERE chart_id = $1`, chartID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

func (r *appRepo) ListApps(ctx context.Context, filter repository.AppListFilter) ([]repository.App, error) {
	statuses := filter.Statuses
	if len(statuses) == 0 {
		statuses = []repository.Status{repository.StatusActive, repository.StatusMissing}
	}
	statusStrs := make([]string, len(statuses))
	for i, s := range statuses {
		statusStrs[i] = string(s)
	}

	query := `SELECT ` + appColumns + ` FROM app WHERE status = ANY($1)`
	args := []any{statusStrs}
	if filter.Domain != "" {
		args = append(args, filter.Domain)
		query += fmt.Sprintf(" AND domain = $%d", len(args))
	}
	if filter.DeployUnit != appmetapb.DeployUnit_DEPLOY_UNIT_UNSPECIFIED {
		args = append(args, deployUnitToDB(filter.DeployUnit))
		query += fmt.Sprintf(" AND deploy_unit = $%d", len(args))
	}
	query += " ORDER BY domain, name"

	rows, err := r.ex.Query(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []repository.App
	for rows.Next() {
		a, err := scanApp(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

func (r *appRepo) GetAppByID(ctx context.Context, appID string) (*repository.App, error) {
	row := r.ex.QueryRow(ctx, `SELECT `+appColumns+` FROM app WHERE app_id = $1`, appID)
	a, err := scanApp(row)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, repository.ErrNotFound
		}
		return nil, err
	}
	return &a, nil
}

func (r *appRepo) GetAppByFullName(ctx context.Context, fullName string) (*repository.App, error) {
	row := r.ex.QueryRow(ctx, `SELECT `+appColumns+` FROM app WHERE domain || '-' || name = $1`, fullName)
	a, err := scanApp(row)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, repository.ErrNotFound
		}
		return nil, err
	}
	return &a, nil
}

func (r *appRepo) ChartsForApp(ctx context.Context, appID string) ([]repository.Chart, error) {
	rows, err := r.ex.Query(ctx, `
		SELECT `+chartColumns+` FROM chart c
		JOIN chart_app ca ON ca.chart_id = c.chart_id
		WHERE ca.app_id = $1
		ORDER BY c.domain, c.name`, appID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []repository.Chart
	for rows.Next() {
		c, err := scanChart(rows)
		if err != nil {
			return nil, err
		}
		if ids, err := r.chartAppIDs(ctx, c.ChartID); err == nil {
			c.AppIDs = ids
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

func (r *appRepo) ListCharts(ctx context.Context, filter repository.ChartListFilter) ([]repository.Chart, error) {
	statuses := filter.Statuses
	if len(statuses) == 0 {
		statuses = []repository.Status{repository.StatusActive, repository.StatusMissing}
	}
	statusStrs := make([]string, len(statuses))
	for i, s := range statuses {
		statusStrs[i] = string(s)
	}

	query := `SELECT ` + chartColumns + ` FROM chart WHERE status = ANY($1)`
	args := []any{statusStrs}
	if filter.Domain != "" {
		args = append(args, filter.Domain)
		query += fmt.Sprintf(" AND domain = $%d", len(args))
	}
	query += " ORDER BY domain, name"

	rows, err := r.ex.Query(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []repository.Chart
	for rows.Next() {
		c, err := scanChart(rows)
		if err != nil {
			return nil, err
		}
		if ids, err := r.chartAppIDs(ctx, c.ChartID); err == nil {
			c.AppIDs = ids
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

func (r *appRepo) GetChartByID(ctx context.Context, chartID string) (*repository.Chart, error) {
	row := r.ex.QueryRow(ctx, `SELECT `+chartColumns+` FROM chart WHERE chart_id = $1`, chartID)
	c, err := scanChart(row)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, repository.ErrNotFound
		}
		return nil, err
	}
	if ids, err := r.chartAppIDs(ctx, c.ChartID); err == nil {
		c.AppIDs = ids
	}
	return &c, nil
}

func (r *appRepo) GetChartByFullName(ctx context.Context, fullName string) (*repository.Chart, error) {
	row := r.ex.QueryRow(ctx, `SELECT `+chartColumns+` FROM chart WHERE domain || '-' || name = $1`, fullName)
	c, err := scanChart(row)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, repository.ErrNotFound
		}
		return nil, err
	}
	if ids, err := r.chartAppIDs(ctx, c.ChartID); err == nil {
		c.AppIDs = ids
	}
	return &c, nil
}

// ============================================================================
// SetAppStatus
// ============================================================================

func (r *appRepo) SetAppStatus(ctx context.Context, appID string, target repository.Status, reason string) (*repository.App, error) {
	row := r.ex.QueryRow(ctx, `SELECT `+appColumns+` FROM app WHERE app_id = $1`, appID)

	var a repository.App
	var deployUnit, dbStatus string
	if err := row.Scan(&a.AppID, &a.Domain, &a.Name, &a.Description, &a.Language, &a.AppType, &deployUnit, &a.BazelLabel, &a.ImageRepository, &dbStatus, &a.FirstSeenAt, &a.LastSeenAt); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, repository.ErrNotFound
		}
		return nil, err
	}
	a.DeployUnit = deployUnitFromDB(deployUnit)
	a.Status = repository.Status(dbStatus)

	// SetAppStatus supports exactly one human-triage transition: missing ->
	// archived. missing -> active happens automatically via Reconcile's
	// "recovered" path (see ARCHITECTURE.md "Triage"), so it is never legal
	// here. archived -> archived is a no-op success so a retried archive
	// call is safe.
	if a.Status == repository.StatusArchived && target == repository.StatusArchived {
		return &a, nil
	}
	if a.Status != repository.StatusMissing || target != repository.StatusArchived {
		return nil, fmt.Errorf("%w: app %s is %s; only a missing app can be archived", repository.ErrFailedPrecondition, a.FullName(), a.Status)
	}

	if _, err := r.ex.Exec(ctx, `UPDATE app SET status = $2 WHERE app_id = $1`, appID, string(target)); err != nil {
		return nil, err
	}
	a.Status = target
	return &a, nil
}
