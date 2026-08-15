package main

import (
	"context"
	"fmt"

	appregistrypb "github.com/whale-net/everything/tools/app_registry/protos"
)

// listEnvironments calls the registry's EnvironmentRegistry.ListEnvironments RPC and
// returns the environments from the response. The request always passes
// include_archived=false, so archived entries are excluded by default; callers that
// need visibility into archive state should set it explicitly.
func (a *App) listEnvironments(ctx context.Context, includeArchived bool) ([]*appregistrypb.Environment, error) {
	resp, err := a.registry.ListEnvironments(ctx, &appregistrypb.ListEnvironmentsRequest{
		IncludeArchived: includeArchived,
	})
	if err != nil {
		return nil, fmt.Errorf("ListEnvironments: %w", err)
	}

	return resp.Environments, nil
}
