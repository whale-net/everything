package main

import (
	"context"
	"fmt"
	"sync"
	"time"

	pb "github.com/whale-net/everything/tools/app_registry/protos"
	"github.com/whale-net/everything/tools/app_registry/ui/matrix"
)

// environmentStateFanoutTimeout bounds the N concurrent GetEnvironmentState
// calls the deployments matrix fans out (one per environment, FR-7/NFR-5) so
// one slow/hung environment can't hang the whole screen. A per-environment
// failure inside this window still renders — see matrix.ColumnResult.Err and
// buildDeploymentMatrix below — it is never silently treated as "nothing
// deployed".
const environmentStateFanoutTimeout = 8 * time.Second

// buildDeploymentMatrix loads everything screens 01 (dashboard) and 10
// (deployments) need: every environment, every PROMOTABLE/VIA_CHART
// identity, and — fanned out concurrently, one call per environment — the
// state of each environment at instant `at` (0 == current state, via
// GetEnvironmentState's own at=0 "current" semantics).
func (app *App) buildDeploymentMatrix(ctx context.Context, at int64) (*matrix.Matrix, error) {
	envResp, err := app.registry.Environment.ListEnvironments(ctx, &pb.ListEnvironmentsRequest{})
	if err != nil {
		return nil, fmt.Errorf("ListEnvironments: %w", err)
	}
	environments := envResp.GetEnvironments()

	chartsResp, err := app.registry.App.ListCharts(ctx, &pb.ListChartsRequest{})
	if err != nil {
		return nil, fmt.Errorf("ListCharts: %w", err)
	}
	appsResp, err := app.registry.App.ListApps(ctx, &pb.ListAppsRequest{})
	if err != nil {
		return nil, fmt.Errorf("ListApps: %w", err)
	}

	fetchCtx, cancel := context.WithTimeout(ctx, environmentStateFanoutTimeout)
	defer cancel()

	columns := make(map[string]matrix.ColumnResult, len(environments))
	var mu sync.Mutex
	var wg sync.WaitGroup
	for _, env := range environments {
		env := env
		wg.Add(1)
		go func() {
			defer wg.Done()
			resp, err := app.registry.Promotion.GetEnvironmentState(fetchCtx, &pb.GetEnvironmentStateRequest{
				EnvironmentKey: env.GetKey(),
				At:             at,
			})
			result := matrix.ColumnResult{Env: env, Resp: resp, Err: err}
			mu.Lock()
			columns[env.GetKey()] = result
			mu.Unlock()
		}()
	}
	wg.Wait()

	m := matrix.Build(chartsResp.GetCharts(), appsResp.GetApps(), environments, columns)
	m.AsOf = at
	return m, nil
}
