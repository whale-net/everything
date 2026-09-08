// Package config is whagent-net's checked-in agent-definition seed
// source (issue #2121, LB5/NFR6; ARCHITECTURE.md "Domain-owned MCP
// servers and the tool contract"): agents.yaml (embedded below, so the
// binary carries its own seed data rather than reading a mounted path at
// runtime -- mirrors firmware/sensor/catalog's chips.yaml precedent) plus
// the Go shape Load decodes it into. whagent_net/migrate/seed consumes
// this package's Load to build the migrate.Seeder libs/go/migrate.
// WithSeeder registers (whagent_net/migrate/main.go).
//
// # Scaffold status (this task)
//
// Load and Validate below are real, not stubs: parsing/shape-checking a
// config file is pure and I/O-free, the same reasoning
// whagent_net/worker/caps.go's checkCaps documents for why a pure
// function ships whole in Scaffold even while the I/O-touching seeder
// this package feeds (whagent_net/migrate/seed.Seeder) stays a stub until
// Implementation. Validate's model-catalogue check (an unserved model
// must fail the seeder loudly per this task's Testing section) is
// deliberately NOT done here -- checking a model against
// llm.Catalog.Supports is a network call, so that half of validation
// belongs in the seeder (an I/O step), not this pure package.
package config

import (
	_ "embed"
	"fmt"

	"gopkg.in/yaml.v3"
)

//go:embed agents.yaml
var agentsYAML []byte

// ToolServerRefConfig is agents.yaml's tool_set entry shape -- decodes
// 1:1 into whagent_net/session.ToolServerRef.
type ToolServerRefConfig struct {
	ServerURL    string   `yaml:"server_url"`
	AllowedTools []string `yaml:"allowed_tools"`
}

// AgentDefinitionConfig is agents.yaml's per-agent entry shape -- decodes
// into whagent_net/session.AgentDefinition minus Version/CreatedAt, which
// the seeder derives (see agents.yaml's own doc comment and
// whagent_net/migrate/seed's package doc comment for the version-diff
// rule).
type AgentDefinitionConfig struct {
	AgentID      string                `yaml:"agent_id"`
	Model        string                `yaml:"model"`
	ToolSet      []ToolServerRefConfig `yaml:"tool_set"`
	MaxTurns     int                   `yaml:"max_turns"`
	MaxCostUSD   float64               `yaml:"max_cost_usd"`
	RequiredRole string                `yaml:"required_role"`
}

// document is agents.yaml's top-level shape.
type document struct {
	Agents []AgentDefinitionConfig `yaml:"agents"`
}

// Load parses the embedded agents.yaml and validates every entry (see
// Validate) before returning -- a malformed config file fails Load
// itself, before whagent_net/migrate/seed.Seeder ever opens a database
// transaction (this task's Testing section: "fails the seeder loudly
// rather than writing a half-row").
func Load() ([]AgentDefinitionConfig, error) {
	var doc document
	if err := yaml.Unmarshal(agentsYAML, &doc); err != nil {
		return nil, fmt.Errorf("config: parse agents.yaml: %w", err)
	}
	if err := Validate(doc.Agents); err != nil {
		return nil, fmt.Errorf("config: agents.yaml: %w", err)
	}
	return doc.Agents, nil
}

// RequiredRoles returns the deduplicated, non-empty RequiredRole values
// across agents, in stable (first-seen) order. whagent_net/api/main.go
// uses this to derive grpcauth.ServerConfig.DevRoles from the seeded
// agent definitions instead of hand-listing service roles (issue #2154,
// FR9) -- so a new agent definition with a new required_role stays
// coverable under GRPC_AUTH_MODE=none without a main.go change. Pure, no
// I/O, same shape as Validate.
func RequiredRoles(agents []AgentDefinitionConfig) []string {
	seen := make(map[string]struct{}, len(agents))
	roles := make([]string, 0, len(agents))
	for _, a := range agents {
		if a.RequiredRole == "" {
			continue
		}
		if _, dup := seen[a.RequiredRole]; dup {
			continue
		}
		seen[a.RequiredRole] = struct{}{}
		roles = append(roles, a.RequiredRole)
	}
	return roles
}

// Validate checks the shape-only rules Load can enforce without I/O: a
// missing agent_id, a missing model, or a tool_set entry with an empty
// server_url. The model-catalogue check (a model the configured OpenRouter
// provider does not serve) is deliberately not here -- see this package's
// doc comment -- and is whagent_net/migrate/seed.Seeder's job
// (Implementation phase).
func Validate(agents []AgentDefinitionConfig) error {
	seen := make(map[string]struct{}, len(agents))
	for i, a := range agents {
		if a.AgentID == "" {
			return fmt.Errorf("agents[%d]: agent_id is required", i)
		}
		if _, dup := seen[a.AgentID]; dup {
			return fmt.Errorf("agents[%d]: duplicate agent_id %q", i, a.AgentID)
		}
		seen[a.AgentID] = struct{}{}

		if a.Model == "" {
			return fmt.Errorf("agent %q: model is required", a.AgentID)
		}
		if len(a.ToolSet) == 0 {
			return fmt.Errorf("agent %q: tool_set must have at least one entry", a.AgentID)
		}
		for j, ref := range a.ToolSet {
			if ref.ServerURL == "" {
				return fmt.Errorf("agent %q: tool_set[%d]: server_url is required", a.AgentID, j)
			}
		}
	}
	return nil
}
