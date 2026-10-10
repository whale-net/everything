package main

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"

	whagentpb "github.com/whale-net/everything/whagent_net/protos"
)

const agentAdminTestRole = "agent-admin"

func newAgentAdminApp(t *testing.T, srv *fakeAgentServer, adminRole string, tokenRoles ...string) *App {
	t.Helper()
	if srv.agents == nil {
		srv.agents = testAgents()
	}
	return &App{
		auth:           devModeAuthenticator(t),
		session:        newBufconnUISessionClient(t, srv),
		agentAdminRole: adminRole,
		agentTokens:    &fakeAccessTokenReader{freshToken: buildFakeAccessToken(t, tokenRoles)},
	}
}

func agentReq(method, path, agentID string, form url.Values) *http.Request {
	var req *http.Request
	if form != nil {
		req = httptest.NewRequest(method, path, strings.NewReader(form.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	} else {
		req = httptest.NewRequest(method, path, nil)
	}
	req.SetPathValue("agent_id", agentID)
	return req
}

func validForm() url.Values {
	return url.Values{
		"model_kind": {"model"}, "model": {"claude-new"}, "model_definition_id": {"md-1"},
		"tool_set": {"ts"}, "max_turns": {"20"}, "max_cost_usd": {"2.5"}, "max_tool_iterations": {"30"},
		"tool_loading_mode": {"lazy"}, "required_role": {""}, "scope": {"global"}, "system_prompt": {"hi"},
	}
}

func TestAgentDetail_AdminSeesControls_NonAdminDoesNot(t *testing.T) {
	for _, tc := range []struct {
		name      string
		adminRole string
		roles     []string
		want      bool
	}{
		{"admin", agentAdminTestRole, []string{agentAdminTestRole}, true},
		{"non-admin", agentAdminTestRole, []string{"other"}, false},
		{"env unset hides for everyone", "", []string{agentAdminTestRole}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			app := newAgentAdminApp(t, &fakeAgentServer{}, tc.adminRole, tc.roles...)
			w := httptest.NewRecorder()
			app.handleAgentDetail(w, agentReq(http.MethodGet, "/agents/code-review", "code-review", nil))
			require.Equal(t, http.StatusOK, w.Code)
			body := w.Body.String()
			require.Equal(t, tc.want, strings.Contains(body, "/agents/code-review/edit"))
			require.Equal(t, tc.want, strings.Contains(body, "/agents/code-review/history"))
		})
	}
}

func TestAgentRoutes_NonAdminForbiddenAndNeverUpdates(t *testing.T) {
	srv := &fakeAgentServer{}
	app := newAgentAdminApp(t, srv, agentAdminTestRole, "other")
	for name, h := range map[string]func(http.ResponseWriter, *http.Request){
		"edit": app.handleAgentEdit, "history": app.handleAgentHistory,
	} {
		w := httptest.NewRecorder()
		h(w, agentReq(http.MethodGet, "/x", "code-review", nil))
		require.Equal(t, http.StatusForbidden, w.Code, name)
	}
	w := httptest.NewRecorder()
	app.handleAgentUpdate(w, agentReq(http.MethodPost, "/agents/code-review", "code-review", validForm()))
	require.Equal(t, http.StatusForbidden, w.Code)
	require.Empty(t, srv.updateCalls())
}

func TestAgentUpdate_ModelSendsOnlyModel(t *testing.T) {
	srv := &fakeAgentServer{}
	app := newAgentAdminApp(t, srv, agentAdminTestRole, agentAdminTestRole)
	w := httptest.NewRecorder()
	app.handleAgentUpdate(w, agentReq(http.MethodPost, "/agents/code-review", "code-review", validForm()))
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	calls := srv.updateCalls()
	require.Len(t, calls, 1)
	require.Equal(t, "claude-new", calls[0].GetModel())
	require.Nil(t, calls[0].ModelDefinitionId)
	require.Equal(t, int32(20), calls[0].GetMaxTurns())
	require.Contains(t, w.Body.String(), "Saved.")
	require.Contains(t, w.Body.String(), "claude-new")
}

func TestAgentUpdate_ModelDefinitionSendsOnlyDefinition(t *testing.T) {
	srv := &fakeAgentServer{}
	app := newAgentAdminApp(t, srv, agentAdminTestRole, agentAdminTestRole)
	f := validForm()
	f.Set("model_kind", "model_definition")
	f.Set("model", "stale-text")
	w := httptest.NewRecorder()
	app.handleAgentUpdate(w, agentReq(http.MethodPost, "/agents/code-review", "code-review", f))
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	calls := srv.updateCalls()
	require.Len(t, calls, 1)
	require.Empty(t, calls[0].GetModel())
	require.NotNil(t, calls[0].ModelDefinitionId)
	require.Equal(t, "md-1", calls[0].GetModelDefinitionId())
}

func TestAgentUpdate_ErrorKeepsInputs(t *testing.T) {
	for _, code := range []codes.Code{codes.InvalidArgument, codes.PermissionDenied, codes.Aborted, codes.Internal} {
		t.Run(code.String(), func(t *testing.T) {
			srv := &fakeAgentServer{updateErr: status.Error(code, "boom-"+code.String()),
				modelDefs: []*whagentpb.ModelDefinition{{Id: "md-1", Name: "Opus"}, {Id: "md-2", Name: "Sonnet"}}}
			app := newAgentAdminApp(t, srv, agentAdminTestRole, agentAdminTestRole)
			f := validForm()
			f.Set("model_kind", "model_definition")
			f.Set("model_definition_id", "md-2")
			f.Set("max_turns", "0")
			f.Set("system_prompt", "keep me")
			w := httptest.NewRecorder()
			app.handleAgentUpdate(w, agentReq(http.MethodPost, "/agents/code-review", "code-review", f))
			body := w.Body.String()
			require.Equal(t, http.StatusOK, w.Code)
			require.Contains(t, body, "alert-error")
			if code != codes.Aborted {
				require.Contains(t, body, "boom-"+code.String())
			}
			require.Contains(t, body, `value="0"`)
			require.Contains(t, body, "keep me")
			require.Contains(t, body, `value="model_definition" class="radio" checked`)
			require.Regexp(t, `<option value="md-2" selected`, body)
		})
	}
}

func TestAgentEdit_SelectListsExactlyModelDefinitions(t *testing.T) {
	srv := &fakeAgentServer{modelDefs: []*whagentpb.ModelDefinition{
		{Id: "md-a", Name: "Alpha"}, {Id: "md-b", Name: "Beta"}, {Id: "md-c", Name: "Gamma"},
	}}
	app := newAgentAdminApp(t, srv, agentAdminTestRole, agentAdminTestRole)
	w := httptest.NewRecorder()
	app.handleAgentEdit(w, agentReq(http.MethodGet, "/agents/code-review/edit", "code-review", nil))
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	body := w.Body.String()
	require.Equal(t, 3, strings.Count(body, "<option "))
	for _, id := range []string{"md-a", "md-b", "md-c"} {
		require.Contains(t, body, `value="`+id+`"`)
	}
	require.Contains(t, body, "claude-opus-x") // prefilled current model
}

func TestAgentHistory_OrdersRowsAndNamesChangedFields(t *testing.T) {
	t0 := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	t1 := t0.Add(time.Hour)
	t2 := t0.Add(2 * time.Hour)
	mk := func(id string, from time.Time, to *time.Time, turns int32, prompt string) *whagentpb.AgentDefinition {
		d := &whagentpb.AgentDefinition{Id: id, AgentId: "code-review", Scope: "global", Model: "m", ToolSet: "t",
			MaxTurns: turns, MaxCostUsd: 1, MaxToolIterations: 5, ToolLoadingMode: "eager", SystemPrompt: prompt,
			ValidFrom: timestamppb.New(from)}
		if to != nil {
			d.ValidTo = timestamppb.New(*to)
		}
		return d
	}
	d1 := mk("h1", t0, &t1, 5, "p")
	d2 := mk("h2", t1, &t2, 9, "p")
	d3 := mk("h3", t2, nil, 9, "p2")
	srv := &fakeAgentServer{agents: []*whagentpb.AgentDefinition{d3}, history: []*whagentpb.AgentDefinition{d3, d1, d2}}
	app := newAgentAdminApp(t, srv, agentAdminTestRole, agentAdminTestRole)
	w := httptest.NewRecorder()
	app.handleAgentHistory(w, agentReq(http.MethodGet, "/agents/code-review/history", "code-review", nil))
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	body := w.Body.String()
	i1 := strings.Index(body, "2024-01-01 00:00:00 UTC")
	i2 := strings.Index(body, "2024-01-01 01:00:00 UTC")
	i3 := strings.Index(body, "2024-01-01 02:00:00 UTC")
	require.True(t, i1 >= 0 && i1 < i2 && i2 < i3, "rows out of order")
	require.Contains(t, body, "badge-success\">current")
	require.Contains(t, body, ">max_turns<")
	require.Contains(t, body, ">system_prompt<")
	for _, no := range []string{">model<", ">tool_set<", ">scope<", ">max_cost_usd<"} {
		require.NotContains(t, body, no)
	}
}

func TestChangedAgentFields(t *testing.T) {
	a := &whagentpb.AgentDefinition{Model: "m", ToolSet: "t", MaxTurns: 1, MaxCostUsd: 1, MaxToolIterations: 1,
		ToolLoadingMode: "x", Scope: "g", SystemPrompt: "p"}
	require.Empty(t, changedAgentFields(a, a))

	b := &whagentpb.AgentDefinition{Model: "m2", ToolSet: "t", MaxTurns: 2, MaxCostUsd: 1, MaxToolIterations: 1,
		ToolLoadingMode: "x", Scope: "g", SystemPrompt: "p", RequiredRole: ptr("r")}
	require.Equal(t, []string{"model", "max_turns", "required_role"}, changedAgentFields(a, b))

	md := &whagentpb.AgentDefinition{ModelDefinitionId: ptr("md"), ToolSet: "t", MaxTurns: 1, MaxCostUsd: 1,
		MaxToolIterations: 1, ToolLoadingMode: "x", Scope: "g", SystemPrompt: "p"}
	require.Equal(t, []string{"model", "model_definition"}, changedAgentFields(a, md))
}
