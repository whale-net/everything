// Package register creates the agent definitions declared in agents.yaml
// that have no current row. It never updates an existing agent.
package register

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/stdlib"

	"github.com/whale-net/everything/whagent_net/config"
	"github.com/whale-net/everything/whagent_net/session"
)

// Run ensures each model definition exists, then registers each agent that
// has no current row, returning how many were created.
func Run(ctx context.Context, q session.Querier, modelDefs []config.ModelDefinitionConfig, agents []config.AgentDefinitionConfig, logger *slog.Logger) (int, error) {
	modelIDs := make(map[string]uuid.UUID, len(modelDefs))
	for _, md := range modelDefs {
		def := &session.ModelDefinition{
			Name:  md.Name,
			Model: md.Model,
			Provider: session.ProviderPreferences{
				Only: md.Provider.Only, Ignore: md.Provider.Ignore, Order: md.Provider.Order,
				Quantizations: md.Provider.Quantizations, Sort: md.Provider.Sort,
				AllowFallbacks: md.Provider.AllowFallbacks, RequireParameters: md.Provider.RequireParameters,
				DataCollection: md.Provider.DataCollection,
			},
		}
		if err := session.EnsureModelDefinition(ctx, q, def); err != nil {
			return 0, fmt.Errorf("model definition %q: %w", md.Name, err)
		}
		modelIDs[md.Name] = def.ID
	}

	created := 0
	for _, a := range agents {
		def := &session.AgentDefinition{
			AgentID:           a.AgentID,
			Scope:             a.Scope,
			MaxTurns:          a.MaxTurns,
			MaxCostUSD:        a.MaxCostUSD,
			MaxToolIterations: a.MaxToolIterations,
			ToolLoadingMode:   session.ToolLoadingMode(a.ToolLoadingMode),
			ToolSet:           make([]session.ToolServerRef, 0, len(a.ToolSet)),
		}
		if a.Model != "" {
			m := a.Model
			def.Model = &m
		}
		if a.ModelDefinition != "" {
			id := modelIDs[a.ModelDefinition]
			def.ModelDefinitionID = &id
		}
		if a.RequiredRole != "" {
			r := a.RequiredRole
			def.RequiredRole = &r
		}
		if a.SystemPrompt != "" {
			p := a.SystemPrompt
			def.SystemPrompt = &p
		}
		for _, ref := range a.ToolSet {
			def.ToolSet = append(def.ToolSet, session.ToolServerRef{ServerURL: ref.ServerURL, AllowedTools: ref.AllowedTools})
		}

		ok, err := session.RegisterAgentDefinition(ctx, q, def)
		if err != nil {
			return created, fmt.Errorf("agent %q: %w", a.AgentID, err)
		}
		if ok {
			created++
			logger.Info("registered agent definition", "agent_id", a.AgentID, "id", def.ID)
		} else {
			logger.Debug("agent definition already has a current row, skipped", "agent_id", a.AgentID)
		}
	}
	return created, nil
}

// Seeder is a migrate.Seeder that registers the embedded agents.yaml.
func Seeder(ctx context.Context, db *sql.DB) error {
	modelDefs, agents, err := config.Load()
	if err != nil {
		return err
	}
	conn, err := db.Conn(ctx)
	if err != nil {
		return fmt.Errorf("acquire connection: %w", err)
	}
	defer conn.Close()
	return conn.Raw(func(driverConn any) error {
		c, ok := driverConn.(*stdlib.Conn)
		if !ok {
			return fmt.Errorf("unexpected driver connection %T", driverConn)
		}
		_, err := Run(ctx, c.Conn(), modelDefs, agents, slog.Default())
		return err
	})
}
