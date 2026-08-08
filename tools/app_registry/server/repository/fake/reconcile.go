package fake

import (
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/whale-net/everything/tools/app_registry/server/repository"
	appmetapb "github.com/whale-net/everything/tools/appmeta/proto"
)

// reconcile implements the FULL replace described in ARCHITECTURE.md and
// AGENTS-2a.md's ReconcileApps scope. It is shared logic the postgres
// implementation mirrors in SQL — see postgres/app.go's Reconcile for the
// same state machine expressed as UPDATE/INSERT statements.
func reconcile(s *state, apps []*appmetapb.AppManifest, charts []*appmetapb.ChartManifest, dryRun bool) (*repository.ReconcileResult, error) {
	workState := s
	if dryRun {
		workState = s.clone()
	}

	now := time.Now().UTC()
	result := &repository.ReconcileResult{}

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
		appIDs, err := resolveChartApps(workState, cm)
		if err != nil {
			return nil, err
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

	return result, nil
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

// resolveChartApps resolves ChartManifest.apps (bare app names) to app_ids,
// per ARCHITECTURE.md/PLAN.md: "fail the reconcile if any is unknown."
// Same-domain matches are preferred; a cross-domain match is accepted only
// if it is unique.
func resolveChartApps(s *state, cm *appmetapb.ChartManifest) ([]string, error) {
	ids := make([]string, 0, len(cm.Apps))
	for _, name := range cm.Apps {
		if id, _, ok := findAppByDomainName(s, cm.Domain, name); ok {
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
			return nil, fmt.Errorf("%w: chart %s/%s references unknown app %q", repository.ErrInvalidArgument, cm.Domain, cm.Name, name)
		case 1:
			ids = append(ids, matches[0])
		default:
			return nil, fmt.Errorf("%w: chart %s/%s app name %q is ambiguous across domains", repository.ErrInvalidArgument, cm.Domain, cm.Name, name)
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
