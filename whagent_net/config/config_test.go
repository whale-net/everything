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
		Domain:  "test-domain",
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
	assert.NoError(t, Validate(nil, []AgentDefinitionConfig{validAgent()}))
}

func TestValidate_MissingAgentID_FailsLoudly(t *testing.T) {
	agent := validAgent()
	agent.AgentID = ""

	err := Validate(nil, []AgentDefinitionConfig{agent})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "agent_id is required")
}

func TestValidate_DuplicateAgentID_FailsLoudly(t *testing.T) {
	agent := validAgent()
	other := validAgent()

	err := Validate(nil, []AgentDefinitionConfig{agent, other})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "duplicate agent_id")
}

// TestValidate_MissingDomain_FailsLoudly proves an entry with no `domain`
// key fails Validate as a config error (issue #2424 FR1) -- never
// deferred to seed time.
func TestValidate_MissingDomain_FailsLoudly(t *testing.T) {
	agent := validAgent()
	agent.Domain = ""

	err := Validate(nil, []AgentDefinitionConfig{agent})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "domain is required")
}

// TestValidate_EmptyStringDomain_FailsLoudly mirrors the missing-key case
// for a `domain: ""` entry -- both must fail identically since Go's yaml
// decode leaves both as the empty string.
func TestValidate_EmptyStringDomain_FailsLoudly(t *testing.T) {
	agent := validAgent()
	agent.Domain = ""

	err := Validate(nil, []AgentDefinitionConfig{agent})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "domain is required")
}

func TestValidate_MissingModelAndModelDefinition_FailsLoudly(t *testing.T) {
	agent := validAgent()
	agent.Model = ""

	err := Validate(nil, []AgentDefinitionConfig{agent})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "exactly one of model or model_definition is required")
}

func TestValidate_ModelAndModelDefinitionBothSet_FailsLoudly(t *testing.T) {
	agent := validAgent()
	agent.ModelDefinition = "shared-model"

	err := Validate([]ModelDefinitionConfig{{Name: "shared-model", Model: "anthropic/claude-3.5-sonnet"}}, []AgentDefinitionConfig{agent})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "mutually exclusive")
}

func TestValidate_ModelDefinitionNotDefined_FailsLoudly(t *testing.T) {
	agent := validAgent()
	agent.Model = ""
	agent.ModelDefinition = "does-not-exist"

	err := Validate(nil, []AgentDefinitionConfig{agent})
	require.Error(t, err)
	assert.Contains(t, err.Error(), `model_definition "does-not-exist" is not defined`)
}

func TestValidate_AcceptsModelDefinitionReference(t *testing.T) {
	agent := validAgent()
	agent.Model = ""
	agent.ModelDefinition = "shared-model"

	err := Validate([]ModelDefinitionConfig{{Name: "shared-model", Model: "anthropic/claude-3.5-sonnet"}}, []AgentDefinitionConfig{agent})
	assert.NoError(t, err)
}

func TestValidate_ModelDefinitionMissingName_FailsLoudly(t *testing.T) {
	err := Validate([]ModelDefinitionConfig{{Model: "anthropic/claude-3.5-sonnet"}}, []AgentDefinitionConfig{validAgent()})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "name is required")
}

func TestValidate_ModelDefinitionMissingModel_FailsLoudly(t *testing.T) {
	err := Validate([]ModelDefinitionConfig{{Name: "shared-model"}}, []AgentDefinitionConfig{validAgent()})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "model is required")
}

func TestValidate_DuplicateModelDefinitionName_FailsLoudly(t *testing.T) {
	modelDefs := []ModelDefinitionConfig{
		{Name: "shared-model", Model: "anthropic/claude-3.5-sonnet"},
		{Name: "shared-model", Model: "openai/gpt-4o"},
	}
	err := Validate(modelDefs, []AgentDefinitionConfig{validAgent()})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "duplicate name")
}

func TestValidate_EmptyToolSet_FailsLoudly(t *testing.T) {
	agent := validAgent()
	agent.ToolSet = nil

	err := Validate(nil, []AgentDefinitionConfig{agent})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "tool_set must have at least one entry")
}

func TestValidate_ToolSetMissingServerURL_FailsLoudly(t *testing.T) {
	agent := validAgent()
	agent.ToolSet = []ToolServerRefConfig{{ServerURL: "", AllowedTools: nil}}

	err := Validate(nil, []AgentDefinitionConfig{agent})
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

	err := Validate(nil, []AgentDefinitionConfig{good, bad})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "second-agent")
}

// TestRequiredRoles_DedupesAndSkipsEmpty proves RequiredRoles' two rules
// directly (issue #2154, FR9): a required_role shared by more than one
// agent appears exactly once in the result, and an agent with an empty
// required_role contributes nothing -- so grpcauth.ServerConfig.DevRoles
// (whagent_net/api/main.go) never carries a duplicate or a blank entry.
func TestRequiredRoles_DedupesAndSkipsEmpty(t *testing.T) {
	shared := validAgent()
	shared.AgentID = "shared-a"
	shared.RequiredRole = "whagent-shared-role"

	sameRole := validAgent()
	sameRole.AgentID = "shared-b"
	sameRole.RequiredRole = "whagent-shared-role"

	noRole := validAgent()
	noRole.AgentID = "no-role"
	noRole.RequiredRole = ""

	roles := RequiredRoles([]AgentDefinitionConfig{shared, sameRole, noRole})
	assert.Equal(t, []string{"whagent-shared-role"}, roles)
}

// TestRequiredRoles_RealAgentsYAML proves RequiredRoles generalizes past
// hand-built fixtures to the real checked-in agents.yaml this package
// embeds -- config.Load()'s result must include the seeded
// audience-score-system-research agent's required_role, the same value
// whagent_net/api/main_test.go's TestDevRolesCarrySeededRequiredRoles
// asserts ends up in AuthModeNone's dev Claims.
func TestRequiredRoles_RealAgentsYAML(t *testing.T) {
	_, agents, err := Load()
	require.NoError(t, err)

	roles := RequiredRoles(agents)
	assert.Contains(t, roles, "whagent-audience-score-system-research")
}

// TestLoad_EmbeddedAgentsYAML_ParsesAndValidates is a sanity check that
// the real checked-in agents.yaml this package embeds is itself
// well-formed -- Load fails loudly (per this package's doc comment) on a
// malformed embed the same way it would for any other malformed config,
// so this guards against the checked-in seed data itself drifting into an
// invalid shape.
func TestLoad_EmbeddedAgentsYAML_ParsesAndValidates(t *testing.T) {
	_, agents, err := Load()
	require.NoError(t, err)
	require.NotEmpty(t, agents, "agents.yaml must seed at least one real agent definition (LB5/NFR6)")

	for _, a := range agents {
		assert.NotEmpty(t, a.AgentID)
		assert.NotEmpty(t, a.Domain, "agent %q must carry a non-empty domain", a.AgentID)
		assert.True(t, a.Model != "" || a.ModelDefinition != "", "agent %q must name a model or model_definition", a.AgentID)
		assert.NotEmpty(t, a.ToolSet)
		for _, ref := range a.ToolSet {
			assert.True(t, strings.HasPrefix(ref.ServerURL, "http"), "tool_set server_url %q should be an http(s) URL", ref.ServerURL)
		}
	}
}
