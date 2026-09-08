package config

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// validAgent returns a config.AgentDefinitionConfig that passes Validate,
// for tests to copy and mutate exactly one field -- keeps each failure
// case isolated to the one rule it's proving (this task's Testing
// section: "a definition naming ... a malformed tool_set, or a missing
// agent_id fails the seeder loudly").
func validAgent() AgentDefinitionConfig {
	return AgentDefinitionConfig{
		AgentID: "test-agent",
		Model:   "anthropic/claude-3.5-sonnet",
		ToolSet: []ToolServerRefConfig{
			{ServerURL: "http://mcp.example.com:8081/", AllowedTools: nil},
		},
		MaxTurns:     100,
		MaxCostUSD:   1.0,
		RequiredRole: "whagent-test-agent",
	}
}

func TestValidate_AcceptsAWellFormedDefinition(t *testing.T) {
	assert.NoError(t, Validate([]AgentDefinitionConfig{validAgent()}))
}

func TestValidate_MissingAgentID_FailsLoudly(t *testing.T) {
	agent := validAgent()
	agent.AgentID = ""

	err := Validate([]AgentDefinitionConfig{agent})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "agent_id is required")
}

func TestValidate_DuplicateAgentID_FailsLoudly(t *testing.T) {
	agent := validAgent()
	other := validAgent()

	err := Validate([]AgentDefinitionConfig{agent, other})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "duplicate agent_id")
}

func TestValidate_MissingModel_FailsLoudly(t *testing.T) {
	agent := validAgent()
	agent.Model = ""

	err := Validate([]AgentDefinitionConfig{agent})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "model is required")
}

func TestValidate_EmptyToolSet_FailsLoudly(t *testing.T) {
	agent := validAgent()
	agent.ToolSet = nil

	err := Validate([]AgentDefinitionConfig{agent})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "tool_set must have at least one entry")
}

func TestValidate_ToolSetMissingServerURL_FailsLoudly(t *testing.T) {
	agent := validAgent()
	agent.ToolSet = []ToolServerRefConfig{{ServerURL: "", AllowedTools: nil}}

	err := Validate([]AgentDefinitionConfig{agent})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "server_url is required")
}

// TestValidate_FirstBadEntryReportedByIndex proves a multi-agent config's
// error names the OFFENDING entry's index/agent_id, not just "something is
// wrong" -- so a config author (or the seeder's caller) can tell which
// entry to fix without a half-row ever being written for the good ones
// (seed.go's "check-all-then-write-all" pass depends on Load/Validate
// failing before Seeder ever opens a transaction).
func TestValidate_FirstBadEntryReportedByIndex(t *testing.T) {
	good := validAgent()
	bad := validAgent()
	bad.AgentID = "second-agent"
	bad.Model = ""

	err := Validate([]AgentDefinitionConfig{good, bad})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "second-agent")
}

// TestLoad_EmbeddedAgentsYAML_ParsesAndValidates is a sanity check that
// the real checked-in agents.yaml this package embeds is itself
// well-formed -- Load fails loudly (per this package's doc comment) on a
// malformed embed the same way it would for any other malformed config,
// so this guards against the checked-in seed data itself drifting into an
// invalid shape.
func TestLoad_EmbeddedAgentsYAML_ParsesAndValidates(t *testing.T) {
	agents, err := Load()
	require.NoError(t, err)
	require.NotEmpty(t, agents, "agents.yaml must seed at least one real agent definition (LB5/NFR6)")

	for _, a := range agents {
		assert.NotEmpty(t, a.AgentID)
		assert.NotEmpty(t, a.Model)
		assert.NotEmpty(t, a.ToolSet)
		for _, ref := range a.ToolSet {
			assert.True(t, strings.HasPrefix(ref.ServerURL, "http"), "tool_set server_url %q should be an http(s) URL", ref.ServerURL)
		}
	}
}
