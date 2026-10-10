package components

import (
	"context"
	"strings"
	"testing"
)

func TestAgentList_Render(t *testing.T) {
	var b strings.Builder
	err := AgentList(AgentListPageData{Agents: []AgentView{
		{AgentID: "a1", Model: "m1", RequiredRole: "admin", MaxTurns: 3, MaxCostUSD: "2.5", MaxToolIterations: 7},
		{AgentID: "a2", Model: "m2"},
	}}).Render(context.Background(), &b)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`href="/agents/a1"`, `href="/agents/a2"`, "badge-info", "admin", "none", "2.5"} {
		if !strings.Contains(b.String(), want) {
			t.Errorf("missing %q", want)
		}
	}
}

func TestAgentList_EmptyAndError(t *testing.T) {
	var b strings.Builder
	_ = AgentList(AgentListPageData{}).Render(context.Background(), &b)
	if !strings.Contains(b.String(), "No agents defined") {
		t.Error("missing empty state")
	}
	b.Reset()
	_ = AgentList(AgentListPageData{FetchError: "boom"}).Render(context.Background(), &b)
	if !strings.Contains(b.String(), "boom") {
		t.Error("missing error")
	}
}

func TestAgentDetail_Render(t *testing.T) {
	var b strings.Builder
	err := AgentDetail(AgentDetailPageData{Agent: AgentView{
		ID: "uid", AgentID: "a1", ModelDefinitionID: "md-1", SystemPrompt: "<b>x</b>", ToolSet: "ts",
	}}).Render(context.Background(), &b)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"uid", "Model definition id", "md-1", "&lt;b&gt;x&lt;/b&gt;", "agent-actions", "font-mono"} {
		if !strings.Contains(b.String(), want) {
			t.Errorf("missing %q", want)
		}
	}
}
