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
	"google.golang.org/protobuf/encoding/protojson"
)

type appRepo struct{ ex dbtx }

// appColumns/chartColumns are read against v_current_app/v_current_chart
// (migration 008, AR-7c) -- identity ⋈ latest `main`-sweep manifest
// snapshot, see that migration's comments. Column NAMES and ORDER are
// unchanged from before AR-7c on purpose: scanApp/scanChart below, and
// every wire type they feed (App/Chart in protos/messages.proto), are
// untouched by this split.
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

const chartColumns = `chart_id, domain, name, description, chart_repository, deploy_unit, status, first_seen_at, last_seen_at, argo_application_name_overrides`

// chartColumnsQualified is chartColumns with the v_current_chart alias
// prefixed on every column, for use in queries that join v_current_chart
// (aliased c) against another table with its own chart_id column (e.g.
// chart_app), where an unqualified `chart_id` in the SELECT list is
// ambiguous to Postgres (SQLSTATE 42702). Column order matches chartColumns
// so scanChart can be reused unchanged.
const chartColumnsQualified = `c.chart_id, c.domain, c.name, c.description, c.chart_repository, c.deploy_unit, c.status, c.first_seen_at, c.last_seen_at, c.argo_application_name_overrides`

func scanChart(row pgx.Row) (repository.Chart, error) {
	var c repository.Chart
	var deployUnit, status string
	if err := row.Scan(&c.ChartID, &c.Domain, &c.Name, &c.Description, &c.ChartRepository, &deployUnit, &status, &c.FirstSeenAt, &c.LastSeenAt, &c.ArgoApplicationNameOverrides); err != nil {
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
				// AR-7c (migration 008): `app` is pure identity now --
				// description/language/app_type/deploy_unit/bazel_label/
				// image_repository all live only in the manifest snapshot
				// below, never on this row -- see appColumns's doc comment.
				_, err := r.ex.Exec(ctx, `
					INSERT INTO app (app_id, domain, name, status, first_seen_at, last_seen_at)
					VALUES ($1, $2, $3, 'active', $4, $4)`,
					appID, am.Domain, am.Name, now)
				if err != nil {
					if de, ok := translatePgError(err, fmt.Sprintf("app %s-%s already recorded", am.Domain, am.Name)); ok {
						return nil, de
					}
					return nil, fmt.Errorf("reconcile: insert app %s/%s: %w", am.Domain, am.Name, err)
				}
				if err := r.recordAppManifestSweep(ctx, appID, am, source, now); err != nil {
					return nil, fmt.Errorf("reconcile: %w", err)
				}
			}
			newApp := appFromManifest(appID, am, repository.StatusActive, now, now)
			result.CreatedApps = append(result.CreatedApps, newApp)
			if appID != "" {
				presentAppIDs[appID] = true
				appIDByKey[am.Domain+"|"+am.Name] = appID
			}
			continue
		}

		wasMissing := existing.Status == repository.StatusMissing
		updated := appFromManifest(existing.AppID, am, repository.StatusActive, existing.FirstSeenAt, now)

		if !dryRun {
			if _, err := r.ex.Exec(ctx, `UPDATE app SET status='active', last_seen_at=$2 WHERE app_id=$1`, existing.AppID, now); err != nil {
				return nil, fmt.Errorf("reconcile: update app %s/%s: %w", am.Domain, am.Name, err)
			}
			if err := r.recordAppManifestSweep(ctx, existing.AppID, am, source, now); err != nil {
				return nil, fmt.Errorf("reconcile: %w", err)
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
	rows, err := r.ex.Query(ctx, `SELECT `+appColumns+` FROM v_current_app WHERE status = 'active'`)
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
					INSERT INTO chart (chart_id, domain, name, status, first_seen_at, last_seen_at)
					VALUES ($1, $2, $3, 'active', $4, $4)`,
					chartID, cm.Domain, cm.Name, now); err != nil {
					if de, ok := translatePgError(err, fmt.Sprintf("chart %s-%s already recorded", cm.Domain, cm.Name)); ok {
						return nil, de
					}
					return nil, fmt.Errorf("reconcile: insert chart %s/%s: %w", cm.Domain, cm.Name, err)
				}
				if err := r.setChartApps(ctx, chartID, appIDs); err != nil {
					return nil, err
				}
				if err := r.recordChartManifestSweep(ctx, chartID, cm, source, now); err != nil {
					return nil, fmt.Errorf("reconcile: %w", err)
				}
			}
			newChart := chartFromManifest(chartID, cm, appIDs, repository.StatusActive, now, now)
			result.CreatedCharts = append(result.CreatedCharts, newChart)
			if chartID != "" {
				presentChartIDs[chartID] = true
			}
			continue
		}

		wasMissing := existing.Status == repository.StatusMissing
		updated := chartFromManifest(existing.ChartID, cm, appIDs, repository.StatusActive, existing.FirstSeenAt, now)

		if !dryRun {
			if _, err := r.ex.Exec(ctx, `UPDATE chart SET status='active', last_seen_at=$2 WHERE chart_id=$1`, existing.ChartID, now); err != nil {
				return nil, fmt.Errorf("reconcile: update chart %s/%s: %w", cm.Domain, cm.Name, err)
			}
			if err := r.setChartApps(ctx, existing.ChartID, appIDs); err != nil {
				return nil, err
			}
			if err := r.recordChartManifestSweep(ctx, existing.ChartID, cm, source, now); err != nil {
				return nil, fmt.Errorf("reconcile: %w", err)
			}
		}
		presentChartIDs[existing.ChartID] = true

		if wasMissing {
			result.RecoveredCharts = append(result.RecoveredCharts, updated)
		} else {
			result.UpdatedCharts = append(result.UpdatedCharts, updated)
		}
	}

	crows, err := r.ex.Query(ctx, `SELECT `+chartColumns+` FROM v_current_chart WHERE status = 'active'`)
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
		// reconcile_run (migration 010, AR-8): one row per sweep that
		// actually applies, replacing the old one-row-per-app-per-sweep
		// record. A dry run writes nothing, same as every other write in
		// this function.
		if _, err := r.ex.Exec(ctx, `
			INSERT INTO reconcile_run (git_sha, source_committed_at, applied_at, apps_seen, charts_seen)
			VALUES ($1, $2, $3, $4, $5)`,
			source.GitSHA, source.SourceCommittedAt, now, len(apps), len(charts)); err != nil {
			return nil, fmt.Errorf("reconcile: record reconcile_run: %w", err)
		}
	}

	return result, nil
}

// ============================================================================
// AssertApps (AR-7c, issue #558)
// ============================================================================

// AssertApps implements repository.AppRepository.AssertApps -- see that doc
// comment for the full contract. Deliberately much smaller than Reconcile:
// no watermark check, no absence sweep, no chart_app membership write --
// just create-or-recover identity plus a manifest snapshot, per item,
// rejecting (not aborting) an ARCHIVED target.
func (r *appRepo) AssertApps(ctx context.Context, apps []*appmetapb.AppManifest, charts []*appmetapb.ChartManifest, source repository.ManifestSource) (*repository.AssertResult, error) {
	now := time.Now().UTC()
	result := &repository.AssertResult{}

	for _, am := range apps {
		existing, err := r.getAppByDomainName(ctx, am.Domain, am.Name)
		if err != nil && !errors.Is(err, repository.ErrNotFound) {
			return nil, fmt.Errorf("assert: look up app %s/%s: %w", am.Domain, am.Name, err)
		}

		if existing == nil {
			appID := uuid.NewString()
			if _, err := r.ex.Exec(ctx, `
				INSERT INTO app (app_id, domain, name, status, first_seen_at, last_seen_at)
				VALUES ($1, $2, $3, 'active', $4, $4)`,
				appID, am.Domain, am.Name, now); err != nil {
				if de, ok := translatePgError(err, fmt.Sprintf("app %s-%s already recorded", am.Domain, am.Name)); ok {
					return nil, de
				}
				return nil, fmt.Errorf("assert: insert app %s/%s: %w", am.Domain, am.Name, err)
			}
			if err := r.recordAppManifestRelease(ctx, appID, am, source); err != nil {
				return nil, fmt.Errorf("assert: %w", err)
			}
			result.CreatedApps = append(result.CreatedApps, appFromManifest(appID, am, repository.StatusActive, now, now))
			continue
		}

		// AR-7c: reject, per item, rather than resurrect an app a human
		// archived on purpose -- see repository.AppRepository.AssertApps's
		// doc comment.
		if existing.Status == repository.StatusArchived {
			result.RejectedApps = append(result.RejectedApps, repository.RejectedOwner{
				Domain: am.Domain, Name: am.Name,
				Reason: fmt.Sprintf("app %s/%s is ARCHIVED -- a human archived it deliberately; AssertApps does not resurrect it", am.Domain, am.Name),
			})
			continue
		}

		wasMissing := existing.Status == repository.StatusMissing
		if _, err := r.ex.Exec(ctx, `UPDATE app SET status='active', last_seen_at=$2 WHERE app_id=$1`, existing.AppID, now); err != nil {
			return nil, fmt.Errorf("assert: update app %s/%s: %w", am.Domain, am.Name, err)
		}
		if err := r.recordAppManifestRelease(ctx, existing.AppID, am, source); err != nil {
			return nil, fmt.Errorf("assert: %w", err)
		}
		updated := appFromManifest(existing.AppID, am, repository.StatusActive, existing.FirstSeenAt, now)
		if wasMissing {
			result.RecoveredApps = append(result.RecoveredApps, updated)
		} else {
			result.UpdatedApps = append(result.UpdatedApps, updated)
		}
	}

	for _, cm := range charts {
		existing, err := r.getChartByDomainName(ctx, cm.Domain, cm.Name)
		if err != nil && !errors.Is(err, repository.ErrNotFound) {
			return nil, fmt.Errorf("assert: look up chart %s/%s: %w", cm.Domain, cm.Name, err)
		}

		if existing == nil {
			chartID := uuid.NewString()
			// Deliberately NOT resolving app_refs / writing chart_app here --
			// see AssertApps's doc comment: composition resolution stays
			// Reconcile-only, because it needs the canonical complete tree
			// to be safe, which a release ref is not guaranteed to be.
			if _, err := r.ex.Exec(ctx, `
				INSERT INTO chart (chart_id, domain, name, status, first_seen_at, last_seen_at)
				VALUES ($1, $2, $3, 'active', $4, $4)`,
				chartID, cm.Domain, cm.Name, now); err != nil {
				if de, ok := translatePgError(err, fmt.Sprintf("chart %s-%s already recorded", cm.Domain, cm.Name)); ok {
					return nil, de
				}
				return nil, fmt.Errorf("assert: insert chart %s/%s: %w", cm.Domain, cm.Name, err)
			}
			if err := r.recordChartManifestRelease(ctx, chartID, cm, source); err != nil {
				return nil, fmt.Errorf("assert: %w", err)
			}
			result.CreatedCharts = append(result.CreatedCharts, chartFromManifest(chartID, cm, nil, repository.StatusActive, now, now))
			continue
		}

		if existing.Status == repository.StatusArchived {
			result.RejectedCharts = append(result.RejectedCharts, repository.RejectedOwner{
				Domain: cm.Domain, Name: cm.Name,
				Reason: fmt.Sprintf("chart %s/%s is ARCHIVED -- a human archived it deliberately; AssertApps does not resurrect it", cm.Domain, cm.Name),
			})
			continue
		}

		wasMissing := existing.Status == repository.StatusMissing
		if _, err := r.ex.Exec(ctx, `UPDATE chart SET status='active', last_seen_at=$2 WHERE chart_id=$1`, existing.ChartID, now); err != nil {
			return nil, fmt.Errorf("assert: update chart %s/%s: %w", cm.Domain, cm.Name, err)
		}
		if err := r.recordChartManifestRelease(ctx, existing.ChartID, cm, source); err != nil {
			return nil, fmt.Errorf("assert: %w", err)
		}
		// existing.AppIDs (populated by getChartByDomainName via
		// chartAppIDs) is left exactly as Reconcile last set it -- AssertApps
		// never touches chart_app membership.
		updated := chartFromManifest(existing.ChartID, cm, existing.AppIDs, repository.StatusActive, existing.FirstSeenAt, now)
		if wasMissing {
			result.RecoveredCharts = append(result.RecoveredCharts, updated)
		} else {
			result.UpdatedCharts = append(result.UpdatedCharts, updated)
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

// identityColumns / scanAppIdentity / getAppByDomainName: as of migration
// 008 (AR-7c), `app` itself is pure identity, so a write-path lookup that
// only needs app_id/status (Reconcile's/AssertApps's existing-row check,
// resolveChartApps's app_ref resolution) queries the bare `app` table
// directly rather than v_current_app -- there is no reason for a write to
// pull in a manifest-snapshot join it doesn't need. Every OTHER field on
// the returned App is left zero-valued; callers of this function must never
// pass its result to a wire response (appToPB) -- construct that from the
// AppManifest/ChartManifest already in hand instead (see Reconcile/
// AssertApps below).
const identityColumns = `app_id, domain, name, status, first_seen_at, last_seen_at`

func scanAppIdentity(row pgx.Row) (repository.App, error) {
	var a repository.App
	var status string
	if err := row.Scan(&a.AppID, &a.Domain, &a.Name, &status, &a.FirstSeenAt, &a.LastSeenAt); err != nil {
		return repository.App{}, err
	}
	a.Status = repository.Status(status)
	return a, nil
}

const chartIdentityColumns = `chart_id, domain, name, status, first_seen_at, last_seen_at`

func scanChartIdentity(row pgx.Row) (repository.Chart, error) {
	var c repository.Chart
	var status string
	if err := row.Scan(&c.ChartID, &c.Domain, &c.Name, &status, &c.FirstSeenAt, &c.LastSeenAt); err != nil {
		return repository.Chart{}, err
	}
	c.Status = repository.Status(status)
	return c, nil
}

func (r *appRepo) getAppByDomainName(ctx context.Context, domain, name string) (*repository.App, error) {
	row := r.ex.QueryRow(ctx, `SELECT `+identityColumns+` FROM app WHERE domain = $1 AND name = $2`, domain, name)
	a, err := scanAppIdentity(row)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, repository.ErrNotFound
		}
		return nil, err
	}
	return &a, nil
}

func (r *appRepo) getChartByDomainName(ctx context.Context, domain, name string) (*repository.Chart, error) {
	row := r.ex.QueryRow(ctx, `SELECT `+chartIdentityColumns+` FROM chart WHERE domain = $1 AND name = $2`, domain, name)
	c, err := scanChartIdentity(row)
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
// Manifest content + history (migration 010, AR-8; originally migration
// 008, AR-7c)
// ============================================================================

// manifestJSONMarshal serializes AppManifest/ChartManifest VERBATIM as
// protojson for storage in app_manifest.manifest_json / chart_manifest.
// manifest_json. UseProtoNames keeps keys snake_case, matching the
// Starlark-emitted JSON these values originated from (ARCHITECTURE.md
// "Shared manifest schema"). EmitUnpopulated writes every field, including
// zero-valued ones (e.g. an unset deploy_unit becomes the literal string
// "DEPLOY_UNIT_UNSPECIFIED" rather than being omitted) -- required for
// "verbatim" to mean what it says, and for migration 008's generated
// columns' CASE expressions to see a key that is genuinely always present.
var manifestJSONMarshal = protojson.MarshalOptions{UseProtoNames: true, EmitUnpopulated: true}

// resolveOrCreateAppManifestContent resolves the content-addressed
// app_manifest row for am under appID (migration 010, AR-8), creating it if
// this exact manifest has never been seen for this owner before. INSERT ...
// ON CONFLICT DO NOTHING + a fallback SELECT keyed on JSONB equality (not a
// hand-computed hash -- Postgres's own equality operator is authoritative)
// is what makes this race-safe against a concurrent Reconcile/AssertApps
// call writing the SAME new content at the same time.
func (r *appRepo) resolveOrCreateAppManifestContent(ctx context.Context, appID string, am *appmetapb.AppManifest) (string, error) {
	body, err := manifestJSONMarshal.Marshal(am)
	if err != nil {
		return "", fmt.Errorf("marshal app manifest for %s/%s: %w", am.Domain, am.Name, err)
	}
	row := r.ex.QueryRow(ctx, `
		INSERT INTO app_manifest (owner_id, manifest_json)
		VALUES ($1, $2)
		ON CONFLICT (owner_id, manifest_hash) DO NOTHING
		RETURNING app_manifest_id`, appID, string(body))
	var id string
	if serr := row.Scan(&id); serr == nil {
		return id, nil
	} else if !errors.Is(serr, pgx.ErrNoRows) {
		return "", fmt.Errorf("record app manifest content for %s/%s: %w", am.Domain, am.Name, serr)
	}
	row = r.ex.QueryRow(ctx, `SELECT app_manifest_id FROM app_manifest WHERE owner_id = $1 AND manifest_json = $2::jsonb`, appID, string(body))
	if err := row.Scan(&id); err != nil {
		return "", fmt.Errorf("resolve app manifest content for %s/%s: %w", am.Domain, am.Name, err)
	}
	return id, nil
}

func (r *appRepo) resolveOrCreateChartManifestContent(ctx context.Context, chartID string, cm *appmetapb.ChartManifest) (string, error) {
	body, err := manifestJSONMarshal.Marshal(cm)
	if err != nil {
		return "", fmt.Errorf("marshal chart manifest for %s/%s: %w", cm.Domain, cm.Name, err)
	}
	row := r.ex.QueryRow(ctx, `
		INSERT INTO chart_manifest (owner_id, manifest_json)
		VALUES ($1, $2)
		ON CONFLICT (owner_id, manifest_hash) DO NOTHING
		RETURNING chart_manifest_id`, chartID, string(body))
	var id string
	if serr := row.Scan(&id); serr == nil {
		return id, nil
	} else if !errors.Is(serr, pgx.ErrNoRows) {
		return "", fmt.Errorf("record chart manifest content for %s/%s: %w", cm.Domain, cm.Name, serr)
	}
	row = r.ex.QueryRow(ctx, `SELECT chart_manifest_id FROM chart_manifest WHERE owner_id = $1 AND manifest_json = $2::jsonb`, chartID, string(body))
	if err := row.Scan(&id); err != nil {
		return "", fmt.Errorf("resolve chart manifest content for %s/%s: %w", cm.Domain, cm.Name, err)
	}
	return id, nil
}

// recordAppManifestSweep performs the SCD2 close-and-open write against
// app_manifest_history for a ReconcileApps sweep (migration 010, AR-8) --
// see AGENTS.md "SCD2". validFrom is the commit's committer time
// (to_timestamp equivalent in Go), falling back to wall clock only for the
// source_committed_at = 0 sentinel -- never NOW() unconditionally, so "value
// at T" answers what was in the tree, not when CI happened to run. Writes
// ZERO new app_manifest_history rows when the owner's currently-open
// interval already has this exact content -- only last_git_sha advances.
func (r *appRepo) recordAppManifestSweep(ctx context.Context, appID string, am *appmetapb.AppManifest, source repository.ManifestSource, now time.Time) error {
	contentID, err := r.resolveOrCreateAppManifestContent(ctx, appID, am)
	if err != nil {
		return err
	}
	validFrom := now
	if source.SourceCommittedAt != 0 {
		validFrom = time.Unix(source.SourceCommittedAt, 0).UTC()
	}

	row := r.ex.QueryRow(ctx, `
		SELECT app_manifest_history_id, app_manifest_id
		FROM app_manifest_history WHERE owner_id = $1 AND valid_to IS NULL`, appID)
	var historyID, currentContentID string
	switch serr := row.Scan(&historyID, &currentContentID); {
	case errors.Is(serr, pgx.ErrNoRows):
		if _, err := r.ex.Exec(ctx, `
			INSERT INTO app_manifest_history (owner_id, app_manifest_id, valid_from, first_git_sha, last_git_sha)
			VALUES ($1, $2, $3, $4, $4)`, appID, contentID, validFrom, source.GitSHA); err != nil {
			return fmt.Errorf("open app manifest history for %s/%s: %w", am.Domain, am.Name, err)
		}
	case serr != nil:
		return fmt.Errorf("read current app manifest history for %s/%s: %w", am.Domain, am.Name, serr)
	case currentContentID == contentID:
		if _, err := r.ex.Exec(ctx, `UPDATE app_manifest_history SET last_git_sha = $2 WHERE app_manifest_history_id = $1`, historyID, source.GitSHA); err != nil {
			return fmt.Errorf("advance app manifest history for %s/%s: %w", am.Domain, am.Name, err)
		}
	default:
		if _, err := r.ex.Exec(ctx, `UPDATE app_manifest_history SET valid_to = $2 WHERE app_manifest_history_id = $1`, historyID, validFrom); err != nil {
			return fmt.Errorf("close app manifest history for %s/%s: %w", am.Domain, am.Name, err)
		}
		if _, err := r.ex.Exec(ctx, `
			INSERT INTO app_manifest_history (owner_id, app_manifest_id, valid_from, first_git_sha, last_git_sha)
			VALUES ($1, $2, $3, $4, $4)`, appID, contentID, validFrom, source.GitSHA); err != nil {
			return fmt.Errorf("open app manifest history for %s/%s: %w", am.Domain, am.Name, err)
		}
	}
	return nil
}

func (r *appRepo) recordChartManifestSweep(ctx context.Context, chartID string, cm *appmetapb.ChartManifest, source repository.ManifestSource, now time.Time) error {
	contentID, err := r.resolveOrCreateChartManifestContent(ctx, chartID, cm)
	if err != nil {
		return err
	}
	validFrom := now
	if source.SourceCommittedAt != 0 {
		validFrom = time.Unix(source.SourceCommittedAt, 0).UTC()
	}

	row := r.ex.QueryRow(ctx, `
		SELECT chart_manifest_history_id, chart_manifest_id
		FROM chart_manifest_history WHERE owner_id = $1 AND valid_to IS NULL`, chartID)
	var historyID, currentContentID string
	switch serr := row.Scan(&historyID, &currentContentID); {
	case errors.Is(serr, pgx.ErrNoRows):
		if _, err := r.ex.Exec(ctx, `
			INSERT INTO chart_manifest_history (owner_id, chart_manifest_id, valid_from, first_git_sha, last_git_sha)
			VALUES ($1, $2, $3, $4, $4)`, chartID, contentID, validFrom, source.GitSHA); err != nil {
			return fmt.Errorf("open chart manifest history for %s/%s: %w", cm.Domain, cm.Name, err)
		}
	case serr != nil:
		return fmt.Errorf("read current chart manifest history for %s/%s: %w", cm.Domain, cm.Name, serr)
	case currentContentID == contentID:
		if _, err := r.ex.Exec(ctx, `UPDATE chart_manifest_history SET last_git_sha = $2 WHERE chart_manifest_history_id = $1`, historyID, source.GitSHA); err != nil {
			return fmt.Errorf("advance chart manifest history for %s/%s: %w", cm.Domain, cm.Name, err)
		}
	default:
		if _, err := r.ex.Exec(ctx, `UPDATE chart_manifest_history SET valid_to = $2 WHERE chart_manifest_history_id = $1`, historyID, validFrom); err != nil {
			return fmt.Errorf("close chart manifest history for %s/%s: %w", cm.Domain, cm.Name, err)
		}
		if _, err := r.ex.Exec(ctx, `
			INSERT INTO chart_manifest_history (owner_id, chart_manifest_id, valid_from, first_git_sha, last_git_sha)
			VALUES ($1, $2, $3, $4, $4)`, chartID, contentID, validFrom, source.GitSHA); err != nil {
			return fmt.Errorf("open chart manifest history for %s/%s: %w", cm.Domain, cm.Name, err)
		}
	}
	return nil
}

// recordAppManifestRelease records a release-time (AssertApps) observation
// (migration 010, AR-8): resolve the content row, then INSERT INTO
// app_manifest_release ... ON CONFLICT (owner_id, git_sha) DO NOTHING --
// idempotent on a repeat AssertApps call for the same commit, exactly like
// migration 008's snapshot write was. Never touches app_manifest_history.
func (r *appRepo) recordAppManifestRelease(ctx context.Context, appID string, am *appmetapb.AppManifest, source repository.ManifestSource) error {
	contentID, err := r.resolveOrCreateAppManifestContent(ctx, appID, am)
	if err != nil {
		return err
	}
	if _, err := r.ex.Exec(ctx, `
		INSERT INTO app_manifest_release (owner_id, app_manifest_id, git_sha)
		VALUES ($1, $2, $3)
		ON CONFLICT (owner_id, git_sha) DO NOTHING`, appID, contentID, source.GitSHA); err != nil {
		return fmt.Errorf("record app manifest release for %s/%s: %w", am.Domain, am.Name, err)
	}
	return nil
}

func (r *appRepo) recordChartManifestRelease(ctx context.Context, chartID string, cm *appmetapb.ChartManifest, source repository.ManifestSource) error {
	contentID, err := r.resolveOrCreateChartManifestContent(ctx, chartID, cm)
	if err != nil {
		return err
	}
	if _, err := r.ex.Exec(ctx, `
		INSERT INTO chart_manifest_release (owner_id, chart_manifest_id, git_sha)
		VALUES ($1, $2, $3)
		ON CONFLICT (owner_id, git_sha) DO NOTHING`, chartID, contentID, source.GitSHA); err != nil {
		return fmt.Errorf("record chart manifest release for %s/%s: %w", cm.Domain, cm.Name, err)
	}
	return nil
}

// appFromManifest/chartFromManifest build the wire-shaped App/Chart struct
// straight from the AppManifest/ChartManifest just asserted/reconciled --
// avoiding a redundant read-back through v_current_app/v_current_chart
// immediately after writing the very row that view would join against.
// Mirrors exactly what the pre-AR-7c code built inline for
// CreatedApps/UpdatedApps -- see Reconcile below.
func appFromManifest(appID string, am *appmetapb.AppManifest, status repository.Status, firstSeenAt, lastSeenAt time.Time) repository.App {
	return repository.App{
		AppID: appID, Domain: am.Domain, Name: am.Name, Description: am.Description,
		Language: am.Language, AppType: am.AppType, DeployUnit: normalizeDeployUnit(am.DeployUnit),
		BazelLabel: am.BinaryTarget, ImageRepository: imageRepository(am),
		Status: status, FirstSeenAt: firstSeenAt, LastSeenAt: lastSeenAt,
	}
}

func chartFromManifest(chartID string, cm *appmetapb.ChartManifest, appIDs []string, status repository.Status, firstSeenAt, lastSeenAt time.Time) repository.Chart {
	return repository.Chart{
		ChartID: chartID, Domain: cm.Domain, Name: cm.Name, DeployUnit: appmetapb.DeployUnit_DEPLOY_UNIT_CHART,
		Status: status, AppIDs: appIDs, FirstSeenAt: firstSeenAt, LastSeenAt: lastSeenAt,
	}
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

	query := `SELECT ` + appColumns + ` FROM v_current_app WHERE status = ANY($1)`
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
	row := r.ex.QueryRow(ctx, `SELECT `+appColumns+` FROM v_current_app WHERE app_id = $1`, appID)
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
	row := r.ex.QueryRow(ctx, `SELECT `+appColumns+` FROM v_current_app WHERE domain || '-' || name = $1`, fullName)
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
		SELECT `+chartColumnsQualified+` FROM v_current_chart c
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

	query := `SELECT ` + chartColumns + ` FROM v_current_chart WHERE status = ANY($1)`
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
	row := r.ex.QueryRow(ctx, `SELECT `+chartColumns+` FROM v_current_chart WHERE chart_id = $1`, chartID)
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
	row := r.ex.QueryRow(ctx, `SELECT `+chartColumns+` FROM v_current_chart WHERE domain || '-' || name = $1`, fullName)
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
	row := r.ex.QueryRow(ctx, `SELECT `+appColumns+` FROM v_current_app WHERE app_id = $1`, appID)

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

// ============================================================================
// SetChartArgoApplicationNameOverride
// ============================================================================

// SetChartArgoApplicationNameOverride sets (argoApplicationName non-empty)
// or clears (argoApplicationName == "") exactly one environmentKey entry in
// chart.argo_application_name_overrides -- every other environment's
// override on this chart is left untouched, via a single-key JSONB
// set/delete rather than a whole-map replace. Reconcile/ReconcileApps above
// never touch this column at all, so an admin-set override survives
// reconciliation. See repository.Chart.ResolveArgoApplicationName.
func (r *appRepo) SetChartArgoApplicationNameOverride(ctx context.Context, chartID, environmentKey, argoApplicationName string) (*repository.Chart, error) {
	tag, err := r.ex.Exec(ctx, `
		UPDATE chart SET argo_application_name_overrides =
			CASE WHEN $3 = '' THEN argo_application_name_overrides - $2
			     ELSE jsonb_set(argo_application_name_overrides, ARRAY[$2], to_jsonb($3::text), true)
			END
		WHERE chart_id = $1`, chartID, environmentKey, argoApplicationName)
	if err != nil {
		return nil, fmt.Errorf("set chart argo application name override: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return nil, repository.ErrNotFound
	}
	return r.GetChartByID(ctx, chartID)
}

// defaultReconcileRunPageSize is what ListReconcileRuns falls back to when
// the caller passes pageSize <= 0. There is no existing
// ListPromotionEvents/ListArtifacts precedent that enforces a default (both
// still read their whole result set unpaginated) -- this is a fresh,
// deliberate choice for this package's first real (LIMIT + keyset cursor)
// pagination, per issue #607.
const defaultReconcileRunPageSize = 50

const reconcileRunColumns = `reconcile_run_id, git_sha, source_committed_at, applied_at, apps_seen, charts_seen`

func scanReconcileRun(row pgx.Row) (repository.ReconcileRun, error) {
	var rr repository.ReconcileRun
	if err := row.Scan(&rr.ReconcileRunID, &rr.GitSHA, &rr.SourceCommittedAt, &rr.AppliedAt, &rr.AppsSeen, &rr.ChartsSeen); err != nil {
		return repository.ReconcileRun{}, err
	}
	return rr, nil
}

// ListReconcileRuns implements repository.AppRepository.ListReconcileRuns --
// see that doc comment for the pagination contract. reconcile_run.applied_at
// has no supporting index (see ARCHITECTURE.md "ListReconcileRuns"); at
// current/foreseeable sweep volume an unindexed ORDER BY ... LIMIT full sort
// is fine by deliberate choice.
func (r *appRepo) ListReconcileRuns(ctx context.Context, since time.Time, pageSize int32, pageToken string) ([]repository.ReconcileRun, string, error) {
	if pageSize <= 0 {
		pageSize = defaultReconcileRunPageSize
	}

	query := `SELECT ` + reconcileRunColumns + ` FROM reconcile_run WHERE 1=1`
	var args []any
	if !since.IsZero() {
		args = append(args, since)
		query += fmt.Sprintf(" AND applied_at >= $%d", len(args))
	}
	if pageToken != "" {
		cursorTS, cursorID, err := decodeKeysetCursor(pageToken)
		if err != nil {
			return nil, "", fmt.Errorf("list reconcile runs: %w", err)
		}
		// Keyset predicate for "resume strictly after (ts, id)" on an
		// ORDER BY applied_at DESC, reconcile_run_id DESC query --
		// explicit casts because the row-value comparison below gives
		// Postgres nothing else to infer $N's type from.
		args = append(args, cursorTS, cursorID)
		query += fmt.Sprintf(" AND (applied_at, reconcile_run_id) < ($%d::timestamptz, $%d::uuid)", len(args)-1, len(args))
	}
	// Fetch one extra row so we can tell whether there is a next page
	// without a separate COUNT(*) query.
	args = append(args, pageSize+1)
	query += fmt.Sprintf(" ORDER BY applied_at DESC, reconcile_run_id DESC LIMIT $%d", len(args))

	rows, err := r.ex.Query(ctx, query, args...)
	if err != nil {
		return nil, "", err
	}
	defer rows.Close()
	var out []repository.ReconcileRun
	for rows.Next() {
		rr, err := scanReconcileRun(rows)
		if err != nil {
			return nil, "", err
		}
		out = append(out, rr)
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
		nextPageToken = encodeKeysetCursor(last.AppliedAt, last.ReconcileRunID)
		out = out[:pageSize]
	}
	return out, nextPageToken, nil
}
