package main

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strconv"
	"strings"

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
	data.CanAdmin = app.isAgentAdmin(r)
	if err := RenderTempl(w, r, data.Layout.Title, components.AgentDetail(data)); err != nil {
		logger.Error("failed to render agent detail page", "error", err)
		http.Error(w, "Internal server error", http.StatusInternalServerError)
	}
}

// isAgentAdmin reports whether the signed-in operator's current access token
// carries the agent admin role. Unset role fails closed. The API remains the
// authority; this only decides what the ui renders and serves.
func (app *App) isAgentAdmin(r *http.Request) bool {
	if app.agentAdminRole == "" {
		return false
	}
	var tokens accessTokenReader = app.auth
	if app.agentTokens != nil {
		tokens = app.agentTokens
	}
	ok, err := adminRoleGranted(tokens, app.agentAdminRole, r)
	if err != nil {
		logging.Get("main").Warn("agent admin check failed", "error", err)
		return false
	}
	return ok
}

func agentFormValues(a *whagentpb.AgentDefinition) components.AgentFormValues {
	v := components.AgentFormValues{
		ModelKind:         "model",
		Model:             a.GetModel(),
		ToolSet:           a.GetToolSet(),
		MaxTurns:          strconv.Itoa(int(a.GetMaxTurns())),
		MaxCostUSD:        strconv.FormatFloat(a.GetMaxCostUsd(), 'f', -1, 64),
		MaxToolIterations: strconv.Itoa(int(a.GetMaxToolIterations())),
		ToolLoadingMode:   a.GetToolLoadingMode(),
		RequiredRole:      a.GetRequiredRole(),
		Scope:             a.GetScope(),
		SystemPrompt:      a.GetSystemPrompt(),
	}
	if a.ModelDefinitionId != nil {
		v.ModelKind = "model_definition"
		v.ModelDefinitionID = a.GetModelDefinitionId()
	}
	return v
}

func agentFormFromRequest(r *http.Request) components.AgentFormValues {
	g := func(k string) string { return r.PostFormValue(k) }
	return components.AgentFormValues{
		ModelKind:         g("model_kind"),
		Model:             g("model"),
		ModelDefinitionID: g("model_definition_id"),
		ToolSet:           g("tool_set"),
		MaxTurns:          g("max_turns"),
		MaxCostUSD:        g("max_cost_usd"),
		MaxToolIterations: g("max_tool_iterations"),
		ToolLoadingMode:   g("tool_loading_mode"),
		RequiredRole:      g("required_role"),
		Scope:             g("scope"),
		SystemPrompt:      g("system_prompt"),
	}
}

// updateRequestFromForm builds the UpdateAgent request, sending only the
// chosen one of model / model_definition_id.
func updateRequestFromForm(agentID string, v components.AgentFormValues) (*whagentpb.UpdateAgentRequest, error) {
	turns, err := strconv.ParseInt(strings.TrimSpace(v.MaxTurns), 10, 32)
	if err != nil {
		return nil, fmt.Errorf("max turns must be a whole number")
	}
	iters, err := strconv.ParseInt(strings.TrimSpace(v.MaxToolIterations), 10, 32)
	if err != nil {
		return nil, fmt.Errorf("max tool iterations must be a whole number")
	}
	cost, err := strconv.ParseFloat(strings.TrimSpace(v.MaxCostUSD), 64)
	if err != nil {
		return nil, fmt.Errorf("max cost must be a number")
	}
	req := &whagentpb.UpdateAgentRequest{
		AgentId:           agentID,
		ToolSet:           v.ToolSet,
		MaxTurns:          int32(turns),
		MaxCostUsd:        cost,
		MaxToolIterations: int32(iters),
		ToolLoadingMode:   v.ToolLoadingMode,
		Scope:             v.Scope,
		SystemPrompt:      v.SystemPrompt,
	}
	if v.ModelKind == "model_definition" {
		id := v.ModelDefinitionID
		req.ModelDefinitionId = &id
	} else {
		req.Model = v.Model
	}
	if v.RequiredRole != "" {
		role := v.RequiredRole
		req.RequiredRole = &role
	}
	return req, nil
}

func (app *App) modelDefinitionOptions(r *http.Request) ([]components.ModelDefOption, error) {
	resp, err := app.session.Client().ListModelDefinitions(r.Context(), &whagentpb.ListModelDefinitionsRequest{})
	if err != nil {
		return nil, err
	}
	var out []components.ModelDefOption
	for _, md := range resp.GetModelDefinitions() {
		out = append(out, components.ModelDefOption{ID: md.GetId(), Name: md.GetName()})
	}
	return out, nil
}

// renderAgentFragment renders c as a bare fragment for htmx requests and
// inside the page shell otherwise.
func renderAgentFragment(w http.ResponseWriter, r *http.Request, title string, c interface {
	Render(ctx context.Context, w io.Writer) error
}) {
	var err error
	if r.Header.Get("HX-Request") == "true" {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		err = c.Render(r.Context(), w)
	} else {
		err = RenderTempl(w, r, title, c)
	}
	if err != nil {
		logging.Get("main").Error("failed to render agent fragment", "error", err)
		http.Error(w, "Internal server error", http.StatusInternalServerError)
	}
}

// handleAgentEdit is GET /agents/{agent_id}/edit (admin only).
func (app *App) handleAgentEdit(w http.ResponseWriter, r *http.Request) {
	if !app.isAgentAdmin(r) {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	ctx := r.Context()
	agentID := r.PathValue("agent_id")
	resp, err := app.session.Client().GetAgent(ctx, &whagentpb.GetAgentRequest{AgentId: agentID})
	if err != nil {
		if status.Code(err) == codes.NotFound {
			http.NotFound(w, r)
			return
		}
		logging.Get("main").Error("failed to get agent", "agent_id", agentID, "error", err)
		http.Error(w, "the whagent-net api is unavailable", http.StatusBadGateway)
		return
	}
	data := components.AgentEditPageData{
		Layout:  components.LayoutData{Title: "Edit " + agentID, Active: "Agents", User: htmxauth.GetUser(ctx)},
		AgentID: agentID,
		Values:  agentFormValues(resp.GetCurrent()),
	}
	if data.ModelDefinitions, err = app.modelDefinitionOptions(r); err != nil {
		logging.Get("main").Error("failed to list model definitions", "error", err)
		data.Error = "could not load model definitions"
	}
	if err := RenderTempl(w, r, data.Layout.Title, components.AgentEditPage(data)); err != nil {
		logging.Get("main").Error("failed to render agent edit page", "error", err)
		http.Error(w, "Internal server error", http.StatusInternalServerError)
	}
}

// handleAgentUpdate is POST /agents/{agent_id} (admin only).
func (app *App) handleAgentUpdate(w http.ResponseWriter, r *http.Request) {
	if !app.isAgentAdmin(r) {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	ctx := r.Context()
	agentID := r.PathValue("agent_id")
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad form", http.StatusBadRequest)
		return
	}
	vals := agentFormFromRequest(r)

	fail := func(msg string) {
		data := components.AgentEditPageData{
			Layout:  components.LayoutData{Title: "Edit " + agentID, Active: "Agents", User: htmxauth.GetUser(ctx)},
			AgentID: agentID,
			Values:  vals,
			Error:   msg,
		}
		data.ModelDefinitions, _ = app.modelDefinitionOptions(r)
		renderAgentFragment(w, r, data.Layout.Title, components.AgentEditPage(data))
	}

	req, err := updateRequestFromForm(agentID, vals)
	if err != nil {
		fail(err.Error())
		return
	}
	resp, err := app.session.Client().UpdateAgent(ctx, req)
	if err != nil {
		logging.Get("main").Error("failed to update agent", "agent_id", agentID, "error", err)
		msg := status.Convert(err).Message()
		if status.Code(err) == codes.Aborted {
			msg = "the agent changed since you loaded it; reload and retry (" + msg + ")"
		}
		fail(msg)
		return
	}
	renderAgentFragment(w, r, agentID, components.AgentSaved(agentToView(resp.GetCurrent())))
}

// handleAgentHistory is GET /agents/{agent_id}/history (admin only).
func (app *App) handleAgentHistory(w http.ResponseWriter, r *http.Request) {
	if !app.isAgentAdmin(r) {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	ctx := r.Context()
	agentID := r.PathValue("agent_id")
	resp, err := app.session.Client().GetAgent(ctx, &whagentpb.GetAgentRequest{AgentId: agentID, IncludeHistory: true})
	if err != nil {
		if status.Code(err) == codes.NotFound {
			http.NotFound(w, r)
			return
		}
		logging.Get("main").Error("failed to get agent history", "agent_id", agentID, "error", err)
		http.Error(w, "the whagent-net api is unavailable", http.StatusBadGateway)
		return
	}
	data := components.AgentHistoryPageData{
		Layout:  components.LayoutData{Title: agentID + " history", Active: "Agents", User: htmxauth.GetUser(ctx)},
		AgentID: agentID,
		Rows:    buildAgentHistoryRows(resp.GetCurrent(), resp.GetHistory()),
	}
	if err := RenderTempl(w, r, data.Layout.Title, components.AgentHistoryPage(data)); err != nil {
		logging.Get("main").Error("failed to render agent history page", "error", err)
		http.Error(w, "Internal server error", http.StatusInternalServerError)
	}
}

func fmtAgentTime(a *whagentpb.AgentDefinition) (string, string) {
	from, to := "", ""
	if a.GetValidFrom() != nil {
		from = a.GetValidFrom().AsTime().UTC().Format("2006-01-02 15:04:05 UTC")
	}
	if a.GetValidTo() != nil {
		to = a.GetValidTo().AsTime().UTC().Format("2006-01-02 15:04:05 UTC")
	}
	return from, to
}

// buildAgentHistoryRows orders definitions by valid_from and names the
// fields that changed from each prior definition.
func buildAgentHistoryRows(current *whagentpb.AgentDefinition, history []*whagentpb.AgentDefinition) []components.AgentHistoryRow {
	defs := append([]*whagentpb.AgentDefinition(nil), history...)
	found := false
	for _, d := range defs {
		if d.GetId() == current.GetId() {
			found = true
		}
	}
	if !found && current != nil {
		defs = append(defs, current)
	}
	sort.SliceStable(defs, func(i, j int) bool {
		return defs[i].GetValidFrom().AsTime().Before(defs[j].GetValidFrom().AsTime())
	})
	rows := make([]components.AgentHistoryRow, 0, len(defs))
	for i, d := range defs {
		from, to := fmtAgentTime(d)
		row := components.AgentHistoryRow{ValidFrom: from, ValidTo: to, Current: d.GetValidTo() == nil}
		if i > 0 {
			row.Changed = changedAgentFields(defs[i-1], d)
		}
		rows = append(rows, row)
	}
	return rows
}

// changedAgentFields names the editable fields that differ between two
// definitions.
func changedAgentFields(prev, cur *whagentpb.AgentDefinition) []string {
	var out []string
	add := func(name string, changed bool) {
		if changed {
			out = append(out, name)
		}
	}
	add("model", prev.GetModel() != cur.GetModel())
	add("model_definition", prev.GetModelDefinitionId() != cur.GetModelDefinitionId())
	add("tool_set", prev.GetToolSet() != cur.GetToolSet())
	add("max_turns", prev.GetMaxTurns() != cur.GetMaxTurns())
	add("max_cost_usd", prev.GetMaxCostUsd() != cur.GetMaxCostUsd())
	add("max_tool_iterations", prev.GetMaxToolIterations() != cur.GetMaxToolIterations())
	add("tool_loading_mode", prev.GetToolLoadingMode() != cur.GetToolLoadingMode())
	add("required_role", prev.GetRequiredRole() != cur.GetRequiredRole())
	add("scope", prev.GetScope() != cur.GetScope())
	add("system_prompt", prev.GetSystemPrompt() != cur.GetSystemPrompt())
	return out
}
