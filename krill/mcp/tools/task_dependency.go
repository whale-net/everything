// This file (issue #2720, FR2, C14) is krill's work-axis dependency-
// declaration MCP tool: declare_task_dependencies, a thin wrapper over
// store.TaskStore.DeclareDependency (krill/store/task_dependency.go),
// mirroring krill/api/handlers/task_dependency.go's HTTP surface for the
// same capability (LB7). Restricted to PersonaSwarmOperator, same
// rationale as task.go's create_task: root plan issue #2717's Personas
// section states the Swarm Operator "creates tasks and their dependency
// edges" in this milestone.
package tools

import (
	"context"
	"fmt"

	"github.com/google/uuid"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/whale-net/everything/krill/api/handlers"
	"github.com/whale-net/everything/krill/mcp/server"
	"github.com/whale-net/everything/krill/store"
)

// declareTaskDependenciesInput is declare_task_dependencies' argument
// schema (FR2), mirroring declareTaskDependenciesRequest
// (api/handlers/task_dependency.go).
type declareTaskDependenciesInput struct {
	krillSessionInput
	TaskID           string   `json:"task_id" jsonschema:"The task that depends on the ids below, as a UUID string."`
	DependsOnTaskIDs []string `json:"depends_on_task_ids" jsonschema:"The task ids task_id depends on, as UUID strings -- task_id is unclaimable until every one of these reaches its own terminal Done lane (FR2/FR3)."`
}

// RegisterDeclareTaskDependencies registers declare_task_dependencies
// (FR2): records that task_id depends on each id in depends_on_task_ids
// via store.TaskStore.DeclareDependency, mirroring
// krill/api/handlers/task_dependency.go's DeclareTaskDependenciesHandler
// for the same capability (LB7).
func RegisterDeclareTaskDependencies(reg *server.Registry, sessions store.SessionStore, tasks store.TaskStore) {
	server.RegisterWrite(reg, &mcp.Tool{
		Name:        "declare_task_dependencies",
		Description: "Declare that a task depends on one or more other tasks -- it is excluded from the claimable set until every dependency reaches its own terminal Done lane (FR2).",
	}, []server.Persona{server.PersonaSwarmOperator}, func(ctx context.Context, _ *mcp.CallToolRequest, in declareTaskDependenciesInput) (*mcp.CallToolResult, handlers.IDResponse, error) {
		var zero handlers.IDResponse

		sess, err := requireKrillSession(ctx, sessions, in.KrillSessionID)
		if err != nil {
			return nil, zero, err
		}

		taskID, err := uuid.Parse(in.TaskID)
		if err != nil {
			return nil, zero, fmt.Errorf("task_id: invalid or missing UUID")
		}

		dependsOnTaskIDs := make([]uuid.UUID, len(in.DependsOnTaskIDs))
		for i, raw := range in.DependsOnTaskIDs {
			id, err := uuid.Parse(raw)
			if err != nil {
				return nil, zero, fmt.Errorf("depends_on_task_ids[%d]: invalid or missing UUID", i)
			}
			dependsOnTaskIDs[i] = id
		}

		if err := tasks.DeclareDependency(ctx, store.DeclareDependencyParams{
			ScopeID:          sess.ScopeID,
			TaskID:           taskID,
			DependsOnTaskIDs: dependsOnTaskIDs,
			Acting:           sess.Acting,
			OnBehalfOf:       sess.OnBehalfOf,
		}); err != nil {
			return nil, zero, err
		}
		return nil, handlers.IDResponse{ID: taskID.String()}, nil
	})
}
