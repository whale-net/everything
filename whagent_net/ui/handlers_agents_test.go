package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"

	whagentpb "github.com/whale-net/everything/whagent_net/protos"
)

// fakeAgentServer serves ListAgents/GetAgent from a fixed slice.
type fakeAgentServer struct {
	whagentpb.UnimplementedSessionServiceServer
	agents []*whagentpb.AgentDefinition
}

func (f *fakeAgentServer) ListAgents(context.Context, *whagentpb.ListAgentsRequest) (*whagentpb.ListAgentsResponse, error) {
	return &whagentpb.ListAgentsResponse{Agents: f.agents}, nil
}

func (f *fakeAgentServer) GetAgent(_ context.Context, req *whagentpb.GetAgentRequest) (*whagentpb.GetAgentResponse, error) {
	for _, a := range f.agents {
		if a.GetAgentId() == req.GetAgentId() {
			return &whagentpb.GetAgentResponse{Current: a}, nil
		}
	}
	return nil, status.Error(codes.NotFound, "no such agent")
}

func testAgents() []*whagentpb.AgentDefinition {
	return []*whagentpb.AgentDefinition{
		{
			Id: "def-uuid-1", AgentId: "code-review", Scope: "global", Model: "claude-opus-x",
			ToolSet: "review-tools", MaxTurns: 12, MaxCostUsd: 3.5, MaxToolIterations: 40,
			ToolLoadingMode: "eager", RequiredRole: ptr("reviewer"),
			SystemPrompt: "You review <code> carefully.\nSecond line.",
			ValidFrom:    timestamppb.New(time.Date(2024, 1, 2, 3, 4, 5, 0, time.UTC)),
		},
		{
			Id: "def-uuid-2", AgentId: "helper", Scope: "global", ModelDefinitionId: ptr("md-77"),
			ToolSet: "basic", MaxTurns: 5, MaxCostUsd: 1, MaxToolIterations: 9,
			ToolLoadingMode: "lazy", SystemPrompt: "help",
		},
	}
}

func newAgentTestApp(t *testing.T) *App {
	t.Helper()
	return &App{
		auth:    devModeAuthenticator(t),
		session: newBufconnUISessionClient(t, &fakeAgentServer{agents: testAgents()}),
	}
}

func TestHandleAgentList_RendersEveryAgent(t *testing.T) {
	app := newAgentTestApp(t)
	w := httptest.NewRecorder()
	app.auth.RequireAuthFunc(app.auth.WithAccessToken(app.handleAgentList))(w, httptest.NewRequest(http.MethodGet, "/agents", nil))
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	body := w.Body.String()
	for _, want := range []string{
		`href="/agents/code-review"`, `href="/agents/helper"`,
		"claude-opus-x", "md-77", "reviewer", "none", "3.5", ">12<", ">40<", ">5<", ">9<",
	} {
		require.Contains(t, body, want)
	}
}

func TestHandleAgentDetail_RendersEveryField(t *testing.T) {
	app := newAgentTestApp(t)
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/agents/code-review", nil)
	req.SetPathValue("agent_id", "code-review")
	app.auth.RequireAuthFunc(app.auth.WithAccessToken(app.handleAgentDetail))(w, req)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	body := w.Body.String()
	for _, want := range []string{
		"def-uuid-1", "code-review", "global", "claude-opus-x", "review-tools", ">12<", "3.5", ">40<",
		"eager", "reviewer", "2024-01-02 03:04:05 UTC", "You review &lt;code&gt; carefully.\nSecond line.",
		`id="agent-actions"`,
	} {
		require.Contains(t, body, want)
	}
}

func TestHandleAgentDetail_UnknownAgent404(t *testing.T) {
	app := newAgentTestApp(t)
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/agents/nope", nil)
	req.SetPathValue("agent_id", "nope")
	app.auth.RequireAuthFunc(app.auth.WithAccessToken(app.handleAgentDetail))(w, req)
	require.Equal(t, http.StatusNotFound, w.Code)
}

func TestAgentRoutes_UnauthenticatedRedirectToLogin(t *testing.T) {
	app := &App{auth: newTestOIDCAuthenticator(t), mcpProvider: newTestMCPProvider(t)}
	mux := http.NewServeMux()
	app.setupRoutes(mux)
	for _, path := range []string{"/agents", "/agents/code-review"} {
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, httptest.NewRequest(http.MethodGet, path, nil))
		require.True(t, requestWasAuthBlocked(w), "%s: status %d", path, w.Code)
		require.False(t, strings.Contains(w.Body.String(), "System prompt"))
	}
}

func ptr(s string) *string { return &s }
