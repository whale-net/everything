package main

import (
	"net/http"
	"strconv"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/whale-net/everything/libs/go/htmxauth"
	"github.com/whale-net/everything/libs/go/logging"
	whagentpb "github.com/whale-net/everything/whagent_net/protos"
	"github.com/whale-net/everything/whagent_net/ui/components"
)

// agentToView converts a proto definition to its render view.
func agentToView(a *whagentpb.AgentDefinition) components.AgentView {
	v := components.AgentView{
		ID:                a.GetId(),
		AgentID:           a.GetAgentId(),
		Scope:             a.GetScope(),
		Model:             a.GetModel(),
		ModelDefinitionID: a.GetModelDefinitionId(),
		ToolSet:           a.GetToolSet(),
		MaxTurns:          a.GetMaxTurns(),
		MaxCostUSD:        strconv.FormatFloat(a.GetMaxCostUsd(), 'f', -1, 64),
		MaxToolIterations: a.GetMaxToolIterations(),
		ToolLoadingMode:   a.GetToolLoadingMode(),
		RequiredRole:      a.GetRequiredRole(),
		SystemPrompt:      a.GetSystemPrompt(),
	}
	if v.Model == "" {
		v.Model = v.ModelDefinitionID
	}
	if a.GetValidFrom() != nil {
		v.ValidFrom = a.GetValidFrom().AsTime().UTC().Format("2006-01-02 15:04:05 UTC")
	}
	return v
}

// handleAgentList is GET /agents.
func (app *App) handleAgentList(w http.ResponseWriter, r *http.Request) {
	logger := logging.Get("main")
	ctx := r.Context()

	data := components.AgentListPageData{
		Layout: components.LayoutData{Title: "Agents", Active: "Agents", User: htmxauth.GetUser(ctx)},
	}
	resp, err := app.session.Client().ListAgents(ctx, &whagentpb.ListAgentsRequest{})
	if err != nil {
		logger.Error("failed to list agents", "error", err)
		data.FetchError = "the whagent-net api is unavailable"
	} else {
		for _, a := range resp.GetAgents() {
			data.Agents = append(data.Agents, agentToView(a))
		}
	}

	if err := RenderTempl(w, r, data.Layout.Title, components.AgentList(data)); err != nil {
		logger.Error("failed to render agent list page", "error", err)
		http.Error(w, "Internal server error", http.StatusInternalServerError)
	}
}

// handleAgentDetail is GET /agents/{agent_id}.
func (app *App) handleAgentDetail(w http.ResponseWriter, r *http.Request) {
	logger := logging.Get("main")
	ctx := r.Context()

	agentID := r.PathValue("agent_id")
	resp, err := app.session.Client().GetAgent(ctx, &whagentpb.GetAgentRequest{AgentId: agentID})
	if err != nil {
		if status.Code(err) == codes.NotFound {
			http.NotFound(w, r)
			return
		}
		logger.Error("failed to get agent", "agent_id", agentID, "error", err)
		http.Error(w, "the whagent-net api is unavailable", http.StatusBadGateway)
		return
	}

	data := components.AgentDetailPageData{
		Layout: components.LayoutData{Title: agentID, Active: "Agents", User: htmxauth.GetUser(ctx)},
		Agent:  agentToView(resp.GetCurrent()),
	}
	if err := RenderTempl(w, r, data.Layout.Title, components.AgentDetail(data)); err != nil {
		logger.Error("failed to render agent detail page", "error", err)
		http.Error(w, "Internal server error", http.StatusInternalServerError)
	}
}
