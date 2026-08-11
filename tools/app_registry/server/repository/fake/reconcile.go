package fake

import (
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/whale-net/everything/tools/app_registry/server/repository"
	appmetapb "github.com/whale-net/everything/tools/appmeta/proto"
)

// reconcile implements the FULL replace described in ARCHITECTURE.md and
// AGENTS-2a.md's ReconcileApps scope. It is shared logic the postgres
// implementation mirrors in SQL — see postgres/app.go's Reconcile for the
// same state machine expressed as UPDATE/INSERT statements.
//
// source/watermark handling mirrors postgres/app.go exactly (see its doc
// comments and ARCHITECTURE.md "Reconcile watermark"): a dry run never
// reads or writes workState.Watermark at all -- it operates on a throwaway
// clone the caller discards, and the watermark check would be meaningless
// against it anyway. A real (non-dry-run) call checks
// repository.ShouldApplyReconcile before doing any diffing, and advances
// the watermark only after every write below has been computed
// successfully.
func reconcile(s *state, apps []*appmetapb.AppManifest, charts []*appmetapb.ChartManifest, source repository.ReconcileSource, dryRun bool) (*repository.ReconcileResult, error) {
	workState := s
	if dryRun {
		workState = s.clone()
	}

	now := time.Now().UTC()
	result := &repository.ReconcileResult{}

	if !dryRun {
		if !repository.ShouldApplyReconcile(source, workState.Watermark) {
			result.SkippedStale = true
			result.CurrentWatermarkGitSHA = workState.Watermark.GitSHA
			return result, nil
		}
	}

	presentAppIDs := map[string]bool{}
	for _, am := range apps {
		id, existing, found := findAppByDomainName(workState, am.Domain, am.Name)
		if !found {
			id = uuid.NewString()
			newApp := repository.App{
				AppID:           id,
				Domain:          am.Domain,
				Name:            am.Name,
				Description:     am.Description,
				Language:        am.Language,
				AppType:         am.AppType,
				DeployUnit:      normalizeDeployUnit(am.DeployUnit),
				BazelLabel:      am.BinaryTarget,
				ImageRepository: imageRepository(am),
				Status:          repository.StatusActive,
				FirstSeenAt:     now,
				LastSeenAt:      now,
			}
			workState.Apps[id] = newApp
			presentAppIDs[id] = true
			result.CreatedApps = append(result.CreatedApps, newApp)
			continue
		}

		wasMissing := existing.Status == repository.StatusMissing
		updated := existing
		updated.Description = am.Description
		updated.Language = am.Language
		updated.AppType = am.AppType
		updated.DeployUnit = normalizeDeployUnit(am.DeployUnit)
		updated.BazelLabel = am.BinaryTarget
		updated.ImageRepository = imageRepository(am)
		updated.Status = repository.StatusActive
		updated.LastSeenAt = now
		workState.Apps[id] = updated
		presentAppIDs[id] = true

		if wasMissing {
			result.RecoveredApps = append(result.RecoveredApps, updated)
		} else {
			result.UpdatedApps = append(result.UpdatedApps, updated)
		}
	}

	for id, a := range workState.Apps {
		if presentAppIDs[id] {
			continue
		}
		if a.Status == repository.StatusActive {
			a.Status = repository.StatusMissing
			workState.Apps[id] = a
			result.NewlyMissingApps = append(result.NewlyMissingApps, a)
		}
		// Already-missing and archived rows are left untouched.
	}

	presentChartIDs := map[string]bool{}
	for _, cm := range charts {
		appIDs, offending, reason := resolveChartApps(workState, cm)
		if len(offending) > 0 {
			// AR-7a: skip this chart, report it, but do not let it be swept
			// into MISSING -- a chart present in the manifest set but
			// unresolvable is *present*, not absent. See
			// ARCHITECTURE.md "AssertApps (additive) vs. ReconcileApps" and
			// postgres/app.go's Reconcile, which this mirrors.
			result.UnresolvedCharts = append(result.UnresolvedCharts, repository.UnresolvedChart{
				Domain: cm.Domain, Name: cm.Name, AppRefs: offending, Reason: reason,
			})
			if id, _, found := findChartByDomainName(workState, cm.Domain, cm.Name); found {
				presentChartIDs[id] = true
			}
			continue
		}

		id, existing, found := findChartByDomainName(workState, cm.Domain, cm.Name)
		if !found {
			id = uuid.NewString()
			newChart := repository.Chart{
				ChartID:     id,
				Domain:      cm.Domain,
				Name:        cm.Name,
				DeployUnit:  appmetapb.DeployUnit_DEPLOY_UNIT_CHART,
				Status:      repository.StatusActive,
				AppIDs:      appIDs,
				FirstSeenAt: now,
				LastSeenAt:  now,
			}
			workState.Charts[id] = newChart
			presentChartIDs[id] = true
			result.CreatedCharts = append(result.CreatedCharts, newChart)
			continue
		}

		wasMissing := existing.Status == repository.StatusMissing
		updated := existing
		updated.AppIDs = appIDs
		updated.Status = repository.StatusActive
		updated.LastSeenAt = now
		workState.Charts[id] = updated
		presentChartIDs[id] = true

		if wasMissing {
			result.RecoveredCharts = append(result.RecoveredCharts, updated)
		} else {
			result.UpdatedCharts = append(result.UpdatedCharts, updated)
		}
	}

	for id, c := range workState.Charts {
		if presentChartIDs[id] {
			continue
		}
		if c.Status == repository.StatusActive {
			c.Status = repository.StatusMissing
			workState.Charts[id] = c
			result.NewlyMissingCharts = append(result.NewlyMissingCharts, c)
		}
	}

	if !dryRun {
		workState.Watermark = &repository.ReconcileWatermark{
			GitSHA:            source.GitSHA,
			SourceCommittedAt: source.SourceCommittedAt,
			DiscoveredAt:      source.DiscoveredAt,
			UpdatedAt:         now,
		}
	}

	return result, nil
}

// assertApps mirrors postgres/app.go's assertApps -- the additive-only
// counterpart to reconcile above. See repository.AppRepository.AssertApps's
// doc comment for the full contract: create-or-recover only, never marks
// MISSING, never touches chart_app membership, and rejects (per item) any
// app/chart that is currently ARCHIVED rather than silently resurrecting it.
// Unlike reconcile, this never consults or advances s.Watermark.
func assertApps(s *state, apps []*appmetapb.AppManifest, charts []*appmetapb.ChartManifest) *repository.AssertResult {
	now := time.Now().UTC()
	result := &repository.AssertResult{}

	for _, am := range apps {
		id, existing, found := findAppByDomainName(s, am.Domain, am.Name)
		if !found {
			id = uuid.NewString()
			newApp := repository.App{
				AppID:           id,
				Domain:          am.Domain,
				Name:            am.Name,
				Description:     am.Description,
				Language:        am.Language,
				AppType:         am.AppType,
				DeployUnit:      normalizeDeployUnit(am.DeployUnit),
				BazelLabel:      am.BinaryTarget,
				ImageRepository: imageRepository(am),
				Status:          repository.StatusActive,
				FirstSeenAt:     now,
				LastSeenAt:      now,
			}
			s.Apps[id] = newApp
			result.CreatedApps = append(result.CreatedApps, newApp)
			continue
		}

		if existing.Status == repository.StatusArchived {
			result.RejectedApps = append(result.RejectedApps, repository.RejectedOwner{
				Domain: am.Domain, Name: am.Name,
				Reason: fmt.Sprintf("app %s/%s is ARCHIVED -- a human archived it deliberately; AssertApps does not resurrect it", am.Domain, am.Name),
			})
			continue
		}

		wasMissing := existing.Status == repository.StatusMissing
		updated := existing
		updated.Description = am.Description
		updated.Language = am.Language
		updated.AppType = am.AppType
		updated.DeployUnit = normalizeDeployUnit(am.DeployUnit)
		updated.BazelLabel = am.BinaryTarget
		updated.ImageRepository = imageRepository(am)
		updated.Status = repository.StatusActive
		updated.LastSeenAt = now
		s.Apps[id] = updated

		if wasMissing {
			result.RecoveredApps = append(result.RecoveredApps, updated)
		} else {
			result.UpdatedApps = append(result.UpdatedApps, updated)
		}
	}

	for _, cm := range charts {
		id, existing, found := findChartByDomainName(s, cm.Domain, cm.Name)
		if !found {
			id = uuid.NewString()
			newChart := repository.Chart{
				ChartID:     id,
				Domain:      cm.Domain,
				Name:        cm.Name,
				DeployUnit:  appmetapb.DeployUnit_DEPLOY_UNIT_CHART,
				Status:      repository.StatusActive,
				FirstSeenAt: now,
				LastSeenAt:  now,
			}
			s.Charts[id] = newChart
			result.CreatedCharts = append(result.CreatedCharts, newChart)
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
		updated := existing
		updated.Status = repository.StatusActive
		updated.LastSeenAt = now
		// Deliberately NOT touching updated.AppIDs / chart_app membership --
		// see AssertApps's doc comment: composition resolution stays
		// Reconcile-only.
		s.Charts[id] = updated

		if wasMissing {
			result.RecoveredCharts = append(result.RecoveredCharts, updated)
		} else {
			result.UpdatedCharts = append(result.UpdatedCharts, updated)
		}
	}

	return result
}

func findAppByDomainName(s *state, domain, name string) (string, repository.App, bool) {
	for id, a := range s.Apps {
		if a.Domain == domain && a.Name == name {
			return id, a, true
		}
	}
	return "", repository.App{}, false
}

func findChartByDomainName(s *state, domain, name string) (string, repository.Chart, bool) {
	for id, c := range s.Charts {
		if c.Domain == domain && c.Name == name {
			return id, c, true
		}
	}
	return "", repository.Chart{}, false
}

// resolveChartApps resolves a chart manifest's app references to app_ids,
// mirroring postgres/app.go's resolveChartApps exactly (see its doc comment
// for the full rationale). Prefers cm.AppRefs (domain-qualified
// "<domain>/<name>", never ambiguous); falls back to the DEPRECATED
// cm.Apps (bare names, same-domain preferred, cross-domain accepted only if
// unique) when AppRefs is empty -- AR-7a, one release cycle's compatibility.
//
// AR-7a: does not fail on an unresolved reference. Returns the ids resolved
// so far (meaningless if offending is non-empty), every offending reference,
// and a combined reason string -- the caller downgrades a non-empty
// offending to a per-chart skip instead of failing the whole reconcile.
func resolveChartApps(s *state, cm *appmetapb.ChartManifest) (ids []string, offending []string, reason string) {
	qualified := len(cm.AppRefs) > 0
	refs := cm.AppRefs
	if !qualified {
		refs = cm.Apps
	}

	ids = make([]string, 0, len(refs))
	var reasons []string

	for _, ref := range refs {
		if qualified {
			domain, name, ok := strings.Cut(ref, "/")
			if !ok {
				offending = append(offending, ref)
				reasons = append(reasons, fmt.Sprintf("app_ref %q is not domain-qualified", ref))
				continue
			}
			if id, _, found := findAppByDomainName(s, domain, name); found {
				ids = append(ids, id)
				continue
			}
			offending = append(offending, ref)
			reasons = append(reasons, fmt.Sprintf("app_ref %q not found", ref))
			continue
		}

		// DEPRECATED bare-name path -- see doc comment above.
		name := ref
		if id, _, found := findAppByDomainName(s, cm.Domain, name); found {
			ids = append(ids, id)
			continue
		}
		var matches []string
		for id, a := range s.Apps {
			if a.Name == name {
				matches = append(matches, id)
			}
		}
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
		reason = fmt.Sprintf("chart %s/%s: %s", cm.Domain, cm.Name, strings.Join(reasons, "; "))
	}
	return ids, offending, reason
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
